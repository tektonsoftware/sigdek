//go:build !windows

package fsretry

// transient is never true outside Windows: a rename there replaces the file atomically even
// while it's open, and readers see the old or the new copy.
func transient(error) bool { return false }
