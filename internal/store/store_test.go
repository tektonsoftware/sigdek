package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
)

func rec(cmd string) *event.Record {
	now := time.Now()
	return &event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_test", Tool: "claude-code",
		Session: "s1", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "action",
		Action: &event.Action{Kind: "shell", Name: "Bash", Command: cmd, InputHash: strings.Repeat("b", 64)}, Outcome: "ran"}
}

func TestAppendChainsAndVerifies(t *testing.T) {
	s := New(t.TempDir())
	for i := 0; i < 5; i++ {
		if err := s.Append(rec("ls")); err != nil {
			t.Fatal(err)
		}
	}
	head, err := s.Head()
	if err != nil || head.Seq != 5 {
		t.Fatalf("head %+v err %v", head, err)
	}
	n, breaks, err := s.Verify()
	if err != nil || n != 5 || len(breaks) != 0 {
		t.Fatalf("verify: n=%d breaks=%v err=%v", n, breaks, err)
	}
}

func TestConcurrentAppendsKeepOneChain(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Append(rec("echo parallel")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n, breaks, err := s.Verify()
	if err != nil || n != 40 || len(breaks) != 0 {
		t.Fatalf("n=%d breaks=%v err=%v", n, breaks, err)
	}
}

func TestTamperingIsDetected(t *testing.T) {
	s := New(t.TempDir())
	for i := 0; i < 3; i++ {
		s.Append(rec("ls"))
	}
	files, _ := s.Files()
	b, _ := os.ReadFile(files[0])
	os.WriteFile(files[0], []byte(strings.Replace(string(b), `"command":"ls"`, `"command":"rm -rf /"`, 1)), 0o600)
	_, breaks, _ := s.Verify()
	if len(breaks) != 1 || breaks[0].Seq != 1 {
		t.Fatalf("breaks: %+v", breaks)
	}

	// Deleting a record breaks continuity for the one after it.
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	os.WriteFile(files[0], []byte(lines[0]+"\n"+lines[2]+"\n"), 0o600)
	_, breaks, _ = s.Verify()
	if len(breaks) != 1 || breaks[0].Seq != 3 {
		t.Fatalf("after deletion: %+v", breaks)
	}
}

func TestPartialLastLineIsRepaired(t *testing.T) {
	s := New(t.TempDir())
	s.Append(rec("ls"))
	files, _ := s.Files()
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"v":1,"id":"half-writ`)
	f.Close()
	if err := s.Append(rec("pwd")); err != nil {
		t.Fatal(err)
	}
	n, breaks, err := s.Verify()
	if err != nil || n != 2 || len(breaks) != 0 {
		t.Fatalf("n=%d breaks=%v err=%v", n, breaks, err)
	}
}

func TestReadFromCursor(t *testing.T) {
	s := New(t.TempDir())
	for i := 0; i < 6; i++ {
		s.Append(rec("ls"))
	}
	var got []int64
	var last Line
	s.ReadFrom(Cursor{}, func(l Line) bool {
		got = append(got, l.Seq)
		last = l
		return len(got) < 4
	})
	if len(got) != 4 || got[3] != 4 {
		t.Fatalf("got %v", got)
	}
	var rest []int64
	s.ReadFrom(Cursor{Seq: last.Seq, File: last.File, Offset: last.Offset}, func(l Line) bool {
		rest = append(rest, l.Seq)
		var r event.Record
		if json.Unmarshal(l.Raw, &r) != nil || r.Seq != l.Seq {
			t.Errorf("bad line at %d", l.Seq)
		}
		return true
	})
	if len(rest) != 2 || rest[0] != 5 || rest[1] != 6 {
		t.Fatalf("rest %v", rest)
	}
}

// writeRaw seals r without the field limits (as a recorder before them could) and appends it.
func writeRaw(t *testing.T, path string, r *event.Record, seq int64, prev string) string {
	t.Helper()
	r.Seq, r.Prev, r.Hash = seq, prev, ""
	b, _ := json.Marshal(r)
	h, err := event.HashJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	r.Hash = h
	b, _ = json.Marshal(r)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(b, '\n'))
	f.Close()
	return h
}

func logFile(home, month string) string {
	os.MkdirAll(filepath.Join(home, "log"), 0o700)
	return filepath.Join(home, "log", "events-"+month+".jsonl")
}

// QA REC-1: a record over the old 64 KB read window used to stop every later write.
func TestLongLastRecordDoesNotWedge(t *testing.T) {
	home := t.TempDir()
	r := rec(strings.Repeat("x", 300<<10))
	writeRaw(t, logFile(home, "2026-10"), r, 1, event.Genesis)
	s := New(home)
	if err := s.Append(rec("ls")); err != nil {
		t.Fatal(err)
	}
	n, breaks, err := s.Verify()
	if err != nil || n != 2 || len(breaks) != 0 {
		t.Fatalf("n=%d breaks=%v err=%v", n, breaks, err)
	}
}

