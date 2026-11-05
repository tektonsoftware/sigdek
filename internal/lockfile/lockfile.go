// Package lockfile provides an exclusive, process-wide lock on a file, released when
// the process exits even if it crashes (flock on Unix, LockFileEx on Windows).
package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Lock struct{ f *os.File }

// ErrTimeout means another process held the lock for the whole wait.
var ErrTimeout = errors.New("timed out waiting for the lock")

// Acquire blocks until the lock is held.
func Acquire(path string) (*Lock, error) { return acquire(path, true) }

// TryAcquire returns (nil, nil) when another process holds the lock.
func TryAcquire(path string) (*Lock, error) { return acquire(path, false) }

// AcquireTimeout waits at most d for the lock, so a stuck process can't hang every hook.
func AcquireTimeout(path string, d time.Duration) (*Lock, error) {
	deadline := time.Now().Add(d)
	wait := time.Millisecond
	for {
		l, err := acquire(path, false)
		if err != nil || l != nil {
			return l, err
		}
		if time.Now().After(deadline) {
			return nil, ErrTimeout
		}
		time.Sleep(wait)
		wait = min(wait*2, 20*time.Millisecond)
	}
}

func acquire(path string, wait bool) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ok, err := lockFile(f, wait)
	if err != nil || !ok {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	unlockFile(l.f)
	l.f.Close()
	l.f = nil
}
