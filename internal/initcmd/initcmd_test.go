package initcmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/tektonsoftware/sigdek/internal/adapter"
)

const existingClaude = `{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(npm test)"]
  },
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "./scripts/guard.sh"}]}
    ]
  }
}
`

func TestInitMergesAndRemovesCleanly(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(existingClaude), 0o644)

	opts := Options{Root: root, Version: "dev", Tools: []string{"claude-code", "cursor"}}
	changed, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 7 {
		t.Fatalf("changed %v", changed)
	}
	// QA REC-3: line endings pinned for every checkout, and hooks that don't need the exec bit.
	attrs, _ := os.ReadFile(filepath.Join(root, ".sigdek", ".gitattributes"))
	for _, line := range []string{"*.sh text eol=lf", "*.ps1 text eol=crlf"} {
		if !strings.Contains(string(attrs), line+"\n") {
			t.Fatalf(".sigdek/.gitattributes lacks %q:\n%s", line, attrs)
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	s := string(b)
	if strings.Index(s, `"model"`) > strings.Index(s, `"permissions"`) || !strings.Contains(s, "./scripts/guard.sh") {
		t.Fatalf("existing settings not preserved in order:\n%s", s)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string                     `json:"matcher"`
			Hooks   []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	json.Unmarshal(b, &cfg)
	for _, ev := range adapter.ClaudeCodeEvents {
		groups := cfg.Hooks[ev]
		if len(groups) == 0 || groups[len(groups)-1].Hooks[0].Command != claudeCommand {
			t.Fatalf("%s not registered: %+v", ev, groups)
		}
	}
	if len(cfg.Hooks["PreToolUse"]) != 2 {
		t.Fatalf("PreToolUse should keep the guard and add SigDek: %+v", cfg.Hooks["PreToolUse"])
	}

	// Running again changes nothing.
	Run(opts)
	again, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if string(again) != s {
		t.Fatal("init is not idempotent")
	}

	cur, _ := os.ReadFile(filepath.Join(root, ".cursor", "hooks.json"))
	var cc struct {
		Version int                                   `json:"version"`
		Hooks   map[string][]struct{ Command string } `json:"hooks"`
	}
	json.Unmarshal(cur, &cc)
	if cc.Version != 1 || cc.Hooks["preToolUse"][0].Command != `set \'sigdek' ; $a = ${\sigdek} | powershell -NoProfile -ExecutionPolicy Bypass -File .sigdek/launch.ps1 hook cursor preToolUse | Where-Object { $_ -like "{*" } ; if ($a) { $a } else { "{""permission"":""allow""}" } ; exit 0 ; $null = ' ; sh .sigdek/launch.sh hook cursor preToolUse | grep "^{" || echo "{\"permission\":\"allow\"}" #'` {
		t.Fatalf("cursor hooks: %s", cur)
	}

	lock, _ := os.ReadFile(filepath.Join(root, ".sigdek", "recorder.lock"))
	if !strings.Contains(string(lock), "version=dev") || !strings.Contains(string(lock), runtime.GOOS+"_"+runtime.GOARCH+"=") {
		t.Fatalf("lock: %s", lock)
	}
	if st, _ := os.Stat(filepath.Join(root, ".sigdek", "launch.sh")); runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		t.Fatal("launch.sh is not executable")
	}

	// Remove restores the original settings exactly (apart from formatting).
	opts.Remove = true
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	var want, got any
	json.Unmarshal([]byte(existingClaude), &want)
	json.Unmarshal(after, &got)
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("remove did not restore:\n%s", after)
	}
}

func TestInitRefusesInvalidJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	p := filepath.Join(root, ".claude", "settings.json")
	os.WriteFile(p, []byte("{ // comments are not JSON\n}"), 0o644)
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code"}}); err == nil {
		t.Fatal("expected an error")
	}
	if b, _ := os.ReadFile(p); string(b) != "{ // comments are not JSON\n}" {
		t.Fatal("file was modified")
	}
}

func TestInitFetchesReleaseChecksums(t *testing.T) {
	var body strings.Builder
	for i, tg := range Targets {
		ext := ""
		if strings.HasPrefix(tg, "windows") {
			ext = ".exe"
		}
		fmt.Fprintf(&body, "%064x  sigdek_1.2.3_%s%s\n", i+1, tg, ext)
	}
	body.WriteString(strings.Repeat("f", 64) + "  checksums.txt.sig\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body.String())) }))
	defer srv.Close()
	sums, err := fetchChecksums(nil, srv.URL+"/checksums.txt", "1.2.3")
	if err != nil || len(sums) != len(Targets) || sums["windows_arm64"] != fmt.Sprintf("%064x", 6) {
		t.Fatalf("sums %v err %v", sums, err)
	}
	if _, err := fetchChecksums(nil, srv.URL+"/checksums.txt", "9.9.9"); err == nil {
		t.Fatal("a release missing a platform must be rejected")
	}
}

// The launcher is the first thing every developer machine runs; test it as a real shell
// script, under every POSIX shell installed (dash and bash at least on CI's Linux).
func shells(t *testing.T) []string {
	if runtime.GOOS == "windows" {
		t.Skip("sh launcher")
	}
	var out []string
	for _, sh := range []string{"sh", "dash", "bash", "zsh"} {
		if p, err := exec.LookPath(sh); err == nil {
			out = append(out, p)
		}
	}
	return out
}

type launcher struct {
	t     *testing.T
	shell string
	dir   string // holds launch.sh and recorder.lock
	env   []string
}

func newLauncher(t *testing.T, shell, dir string, script []byte) *launcher {
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "launch.sh"), script, 0o644) // no exec bit: hooks run it with sh
	home := t.TempDir()
	return &launcher{t: t, shell: shell, dir: dir, env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}}
}

func (l *launcher) lock(content string) {
	os.WriteFile(filepath.Join(l.dir, "recorder.lock"), []byte(content), 0o644)
}

