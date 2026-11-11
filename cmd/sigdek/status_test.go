package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/initcmd"
	"github.com/tektonsoftware/sigdek/internal/store"
)

// D9: status answers "did it work?" as a checklist, with a next step for anything missing.

type statusCase struct {
	t    *testing.T
	env  statusEnv
	root string
}

func newStatusCase(t *testing.T) *statusCase {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	return &statusCase{t: t, root: root, env: statusEnv{home: t.TempDir(), cwd: root, claudeDir: t.TempDir(), goos: runtime.GOOS, now: time.Now()}}
}

func (c *statusCase) out() string {
	c.env.now = time.Now()
	var b strings.Builder
	writeStatus(&b, c.env)
	return b.String()
}

func (c *statusCase) has(want ...string) string {
	c.t.Helper()
	out := c.out()
	for _, w := range want {
		if !strings.Contains(out, w) {
			c.t.Errorf("status lacks %q:\n%s", w, out)
		}
	}
	return out
}

// recorder is a stand-in binary and its checksum.
var recorderBytes = []byte("#!/bin/sh\nexit 0\n")

func recorderSum() string {
	s := sha256.Sum256(recorderBytes)
	return hex.EncodeToString(s[:])
}

func binName() string {
	if runtime.GOOS == "windows" {
		return "sigdek.exe"
	}
	return "sigdek"
}

// project sets up SigDek's hooks, pinned to 7.0.0 for this platform.
func (c *statusCase) project(ps bool) {
	if _, err := initcmd.Run(initcmd.Options{Root: c.root, Version: "dev", Tools: []string{"claude-code,cursor"}, PowerShell: ps}); err != nil {
		c.t.Fatal(err)
	}
	lock := "version=7.0.0\nurl=https://example.invalid\n" + runtime.GOOS + "_" + runtime.GOARCH + "=" + recorderSum() + "\n"
	os.WriteFile(filepath.Join(c.root, ".sigdek", "recorder.lock"), []byte(lock), 0o644)
}

func (c *statusCase) cached(dir string, body []byte) {
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, binName()), body, 0o755)
}

func (c *statusCase) signIn() {
	config.SaveDevice(c.env.home, config.Device{ID: "dev_status", Token: "tok", User: "ana@larkspur.example", OrgName: "Larkspur Labs", IngestURL: "http://127.0.0.1:9"})
}

func (c *statusCase) health(json string) {
	os.MkdirAll(config.StateDir(c.env.home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(c.env.home), "upload-health.json"), []byte(json), 0o600)
}

func TestStatusForATeamProject(t *testing.T) {
	c := newStatusCase(t)
	c.project(false)
	out := c.has(
		"[x] Claude Code records in this project, through its hooks in .claude/settings.json",
		"[x] Cursor records in this project, through its hooks in .cursor/hooks.json",
		"[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet",
		"It downloads, and is checked, the first time Claude Code or Cursor runs here.",
		"[ ] This machine isn't connected to a team, so its records stay on it\n    Next: run `sigdek login`.",
		"[ ] No records on this machine yet",
	)
	if strings.Contains(out, "Windows") {
		t.Errorf("the default Cursor hooks work on Windows:\n%s", out)
	}

	// Downloading, then failing, then downloaded but not matching, then good.
	dir := filepath.Join(c.env.home, "bin", "7.0.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".downloading"), nil, 0o644)
	c.has("[ ] SigDek 7.0.0, the recorder this project pins, is downloading")
	os.Remove(filepath.Join(dir, ".downloading"))
	os.WriteFile(filepath.Join(dir, ".download-failed"), []byte("3 "+recorderSum()+"\n"), 0o644)
	c.has("isn't downloaded: 3 downloads failed, the last just now", "Next: check that this machine can reach github.com.")
	c.cached(dir, []byte("something else"))
	c.has("[ ] The downloaded SigDek 7.0.0 doesn't match the checksum this project pins, yet the launcher runs it")
	c.cached(dir, recorderBytes)
	c.has("[x] SigDek 7.0.0, the recorder this project pins, is downloaded and matches its checksum")

	// SIGDEK_CACHE moves the cache, as it does for the launchers.
	c.env.cache = t.TempDir()
	c.has("[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet")
	c.cached(filepath.Join(c.env.cache, "7.0.0"), recorderBytes)
	c.has("[x] SigDek 7.0.0, the recorder this project pins, is downloaded")
	c.env.cache = ""

	// Connected, with uploads in each state.
	c.signIn()
	c.has("[x] Connected to Larkspur Labs as ana@larkspur.example", "[ ] Nothing has uploaded yet, because there are no records yet")
	runHook(t, c.env.home, []string{"claude-code"}, cc(c.root, "UserPromptSubmit", `,"prompt":"x"`))
	waitForUploader(t, c.env.home)
	c.health(`{"last_ok":"` + time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339) + `"}`)
	st := store.New(c.env.home)
	st.SaveCursor(store.Cursor{})
	c.has("[x] Uploads work: the last one finished 3 min ago, and 1 record is waiting", "They go up when the agent finishes its turn")
	head, _ := st.Head()
	st.SaveCursor(store.Cursor{Seq: head.Seq})
	out = c.has("[x] Uploads work: the last one finished 3 min ago, and nothing is waiting", "Records     1 on this machine, the newest written")
	if strings.Contains(out, "[ ]") {
		t.Errorf("everything works, but status lists something to do:\n%s", out)
	}
	c.health(`{"last_try":"` + time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339) + `","last_error":"ingest returned 503"}`)
	c.has("[ ] The last upload, 2 min ago, failed: ingest returned 503", "Next: run `sigdek flush` to try again now; nothing is waiting.")

	// Started in a subfolder, the project's hooks don't run.
	sub := filepath.Join(c.root, "packages", "web")
	os.MkdirAll(sub, 0o755)
	c.env.cwd = sub
	c.has("[ ] Claude Code records this project only when it starts in the project's top folder",
		"[ ] Cursor records this project only when the project's top folder is its workspace")
	c.env.cwd = c.root

	// A missing launcher.
	os.Remove(filepath.Join(c.root, ".sigdek", "launch.sh"))
	c.has("[ ] This project's hooks run .sigdek/launch.sh, which is missing, so they record nothing")
}

