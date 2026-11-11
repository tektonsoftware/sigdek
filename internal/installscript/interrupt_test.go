//go:build !windows

package installscript

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Ctrl-C (or a hang-up or TERM) during the download removes the temporary folder, in every
// shell, zsh in its own mode included.
func TestInterruptCleansUp(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		rel.mu.Lock()
		rel.holds["/download/v"+latest+"/sigdek_"+latest+"_"+hostTarget()] = true
		rel.mu.Unlock()
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
			m := newMachine(t, sh, rel)
			cmd := exec.Command(sh, script(t))
			cmd.Dir = m.home
			cmd.Env = []string{"HOME=" + m.home, "TMPDIR=" + m.tmp, "PATH=" + m.path, "SHELL=/bin/zsh", "LC_ALL=C", "SIGDEK_RELEASE_BASE=" + rel.base}
			var out strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &out
			// Its own process group, signalled as a whole, as a terminal does.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// Wait until the binary is partly downloaded: curl makes the file once data comes
			// (it may still hold the bytes in its buffer).
			partial := ""
			for deadline := time.Now().Add(10 * time.Second); partial == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if found, _ := filepath.Glob(filepath.Join(m.tmp, "sigdek-install.*", "sigdek_*")); len(found) > 0 {
					partial = found[0]
				}
			}
			if partial == "" {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
				t.Fatalf("%v: the download never started; output %q", sig, out.String())
			}
			syscall.Kill(-cmd.Process.Pid, sig)
			select {
			case err := <-done:
				if err == nil {
					t.Errorf("%v: exit 0 after the signal", sig)
				}
			case <-time.After(15 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
				t.Fatalf("%v: still running 15 s after the signal; output %q", sig, out.String())
			}
			if left, _ := os.ReadDir(m.tmp); len(left) > 0 {
				t.Errorf("%v: left %s in TMPDIR; output %q", sig, left[0].Name(), out.String())
			}
			notCreated(t, m.defaultDir())
		}
	})
}