func (l *launcher) run(stdin string, env []string, args ...string) (string, int) {
	cmd := exec.Command(l.shell, append([]string{filepath.Join(l.dir, "launch.sh")}, args...)...)
	cmd.Env = append(append([]string{}, l.env...), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return string(out), code
}

func fakeRecorder(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "fake-sigdek")
	os.WriteFile(p, []byte("#!/bin/sh\necho \"recorder ran: $*\"\ncat\n"), 0o755)
	return p
}

func TestLauncherDownloadsVerifiesAndCaches(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	osName := map[string]string{"darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	fakeBin := []byte("#!/bin/sh\necho \"recorder ran: $*\"\ncat\n")
	sum := sha256.Sum256(fakeBin)
	name := fmt.Sprintf("sigdek_7.0.0_%s_%s", osName, runtime.GOARCH)
	for _, sh := range shells(t) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if r.URL.Path != "/"+name {
				w.WriteHeader(404)
				return
			}
			w.Write(fakeBin)
		}))
		l := newLauncher(t, sh, t.TempDir(), launchSh)
		pin := func(want string) {
			l.lock(fmt.Sprintf("version=7.0.0\nurl=%s\n%s_%s=%s\n", srv.URL, osName, runtime.GOARCH, want))
		}
		cache := t.TempDir()
		env := []string{"SIGDEK_CACHE=" + cache}

		// Wrong checksum: refuses to run, prints nothing, exits 0.
		pin(strings.Repeat("0", 64))
		if out, code := l.run(`{"hook_event_name":"Stop"}`, env, "hook", "claude-code"); code != 0 || out != "" {
			t.Fatalf("%s: mismatch should be silent and successful: %q %d", sh, out, code)
		}
		if _, err := os.Stat(filepath.Join(cache, "7.0.0", "sigdek")); err == nil {
			t.Fatalf("%s: a binary with the wrong checksum was kept", sh)
		}

		// Right checksum (a new pin retries at once, despite the failure): downloads once,
		// runs with arguments and stdin, then uses the cache.
		pin(hex.EncodeToString(sum[:]))
		out, code := l.run(`{"hook_event_name":"Stop"}`, env, "hook", "claude-code")
		if code != 0 || !strings.Contains(out, "recorder ran: hook claude-code") || !strings.Contains(out, `"hook_event_name":"Stop"`) {
			t.Fatalf("%s: out %q code %d", sh, out, code)
		}
		before := hits
		if out, _ := l.run("{}", env, "hook", "claude-code"); !strings.Contains(out, "recorder ran") || hits != before {
			t.Fatalf("%s: second run should use the cache (hits %d -> %d)", sh, before, hits)
		}

		// Server unreachable and nothing cached: silent success, and Cursor still gets its answer.
		srv.Close()
		env = []string{"SIGDEK_CACHE=" + t.TempDir()}
		if out, code := l.run("{}", env, "hook", "claude-code"); code != 0 || out != "" {
			t.Fatalf("%s: offline: %q %d", sh, out, code)
		}
		if out, code := l.run("{}", env, "hook", "cursor", "preToolUse"); code != 0 || out != "{\"permission\":\"allow\"}\n" {
			t.Fatalf("%s: offline cursor: %q %d", sh, out, code)
		}
	}
}

// QA REC-4: a failed download is not retried on every event.
func TestLauncherBacksOffAfterAFailedDownload(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	for _, sh := range shells(t) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(500) }))
		defer srv.Close()
		l := newLauncher(t, sh, t.TempDir(), launchSh)
		l.lock(fmt.Sprintf("version=7.0.0\nurl=%s\n%s_%s=%s\n", srv.URL, runtime.GOOS, runtime.GOARCH, strings.Repeat("a", 64)))
		cache := t.TempDir()
		env := []string{"SIGDEK_CACHE=" + cache}
		stamp := filepath.Join(cache, "7.0.0", ".download-failed")
		l.run("{}", env, "hook", "claude-code")
		l.run("{}", env, "hook", "claude-code")
		if hits != 1 {
			t.Fatalf("%s: retried at once after a failure: %d requests", sh, hits)
		}
		old := time.Now().Add(-90 * time.Second)
		os.Chtimes(stamp, old, old) // past the first wait (1 minute)
		l.run("{}", env, "hook", "claude-code")
		if hits != 2 {
			t.Fatalf("%s: no retry after the wait: %d requests", sh, hits)
		}
		os.Chtimes(stamp, old, old) // within the second wait (2 minutes)
		l.run("{}", env, "hook", "claude-code")
		if hits != 2 {
			t.Fatalf("%s: the wait should grow: %d requests", sh, hits)
		}
	}
	for _, flag := range []string{`--connect-timeout 2 --max-time "$SECS"`, "SECS=8\n", `[ "$OS" = windows ] && SECS=6`, "wget -q -T 8 -t 1"} {
		if !strings.Contains(string(launchSh), flag) {
			t.Errorf("launch.sh lost %q; the download must fit inside the 10 s hook timeout", flag)
		}
	}
	for _, want := range []string{"$started.AddSeconds(7)", "$ProgressPreference = 'SilentlyContinue'", "[Console]::InputEncoding", "ReadAsync", ".download-failed"} {
		if !strings.Contains(string(launchPs1), want) {
			t.Errorf("launch.ps1 lost %q", want)
		}
	}
}

