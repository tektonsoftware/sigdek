// Package upload sends unsent records to the ingest service in batches.
package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/apierr"
	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/fsretry"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

const (
	BatchRecords = 500
	BatchBytes   = 4 << 20
	// MaxResponse is how much of an ingest response is read; the team's rules ride on it.
	MaxResponse = 4 << 20
	// TokenRetry is how often background uploads try again once the server has rejected this
	// machine's token, until `sigdek login` succeeds or `sigdek flush` is run by hand.
	TokenRetry = time.Hour
	// MaxBackoff caps the wait after rate limiting or server errors.
	MaxBackoff = time.Hour
)

var (
	ErrNotSignedIn = errors.New("not signed in; run `sigdek login`")
	ErrRevoked     = errors.New("this device's token was rejected; run `sigdek login` again")
	ErrBusy        = errors.New("another upload is already running")
	// ErrPaused: a background upload isn't due yet (see Paused).
	ErrPaused = errors.New("uploads are paused")
)

// Response is what the ingest service returns for a batch.
type Response struct {
	Accepted    int        `json:"accepted"`
	Duplicates  int        `json:"duplicates"`
	ChainBreaks int        `json:"chain_breaks"`
	Invalid     int        `json:"invalid"` // records the server refused as malformed; they show as breaks
	Rules       *rules.Set `json:"rules,omitempty"`

	unread error // a 200 whose body couldn't be read; the batch still counts as accepted
}

type Summary struct {
	Sent, Accepted, Duplicates, ChainBreaks, Invalid, Batches int
	Skipped                                                   int // records the server can never take (too large)
}

// Health is what uploads remember between runs, for back-off and for `sigdek status`.
type Health struct {
	LastOK     time.Time `json:"last_ok,omitzero"`
	LastTry    time.Time `json:"last_try,omitzero"`
	LastError  string    `json:"last_error,omitempty"`  // the last attempt's error; "" when it worked
	Fails      int       `json:"fails,omitempty"`       // rate limits and server errors in a row
	RetryAfter time.Time `json:"retry_after,omitzero"`  // no background upload before this
	Refused    int       `json:"refused,omitempty"`     // records the server refused as malformed, in all
	Skipped    int       `json:"skipped,omitempty"`     // records too large for the server, in all
	RulesError string    `json:"rules_error,omitempty"` // why the last rules in a response weren't used
	SignedIn   time.Time `json:"signed_in,omitzero"`    // the last sign-in; upload errors from before it are over
}

func healthPath(home string) string {
	return filepath.Join(config.StateDir(home), "upload-health.json")
}

// LoadHealth reads what the last uploads left; zero when there were none.
func LoadHealth(home string) Health {
	var h Health
	if b, err := fsretry.ReadFile(healthPath(home)); err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

func saveHealth(home string, h Health) {
	b, _ := json.Marshal(h)
	p := healthPath(home)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "health-*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr != nil || cerr != nil || fsretry.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
	}
}

// SignedIn notes a sign-in: the errors and waits of uploads before it are over.
func SignedIn(home string, now time.Time) {
	h := LoadHealth(home)
	h.LastError, h.Fails, h.RetryAfter, h.SignedIn = "", 0, time.Time{}, now
	saveHealth(home, h)
}

// ErrorsOverAt is when the upload errors in the error log stopped mattering: the start of the
// last upload that sent everything, or the last sign-in, whichever is later. A warning logged
// during that upload (an answer it couldn't read, a record too large) still counts.
func (h Health) ErrorsOverAt() time.Time {
	over := h.SignedIn
	if h.LastError == "" && !h.LastOK.IsZero() {
		start := h.LastTry
		if start.IsZero() || start.After(h.LastOK) {
			start = h.LastOK
		}
		if start.After(over) {
			over = start
		}
	}
	return over
}

// Paused says whether background uploads should wait, and why: the server rejected this
// machine's token less than TokenRetry ago, or asked it to slow down (rate limit, server
// error) and the wait isn't over. `sigdek flush` by hand doesn't wait.
func Paused(home string, now time.Time) (bool, string) {
	if st, err := os.Stat(config.TokenRejected(home)); err == nil && now.Sub(st.ModTime()) < TokenRetry {
		return true, "the server rejected this machine's token"
	}
	if h := LoadHealth(home); now.Before(h.RetryAfter) {
		return true, h.LastError
	}
	return false, ""
}

