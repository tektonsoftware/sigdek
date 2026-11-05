//go:build windows

package fsretry

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied     = 5
	errorSharingViolation = 32
	errorLockViolation    = 33
)

// transient reports the errors Windows returns while another process is replacing, or
// holding open, the same file.
func transient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errorSharingViolation || errno == errorAccessDenied || errno == errorLockViolation
}
