package fsretry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Readers running while another process replaces the file always get one whole copy, and
// the writer gets its turn. The readers pause between reads the way hooks do; on Windows a
// reader that never lets go would hold off a rename (callers retry on their next run).
func TestReadWhileReplacing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := os.WriteFile(p, []byte(`{"n":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 1; i <= 200; i++ {
			tmp := filepath.Join(dir, fmt.Sprintf("state-%d.tmp", i))
			if err := os.WriteFile(tmp, []byte(fmt.Sprintf(`{"n":%d}`, i)), 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := Rename(tmp, p); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				b, err := ReadFile(p)
				if err != nil {
					t.Error(err)
					return
				}
				if !bytes.HasPrefix(b, []byte(`{"n":`)) || !bytes.HasSuffix(b, []byte(`}`)) {
					t.Errorf("partial read %q", b)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

func TestMissingFileIsNotRetried(t *testing.T) {
	if _, err := ReadFile(filepath.Join(t.TempDir(), "none")); !os.IsNotExist(err) {
		t.Fatalf("got %v", err)
	}
}