// QA REC-3: core.autocrlf gives launch.sh CRLF line endings on Windows; it must still run,
// and nothing may make it exit 2, which Claude Code treats as "block".
func TestLauncherSurvivesCRLFAndShellErrors(t *testing.T) {
	fake := fakeRecorder(t)
	crlf := []byte(strings.ReplaceAll(string(launchSh), "\n", "\r\n"))
	broken := []byte(strings.Replace(string(launchSh), "sigdek_eol=lf\n", "sigdek_eol=lf\nif then fi )\n", 1))
	// QA2-REC-1: two branches that pinned different versions conflict in the header comment;
	// the trap comes first, so the launcher still answers by itself.
	src := "# Source: https://github.com/tektonsoftware/sigdek\n"
	conflicted := []byte(strings.Replace(string(launchSh), src, "<<<<<<< HEAD\n"+src+"=======\n# Source: (v0.5)\n>>>>>>> feature\n", 1))
	if bytes.Equal(conflicted, launchSh) {
		t.Fatal("header line not found")
	}
	for _, sh := range shells(t) {
		for name, script := range map[string][]byte{"crlf": crlf, "syntax error": broken, "conflict in the header": conflicted} {
			l := newLauncher(t, sh, filepath.Join(t.TempDir(), ".sigdek"), script)
			if name == "crlf" {
				if out, code := l.run("{}", []string{"SIGDEK_BIN=" + fake}, "hook", "claude-code"); code != 0 || !strings.Contains(out, "recorder ran: hook claude-code") {
					t.Errorf("%s %s: %q exit %d", sh, name, out, code)
				}
			}
			for _, args := range [][]string{{"hook", "claude-code"}, {"hook", "cursor", "preToolUse"}} {
				want := ""
				if args[1] == "cursor" {
					want = "{\"permission\":\"allow\"}\n"
				}
				if out, code := l.run("{}", nil, args...); code != 0 || out != want {
					t.Errorf("%s %s %v: %q exit %d", sh, name, args, out, code)
				}
			}
		}
	}
}

// QA REC-5: whatever stops the launcher before the recorder runs, Cursor gets the same safe
// answer the recorder itself would give.
func TestLauncherAnswersCursorOnEveryFailure(t *testing.T) {
	events := append([]string{"beforeShellExecution", "beforeMCPExecution", "beforeReadFile", "afterFileEdit"}, adapter.CursorEvents...)
	for _, sh := range shells(t) {
		l := newLauncher(t, sh, filepath.Join(t.TempDir(), ".sigdek"), launchSh)
		for _, lock := range []string{"", "url=https://example.invalid\n", "version=1.0.0\n"} {
			os.Remove(filepath.Join(l.dir, "recorder.lock"))
			if lock != "" {
				l.lock(lock) // no version, or a version with no checksum for this platform
			}
			for _, ev := range events {
				if out, code := l.run("{}", nil, "hook", "cursor", ev); code != 0 || out != string(adapter.CursorAllowFor(ev)) {
					t.Errorf("%s lock %q %s: %q exit %d, want %q", sh, lock, ev, out, code, adapter.CursorAllowFor(ev))
				}
			}
			if out, code := l.run("{}", nil, "hook", "claude-code"); code != 0 || out != "" {
				t.Errorf("%s lock %q claude-code: %q exit %d", sh, lock, out, code)
			}
		}
	}
}

// QA REC-10: with the plugin installed and `sigdek init` in the repository, the plugin's
// launcher steps aside, so each event is recorded once, by the repository's setup.
// QA2-REC-10: only in the project folder itself. Claude Code reads .claude/settings.json only
// from the folder the session started in and Cursor from the workspace root, so started in a
// subdirectory, the repository's hooks don't run and the plugin must record.
func TestPluginLauncherStepsAsideForRepoHooks(t *testing.T) {
	fake := fakeRecorder(t)
	for _, sh := range shells(t) {
		project := t.TempDir()
		os.MkdirAll(filepath.Join(project, ".git"), 0o755)
		sub := filepath.Join(project, "packages", "web")
		os.MkdirAll(sub, 0o755)
		if _, err := Run(Options{Root: project, Version: "dev", Tools: []string{"claude-code"}}); err != nil {
			t.Fatal(err)
		}
		plugin := newLauncher(t, sh, filepath.Join(t.TempDir(), "plugin", "scripts"), launchSh)
		env := []string{"SIGDEK_BIN=" + fake, "CLAUDE_PROJECT_DIR=" + project, "CURSOR_PROJECT_DIR=" + project}

		if out, code := plugin.run("{}", env, "hook", "claude-code"); code != 0 || out != "" {
			t.Errorf("%s: plugin should step aside for the repo's Claude Code hooks: %q", sh, out)
		}
		// The repository set up Claude Code only, so the Cursor plugin keeps recording.
		if out, _ := plugin.run("{}", env, "hook", "cursor", "preToolUse"); !strings.Contains(out, "recorder ran") {
			t.Errorf("%s: cursor plugin stepped aside without repo Cursor hooks: %q", sh, out)
		}
		if _, err := Run(Options{Root: project, Version: "dev", Tools: []string{"cursor"}}); err != nil {
			t.Fatal(err)
		}
		if out, code := plugin.run("{}", env, "hook", "cursor", "preToolUse"); code != 0 || out != "{\"permission\":\"allow\"}\n" {
			t.Errorf("%s: cursor plugin should step aside with the safe answer: %q", sh, out)
		}
		// Started in a subdirectory: the repository's hooks aren't read, so the plugin records.
		inSub := []string{"SIGDEK_BIN=" + fake, "CLAUDE_PROJECT_DIR=" + sub, "CURSOR_PROJECT_DIR=" + sub}
		for _, args := range [][]string{{"hook", "claude-code"}, {"hook", "cursor", "preToolUse"}} {
			if out, _ := plugin.run("{}", inSub, args...); !strings.Contains(out, "recorder ran") {
				t.Errorf("%s %v: started in a subdirectory, the plugin must record: %q", sh, args, out)
			}
		}
		// D11: a command a person runs through the plugin (/sigdek:connect runs `login`) isn't a
		// hook event, so the plugin runs it even in a project with SigDek's own hooks.
		if out, _ := plugin.run("", env, "login", "--no-browser"); !strings.Contains(out, "recorder ran: login --no-browser") {
			t.Errorf("%s: login through the plugin stepped aside: %q", sh, out)
		}
		// The repository's own launcher runs as usual.
		repo := &launcher{t: t, shell: sh, dir: filepath.Join(project, ".sigdek"), env: plugin.env}
		if out, _ := repo.run("{}", env, "hook", "claude-code"); !strings.Contains(out, "recorder ran") {
			t.Errorf("%s: the repository's launcher must not step aside: %q", sh, out)
		}
	}
}

