// Package spawn starts `sigdek flush` as a detached background process, so uploading
// never happens inside the hook path.
package spawn

import (
	"os"
	"os/exec"
)

// Flush starts the upload in the background and returns immediately.
func Flush() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "flush", "--background")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
