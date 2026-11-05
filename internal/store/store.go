// Package store is the local, append-only log: one JSON record per line in
// ~/.sigdek/log/events-YYYY-MM.jsonl, chained by hash. The chain head is always read
// back from the log itself, so there is no second file that can disagree with it.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/fsretry"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
)

// LockWait is how long a hook waits for another process to finish writing before it gives
// up on this record (and logs that), so a stuck process can never hang the agent.
var LockWait = time.Second

type Store struct {
	Home string
	Now  func() time.Time // picks the month file; time.Now when nil
	Warn func(error)      // told about repairs; may be nil
}

func New(home string) *Store { return &Store{Home: home} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) warn(err error) {
	if s.Warn != nil {
		s.Warn(err)
	}
}

// Head is the last record in the chain.
type Head struct {
	Seq  int64
	Hash string
}

// Append seals the record onto the end of the chain and writes it, holding the chain lock.
func (s *Store) Append(r *event.Record) error {
	lock, err := lockfile.AcquireTimeout(config.ChainLock(s.Home), LockWait)
	if err != nil {
		return fmt.Errorf("chain lock: %w", err)
	}
	defer lock.Release()

	head, newest, err := s.head(true)
	if err != nil {
		return err
	}
	if err := r.Seal(head.Seq+1, head.Hash); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	dir := config.LogDir(s.Home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A clock that ran ahead may have left a newer month's file. Records go on the end of the
	// newest file, never an older one, so files stay in chain order whatever the clock says.
	name := "events-" + s.now().UTC().Format("2006-01") + ".jsonl"
	if newest != "" && filepath.Base(newest) > name {
		name = filepath.Base(newest)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// Head returns the current chain head. It only reads, so it works on a read-only log and
// doesn't wait for a writer.
func (s *Store) Head() (Head, error) {
	h, _, err := s.head(false)
	return h, err
}

// head finds the last readable record, newest file first, and returns the newest file's
// path. Unreadable or blank lines at the end are skipped (and reported by Verify) instead of
// stopping every later write. With repair, a partial final line left by a crash mid-write is
// cut off so the next record starts on a clean line.
func (s *Store) head(repair bool) (Head, string, error) {
	files, err := s.Files()
	if err != nil {
		return Head{}, "", err
	}
	newest := ""
	if len(files) > 0 {
		newest = files[len(files)-1]
	}
	for i := len(files) - 1; i >= 0; i-- {
		if repair {
			if err := cutPartialLine(files[i]); err != nil {
				return Head{}, "", err
			}
		}
		h, skipped, err := lastRecord(files[i])
		if err != nil {
			return Head{}, "", err
		}
		if skipped > 0 && repair {
			s.warn(fmt.Errorf("%s ends with %d unreadable line(s); continuing the chain after the last readable record (run `sigdek verify`)", filepath.Base(files[i]), skipped))
		}
		if h.Seq > 0 {
			return h, newest, nil
		}
	}
	return Head{Seq: 0, Hash: event.Genesis}, newest, nil
}

// cutPartialLine truncates anything after the file's last newline.
func cutPartialLine(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		f.Close()
		return err
	}
	last := make([]byte, 1)
	_, err = f.ReadAt(last, st.Size()-1)
	if err != nil || last[0] == '\n' {
		f.Close()
		return err
	}
	keep := int64(0)
	err = scanBack(f, st.Size(), func(_ []byte, start int64) bool {
		if start > 0 {
			keep = start
		}
		return false
	})
	f.Close()
	if err != nil {
		return err
	}
	return os.Truncate(path, keep)
}

// header is the part of a record the chain needs.
type header struct {
	Seq  int64  `json:"seq"`
	Prev string `json:"prev"`
	Hash string `json:"hash"`
}

func parseHeader(raw []byte) (header, bool) {
	var h header
	if json.Unmarshal(raw, &h) != nil || h.Seq < 1 || len(h.Hash) != 64 {
		return h, false
	}
	return h, true
}

// lastRecord returns the last readable record in a file and how many lines after it weren't
// readable. A final line with no newline yet is ignored.
func lastRecord(path string) (Head, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return Head{}, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Head{}, 0, err
	}
	var head Head
	skipped, first := 0, true
	err = scanBack(f, st.Size(), func(line []byte, _ int64) bool {
		if first {
			first = false
			return true // the text after the last newline: empty, or an unfinished write
		}
		if h, ok := parseHeader(line); ok {
			head = Head{Seq: h.Seq, Hash: h.Hash}
			return false
		}
		skipped++
		return true
	})
	return head, skipped, err
}

// scanBack calls fn for each line of f before end, last line first, with the line's start
// offset. The text after the final newline counts as the first "line". It reads backwards in
// growing chunks, so a long line costs about its own length.
func scanBack(f *os.File, end int64, fn func(line []byte, start int64) bool) error {
	pos := end
	var tail []byte // bytes [pos, pos+len(tail)) not yet handed out
	chunk := int64(64 << 10)
	for {
		if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
			if !fn(tail[i+1:], pos+int64(i)+1) {
				return nil
			}
			tail = tail[:i]
			continue
		}
		if pos == 0 {
			fn(tail, 0)
			return nil
		}
		n := min(chunk, pos)
		pos -= n
		b := make([]byte, n, n+int64(len(tail)))
		if _, err := f.ReadAt(b, pos); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		tail = append(b, tail...)
		chunk = min(chunk*2, 8<<20)
	}
}