// D11: /sigdek:connect runs `sigdek login` through the plugin's own launcher and the recorder
// it downloaded for its hooks. The command the skill tells Claude to run is the one its
// allowed-tools rule lets run without asking, and it signs in even in a project that has
// SigDek's own hooks, where the plugin's hooks step aside.
func TestConnectSkillRunsLoginThroughThePluginLauncher(t *testing.T) {
	b, err := os.ReadFile("../../plugins/claude-code/skills/connect/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := strings.ReplaceAll(string(b), "\r\n", "\n")
	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !ok || !strings.HasPrefix(skill, "---\n") {
		t.Fatal("SKILL.md has no frontmatter")
	}
	const command = `CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" login`
	for _, want := range []string{"name: connect\n", "disable-model-invocation: true\n", "allowed-tools: Bash(" + command + ") Read\n"} {
		if !strings.Contains(front+"\n", want) {
			t.Errorf("frontmatter lacks %q:\n%s", want, front)
		}
	}
	if !strings.Contains(body, "\n   "+command+"\n") || !strings.Contains(body, "run_in_background: true") {
		t.Errorf("the skill doesn't give the command to run in the background:\n%s", body)
	}
	fake := fakeRecorder(t)
	for _, sh := range shells(t) {
		project := t.TempDir()
		os.MkdirAll(filepath.Join(project, ".git"), 0o755)
		if _, err := Run(Options{Root: project, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "plugin")
		os.MkdirAll(filepath.Join(root, "scripts"), 0o755)
		os.WriteFile(filepath.Join(root, "scripts", "launch.sh"), launchSh, 0o644)
		data := filepath.Join(t.TempDir(), "plugin data")
		run := strings.NewReplacer("${CLAUDE_PLUGIN_DATA}", data, "${CLAUDE_PLUGIN_ROOT}", root).Replace(command)
		out, code := runAs(sh, project, run, "", []string{"SIGDEK_BIN=" + fake, "CLAUDE_PROJECT_DIR=" + project})
		if code != 0 || !strings.HasPrefix(out, "recorder ran: login\n") {
			t.Errorf("%s: exit %d, %q", sh, code, out)
		}
		// Without SIGDEK_BIN, the launcher looks for the recorder in the plugin's data folder.
		lock := "version=7.2.0\nurl=https://example.invalid\n" + Platform() + "=" + strings.Repeat("a", 64) + "\n"
		os.WriteFile(filepath.Join(root, "scripts", "recorder.lock"), []byte(lock), 0o644)
		rec, _ := os.ReadFile(fake)
		os.MkdirAll(filepath.Join(data, "7.2.0"), 0o755)
		os.WriteFile(filepath.Join(data, "7.2.0", "sigdek"), rec, 0o755)
		if out, _ := runAs(sh, project, run, "", []string{"CLAUDE_PROJECT_DIR=" + project, "SIGDEK_HOME=" + t.TempDir()}); !strings.HasPrefix(out, "recorder ran: login\n") {
			t.Errorf("%s: the plugin's cached recorder didn't run: %q", sh, out)
		}
	}
}

// QA REC-16: the launcher's cache follows SIGDEK_HOME.
func TestLauncherCacheFollowsSigdekHome(t *testing.T) {
	fake, _ := os.ReadFile(fakeRecorder(t))
	for _, sh := range shells(t) {
		l := newLauncher(t, sh, filepath.Join(t.TempDir(), ".sigdek"), launchSh)
		l.lock("# comment\nversion=7.1.0\nurl=https://example.invalid\n")
		home := t.TempDir()
		os.MkdirAll(filepath.Join(home, "bin", "7.1.0"), 0o755)
		os.WriteFile(filepath.Join(home, "bin", "7.1.0", "sigdek"), fake, 0o755)
		if out, _ := l.run("{}", []string{"SIGDEK_HOME=" + home}, "hook", "claude-code"); !strings.Contains(out, "recorder ran") {
			t.Errorf("%s: %q", sh, out)
		}
	}
}

// The plugins register exactly the events the adapters handle.
func TestPluginsRegisterEveryEvent(t *testing.T) {
	var cc struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	b, err := os.ReadFile("../../plugins/claude-code/hooks/hooks.json")
	if err != nil || json.Unmarshal(b, &cc) != nil {
		t.Fatalf("claude-code hooks.json: %v", err)
	}
	if len(cc.Hooks) != len(adapter.ClaudeCodeEvents) {
		t.Fatalf("claude-code plugin has %d events, adapter handles %d", len(cc.Hooks), len(adapter.ClaudeCodeEvents))
	}
	for _, ev := range adapter.ClaudeCodeEvents {
		if g := cc.Hooks[ev]; len(g) != 1 || g[0].Hooks[0].Command != `sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" hook claude-code; exit 0` {
			t.Errorf("claude-code plugin: %s missing or wrong", ev)
		}
	}
	var cu struct {
		Version int                                   `json:"version"`
		Hooks   map[string][]struct{ Command string } `json:"hooks"`
	}
	b, err = os.ReadFile("../../plugins/cursor/hooks/hooks.json")
	if err != nil || json.Unmarshal(b, &cu) != nil {
		t.Fatalf("cursor hooks.json: %v", err)
	}
	for _, ev := range adapter.CursorEvents {
		if h := cu.Hooks[ev]; len(h) != 1 || h[0].Command != `sh "${CURSOR_PLUGIN_ROOT}/scripts/launch.sh" hook cursor `+ev+` | grep '^{' || echo '`+strings.TrimSpace(string(adapter.CursorAllowFor(ev)))+`'` {
			t.Errorf("cursor plugin: %s missing or wrong", ev)
		}
	}
	for _, p := range []string{"claude-code", "cursor"} {
		got, _ := os.ReadFile("../../plugins/" + p + "/scripts/launch.sh")
		if string(got) != string(launchSh) {
			t.Errorf("plugins/%s/scripts/launch.sh differs from the template; run scripts/sync-plugins.sh", p)
		}
	}
}

// QA REC-13: a problem with any file stops init before it writes anything.
func TestInitChecksEveryFileBeforeWritingAny(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".cursor"), []byte("a file where the folder should be"), 0o644)
	changed, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}})
	if err == nil || len(changed) != 0 {
		t.Fatalf("err %v changed %v", err, changed)
	}
	for _, p := range []string{".claude/settings.json", ".sigdek/launch.sh"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s was written although init failed", p)
		}
	}
}