// Options for FlushWith.
type Options struct {
	Client     *http.Client
	Background bool             // started by a hook: respects Paused
	Log        func(error)      // problems that don't stop the upload; may be nil
	Now        func() time.Time // time.Now when nil
	// Within, when set, bounds the whole upload, waiting for another upload to finish
	// included; what isn't sent by then is left for the next one.
	Within time.Duration
}

// Flush uploads everything after the cursor, as `sigdek flush` does by hand.
func Flush(home, userAgent string, client *http.Client) (Summary, error) {
	return FlushWith(home, userAgent, Options{Client: client})
}

// FlushWith uploads everything after the cursor. Only one flush runs at a time.
func FlushWith(home, userAgent string, o Options) (Summary, error) {
	var sum Summary
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(error) {}
	}
	ctx := context.Background()
	var lock *lockfile.Lock
	var err error
	if o.Within > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Within)
		defer cancel()
		// Another upload may be sending this very record; wait for it rather than give up.
		if lock, err = lockfile.AcquireTimeout(config.FlushLock(home), o.Within); errors.Is(err, lockfile.ErrTimeout) {
			return sum, ErrBusy
		}
	} else {
		lock, err = lockfile.TryAcquire(config.FlushLock(home))
	}
	if err != nil {
		return sum, err
	}
	if lock == nil {
		return sum, ErrBusy
	}
	defer lock.Release()

	dev, err := config.LoadDevice(home)
	if err != nil {
		return sum, err
	}
	if !dev.SignedIn() {
		return sum, ErrNotSignedIn
	}
	if o.Background {
		if paused, _ := Paused(home, o.Now()); paused {
			return sum, ErrPaused
		}
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Second}
	}
	u := &uploader{ctx: ctx, home: home, dev: dev, ua: userAgent, o: o, st: store.New(home), sum: &sum, health: LoadHealth(home)}
	u.health.LastTry = o.Now()
	defer func() { saveHealth(home, u.health) }()
	for {
		var batch []store.Line
		size := 0
		err := u.st.ReadFrom(u.st.LoadCursor(), func(l store.Line) bool {
			batch = append(batch, l)
			size += len(l.Raw) + 1
			return len(batch) < BatchRecords && size < BatchBytes
		})
		if err != nil {
			return sum, err
		}
		if len(batch) == 0 {
			u.ok()
			return sum, nil
		}
		if err := u.sendAll(batch); err != nil {
			return sum, u.failed(err)
		}
	}
}

type uploader struct {
	ctx      context.Context
	home, ua string
	dev      config.Device
	o        Options
	st       *store.Store
	sum      *Summary
	health   Health
	accepted bool // the server took a request in this run
}

// sendAll sends a batch and moves the cursor past it. A batch the server finds too large is
// split in halves; a single record that is still too large can never be accepted, so it is
// skipped (it stays on this machine) and reported. That needs proof the server takes other
// records, or a server or proxy that refuses every request would have them all skipped: one
// went through in this run, or the next record, sent on its own, does.
func (u *uploader) sendAll(batch []store.Line) error {
	resp, err := send(u.ctx, u.o.Client, u.dev, u.ua, batch)
	var api *apierr.Error
	if errors.As(err, &api) && api.Status == http.StatusRequestEntityTooLarge {
		if len(batch) == 1 {
			if !u.accepted && !u.probeAfter(batch[0]) {
				return err // back off; nothing is skipped
			}
			u.o.Log(fmt.Errorf("upload: record %d (%d bytes) is larger than the server accepts; it was skipped and stays on this machine", batch[0].Seq, len(batch[0].Raw)))
			u.sum.Skipped++
			u.health.Skipped++
			return u.advance(batch[0])
		}
		mid := len(batch) / 2
		if err := u.sendAll(batch[:mid]); err != nil {
			return err
		}
		return u.sendAll(batch[mid:])
	}
	if err != nil {
		return err
	}
	u.accepted = true
	if err := u.advance(batch[len(batch)-1]); err != nil {
		return err
	}
	_ = os.Remove(config.TokenRejected(u.home))
	if resp.unread != nil {
		u.o.Log(fmt.Errorf("upload: the server accepted %d records, but its answer couldn't be read (%v); the team's rules weren't updated", len(batch), resp.unread))
	}
	if resp.Rules != nil {
		u.useRules(*resp.Rules)
	}
	u.sum.Batches++
	u.sum.Sent += len(batch)
	u.sum.Accepted += resp.Accepted
	u.sum.Duplicates += resp.Duplicates
	u.sum.ChainBreaks += resp.ChainBreaks
	u.sum.Invalid += resp.Invalid
	u.health.Refused += resp.Invalid
	return nil
}

