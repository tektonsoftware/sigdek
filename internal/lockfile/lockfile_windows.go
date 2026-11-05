//go:build windows

package lockfile

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
	errorLockViolation      = 33
)

func lockFile(f *os.File, wait bool) (bool, error) {
	flags := uintptr(lockfileExclusiveLock)
	if !wait {
		flags |= lockfileFailImmediately
	}
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return true, nil
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errorLockViolation && !wait {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	var ol syscall.Overlapped
	procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
}
