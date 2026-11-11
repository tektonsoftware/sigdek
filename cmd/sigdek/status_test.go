package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func (c *statusCase) lacks(not ...string) {
	c.t.Helper()
	out := c.out()
	for _, n := range not {
		if strings.Contains(out, n) {
			c.t.Errorf("status has %q:\n%s", n, out)
		}
	}
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

// project sets up SigDek's hooks for tools, with Cursor's in a form, pinned to 7.0.0 for this
// platform.
func (c *statusCase) project(tools, form string) {
	if _, err := initcmd.Run(initcmd.Options{Root: c.root, Version: "dev", Tools: []string{tools}, CursorForm: form}); err != nil {
		c.t.Fatal(err)
	}
	var sums []string
	for _, goos := range []string{"darwin", "linux", "windows"} {
		sums = append(sums, goos+"_"+runtime.GOARCH+"="+recorderSum())
	}
	c.lock("7.0.0", sums...)
}

func (c *statusCase) lock(version string, sums ...string) {
	lock := "version=" + version + "\nurl=https://example.invalid\n" + strings.Join(sums, "\n") + "\n"
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

const (
	ccRecords    = "[x] Claude Code records in this project, through its hooks in .claude/settings.json"
	cursorRecord = "[x] Cursor records in this project, through its hooks in .cursor/hooks.json"
	ccBlocked    = "[ ] Claude Code has hooks in this project (.claude/settings.json), but they record nothing"
	cuBlocked    = "[ ] Cursor has hooks in this project (.cursor/hooks.json), but they record nothing"
)

func TestStatusForATeamProject(t *testing.T) {
	c := newStatusCase(t)
	c.project("claude-code,cursor", initcmd.CursorBoth)
	out := c.has(
		ccRecords, cursorRecord,
		"[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet",
		"It downloads, and is checked, the first time Claude Code or Cursor runs here.",
		"[ ] This machine isn't connected to a team, so its records stay on it\n    Next: run `sigdek login`.",
		"[ ] No records on this machine yet\n    Next: start Claude Code or Cursor here",
	)
	if strings.Contains(out, "Cursor on Windows can't run") {
		t.Errorf("the sh-and-PowerShell hooks work on Windows:\n%s", out)
	}

	// The recorder downloading: the hooks record once it's done.
	dir := filepath.Join(c.env.home, "bin", "7.0.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".downloading"), nil, 0o644)
	c.has(ccRecords, "[ ] SigDek 7.0.0, the recorder this project pins, is downloading")
	os.Remove(filepath.Join(dir, ".downloading"))

	// QA1-SETUP-1: while downloads fail, nothing records, and status says so first.
	os.WriteFile(filepath.Join(dir, ".download-failed"), []byte("3 "+recorderSum()+"\n"), 0o644)
	c.has(ccBlocked, cuBlocked, "Nothing is recorded until the problem below is fixed.",
		"isn't downloaded: 3 downloads failed, the last just now", "Next: check that this machine can reach github.com.")
	c.lacks(ccRecords, cursorRecord, "No records on this machine yet")

	// QA1-SETUP-11: a cached recorder the launcher can't run, as the launcher sees it.
	c.cached(dir, recorderBytes)
	if runtime.GOOS != "windows" {
		os.Chmod(filepath.Join(dir, binName()), 0o644)
		c.has(ccRecords, "[ ] SigDek 7.0.0, the recorder this project pins, is downloaded but can't be run (it isn't executable)")
		os.Chmod(filepath.Join(dir, binName()), 0o755)
	}
	c.cached(dir, []byte("something else"))
	c.has("[ ] The downloaded SigDek 7.0.0 doesn't match the checksum this project pins, yet the launcher runs it")
	c.cached(dir, recorderBytes)
	c.has(ccRecords, "[x] SigDek 7.0.0, the recorder this project pins, is downloaded and matches its checksum")

	// SIGDEK_CACHE moves the cache, as it does for the launchers.
	c.env.cache = t.TempDir()
	c.has("[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet")
	c.cached(filepath.Join(c.env.cache, "7.0.0"), recorderBytes)
	c.has("[x] SigDek 7.0.0, the recorder this project pins, is downloaded")
	c.env.cache = ""

	// QA1-SETUP-1: no build for this platform, or a development pin: nothing records.
	c.lock("7.0.0", "plan9_amd64="+recorderSum())
	c.has(ccBlocked, cuBlocked, "recorder.lock has no build of SigDek 7.0.0 for "+runtime.GOOS+"/"+runtime.GOARCH)
	c.lock("dev", runtime.GOOS+"_"+runtime.GOARCH+"="+recorderSum())
	c.has(ccBlocked, "development build of SigDek")
	c.env.bin = os.Args[0] // SIGDEK_BIN runs a build of your own instead
	c.has(ccRecords, "SIGDEK_BIN is set, so hooks started from here run ")
	c.env.bin = ""
	c.lock("7.0.0", runtime.GOOS+"_"+runtime.GOARCH+"="+recorderSum())

	// Connected, with uploads in each state.
	c.signIn()
	c.has("[x] Connected to Larkspur Labs as ana@larkspur.example", "[ ] Nothing has uploaded yet, because there are no records yet")
	// A record made while signed out, so the hook starts no upload that could race this test.
	config.SaveDevice(c.env.home, config.Device{ID: "dev_status"})
	runHook(t, c.env.home, []string{"claude-code"}, cc(c.root, "UserPromptSubmit", `,"prompt":"x"`))
	c.signIn()
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
	c.health(`{"last_try":"` + time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339) + `","last_error":"ingest returned 503","retry_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`)
	c.has("[ ] Uploads wait until ", "Next: `sigdek flush` tries now")

	// QA1-SETUP-13: after the server rejected the token, the only next step is to sign in again.
	os.MkdirAll(config.StateDir(c.env.home), 0o700)
	os.WriteFile(config.TokenRejected(c.env.home), nil, 0o600)
	c.has("but the server rejected this machine's token", "Next: run `sigdek login` again.", "[ ] Uploads have stopped until this machine signs in again")
	c.lacks("sigdek flush")
	os.Remove(config.TokenRejected(c.env.home))

	// Started in a subfolder, the project's hooks don't run.
	sub := filepath.Join(c.root, "packages", "web")
	os.MkdirAll(sub, 0o755)
	c.env.cwd = sub
	c.has("[ ] Claude Code records this project only when it starts in the project's top folder",
		"[ ] Cursor records this project only when the project's top folder is its workspace")
	c.env.cwd = c.root

	// QA1-SETUP-1: a missing launcher; nothing records, and there's no "start the agent" step.
	os.Remove(filepath.Join(c.root, ".sigdek", "launch.sh"))
	c.has(ccBlocked, cuBlocked, "[ ] This project's hooks run .sigdek/launch.sh, which is missing")
}

// QA1-SETUP-10: a Cursor-only project's next step names Cursor alone.
func TestStatusForACursorOnlyProject(t *testing.T) {
	c := newStatusCase(t)
	c.project("cursor", initcmd.CursorBoth)
	c.has(cursorRecord, "Next: start Cursor here and give the agent a task.")
	c.lacks("Claude Code or Cursor here")
}

func TestStatusForCursorHookForms(t *testing.T) {
	c := newStatusCase(t)
	c.project("claude-code,cursor", initcmd.CursorSh)
	c.env.goos = "darwin"
	c.has(cursorRecord, "Cursor on Windows can't run these hooks. If anyone uses Cursor on Windows here, run",
		"`sigdek init --windows` and commit the change.")
	c.env.goos = "windows"
	c.has("[ ] Cursor records nothing on this machine: Cursor on Windows can't run this project's Cursor hooks",
		"Next: run `sigdek init --windows` in ")
	// The sh form 0.1.0 wrote is the same.
	p := filepath.Join(c.root, ".cursor", "hooks.json")
	os.WriteFile(p, []byte(`{"version":1,"hooks":{"stop":[{"command":"sh .sigdek/launch.sh hook cursor stop | grep '^{' || echo '{}'"}]}}`), 0o644)
	c.has("Cursor on Windows can't run this project's Cursor hooks")

	// --powershell hooks, read on macOS or Linux; 0.1.0's, with Bypass, too.
	c.project("claude-code,cursor", initcmd.CursorPowerShell)
	c.env.goos = "linux"
	c.has("[ ] Cursor records nothing on this machine: this project's Cursor hooks need PowerShell")
	os.WriteFile(p, []byte(`{"version":1,"hooks":{"stop":[{"command":"powershell -NoProfile -ExecutionPolicy Bypass -File .sigdek/launch.ps1 hook cursor stop"}]}}`), 0o644)
	c.has("this project's Cursor hooks need PowerShell")
	c.lacks("older sigdek init")
	c.env.goos = "windows"
	c.has(cursorRecord)

	// QA1-SETUP-12: mixed forms, one event left in the PowerShell-only form.
	c.project("claude-code,cursor", initcmd.CursorSh)
	b, _ := os.ReadFile(p)
	stop := `sh .sigdek/launch.sh hook cursor stop | grep '^{' || echo '{}'`
	mixed := strings.Replace(string(b), strings.NewReplacer(`'`, `'`, `"`, `\"`).Replace(stop), "powershell -NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -File .sigdek/launch.ps1 hook cursor stop", 1)
	if mixed == string(b) {
		t.Fatal("couldn't mix the forms")
	}
	os.WriteFile(p, []byte(mixed), 0o644)
	c.env.goos = "darwin"
	c.has("[ ] Cursor records only some of what it does on this machine")
	c.lacks(cursorRecord)

	// On Windows, Claude Code needs Git Bash to run hooks.
	c.project("claude-code,cursor", initcmd.CursorBoth)
	c.env.goos = "windows"
	c.env.gitBash = func() bool { return false }
	c.has("[ ] Claude Code on Windows runs hooks through Git Bash, and it isn't installed here")
	c.env.gitBash = func() bool { return true }
	c.lacks("Git Bash")
}

func TestStatusForThePlugin(t *testing.T) {
	c := newStatusCase(t)
	os.WriteFile(filepath.Join(c.env.claudeDir, "settings.json"), []byte(`{"enabledPlugins":{"sigdek@sigdek":true}}`), 0o644)
	// QA1-SETUP-14: without Claude Code's list of installed plugins, status doesn't say which
	// version the plugin runs.
	c.cached(filepath.Join(c.env.claudeDir, "plugins", "data", "sigdek-sigdek", "6.0.0"), recorderBytes)
	out := c.has(
		"[x] Claude Code records here, through SigDek's plugin (turned on in ",
		"[-] Cursor: this project has no SigDek hooks for it",
		"[ ] Can't tell which recorder SigDek's plugin runs: Claude Code's list of installed plugins doesn't say",
		"Downloaded for the plugin: SigDek 6.0.0.",
	)
	if strings.Contains(out, "Nothing records") || strings.Contains(out, "6.0.0, the recorder") {
		t.Errorf("plugin status:\n%s", out)
	}

	// With the list, status checks the install whose scope the setting stands for.
	install := func(version string) string {
		dir := filepath.Join(c.env.claudeDir, "plugins", "cache", "sigdek", "sigdek", version)
		os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
		os.WriteFile(filepath.Join(dir, "scripts", "recorder.lock"), []byte("version="+version+"\n"+runtime.GOOS+"_"+runtime.GOARCH+"="+recorderSum()+"\n"), 0o644)
		return jsonString(dir)
	}
	os.WriteFile(filepath.Join(c.env.claudeDir, "plugins", "installed_plugins.json"), []byte(`{"version":2,"plugins":{"sigdek@sigdek":[`+
		`{"scope":"project","projectPath":`+jsonString(t.TempDir())+`,"installPath":`+install("5.0.0")+`},`+
		`{"scope":"local","projectPath":`+jsonString(c.root)+`,"installPath":`+install("8.0.0")+`},`+
		`{"scope":"user","installPath":`+install("7.0.0")+`,"version":"7.0.0"}]}}`), 0o644)
	c.has("[ ] SigDek 7.0.0, the recorder SigDek's plugin pins, isn't downloaded yet",
		"It downloads, and is checked, the first time Claude Code runs with the plugin on.")
	c.cached(filepath.Join(c.env.claudeDir, "plugins", "data", "sigdek-sigdek", "7.0.0"), recorderBytes)
	c.has("[x] SigDek 7.0.0, the recorder SigDek's plugin pins, is downloaded and matches its checksum")
	// Turned on for this project only: its local install.
	os.MkdirAll(filepath.Join(c.root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(c.root, ".claude", "settings.local.json"), []byte(`{"enabledPlugins":{"sigdek@sigdek":true}}`), 0o644)
	c.has("[ ] SigDek 8.0.0, the recorder SigDek's plugin pins, isn't downloaded yet")
	os.Remove(filepath.Join(c.root, ".claude", "settings.local.json"))

	// In a project with its own hooks, at its top folder, the project's hooks record.
	c.project("claude-code,cursor", initcmd.CursorBoth)
	c.has(ccRecords, "[ ] SigDek 7.0.0, the recorder this project pins, isn't downloaded yet")
	// In a subfolder, the plugin does.
	sub := filepath.Join(c.root, "app")
	os.MkdirAll(sub, 0o755)
	c.env.cwd = sub
	c.has("[x] Claude Code records here, through SigDek's plugin", "Started in ", "[x] SigDek 7.0.0, the recorder SigDek's plugin pins, is downloaded")
	// A plugin whose recorder can't download records nothing.
	os.RemoveAll(filepath.Join(c.env.claudeDir, "plugins", "data", "sigdek-sigdek", "7.0.0"))
	failed := filepath.Join(c.env.claudeDir, "plugins", "data", "sigdek-sigdek", "7.0.0")
	os.MkdirAll(failed, 0o755)
	os.WriteFile(filepath.Join(failed, ".download-failed"), []byte("2 x\n"), 0o644)
	c.has("[ ] SigDek's Claude Code plugin is on here (turned on in ", "but it records nothing", "2 downloads failed")
}

func TestStatusOutsideAnyProject(t *testing.T) {
	c := newStatusCase(t)
	c.env.cwd = t.TempDir()
	c.has("[ ] Nothing records Claude Code or Cursor in this folder yet", "https://sigdek.com/start/")
	// Status only reads.
	if entries, _ := os.ReadDir(c.env.home); len(entries) != 0 {
		t.Errorf("status wrote %v", entries)
	}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// QA2-SETUP-1: a hook file that isn't valid JSON, as after a bad merge, loads no hooks; status
// says so for that file, gives a next step that works, and never says it records.
func TestStatusForHookFilesThatDontParse(t *testing.T) {
	conflict := func(path string) {
		b, _ := os.ReadFile(path)
		lines := strings.SplitAfter(string(b), "\n")
		os.WriteFile(path, []byte(strings.Join(lines[:4], "")+"<<<<<<< HEAD\n"+lines[4]+"=======\n"+lines[4]+">>>>>>> feature\n"+strings.Join(lines[5:], "")), 0o644)
	}
	for _, goos := range []string{"darwin", "windows"} {
		c := newStatusCase(t)
		c.env.goos = goos
		c.project("claude-code,cursor", initcmd.CursorBoth)
		conflict(filepath.Join(c.root, ".cursor", "hooks.json"))
		c.has(ccRecords, "[ ] .cursor/hooks.json isn't valid JSON (a merge conflict?), so Cursor loads no hooks from it",
			"Next: fix it first (`sigdek init` won't change a file that isn't valid JSON)")
		c.lacks(cursorRecord, "need PowerShell", "can't run this project's Cursor hooks", "Cursor: this project has no SigDek hooks")
		conflict(filepath.Join(c.root, ".claude", "settings.json"))
		c.has("[ ] .claude/settings.json isn't valid JSON (a merge conflict?), so Claude Code loads no hooks from it")
		c.lacks(ccRecords, "No records on this machine yet")
		// With SigDek's plugin on, its hooks still step aside there (launch.sh judges the file by
		// its text), so nothing records at the top folder.
		os.WriteFile(filepath.Join(c.env.claudeDir, "settings.json"), []byte(`{"enabledPlugins":{"sigdek@sigdek":true}}`), 0o644)
		c.lacks("through SigDek's plugin")
	}
}

// QA2-SETUP-2, -4: on Windows, Cursor's PowerShell forms run launch.ps1, so status checks that
// file, and its downloaded-file mark, and says what it can't see.
func TestStatusForLaunchPs1OnWindows(t *testing.T) {
	c := newStatusCase(t)
	c.env.goos = "windows"
	c.env.gitBash = func() bool { return true }
	c.project("claude-code,cursor", initcmd.CursorBoth)
	ps1 := filepath.Join(c.root, ".sigdek", "launch.ps1")
	c.has(cursorRecord, "On Windows they run .sigdek/launch.ps1 under PowerShell's RemoteSigned policy.",
		"`Unblock-File .sigdek\\launch.ps1` removes the mark", "a network share or on Windows Server Core. Status can't see a refusal.")
	// Marked as downloaded from the internet: on Windows the Zone.Identifier stream; elsewhere a
	// file by that name stands in for it.
	if err := os.WriteFile(ps1+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.has("[ ] .sigdek/launch.ps1 is marked as downloaded from the internet, so PowerShell refuses to run it",
		"`Unblock-File .sigdek\\launch.ps1`.")
	c.lacks(cursorRecord)
	os.WriteFile(ps1+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=1\r\n"), 0o644) // the local intranet
	c.has(cursorRecord)
	os.Remove(ps1)
	c.has("[ ] Cursor has hooks in this project, but on Windows they run .sigdek/launch.ps1, which is missing")
	c.lacks(cursorRecord)
	// Claude Code runs launch.sh through Git Bash, so it still records.
	c.has(ccRecords)
	// On macOS, launch.ps1 doesn't matter, and there's no PowerShell note.
	c.env.goos = "darwin"
	c.has(cursorRecord)
	c.lacks("launch.ps1")
}

// QA2-SETUP-8: on macOS, a project whose Cursor hooks are partly sh-only gets the Windows note.
func TestStatusNotesPartlyShOnlyCursorHooks(t *testing.T) {
	c := newStatusCase(t)
	c.env.goos = "darwin"
	c.project("claude-code,cursor", initcmd.CursorBoth)
	p := filepath.Join(c.root, ".cursor", "hooks.json")
	var hooks struct {
		Version int                                   `json:"version"`
		Hooks   map[string][]struct{ Command string } `json:"hooks"`
	}
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &hooks)
	hooks.Hooks["stop"][0].Command = `sh .sigdek/launch.sh hook cursor stop | grep '^{' || echo '{}'`
	b, _ = json.Marshal(hooks)
	os.WriteFile(p, b, 0o644)
	c.has(cursorRecord, "Cursor on Windows can't run some of these hooks.")
}
