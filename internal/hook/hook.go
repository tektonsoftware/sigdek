// Package hook is the hot path: the agent tool runs `sigdek hook <tool>` on every event
// and waits for it. The contract, enforced by tests:
//   - it always exits 0, and any internal error lets the action go ahead
//   - stdout carries only the response the tool expects (a denial, or Cursor's JSON), and
//     it is written before the record, so a slow disk or a busy lock can't hold it up
//   - it reads one JSON value from stdin and never waits for more than StdinDeadline
//   - every event that arrives but writes nothing, other than by design, is logged
//   - uploading happens in a separate background process, except for a record of an action
//     that changes SigDek's own setup, which is uploaded here for at most UrgentUpload
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/tektonsoftware/sigdek/internal/adapter"
	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
	"github.com/tektonsoftware/sigdek/internal/upload"
)

const (
	MaxStdin   = 8 << 20
	FlushEvery = time.Minute
	// UrgentUpload bounds the upload of a record made before an action that may delete the log.
	UrgentUpload = time.Second
)

// StdinDeadline bounds the wait for the hook's input.
var StdinDeadline = 2 * time.Second

// Options lets tests replace the clock and the uploads.
type Options struct {
	Home  string
	Now   func() time.Time
	Spawn func() error // start a background upload
	// Upload sends unsent records now, giving up after the time given; nil leaves it to Spawn.
	Upload func(time.Duration) error
}

// Run handles one event and never returns an error: failures go to the error log.
func Run(tool, hint string, in io.Reader, out io.Writer, o Options) {
	answered := false
	defer func() {
		if p := recover(); p != nil {
			LogError(o.Home, fmt.Errorf("panic in hook %s: %v\n%s", tool, p, debug.Stack()))
		}
		// Cursor blocks permission hooks on missing or invalid output, so always answer.
		if tool == "cursor" && !answered {
			out.Write(adapter.CursorAllowFor(hint))
		}
	}()

	raw, complete, err := readStdin(in)
	if err != nil {
		LogError(o.Home, fmt.Errorf("reading %s hook input: %w", tool, err))
	}
	dev, err := config.LoadDevice(o.Home)
	record := true
	if err != nil {
		// Records stay on this machine's chain, under its own id, while device.json can't be
		// read; with no id to be found, nothing is recorded (Block rules still apply).
		if dev.ID = config.SalvageID(o.Home); dev.ID == "" {
			record = false
			LogError(o.Home, fmt.Errorf("loading device: %w; nothing is recorded until it is fixed", err))
		} else {
			LogError(o.Home, fmt.Errorf("loading device: %w; recording as %s, not uploading", err, dev.ID))
		}
	}
	set, err := rules.LoadChecked(config.RulesPath(o.Home))
	if err != nil {
		LogError(o.Home, err)
	}
	ctx := adapter.Context{Now: o.Now(), Device: dev, Rules: set, Partial: !complete}

	var res adapter.Result
	switch tool {
	case "claude-code":
		res = adapter.ClaudeCode(raw, ctx)
	case "cursor":
		res = adapter.Cursor(raw, ctx, hint)
	default:
		LogError(o.Home, fmt.Errorf("unknown agent tool %q; nothing recorded", tool))
		return
	}
	if res.Drop != "" {
		LogError(o.Home, fmt.Errorf("%s hook: %s; nothing recorded", tool, res.Drop))
	}

	if res.Stdout != nil {
		out.Write(res.Stdout)
		answered = true
	}
	written := false
	if res.Record != nil && record {
		st := store.New(o.Home)
		st.Warn = func(err error) { LogError(o.Home, err) }
		if err := st.Append(res.Record); err != nil {
			LogError(o.Home, fmt.Errorf("writing %s record: %w; this event was not recorded", res.Record.Event, err))
		} else {
			written = true
		}
	}
	if dev.SignedIn() && (res.Flush || flushDue(o.Home, ctx.Now)) {
		touchFlushStamp(o.Home, ctx.Now)
		// No upload at all while the server has rejected the token or asked to slow down.
		if paused, _ := upload.Paused(o.Home, ctx.Now); !paused {
			// A record made before the action deletes the log goes up now if it can; a failed or
			// slow try leaves it to the background upload, as for every other record.
			if res.Urgent && written && o.Upload != nil && o.Upload(UrgentUpload) == nil {
				return
			}
			if err := o.Spawn(); err != nil {
				LogError(o.Home, fmt.Errorf("starting upload: %w", err))
			}
		}
	}
}

// capture keeps a copy of what was read, for when the JSON never completes.
type capture struct {
	mu sync.Mutex
	b  []byte
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.b = append(c.b, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *capture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.b...)
}

// readStdin returns the first JSON value on stdin as soon as it is complete, without waiting
// for stdin to close. complete is false when the value was cut off, by MaxStdin or by
// StdinDeadline; raw then holds what arrived, for the adapters to use what they can.
func readStdin(in io.Reader) (raw []byte, complete bool, err error) {
	if f, ok := in.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			return nil, true, nil // a terminal, not a hook invocation
		}
	}
	c := &capture{}
	lr := &io.LimitedReader{R: in, N: MaxStdin}
	dec := json.NewDecoder(io.TeeReader(lr, c))
	type result struct {
		v   json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var v json.RawMessage
		err := dec.Decode(&v)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			// Not JSON encoding/json can read (nested too deeply, or broken part way): read the
			// rest too, so the adapter's lenient decoder sees the fields after the problem.
			_, _ = io.Copy(io.Discard, io.TeeReader(lr, c))
		}
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		switch {
		case r.err == nil:
			return r.v, true, nil
		case errors.Is(r.err, io.EOF):
			return nil, true, nil // empty input
		case lr.N <= 0:
			return c.bytes(), false, fmt.Errorf("input is over %d MB; using the fields that arrived", MaxStdin>>20)
		case errors.Is(r.err, io.ErrUnexpectedEOF):
			return c.bytes(), false, errors.New("input ended in the middle of the JSON; using the fields that arrived")
		default:
			return c.bytes(), true, nil // not JSON encoding/json reads; the adapter does what it can
		}
	case <-time.After(StdinDeadline):
		return c.bytes(), false, fmt.Errorf("input incomplete after %s (stdin not closed); using the fields that arrived", StdinDeadline)
	}
}

func stampPath(home string) string { return filepath.Join(config.StateDir(home), "flush.stamp") }

func flushDue(home string, now time.Time) bool {
	st, err := os.Stat(stampPath(home))
	return err != nil || now.Sub(st.ModTime()) >= FlushEvery
}

func touchFlushStamp(home string, now time.Time) {
	p := stampPath(home)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	}
	_ = os.Chtimes(p, now, now)
}

// LogError appends to ~/.sigdek/errors.log, keeping it under about 1 MB.
func LogError(home string, err error) {
	defer func() { _ = recover() }()
	p := config.ErrorLog(home)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if st, e := os.Stat(p); e == nil && st.Size() > 1<<20 {
		_ = os.Rename(p, p+".1")
	}
	// To the millisecond, so `sigdek status` can tell an error from the upload that followed it.
	line, _ := json.Marshal(map[string]string{"at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), "error": err.Error()})
	if f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); e == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}
