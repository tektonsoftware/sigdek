// Package fsretry reads and replaces small state files (device.json, rules.json, the upload
// cursor) that other recorder processes may be replacing at the same moment.
//
// On Windows, opening a file while another process renames a new copy over it, or renaming
// over a file another process has open, fails for a moment with a sharing error. Hooks run
// side by side, so these calls retry for a short while there. Elsewhere they run once.
package fsretry

import (
	"os"
	"time"
)

// Budget is how long a call keeps retrying a sharing error before giving up.
var Budget = 500 * time.Millisecond

const pause = 5 * time.Millisecond

// ReadFile is os.ReadFile, retried while the file is briefly locked by a replace. On Windows
// it also lets a writer replace the file while it's open (see readfile_windows.go).
func ReadFile(path string) ([]byte, error) {
	var b []byte
	err := retry(func() (err error) {
		b, err = readFile(path)
		return err
	})
	return b, err
}

// Rename is os.Rename, retried while a reader briefly holds the target open. It can still
// fail if readers keep the file open for the whole budget; callers write these files again
// on their next run.
func Rename(from, to string) error {
	return retry(func() error { return os.Rename(from, to) })
}

func retry(op func() error) error {
	deadline := time.Now().Add(Budget)
	for {
		err := op()
		if err == nil || !transient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(pause)
	}
}