// probeAfter sends the record after l on its own and reports whether the server took it. It
// is sent again with the rest afterwards; the server counts that as a duplicate.
func (u *uploader) probeAfter(l store.Line) bool {
	var next []store.Line
	_ = u.st.ReadFrom(store.Cursor{Seq: l.Seq, File: l.File, Offset: l.Offset}, func(x store.Line) bool {
		next = append(next, x)
		return false
	})
	if len(next) == 0 {
		return false
	}
	if _, err := send(u.ctx, u.o.Client, u.dev, u.ua, next); err != nil {
		return false
	}
	u.accepted = true
	return true
}

func (u *uploader) advance(last store.Line) error {
	return u.st.SaveCursor(store.Cursor{Seq: last.Seq, File: last.File, Offset: last.Offset})
}

// useRules caches the team's rules when they changed, and logs any the recorder can't use.
func (u *uploader) useRules(set rules.Set) {
	if reflect.DeepEqual(set, rules.Load(config.RulesPath(u.home))) {
		return
	}
	if err := rules.Save(config.RulesPath(u.home), set); err != nil {
		u.o.Log(fmt.Errorf("upload: saving the team's rules: %w", err))
		return
	}
	for _, p := range set.Problems() {
		u.o.Log(fmt.Errorf("rules: %w; it never matches until it is fixed in the dashboard", p))
	}
}

// ok notes a run that sent everything.
func (u *uploader) ok() {
	u.health.LastOK, u.health.LastError, u.health.Fails, u.health.RetryAfter = u.o.Now(), "", 0, time.Time{}
}

// failed notes why a run stopped and how long background uploads wait before the next.
func (u *uploader) failed(err error) error {
	now := u.o.Now()
	u.health.LastError = err.Error()
	var api *apierr.Error
	var ra *retryAfter
	switch {
	case errors.Is(err, ErrRevoked):
		// Remembered so `sigdek status` stops saying "Signed in" as if all were well, and so
		// hooks retry only hourly (the marker's age).
		_ = os.MkdirAll(config.StateDir(u.home), 0o700)
		_ = os.WriteFile(config.TokenRejected(u.home), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
	case errors.As(err, &api) && api.Status >= 400:
		u.health.Fails++
		wait := time.Minute << min(u.health.Fails-1, 6)
		if errors.As(err, &ra) && ra.wait > 0 {
			wait = ra.wait
		}
		u.health.RetryAfter = now.Add(min(wait, MaxBackoff))
	}
	return err
}

// retryAfter is a rate limit or server error, with the response's Retry-After.
type retryAfter struct {
	err  *apierr.Error
	wait time.Duration
}

func (r *retryAfter) Error() string { return r.err.Error() }
func (r *retryAfter) Unwrap() error { return r.err }

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}

func send(ctx context.Context, client *http.Client, dev config.Device, userAgent string, batch []store.Line) (*Response, error) {
	var body bytes.Buffer
	body.WriteString(`{"device":`)
	id, _ := json.Marshal(dev.ID)
	body.Write(id)
	body.WriteString(`,"records":[`)
	for i, l := range batch {
		if i > 0 {
			body.WriteByte(',')
		}
		body.Write(l.Raw)
	}
	body.WriteString("]}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(dev.Ingest(), "/")+"/v1/events", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+dev.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, rerr := io.ReadAll(io.LimitReader(res.Body, MaxResponse))
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		if e := apierr.New(req.URL.String(), res.StatusCode, b); e.Message != "" {
			return nil, fmt.Errorf("%w (the server said: %s)", ErrRevoked, e.Message)
		}
		return nil, ErrRevoked
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		e := apierr.New(req.URL.String(), res.StatusCode, b)
		return nil, &retryAfter{e, parseRetryAfter(res.Header.Get("Retry-After"), time.Now())}
	case res.StatusCode != http.StatusOK:
		return nil, apierr.New(req.URL.String(), res.StatusCode, b)
	}
	// 200 means the server took the batch. An answer that can't be read (cut off, or a proxy's
	// page) doesn't undo that: the cursor moves on and the rules stay as they are.
	var r Response
	if err := errors.Join(rerr, json.Unmarshal(b, &r)); err != nil {
		return &Response{Accepted: len(batch), unread: err}, nil
	}
	return &r, nil
}