// QA REC-13: settings keep their permissions, a symlinked settings file stays a link, a
// UTF-8 BOM is accepted, and --tools may have spaces.
func TestInitPreservesModesSymlinksAndAcceptsBOM(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(root, ".cursor"), 0o755)
	shared := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(shared, []byte("\xef\xbb\xbf"+existingClaude), 0o600)
	link := filepath.Join(root, ".claude", "settings.json")
	if err := os.Symlink(shared, link); err != nil {
		t.Skipf("symlinks: %v", err)
	}
	os.WriteFile(filepath.Join(root, ".cursor", "hooks.json"), []byte("\xef\xbb\xbf{\"version\":1}"), 0o640)

	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{" claude-code , Cursor "}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the settings symlink was replaced by a file")
	}
	b, _ := os.ReadFile(shared)
	if !strings.Contains(string(b), "hook claude-code") || !strings.Contains(string(b), "./scripts/guard.sh") || bytes.HasPrefix(b, []byte("\xef\xbb\xbf")) {
		t.Fatalf("link target not updated in place:\n%s", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(shared); st.Mode().Perm() != 0o600 {
			t.Errorf("settings mode %v, want 0600", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Join(root, ".cursor", "hooks.json")); st.Mode().Perm() != 0o640 {
			t.Errorf("hooks.json mode %v, want 0640", st.Mode().Perm())
		}
	}
	// Running again changes nothing and says so.
	if changed, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil || len(changed) != 0 {
		t.Fatalf("second run changed %v (%v)", changed, err)
	}
}

func TestParseTools(t *testing.T) {
	if got, err := ParseTools([]string{"claude-code, cursor", "cursor"}); err != nil || strings.Join(got, ",") != "claude-code,cursor" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", " , ", "vim"} {
		if _, err := ParseTools([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// init --remove takes .sigdek out too, once no hook file uses it.
func TestInitRemoveCleansUpTheLaunchers(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sigdek", "launch.sh")); err != nil {
		t.Fatal("removed the launchers while Cursor's hooks still use them")
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"cursor"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sigdek")); err == nil {
		t.Fatal(".sigdek left behind after every hook was removed")
	}
}

// hookCommands reads back every command init wrote, as each tool would run it.
func hookCommands(t *testing.T, root string) (claude []string, cursor map[string]string) {
	t.Helper()
	var cc struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	b, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err := json.Unmarshal(b, &cc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, groups := range cc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if !seen[h.Command] {
					seen[h.Command] = true
					claude = append(claude, h.Command)
				}
			}
		}
	}
	var cu struct {
		Hooks map[string][]struct{ Command string } `json:"hooks"`
	}
	b, _ = os.ReadFile(filepath.Join(root, ".cursor", "hooks.json"))
	if err := json.Unmarshal(b, &cu); err != nil {
		t.Fatal(err)
	}
	cursor = map[string]string{}
	for ev, list := range cu.Hooks {
		cursor[ev] = list[0].Command
	}
	return claude, cursor
}

// runAs runs a hook command the way the agent tools do on macOS and Linux: `<shell> -c`.
func runAs(shell, dir, command, stdin string, env []string) (string, int) {
	cmd := exec.Command(shell, "-c", command)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return string(out), code
}

// QA2-REC-1: a missing, unreadable or broken launcher must never make a hook command exit 2,
// which Claude Code treats as "block" (PreToolUse, UserPromptSubmit, Stop, ConfigChange) and
// Cursor as "deny". That happens on a branch made before SigDek was added, with .sigdek
// ignored or deleted, or with merge-conflict markers in launch.sh.
func TestHookCommandsNeverBlockWhenTheLauncherIsBroken(t *testing.T) {
	conflict := func(at int) []byte {
		lines := strings.SplitAfter(string(launchSh), "\n")
		mark := "<<<<<<< HEAD\n" + lines[at] + "=======\n" + lines[at] + ">>>>>>> feature\n"
		return []byte(strings.Join(lines[:at], "") + mark + strings.Join(lines[at+1:], ""))
	}
	damage := map[string]func(dir string){
		"no .sigdek folder":          func(dir string) { os.RemoveAll(dir) },
		"no launch.sh":               func(dir string) { os.Remove(filepath.Join(dir, "launch.sh")) },
		"conflict in the trap lines": func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), conflict(3), 0o644) },
		"conflict in the header":     func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), conflict(10), 0o644) },
		"conflict further down":      func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), conflict(40), 0o644) },
		"empty launch.sh":            func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), nil, 0o644) },
		"launch.sh is a folder": func(dir string) {
			os.Remove(filepath.Join(dir, "launch.sh"))
			os.Mkdir(filepath.Join(dir, "launch.sh"), 0o755)
		},
		"git-lfs pointer": func(dir string) {
			os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 4981\n"), 0o644)
		},
		"unreadable launch.sh":       func(dir string) { os.Chmod(filepath.Join(dir, "launch.sh"), 0) },
		"launcher exits 2 by itself": func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("exit 2\n"), 0o644) },
		"recorder.lock is conflicted": func(dir string) {
			os.WriteFile(filepath.Join(dir, "recorder.lock"), []byte("<<<<<<< HEAD\nversion=1\n=======\nversion=2\n>>>>>>> x\n"), 0o644)
		},
		"launcher killed by a signal":  func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("kill -9 $$\n"), 0o644) },
		"launch.sh with CRLF and junk": func(dir string) { os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("\r\n)(\r\n"), 0o644) },
	}
	for _, sh := range shells(t) {
		for name, broke := range damage {
			root := t.TempDir()
			if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
				t.Fatal(err)
			}
			broke(filepath.Join(root, ".sigdek"))
			if name == "unreadable launch.sh" && os.Geteuid() == 0 {
				continue // root reads it anyway
			}
			claude, cursor := hookCommands(t, root)
			env := []string{"CLAUDE_PROJECT_DIR=" + root, "CURSOR_PROJECT_DIR=" + root, "SIGDEK_HOME=" + t.TempDir()}
			for _, c := range claude {
				in := fmt.Sprintf(`{"session_id":"s","cwd":%q,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, root)
				// A git-lfs pointer runs its "size 4981" line as a command; macOS has a size(1) that
				// prints a header. Stray text with exit 0 doesn't block Claude Code. (.sigdek's own
				// .gitattributes keeps LFS filters off these files, so a pointer shouldn't happen.)
				if out, code := runAs(sh, root, c, in, env); code != 0 || (out != "" && name != "git-lfs pointer") {
					t.Errorf("%s, %s: Claude Code command %q: exit %d, stdout %q", sh, name, c, code, out)
				}
			}
			for ev, c := range cursor {
				safe := string(adapter.CursorAllowFor(ev))
				out, code := runAs(sh, root, c, `{"hook_event_name":"`+ev+`"}`, env)
				// QA3-REC-6: an empty launcher, or a folder in its place, prints nothing and exits 0;
				// Cursor still gets its answer, which it needs under failClosed.
				if code != 0 || out != safe {
					t.Errorf("%s, %s: Cursor %s %q: exit %d, stdout %q, want %q", sh, name, ev, c, code, out, safe)
				}
			}
		}
	}
}

// QA2-REC-1: the plugins' hook commands keep the same promise when the plugin's own files
// are gone (a half-removed plugin cache).
func TestPluginHookCommandsNeverBlock(t *testing.T) {
	var cc struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	b, _ := os.ReadFile("../../plugins/claude-code/hooks/hooks.json")
	json.Unmarshal(b, &cc)
	var cu struct {
		Hooks map[string][]struct{ Command string } `json:"hooks"`
	}
	b, _ = os.ReadFile("../../plugins/cursor/hooks/hooks.json")
	json.Unmarshal(b, &cu)
	for _, sh := range shells(t) {
		gone := filepath.Join(t.TempDir(), "removed-plugin")
		env := []string{"CLAUDE_PLUGIN_ROOT=" + gone, "CURSOR_PLUGIN_ROOT=" + gone, "CLAUDE_PROJECT_DIR=" + gone}
		for ev, groups := range cc.Hooks {
			if out, code := runAs(sh, t.TempDir(), groups[0].Hooks[0].Command, "{}", env); code != 0 || out != "" {
				t.Errorf("%s: Claude Code plugin %s: exit %d, stdout %q", sh, ev, code, out)
			}
		}
		for ev, list := range cu.Hooks {
			if out, code := runAs(sh, t.TempDir(), list[0].Command, "{}", env); code != 0 || out != string(adapter.CursorAllowFor(ev)) {
				t.Errorf("%s: Cursor plugin %s: exit %d, stdout %q", sh, ev, code, out)
			}
		}
	}
}

// QA2-REC-1: init names the files git would leave out of the commit, which would leave
// teammates with hooks and no launcher.
func TestIgnoredFilesAreNamed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code"}}); err != nil {
		t.Fatal(err)
	}
	paths := Committed([]string{"claude-code"})
	if got := Ignored(root, paths); len(got) != 0 {
		t.Fatalf("nothing is ignored yet, got %v", got)
	}
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".sigdek/\n"), 0o644)
	got := Ignored(root, paths)
	if len(got) != len(ourFiles) || !strings.HasPrefix(got[0], ".sigdek/") {
		t.Fatalf("ignored %v", got)
	}
	if Ignored(t.TempDir(), paths) != nil {
		t.Fatal("outside a repository git can't say, so nothing is reported")
	}
}

// QA2-REC-9: with a lock that has no build for this platform (`init --offline` on another
// machine), the launcher says so in the error log, once a day, instead of exiting silently.
func TestLauncherLogsAMissingPlatform(t *testing.T) {
	for _, sh := range shells(t) {
		l := newLauncher(t, sh, filepath.Join(t.TempDir(), ".sigdek"), launchSh)
		other := "windows_amd64"
		if runtime.GOOS == "windows" {
			other = "linux_amd64"
		}
		l.lock("version=0.9.9\nurl=https://example.invalid\n" + other + "=" + strings.Repeat("a", 64) + "\n")
		home := t.TempDir()
		env := []string{"SIGDEK_HOME=" + home}
		for i := 0; i < 3; i++ {
			if out, code := l.run("{}", env, "hook", "cursor", "preToolUse"); code != 0 || out != "{\"permission\":\"allow\"}\n" {
				t.Fatalf("%s: %q exit %d", sh, out, code)
			}
		}
		b, _ := os.ReadFile(filepath.Join(home, "errors.log"))
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var entry struct{ At, Error string }
		if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &entry) != nil || entry.At == "" ||
			!strings.Contains(entry.Error, "no build for "+runtime.GOOS+"/"+runtime.GOARCH) {
			t.Fatalf("%s: errors.log:\n%s", sh, b)
		}
		if st, err := os.Stat(filepath.Join(home, "errors.log")); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("%s: errors.log mode %v", sh, st.Mode().Perm())
		}
	}
	for _, want := range []string{"no build for windows/", "Tls12"} {
		if !strings.Contains(string(launchPs1), want) {
			t.Errorf("launch.ps1 lost %q", want)
		}
	}
}

func TestReadLock(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(Options{Root: root, Version: "dev", Offline: true, Tools: []string{"cursor"}}); err != nil {
		t.Fatal(err)
	}
	l, err := ReadLock(root)
	if err != nil || l.Version != "dev" || len(l.Sums) != 1 || l.Sums[Platform()] == "" {
		t.Fatalf("%+v %v", l, err)
	}
}

// QA2-REC-17: init and init --remove leave the developer's own hooks exactly as written,
// keys in their order, not sorted.
func TestInitKeepsKeyOrder(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(root, ".cursor"), 0o755)
	claude := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"./notify.sh","timeout":5}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./scripts/guard.sh"}]}]},"model":"opus"}`
	cursor := `{"version":1,"hooks":{"afterFileEdit":[{"command":"./fmt.sh","timeout":3,"matcher":"*.go"}]}}`
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(claude), 0o644)
	os.WriteFile(filepath.Join(root, ".cursor", "hooks.json"), []byte(cursor), 0o644)
	compact := func(p string) string {
		b, _ := os.ReadFile(p)
		var out bytes.Buffer
		json.Compact(&out, b)
		return out.String()
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
		t.Fatal(err)
	}
	got := compact(filepath.Join(root, ".claude", "settings.json"))
	for _, want := range []string{`{"matcher":"Bash","hooks":[{"type":"command","command":"./scripts/guard.sh"}]}`, `{"matcher":"","hooks":[{"type":"command","command":"./notify.sh","timeout":5}]}`, `"type":"command","command":"sh \"$CLAUDE_PROJECT_DIR`} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost order: want %s in\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, `{"hooks":{"Stop":`) || !strings.HasSuffix(got, `,"model":"opus"}`) {
		t.Fatalf("top-level order changed:\n%s", got)
	}
	if c := compact(filepath.Join(root, ".cursor", "hooks.json")); !strings.Contains(c, `{"command":"./fmt.sh","timeout":3,"matcher":"*.go"}`) {
		t.Fatalf("cursor:\n%s", c)
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if got := compact(filepath.Join(root, ".claude", "settings.json")); got != claude {
		t.Fatalf("remove didn't restore the file:\n%s\nwant\n%s", got, claude)
	}
	if got := compact(filepath.Join(root, ".cursor", "hooks.json")); got != cursor {
		t.Fatalf("remove didn't restore hooks.json:\n%s", got)
	}
}

// QA3-REC-6: the Cursor command passes on the recorder's answer, a denial included, exactly
// as printed; and answers itself when the launcher prints nothing.
func TestCursorCommandPassesOnTheAnswer(t *testing.T) {
	deny := `{"permission":"deny","user_message":"Blocked by a SigDek rule","agent_message":"x"}`
	for _, sh := range shells(t) {
		for launcher, want := range map[string]string{
			"printf '%s\\n' '" + deny + "'\n": deny + "\n",
			"printf '%s' '" + deny + "'\n":    deny + "\n",
			"exit 0\n":                        `{"permission":"allow"}` + "\n",
			"printf '\\n'; exit 0\n":          `{"permission":"allow"}` + "\n",
		} {
			root := t.TempDir()
			if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(root, ".sigdek", "launch.sh"), []byte(launcher), 0o644)
			_, cursor := hookCommands(t, root)
			if out, code := runAs(sh, root, cursor["preToolUse"], `{}`, nil); code != 0 || out != want {
				t.Errorf("%s, launcher %q: exit %d, stdout %q, want %q", sh, launcher, code, out, want)
			}
		}
	}
}