func TestStatusForCursorHookForms(t *testing.T) {
	c := newStatusCase(t)
	c.project(false)
	// The sh-only hooks `sigdek init` wrote up to 0.1.0.
	p := filepath.Join(c.root, ".cursor", "hooks.json")
	os.WriteFile(p, []byte(`{"version":1,"hooks":{"stop":[{"command":"sh .sigdek/launch.sh hook cursor stop | grep '^{' || echo '{}'"}]}}`), 0o644)
	if initcmd.CursorForm(c.root) != initcmd.CursorSh {
		t.Fatalf("form %q", initcmd.CursorForm(c.root))
	}
	c.env.goos = "darwin"
	c.has("[x] Cursor records in this project", "Cursor on Windows can't run these hooks. If teammates use Cursor on Windows, run `sigdek init`")
	c.env.goos = "windows"
	c.has("[ ] Cursor records nothing on this machine: Cursor on Windows can't run this project's hooks")

	// --powershell hooks, read on macOS or Linux.
	c.project(true)
	c.env.goos = "linux"
	c.has("[ ] Cursor records nothing on this machine: this project's Cursor hooks need PowerShell")
	c.env.goos = "windows"
	c.has("[x] Cursor records in this project")

	// On Windows, Claude Code needs Git Bash to run hooks.
	c.env.gitBash = func() bool { return false }
	c.has("[ ] Claude Code on Windows runs hooks through Git Bash, and it isn't installed here")
	c.env.gitBash = func() bool { return true }
	if out := c.out(); strings.Contains(out, "Git Bash") {
		t.Errorf("Git Bash named while it's there:\n%s", out)
	}
}

func TestStatusForThePlugin(t *testing.T) {
	c := newStatusCase(t)
	os.WriteFile(filepath.Join(c.env.claudeDir, "settings.json"), []byte(`{"enabledPlugins":{"sigdek@sigdek":true}}`), 0o644)
	out := c.has(
		"[x] Claude Code records here, through SigDek's plugin (turned on in ",
		"[-] Cursor: this project has no SigDek hooks for it",
		"[ ] The recorder SigDek's plugin runs isn't downloaded yet",
		"It downloads, and is checked, the first time Claude Code runs with the plugin on.",
	)
	if strings.Contains(out, "Nothing records") {
		t.Errorf("the plugin records here:\n%s", out)
	}

	// With Claude Code's list of installed plugins, status checks the plugin's own pin.
	install := filepath.Join(c.env.claudeDir, "plugins", "cache", "sigdek", "sigdek", "7.0.0")
	os.MkdirAll(filepath.Join(install, "scripts"), 0o755)
	os.WriteFile(filepath.Join(install, "scripts", "recorder.lock"), []byte("version=7.0.0\n"+runtime.GOOS+"_"+runtime.GOARCH+"="+recorderSum()+"\n"), 0o644)
	os.WriteFile(filepath.Join(c.env.claudeDir, "plugins", "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"sigdek@sigdek":[{"scope":"user","installPath":`+jsonString(install)+`,"version":"7.0.0"}]}}`), 0o644)
	c.has("[ ] SigDek 7.0.0, the recorder SigDek's plugin pins, isn't downloaded yet")
	c.cached(filepath.Join(c.env.claudeDir, "plugins", "data", "sigdek-sigdek", "7.0.0"), recorderBytes)
	c.has("[x] SigDek 7.0.0, the recorder SigDek's plugin pins, is downloaded and matches its checksum")

	// In a project with its own hooks, at its top folder, the project's hooks record.
	c.project(false)
	c.has("[x] Claude Code records in this project", "[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet")
	// In a subfolder, the plugin does.
	sub := filepath.Join(c.root, "app")
	os.MkdirAll(sub, 0o755)
	c.env.cwd = sub
	c.has("[x] Claude Code records here, through SigDek's plugin", "Started in ", "[x] SigDek 7.0.0, the recorder SigDek's plugin pins, is downloaded")
}

func TestStatusOutsideAnyProject(t *testing.T) {
	c := newStatusCase(t)
	c.env.cwd = t.TempDir()
	c.has("[ ] Nothing records Claude Code or Cursor in this folder yet", "https://sigdek.com/start/")
	c.env.bin = os.Args[0]
	c.has("SIGDEK_BIN is set, so hooks started from here run ")
	// Status only reads.
	if entries, _ := os.ReadDir(c.env.home); len(entries) != 0 {
		t.Errorf("status wrote %v", entries)
	}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
