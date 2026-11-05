package config

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// QA REC-14: the first hooks of a session run at once; they must all get the same device id.
func TestConcurrentFirstUseAgreesOnOneDevice(t *testing.T) {
	for round := 0; round < 5; round++ {
		home := t.TempDir()
		ids := make([]string, 16)
		var wg sync.WaitGroup
		for i := range ids {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				d, err := LoadDevice(home)
				if err != nil {
					t.Error(err)
				}
				ids[i] = d.ID
			}(i)
		}
		wg.Wait()
		for _, id := range ids {
			if id != ids[0] || !strings.HasPrefix(id, "dev_") {
				t.Fatalf("round %d: ids differ: %v", round, ids)
			}
		}
		entries, _ := os.ReadDir(home)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Fatalf("temporary file left behind: %s", e.Name())
			}
		}
	}
}

// QA REC-16: a corrupt device.json names the file instead of a bare JSON error.
func TestCorruptDeviceFileIsNamed(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(DevicePath(home), []byte("{not json"), 0o600)
	if _, err := LoadDevice(home); err == nil || !strings.Contains(err.Error(), DevicePath(home)) {
		t.Fatalf("got %v", err)
	}
}