// D10: on Windows, Cursor runs each hook command inside its own PowerShell wrapper, which pipes
// the input in: `Get-Content payload.json -Raw | & { $input | <command> }`. The default command
// then runs launch.ps1 with that input and passes on its answer, under Windows PowerShell 5.1
// and PowerShell 7, and answers for itself when launch.ps1 is missing or broken. Elsewhere,
// PowerShell 7 (where installed) reads the same command and, with no Windows PowerShell to run
// launch.ps1, still answers.
func TestCursorCommandsUnderCursorsPowerShellWrapper(t *testing.T) {
	var shells []string
	for _, ps := range []string{"powershell", "pwsh"} {
		if _, err := exec.LookPath(ps); err == nil && (ps == "pwsh" || runtime.GOOS == "windows") {
			shells = append(shells, ps)
		}
	}
	if runtime.GOOS == "windows" && (len(shells) == 0 || shells[0] != "powershell") {
		t.Fatal("Windows PowerShell isn't installed; this test must run under it on Windows")
	}
	if len(shells) == 0 {
		t.Skip("no PowerShell installed")
	}
	exe, _ := os.Executable()
	wrap := func(ps, root, command, stdin string, extra ...string) (string, int) {
		t.Helper()
		payload := filepath.Join(t.TempDir(), "payload.json")
		os.WriteFile(payload, []byte(stdin), 0o644)
		script := "$OutputEncoding = [System.Text.Encoding]::UTF8; Get-Content -LiteralPath '" + payload + "' -Raw | & { $input | " + command + " }"
		u := utf16.Encode([]rune(script))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			b[2*i], b[2*i+1] = byte(c), byte(c>>8)
		}
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
		cmd.Dir = root
		cmd.Env = cleanEnv(extra...)
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(out), "\r\n", "\n"), code
	}
	for _, ps := range shells {
		root := t.TempDir()
		if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
			t.Fatal(err)
		}
		_, cursor := hookCommands(t, root)
		home := t.TempDir()
		for _, ev := range []string{"preToolUse", "beforeSubmitPrompt", "stop"} {
			in := `{"hook_event_name":"` + ev + `","prompt":"caf\u00e9"}`
			got := filepath.Join(t.TempDir(), "stdin")
			out, code := wrap(ps, root, cursor[ev], in, "SIGDEK_HOME="+home, "SIGDEK_BIN="+exe, "SIGDEK_TEST_RECORDER=answer", "SIGDEK_TEST_STDIN="+got)
			want := cursorSafe(ev) + "\n"
			if runtime.GOOS == "windows" {
				want = `{"permission":"deny","user_message":"recorder ran: hook cursor ` + ev + `"}` + "\n"
				if b, _ := os.ReadFile(got); !strings.Contains(string(b), `"hook_event_name":"`+ev+`"`) {
					t.Errorf("%s %s: the recorder got %q", ps, ev, b)
				}
			}
			if code != 0 || out != want {
				t.Errorf("%s %s: exit %d, stdout %q, want %q", ps, ev, code, out, want)
			}
		}
		damage := map[string]func(dir string){
			"no .sigdek folder": func(dir string) { os.RemoveAll(dir) },
			"no launch.ps1":     func(dir string) { os.Remove(filepath.Join(dir, "launch.ps1")) },
			"empty launch.ps1":  func(dir string) { os.WriteFile(filepath.Join(dir, "launch.ps1"), nil, 0o644) },
			"launch.ps1 is a folder": func(dir string) {
				os.Remove(filepath.Join(dir, "launch.ps1"))
				os.Mkdir(filepath.Join(dir, "launch.ps1"), 0o755)
			},
			"conflict in launch.ps1": func(dir string) {
				os.WriteFile(filepath.Join(dir, "launch.ps1"), []byte("<<<<<<< HEAD\n$x = 1\n=======\n$x = 2\n>>>>>>> b\n"), 0o644)
			},
			"launch.ps1 prints other text and exits 2": func(dir string) {
				os.WriteFile(filepath.Join(dir, "launch.ps1"), []byte("Write-Output 'version https://git-lfs.github.com/spec/v1'\nexit 2\n"), 0o644)
			},
		}
		for name, broke := range damage {
			root := t.TempDir()
			if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}}); err != nil {
				t.Fatal(err)
			}
			broke(filepath.Join(root, ".sigdek"))
			_, cursor := hookCommands(t, root)
			for _, ev := range []string{"preToolUse", "beforeSubmitPrompt"} {
				if out, code := wrap(ps, root, cursor[ev], `{"hook_event_name":"`+ev+`"}`, "SIGDEK_HOME="+home); code != 0 || out != cursorSafe(ev)+"\n" {
					t.Errorf("%s, %s, %s: exit %d, stdout %q", ps, name, ev, code, out)
				}
			}
		}
	}
}