// QA REC-1: an unreadable last line is skipped (and reported), not a wall; a trailing blank
// line must not restart the chain at seq 1.
func TestUnreadableOrBlankLastLineIsSkipped(t *testing.T) {
	for _, junk := range []string{"not a record\n", "\n", "{\"seq\":\"x\"}\n\n"} {
		home := t.TempDir()
		var warned []error
		s := &Store{Home: home, Warn: func(err error) { warned = append(warned, err) }}
		s.Append(rec("one"))
		s.Append(rec("two"))
		files, _ := s.Files()
		f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString(junk)
		f.Close()
		if err := s.Append(rec("three")); err != nil {
			t.Fatalf("%q: %v", junk, err)
		}
		if h, _ := s.Head(); h.Seq != 3 {
			t.Fatalf("%q: head %+v, want seq 3", junk, h)
		}
		n, breaks, _ := s.Verify()
		if n != 3 || len(breaks) == 0 || breaks[0].Line != 3 || breaks[0].Seq != 0 {
			t.Fatalf("%q: verify should report the bad line and nothing else: n=%d %+v", junk, n, breaks)
		}
		for _, b := range breaks {
			if b.Seq != 0 {
				t.Fatalf("%q: chain broken around the bad line: %+v", junk, breaks)
			}
		}
		if len(warned) != 1 {
			t.Fatalf("%q: want one warning, got %v", junk, warned)
		}
	}
}

// QA REC-2: a clock that ran ahead leaves a newer month's file; later records must go on the
// end of it, not fork the chain in the current month's file.
func TestClockAheadDoesNotForkTheChain(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := &Store{Home: home, Now: func() time.Time { return now }}
	s.Append(rec("a"))
	now = time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC) // clock jumps ahead
	s.Append(rec("b"))
	now = time.Date(2026, 10, 30, 12, 5, 0, 0, time.UTC) // and is corrected
	s.Append(rec("c"))
	s.Append(rec("d"))
	n, breaks, err := s.Verify()
	if err != nil || n != 4 || len(breaks) != 0 {
		t.Fatalf("n=%d breaks=%v err=%v", n, breaks, err)
	}
	if h, _ := s.Head(); h.Seq != 4 {
		t.Fatalf("head %+v", h)
	}
	b, _ := os.ReadFile(logFile(home, "2027-01"))
	if strings.Count(string(b), "\n") != 3 {
		t.Fatalf("records after the jump should follow it in the newest file:\n%s", b)
	}
}

// QA REC-2: uploads are driven by seq, so records in a file that sorts before the cursor's
// file (left by an older recorder after a clock jump) are still sent.
func TestReadFromFollowsSeqAcrossFiles(t *testing.T) {
	home := t.TempDir()
	older, newer := logFile(home, "2026-10"), logFile(home, "2027-01")
	prev := event.Genesis
	var newerSize int64
	for seq := int64(1); seq <= 5; seq++ {
		path := older
		if seq == 3 {
			path = newer
		}
		prev = writeRaw(t, path, rec("x"), seq, prev)
		if seq == 3 {
			st, _ := os.Stat(newer)
			newerSize = st.Size()
		}
	}
	var got []int64
	New(home).ReadFrom(Cursor{Seq: 3, File: filepath.Base(newer), Offset: newerSize}, func(l Line) bool {
		got = append(got, l.Seq)
		return true
	})
	if len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("got %v, want [4 5]", got)
	}
	// A cursor with only a seq (as set at sign-in) works too, and a stale offset is ignored.
	got = nil
	New(home).ReadFrom(Cursor{Seq: 2, File: filepath.Base(older), Offset: 1 << 40}, func(l Line) bool {
		got = append(got, l.Seq)
		return true
	})
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 records after seq 2", got)
	}
}

// QA REC-12: a process stuck holding the chain lock costs a hook about a second, not forever.
func TestAppendGivesUpOnAStuckLock(t *testing.T) {
	home := t.TempDir()
	LockWait = 200 * time.Millisecond
	defer func() { LockWait = time.Second }()
	held, err := lockfile.Acquire(config.ChainLock(home))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	start := time.Now()
	err = New(home).Append(rec("ls"))
	if !errors.Is(err, lockfile.ErrTimeout) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

// QA REC-16: status reads the head of a read-only log.
func TestHeadWorksOnAReadOnlyLog(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root Unix user for permissions to apply")
	}
	home := t.TempDir()
	s := New(home)
	s.Append(rec("ls"))
	files, _ := s.Files()
	os.Chmod(files[0], 0o400)
	os.Chmod(filepath.Join(home, "log"), 0o500)
	defer os.Chmod(filepath.Join(home, "log"), 0o700)
	if h, err := s.Head(); err != nil || h.Seq != 1 {
		t.Fatalf("head %+v err %v", h, err)
	}
}