// Files lists log files oldest first.
func (s *Store) Files() ([]string, error) {
	entries, err := os.ReadDir(config.LogDir(s.Home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "events-") && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(config.LogDir(s.Home), e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Cursor marks how far uploads have got: everything at or before Seq is on the server.
// File and Offset only say where to resume reading; Seq decides what is sent.
type Cursor struct {
	Seq    int64  `json:"seq"`
	File   string `json:"file"`   // base name of the file holding the next unsent record
	Offset int64  `json:"offset"` // byte offset of the next unsent record in File
}

func (s *Store) LoadCursor() Cursor {
	var c Cursor
	b, err := fsretry.ReadFile(config.UploadState(s.Home))
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (s *Store) SaveCursor(c Cursor) error {
	b, _ := json.Marshal(c)
	p := config.UploadState(s.Home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "upload-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	return fsretry.Rename(tmp.Name(), p)
}

// Line is one record as stored, with where it sits in the log.
type Line struct {
	Raw    []byte
	Seq    int64
	File   string
	Offset int64 // offset just past this line
}

// ReadFrom calls fn, in file order, for each record whose seq is after the cursor's, until fn
// returns false. Files whose last record is already covered are skipped without reading them,
// and the cursor's file is read from its offset; every other file is read in full, so records
// in a file that sorts before the cursor's (a clock that ran ahead) are still sent.
func (s *Store) ReadFrom(c Cursor, fn func(Line) bool) error {
	files, err := s.Files()
	if err != nil {
		return err
	}
	for _, path := range files {
		h, _, err := lastRecord(path)
		if err != nil {
			return err
		}
		if h.Seq <= c.Seq {
			continue
		}
		var offset int64
		if filepath.Base(path) == c.File {
			if st, err := os.Stat(path); err == nil && c.Offset <= st.Size() {
				offset = c.Offset
			}
		}
		cont, err := readLines(path, offset, func(raw []byte, _ int, end int64) bool {
			h, ok := parseHeader(raw)
			if !ok || h.Seq <= c.Seq {
				return true
			}
			return fn(Line{Raw: append([]byte(nil), raw...), Seq: h.Seq, File: filepath.Base(path), Offset: end})
		})
		if err != nil {
			return err
		}
		if !cont {
			return nil
		}
	}
	return nil
}

// At finds the record with the given seq and returns it with its hash; ok is false when no
// readable record has that seq.
func (s *Store) At(seq int64) (l Line, hash string, ok bool, err error) {
	if seq < 1 {
		return Line{}, "", false, nil
	}
	err = s.ReadFrom(Cursor{Seq: seq - 1}, func(x Line) bool {
		if x.Seq == seq {
			if h, good := parseHeader(x.Raw); good {
				l, hash, ok = x, h.Hash, true
			}
		}
		return false // the first record after seq-1 is the one, or there is none
	})
	return l, hash, ok, err
}

// readLines calls fn for every complete line (newline-terminated) from offset on, with its
// 1-based line number (counted from offset) and the offset just past it.
func readLines(path string, offset int64, fn func(raw []byte, lineNo int, end int64) bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	pos, n := offset, 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			pos += int64(len(line))
			n++
			if !fn(bytes.TrimRight(line, "\r\n"), n, pos) {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// Break describes a place where the local chain doesn't hold.
type Break struct {
	File   string
	Line   int   // 1-based line in File
	Seq    int64 // 0 when the line isn't a readable record
	Reason string
}

// Verify recomputes every hash and checks seq and prev continuity across all files. Lines
// that aren't readable records are reported too, not skipped.
func (s *Store) Verify() (count int64, breaks []Break, err error) {
	prev, expect := event.Genesis, int64(1)
	files, err := s.Files()
	if err != nil {
		return 0, nil, err
	}
	for _, path := range files {
		base := filepath.Base(path)
		lines := 0
		_, err := readLines(path, 0, func(raw []byte, n int, _ int64) bool {
			lines = n
			if len(bytes.TrimSpace(raw)) == 0 {
				breaks = append(breaks, Break{base, n, 0, "blank line"})
				return true
			}
			r, ok := parseHeader(raw)
			if !ok {
				breaks = append(breaks, Break{base, n, 0, "not a readable record"})
				return true
			}
			count++
			h, herr := event.HashJSON(raw)
			switch {
			case herr != nil || h != r.Hash:
				breaks = append(breaks, Break{base, n, r.Seq, "record was changed after it was written"})
			case r.Seq != expect:
				breaks = append(breaks, Break{base, n, r.Seq, fmt.Sprintf("expected seq %d", expect)})
			case r.Prev != prev:
				breaks = append(breaks, Break{base, n, r.Seq, "does not follow the previous record"})
			}
			prev, expect = r.Hash, r.Seq+1
			return true
		})
		if err != nil {
			return count, breaks, err
		}
		if unfinished(path) {
			breaks = append(breaks, Break{base, lines + 1, 0, "unfinished last line: a write was cut off, and the next record replaces it"})
		}
	}
	return count, breaks, nil
}

// unfinished reports whether a file ends part way through a line.
func unfinished(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false
	}
	b := make([]byte, 1)
	_, err = f.ReadAt(b, st.Size()-1)
	return err == nil && b[0] != '\n'
}