// QA3-REC-6: status asks for sigdek init again when a repository has hook commands an older
// version wrote, and not for the current ones, --powershell included.
func TestOutdatedHookCommands(t *testing.T) {
	root := t.TempDir()
	if got := Outdated(root); len(got) != 0 {
		t.Fatalf("no hook files: %v", got)
	}
	for _, ps := range []bool{false, true} {
		if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"claude-code,cursor"}, PowerShell: ps}); err != nil {
			t.Fatal(err)
		}
		if got := Outdated(root); len(got) != 0 {
			t.Fatalf("current commands (powershell %v) called outdated: %v", ps, got)
		}
	}
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755)
		os.WriteFile(filepath.Join(root, path), []byte(body), 0o644)
	}
	for _, old := range []string{
		`sh .sigdek/launch.sh hook cursor preToolUse`,
		`sh .sigdek/launch.sh hook cursor preToolUse || echo '{\"permission\":\"allow\"}'`,
	} {
		write(".cursor/hooks.json", `{"version":1,"hooks":{"stop":[{"command":"./my-own-hook.sh"}],"preToolUse":[{"command":"`+old+`"}]}}`)
		if got := Outdated(root); len(got) != 1 || got[0] != "cursor" {
			t.Errorf("%s: %v", old, got)
		}
	}
	// D10: the sh-only form of 0.1.0 doesn't block, so it isn't outdated; it records nothing for
	// Cursor on Windows, which CursorForm says.
	if got := CursorForm(root); got != CursorOlder {
		t.Errorf("CursorForm with an older form: %q", got)
	}
	v010 := `sh .sigdek/launch.sh hook cursor preToolUse | grep '^{' || echo '{\"permission\":\"allow\"}'`
	write(".cursor/hooks.json", `{"version":1,"hooks":{"stop":[{"command":"./my-own-hook.sh"}],"preToolUse":[{"command":"`+v010+`"}]}}`)
	if got := Outdated(root); len(got) != 0 || CursorForm(root) != CursorSh {
		t.Errorf("0.1.0 form: outdated %v, form %q", got, CursorForm(root))
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"cursor"}}); err != nil || CursorForm(root) != CursorBoth {
		t.Errorf("init again: %v, form %q", err, CursorForm(root))
	}
	if _, err := Run(Options{Root: root, Version: "dev", Tools: []string{"cursor"}, PowerShell: true}); err != nil || CursorForm(root) != CursorPowerShell {
		t.Errorf("init --powershell: %v, form %q", err, CursorForm(root))
	}
	write(".cursor/hooks.json", `{"version":1,"hooks":{"stop":[{"command":"./my-own-hook.sh"}]}}`)
	write(".claude/settings.json", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"sh \"$CLAUDE_PROJECT_DIR/.sigdek/launch.sh\" hook claude-code"}]}]}}`)
	if got := Outdated(root); len(got) != 1 || got[0] != "claude-code" {
		t.Errorf("old Claude Code command: %v", got)
	}
}

func TestWindowsNote(t *testing.T) {
	// D10: the default Cursor hooks work on Windows too; only --powershell gets a note, on any OS.
	for _, goos := range []string{"windows", "darwin"} {
		if WindowsNote(goos, []string{"claude-code", "cursor"}, true) == "" {
			t.Errorf("%s: no note for --powershell Cursor hooks", goos)
		}
	}
	for _, c := range []struct {
		goos  string
		tools []string
		ps    bool
	}{{"windows", []string{"claude-code", "cursor"}, false}, {"windows", []string{"claude-code"}, true}, {"darwin", []string{"cursor"}, false}} {
		if WindowsNote(c.goos, c.tools, c.ps) != "" {
			t.Errorf("%+v: unexpected note", c)
		}
	}
}
