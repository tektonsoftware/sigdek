package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/initcmd"
)

// QA1-SETUP-9, WALK-5: run through npm's launcher by npx, SigDek prints its next steps as
// `npx sigdek …`, which work when copied; run as an installed command, as `sigdek …`.
func TestNextStepsNameTheWaySigDekWasRun(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js isn't installed")
	}
	// The layout npm installs: the launcher package, and the one for this platform beside it.
	modules := filepath.Join(t.TempDir(), "node_modules")
	launcher := filepath.Join(modules, "sigdek", "bin", "sigdek.js")
	os.MkdirAll(filepath.Dir(launcher), 0o755)
	src, _ := os.ReadFile("../../npm/sigdek/bin/sigdek.js")
	os.WriteFile(launcher, src, 0o755)
	goos := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if goos == "" || arch == "" {
		t.Skip("no npm package for this platform")
	}
	platform := filepath.Join(modules, "@tektonsoftware", "sigdek-"+goos+"-"+arch, "bin", filepath.Base(binary))
	os.MkdirAll(filepath.Dir(platform), 0o755)
	b, _ := os.ReadFile(binary)
	os.WriteFile(platform, b, 0o755)

	repo := repoDir(t)
	run := func(npmEnv []string, args ...string) string {
		cmd := exec.Command(node, append([]string{launcher}, args...)...)
		cmd.Dir = repo
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "npm_") && !strings.HasPrefix(kv, "SIGDEK_") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(append(env, "SIGDEK_HOME="+sigdekHome(t)), npmEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	byNpx := []string{"npm_command=exec", "npm_lifecycle_event=npx"}
	if out := run(byNpx, "init", "--offline"); !strings.Contains(out, "Next: run `npx sigdek login`") {
		t.Errorf("init through npx:\n%s", out)
	}
	if out := run(byNpx, "status"); !strings.Contains(out, "Next: run `npx sigdek login`.") || strings.Contains(out, "run `sigdek ") {
		t.Errorf("status through npx:\n%s", out)
	}
	if out := run(nil, "status"); !strings.Contains(out, "Next: run `sigdek login`.") || strings.Contains(out, "npx") {
		t.Errorf("status as an installed command:\n%s", out)
	}
	if out := run(nil, "init", "--offline"); !strings.Contains(out, "Next: run `sigdek login`") {
		t.Errorf("init as an installed command:\n%s", out)
	}
}

// QA2-SETUP-6: --sh goes back to the sh-only Cursor hooks, which a plain init keeps once the
// project's hooks run under PowerShell; the three form flags exclude each other.
func TestInitFormFlags(t *testing.T) {
	dir, home := repoDir(t), sigdekHome(t)
	init := func(args ...string) (string, error) {
		cmd := exec.Command(binary, append([]string{"init", "--offline"}, args...)...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, step := range []struct {
		args []string
		form string
	}{
		{[]string{"--windows"}, initcmd.CursorBoth},
		{nil, initcmd.CursorBoth},
		{[]string{"--sh"}, initcmd.CursorSh},
		{[]string{"--powershell"}, initcmd.CursorPowerShell},
		{nil, initcmd.CursorBoth},
		{[]string{"--sh"}, initcmd.CursorSh},
	} {
		if out, err := init(step.args...); err != nil || initcmd.CursorForm(dir) != step.form {
			t.Fatalf("init %v: %v, form %q, want %q\n%s", step.args, err, initcmd.CursorForm(dir), step.form, out)
		}
	}
	if out, err := init("--sh", "--windows"); err == nil || !strings.Contains(out, "choose one") {
		t.Errorf("--sh --windows: %v\n%s", err, out)
	}
}
