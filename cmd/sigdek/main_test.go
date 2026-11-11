package main

// End to end through the compiled binary, the way the agent tools run it.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

var binary string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "sigdek-e2e-")
	binary = filepath.Join(dir, "sigdek")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type run struct {
	code     int
	out, err string
	elapsed  time.Duration
}

// sigdekHome is a temporary SIGDEK_HOME. Hooks start `sigdek flush` in the background,
// and that process can still be writing its error log when a test ends, so removal is
// retried for a few seconds instead of failing the test the way t.TempDir's cleanup does.
func sigdekHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sigdek-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("left %s behind", dir)
	})
	return dir
}

func runHook(t *testing.T, home string, args []string, stdin string) run {
	t.Helper()
	return runHookEnv(t, home, nil, args, stdin)
}

// runHookEnv is runHook with extra environment variables, such as the ones Cursor sets. Any
// CURSOR_ variables of the test's own environment are left out.
func runHookEnv(t *testing.T, home string, env []string, args []string, stdin string) run {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"hook"}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CURSOR_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(append(cmd.Env, "SIGDEK_HOME="+home), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := run{out: out.String(), err: errb.String(), elapsed: time.Since(start)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	}
	return r
}

func repoDir(t *testing.T) string {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/api.git\n"), 0o644)
	return dir
}

func cc(cwd, event string, extra string) string {
	return fmt.Sprintf(`{"session_id":"s1","prompt_id":"p1","cwd":%q,"permission_mode":"default","hook_event_name":%q%s}`, cwd, event, extra)
}

func TestHookContract(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	silent := []string{
		cc(dir, "SessionStart", `,"source":"startup","model":"claude-opus-5-5"`),
		cc(dir, "UserPromptSubmit", `,"prompt":"hello"`),
		cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"}`),
		cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"a"},"duration_ms":4`),
		cc(dir, "Stop", ``),
		cc(dir, "SessionEnd", `,"reason":"other"`),
		"", "not json", `{"hook_event_name":`, strings.Repeat("x", 9<<20),
	}
	for i, in := range silent {
		r := runHook(t, home, []string{"claude-code"}, in)
		if r.code != 0 || r.out != "" || r.err != "" {
			t.Errorf("input %d: code=%d out=%q err=%q", i, r.code, r.out, r.err)
		}
	}
	if r := runHook(t, home, []string{"some-future-tool"}, "{}"); r.code != 0 || r.out != "" {
		t.Errorf("unknown tool: %+v", r)
	}
	// Allowed PreToolUse, Stop and unreadable input write nothing.
	n, breaks, err := store.New(home).Verify()
	if err != nil || n != 4 || len(breaks) != 0 {
		t.Fatalf("want 4 records (start, prompt, action, end): n=%d breaks=%v err=%v", n, breaks, err)
	}
}

func TestHookDenyAndCursorAnswers(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	rules.Save(config.RulesPath(home), rules.Set{Version: 3, Rules: []rules.Rule{
		{ID: "no-force-push", Label: "No force pushes", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "git push*--force*"},
	}})
	r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"git push origin main --force"}`))
	var out struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.out), &out) != nil || out.HookSpecificOutput["permissionDecision"] != "deny" {
		t.Fatalf("deny: %+v", r)
	}

	cursorIn := fmt.Sprintf(`{"conversation_id":"c1","generation_id":"g1","hook_event_name":"preToolUse","workspace_roots":[%q],"cwd":%q,"tool_name":"Shell","tool_input":{"command":"git push -u origin x --force"}}`, dir, dir)
	if r := runHook(t, home, []string{"cursor", "preToolUse"}, cursorIn); !strings.Contains(r.out, `"permission":"deny"`) || r.code != 0 {
		t.Fatalf("cursor deny: %+v", r)
	}
	for _, ev := range []string{"preToolUse", "subagentStart"} {
		if r := runHook(t, home, []string{"cursor", ev}, "garbage"); r.out != "{\"permission\":\"allow\"}\n" || r.code != 0 {
			t.Fatalf("cursor %s garbage: %+v", ev, r)
		}
	}
	// Even with an unwritable home, Cursor gets a valid answer.
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	if r := runHook(t, blocker, []string{"cursor", "beforeSubmitPrompt"}, `{"hook_event_name":"beforeSubmitPrompt","prompt":"hi"}`); r.out != "{\"continue\":true}\n" || r.code != 0 {
		t.Fatalf("unwritable home: %+v", r)
	}
}

// Cursor also runs Claude Code's hooks (Third-Party Imports, on by default), so in a
// repository set up with `sigdek init` each Cursor action reaches `sigdek hook claude-code`
// too. That hook stands aside for Cursor's input, with or without Cursor's environment, and
// still records Claude Code's own input when it runs in Cursor's terminal.
func TestClaudeCodeHookStandsAsideForCursor(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	rules.Save(config.RulesPath(home), rules.Set{Version: 3, Rules: append(rules.Defaults().Rules,
		rules.Rule{ID: "no-force-push", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "git push*--force*"})})
	cursorEnv := []string{"CURSOR_VERSION=2.4.0", "CURSOR_PROJECT_DIR=" + dir, "CLAUDE_PROJECT_DIR=" + dir}
	cursor := func(event, extra string) string {
		return fmt.Sprintf(`{"conversation_id":"c1","generation_id":"g1","model":"gpt-5","cursor_version":"2.4.0","hook_event_name":%q,"workspace_roots":[%q],"user_email":"dev@example.com","transcript_path":null%s}`, event, dir, extra)
	}
	inputs := []string{
		cursor("sessionStart", `,"session_id":"c1"`),
		cursor("beforeSubmitPrompt", `,"prompt":"hello"`),
		cursor("preToolUse", `,"cwd":"`+filepath.ToSlash(dir)+`","tool_name":"Shell","tool_input":{"command":"git push origin main --force"}`),
		cursor("preToolUse", `,"tool_name":"Shell","tool_input":{"command":"rm -rf .sigdek"}`),
		cursor("PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"git push origin main --force"}`),
		cursor("postToolUse", `,"tool_name":"Shell","tool_input":{"command":"npm test"},"duration":12`),
		cursor("PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"npm test"}`),
		cursor("stop", ``),
		cursor("sessionEnd", `,"session_id":"c1"`),
	}
	for _, env := range [][]string{nil, cursorEnv} {
		for _, in := range inputs {
			if r := runHookEnv(t, home, env, []string{"claude-code"}, in); r.code != 0 || r.out != "" || r.err != "" {
				t.Fatalf("env %v, %s: code=%d out=%q err=%q", env, in, r.code, r.out, r.err)
			}
		}
	}
	if n, _, _ := store.New(home).Verify(); n != 0 {
		t.Fatalf("recorded %d Cursor events through the Claude Code hook", n)
	}
	if b, _ := os.ReadFile(config.ErrorLog(home)); len(b) != 0 {
		t.Fatalf("error log: %s", b)
	}

	// Claude Code in Cursor's integrated terminal inherits Cursor's environment: still recorded,
	// and its Block rules still apply.
	for _, in := range []string{
		cc(dir, "SessionStart", `,"source":"startup","model":"claude-opus-5-5"`),
		cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"npm test"},"duration_ms":4`),
	} {
		if r := runHookEnv(t, home, cursorEnv, []string{"claude-code"}, in); r.code != 0 || r.out != "" {
			t.Fatalf("%s: %+v", in, r)
		}
	}
	r := runHookEnv(t, home, cursorEnv, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"git push origin main --force"}`))
	if r.code != 0 || !strings.Contains(r.out, `"permissionDecision":"deny"`) {
		t.Fatalf("Claude Code deny in Cursor's terminal: %+v", r)
	}
	if n, breaks, err := store.New(home).Verify(); n != 3 || len(breaks) != 0 || err != nil {
		t.Fatalf("want 3 Claude Code records (start, action, blocked action): n=%d breaks=%v err=%v", n, breaks, err)
	}
	// And the Cursor hook itself still records Cursor's input.
	if r := runHookEnv(t, home, cursorEnv, []string{"cursor", "postToolUse"}, inputs[5]); r.code != 0 || r.out != "{}\n" {
		t.Fatalf("cursor hook: %+v", r)
	}
	if n, _, _ := store.New(home).Verify(); n != 4 {
		t.Fatalf("the Cursor hook didn't record: n=%d", n)
	}
}

// QA-SITE-3, through the binary: a stored input_hash can't confirm the password stripped from
// the command; it is the hash of the stored, redacted action.
func TestInputHashOfARedactedCommand(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	if r := runHook(t, home, []string{"claude-code"}, cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"mysql -u root -phunter2 -h db.internal"}`)); r.code != 0 || r.out != "" {
		t.Fatalf("%+v", r)
	}
	var lines []string
	store.New(home).ReadFrom(store.Cursor{}, func(l store.Line) bool { lines = append(lines, string(l.Raw)); return true })
	if len(lines) != 1 {
		t.Fatalf("records: %v", lines)
	}
	var rec struct {
		Action struct {
			InputHash string `json:"input_hash"`
		} `json:"action"`
		Redactions int `json:"redactions"`
	}
	json.Unmarshal([]byte(lines[0]), &rec)
	// sha256 of {"command":"mysql -u root -phunter2 -h db.internal"}, the raw input, was stored before.
	const raw = "8ec4241b5984624f06ac61c7229096e452a1d18f3ea386251e7e9a2cd303cb62"
	// sha256 of {"command":"mysql -u root -p[redacted] -h db.internal","kind":"shell","name":"Bash"}
	const redacted = "7745210b0b4768c2aeded97f68ebc360469779291ca9ec4288e0134c0afd9b21"
	if strings.Contains(lines[0], "hunter2") || rec.Redactions != 1 || rec.Action.InputHash != redacted || rec.Action.InputHash == raw {
		t.Fatalf("record: %s", lines[0])
	}
}

func TestHookLatencyBudget(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	in := cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"npm test"},"tool_response":{"stdout":"ok"},"duration_ms":100`)
	var d []time.Duration
	for i := 0; i < 60; i++ {
		d = append(d, runHook(t, home, []string{"claude-code"}, in).elapsed)
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	p95 := d[len(d)*95/100]
	t.Logf("hook start-to-exit: p50 %v, p95 %v", d[len(d)/2], p95)
	// Starting a process costs far more on Windows, and shared CI runners there add scanning
	// spikes (p50 about 22 ms, p95 up to about 80 ms), so the slowest runs get more room.
	budget := 50 * time.Millisecond
	if runtime.GOOS == "windows" {
		budget = 120 * time.Millisecond
	}
	if p95 > budget {
		t.Fatalf("p95 %v is over the %v budget", p95, budget)
	}
	if p50 := d[len(d)/2]; p50 > 50*time.Millisecond {
		t.Fatalf("p50 %v is over 50 ms", p50)
	}
}

func TestConcurrentHookProcesses(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runHook(t, home, []string{"claude-code"}, cc(dir, "PostToolUse", fmt.Sprintf(`,"tool_name":"Read","tool_input":{"file_path":"src/%d.go"}`, i)))
		}(i)
	}
	wg.Wait()
	n, breaks, err := store.New(home).Verify()
	if err != nil || n != 30 || len(breaks) != 0 {
		t.Fatalf("n=%d breaks=%v err=%v", n, breaks, err)
	}
}

// A signed-in device uploads in the background at the end of each turn and of the session,
// without the hook waiting for it, even when an upload ran less than a minute ago.
func TestTurnAndSessionEndTriggerBackgroundUpload(t *testing.T) {
	got := make(chan int, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Records []json.RawMessage `json:"records"`
		}
		json.Unmarshal(b, &req)
		got <- len(req.Records)
		w.Write([]byte(`{"accepted":` + fmt.Sprint(len(req.Records)) + `}`))
	}))
	defer srv.Close()
	home, dir := sigdekHome(t), repoDir(t)
	config.SaveDevice(home, config.Device{ID: "dev_bg", Token: "tok", IngestURL: srv.URL})
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(home), "flush.stamp"), nil, 0o600) // an upload just ran
	runHook(t, home, []string{"claude-code"}, cc(dir, "UserPromptSubmit", `,"prompt":"x"`))
	runHook(t, home, []string{"claude-code"}, cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"}`))
	select {
	case n := <-got:
		t.Fatalf("an action within a minute of the last upload started another (%d records)", n)
	case <-time.After(300 * time.Millisecond):
	}
	// The end of the turn sends the turn's records at once, minute or not (SMOKE-1).
	r := runHook(t, home, []string{"claude-code"}, cc(dir, "Stop", ""))
	if r.elapsed > 200*time.Millisecond {
		t.Fatalf("Stop hook waited for the upload (%v)", r.elapsed)
	}
	select {
	case n := <-got:
		if n != 2 {
			t.Fatalf("Stop uploaded %d records, want the prompt and the action", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop started no background upload")
	}
	waitForUploader(t, home)
	r = runHook(t, home, []string{"claude-code"}, cc(dir, "SessionEnd", `,"reason":"other"`))
	if r.elapsed > 200*time.Millisecond {
		t.Fatalf("SessionEnd hook waited for the upload (%v)", r.elapsed)
	}
	select {
	case n := <-got:
		if n != 1 {
			t.Fatalf("SessionEnd uploaded %d records, want its own", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no background upload arrived")
	}
	waitForUploader(t, home)
}

// waitForUploader waits until the background `sigdek flush` has exited (it holds the flush
// lock until then), so the test's temporary directory can be removed.
func waitForUploader(t *testing.T, home string) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if l, err := lockfile.TryAcquire(config.FlushLock(home)); err == nil && l != nil {
			l.Release()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background upload still running after 5 s")
}

func TestCLICommands(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	runHook(t, home, []string{"claude-code"}, cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"git push origin main --force"}`))
	runCLI := func(args ...string) string {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := runCLI("log"); !strings.Contains(out, "git push origin main --force") || !strings.Contains(out, "[rule: force-push]") {
		t.Fatalf("log: %s", out)
	}
	if out := runCLI("verify"); !strings.Contains(out, "All 1 records") {
		t.Fatalf("verify: %s", out)
	}
	if out := runCLI("status"); !strings.Contains(out, "[ ] This machine isn't connected to a team") || !strings.Contains(out, "Records     1 on this machine") {
		t.Fatalf("status: %s", out)
	}
	if out := runCLI("init", "--offline"); !strings.Contains(out, ".claude/settings.json") || !strings.Contains(out, ".sigdek/launch.sh") {
		t.Fatalf("init: %s", out)
	}
	// A development build pins "dev", which no launcher can download (QA1-SETUP-1).
	if out := runCLI("status"); !strings.Contains(out, "Claude Code has hooks in this project") || !strings.Contains(out, "Cursor has hooks in this project") || !strings.Contains(out, "development build") {
		t.Fatalf("status after init: %s", out)
	}
}

// QA REC-16 and REC-1, through the binary: one error line for a bad flag, an honest logout,
// status after the server rejected the token, verify naming an unreadable line.
func TestCLIMessages(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	cli := func(args ...string) (string, string, int) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return out.String(), errb.String(), code
	}
	if _, stderr, code := cli("log", "-n", "abc"); code != 1 || strings.Count(stderr, "invalid value") != 1 || strings.Contains(stderr, "Usage") {
		t.Errorf("log -n abc: exit %d, stderr %q", code, stderr)
	}
	if out, _, _ := cli("logout"); !strings.Contains(out, "wasn't signed in") {
		t.Errorf("logout when signed out: %q", out)
	}

	// A dead local address: the hooks below start a background upload, which must not reach the real service.
	config.SaveDevice(home, config.Device{ID: "dev_cli", Token: "tok", User: "dev@example.com", OrgName: "Tekton", IngestURL: "http://127.0.0.1:9"})
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(config.TokenRejected(home), nil, 0o600)
	if out, _, _ := cli("status"); !strings.Contains(out, "rejected this machine's token") {
		t.Errorf("status after rejection: %s", out)
	}

	runHook(t, home, []string{"claude-code"}, cc(dir, "UserPromptSubmit", `,"prompt":"x"`))
	files, _ := store.New(home).Files()
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("garbage\n")
	f.Close()
	runHook(t, home, []string{"claude-code"}, cc(dir, "UserPromptSubmit", `,"prompt":"y"`))
	out, _, code := cli("verify")
	if code != 1 || !strings.Contains(out, "line 2: not a readable record") || strings.Contains(out, "record 2:") {
		t.Errorf("verify: exit %d\n%s", code, out)
	}
}

// QA2-REC-1: init says when git would leave its files out of the commit.
func TestInitWarnsAboutIgnoredFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".sigdek/\n"), 0o644)
	cmd := exec.Command(binary, "init", "--offline")
	cmd.Env = append(os.Environ(), "SIGDEK_HOME="+sigdekHome(t))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Warning: git ignores these files") ||
		!strings.Contains(string(out), "  .sigdek/launch.sh\n") || !strings.Contains(string(out), "`git add -f .sigdek`") {
		t.Fatalf("init: %v\n%s", err, out)
	}
}

// QA2-REC-9: status says when the repository's recorder.lock has no build for this machine.
func TestStatusNamesAMissingPlatformBuild(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	os.MkdirAll(filepath.Join(dir, ".sigdek"), 0o755)
	other := "windows_arm64"
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		other = "linux_amd64"
	}
	initCmd := exec.Command(binary, "init", "--offline")
	initCmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
	initCmd.Dir = dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, ".sigdek", "recorder.lock"), []byte("version=0.9.9\nurl=https://example.invalid\n"+other+"="+strings.Repeat("a", 64)+"\n"), 0o644)
	cmd := exec.Command(binary, "status")
	cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "has no build of SigDek 0.9.9 for "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("status:\n%s", out)
	}
}

// QA2-REC-17 and QA2-REC-15: status reads only, says where it is, prints errors readably and
// drops upload errors that a later upload has fixed; commands refuse stray arguments; verify
// names an unfinished last line.
func TestCLIPolish(t *testing.T) {
	home := sigdekHome(t)
	outside := t.TempDir() // not a git repository
	cli := func(dir string, args ...string) (string, string, int) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return out.String(), errb.String(), code
	}
	out, _, _ := cli(outside, "status")
	if !strings.Contains(out, "[ ] Nothing records Claude Code or Cursor in this folder yet") || !strings.Contains(out, "not set up yet") {
		t.Errorf("status outside a repository:\n%s", out)
	}
	if _, err := os.Stat(config.DevicePath(home)); err == nil {
		t.Error("status created device.json")
	}
	for _, args := range [][]string{{"status", "extra"}, {"verify", "--json"}, {"logout", "now"}, {"flush", "--all"}, {"version", "x"}} {
		if _, stderr, code := cli(outside, args...); code != 1 || !strings.Contains(stderr, "sigdek "+args[0]) {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}

	// Errors read as a time and a message; an upload error older than the last good upload is over.
	config.SaveDevice(home, config.Device{ID: "dev_cli", Token: "tok", User: "dev@example.com", OrgName: "Tekton", IngestURL: "http://127.0.0.1:9"})
	os.WriteFile(config.ErrorLog(home), []byte(`{"at":"2026-01-15T20:38:29Z","error":"upload: this device's token was rejected"}`+"\n"), 0o600)
	if out, _, _ := cli(outside, "status"); !strings.Contains(out, "Last error  Jan 15") || strings.Contains(out, `{"at"`) {
		t.Errorf("last error:\n%s", out)
	}
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(home), "upload-health.json"), []byte(`{"last_ok":"2026-01-15T21:00:00Z","last_try":"2026-01-15T21:00:00Z","refused":2}`), 0o600)
	out, _, _ = cli(outside, "status")
	if !strings.Contains(out, "Errors      none") || !strings.Contains(out, "[x] Uploads work: the last one finished") || !strings.Contains(out, "refused 2 as malformed") {
		t.Errorf("after a good upload:\n%s", out)
	}

	// A team rule the recorder can't compile is listed as not working, and not counted.
	rules.Save(config.RulesPath(home), rules.Set{Version: 7, Rules: []rules.Rule{
		{ID: "ok", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"},
		{ID: "bad-re", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "re:rm(?!x)"},
	}})
	if out, _, _ := cli(outside, "status"); !strings.Contains(out, "0 flag, 1 block") || !strings.Contains(out, "Not working: rule bad-re") {
		t.Errorf("broken rule:\n%s", out)
	}

	// verify names a cut-off last line.
	dir := repoDir(t)
	runHook(t, home, []string{"claude-code"}, cc(dir, "UserPromptSubmit", `,"prompt":"x"`))
	files, _ := store.New(home).Files()
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"v":1,"id":"half`)
	f.Close()
	if out, _, code := cli(outside, "verify"); code != 1 || !strings.Contains(out, "line 2: unfinished last line") {
		t.Errorf("verify: exit %d\n%s", code, out)
	}
	waitForUploader(t, home)
}

// QA2-CLAIMS-5 (site review): status says when SigDek's Claude Code plugin is on, from the user's
// settings or the repository's, with the repository's settings winning as in Claude Code.
func TestStatusNamesThePlugin(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	claudeDir := t.TempDir()
	status := func() string {
		cmd := exec.Command(binary, "status")
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home, "CLAUDE_CONFIG_DIR="+claudeDir)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := status(); strings.Contains(out, "plugin (turned") || strings.Contains(out, "plugin is turned") {
		t.Fatalf("no plugin installed, but status names one:\n%s", out)
	}
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"enabledPlugins":{"other@market":true,"sigdek@sigdek":true}}`), 0o644)
	if out := status(); !strings.Contains(out, "[x] Claude Code records here, through SigDek's plugin (turned on in "+filepath.Join(claudeDir, "settings.json")) && !strings.Contains(out, "through SigDek's plugin (turned on in ~") {
		t.Fatalf("user-level plugin not named:\n%s", out)
	}
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"enabledPlugins":{"sigdek@sigdek":false}}`), 0o644)
	if out := status(); !strings.Contains(out, "[ ] SigDek's Claude Code plugin is turned off here (this repo's .claude/settings.local.json)") {
		t.Fatalf("repository's off switch not named:\n%s", out)
	}
	os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"enabledPlugins":{"sigdek@claude-plugins-official":true}}`), 0o644)
	if out := status(); !strings.Contains(out, "through SigDek's plugin (turned on in this repo's .claude/settings.local.json)") {
		t.Fatalf("plugin from another marketplace not named:\n%s", out)
	}
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{not json`), 0o644)
	os.Remove(filepath.Join(dir, ".claude", "settings.local.json"))
	if out := status(); strings.Contains(out, "plugin (turned") || strings.Contains(out, "plugin is turned") || !strings.Contains(out, "Errors      none") {
		t.Fatalf("unreadable settings should be ignored quietly:\n%s", out)
	}
}

// QA2-REC-1: status points out hook commands written before they stopped passing on a failure.
func TestStatusNamesOldHookCommands(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	old := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"sh \"$CLAUDE_PROJECT_DIR/.sigdek/launch.sh\" hook claude-code"}]}]}}`
	os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(old), 0o644)
	status := func() string {
		cmd := exec.Command(binary, "status")
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := status(); !strings.Contains(out, "written by an older sigdek init") {
		t.Fatalf("old commands not named:\n%s", out)
	}
	cmd := exec.Command(binary, "init", "--offline")
	cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	if out := status(); strings.Contains(out, "older sigdek init") {
		t.Fatalf("still named after init:\n%s", out)
	}
	// QA3-REC-6: the Cursor commands before they answered for a launcher that prints nothing.
	cur := filepath.Join(dir, ".cursor", "hooks.json")
	b, _ := os.ReadFile(cur)
	older := strings.NewReplacer(`.sigdek/launch.sh hook cursor stop | grep \"^{\" || echo`, `.sigdek/launch.sh hook cursor stop || echo`,
		`.sigdek/launch.sh hook cursor stop | grep '^{' || echo`, `.sigdek/launch.sh hook cursor stop || echo`).Replace(string(b))
	if older == string(b) {
		t.Fatalf("no Cursor stop command to make old:\n%s", b)
	}
	os.WriteFile(cur, []byte(older), 0o644)
	if out := status(); !strings.Contains(out, "written by an older sigdek init") {
		t.Fatalf("old Cursor commands not named:\n%s", out)
	}
}

// QA3-REC-2, QA3-REC-10: logout says recording goes on and when those records upload; status
// drops upload errors from before the last sign-in, and keeps a warning from the last upload.
func TestLogoutAndStatusAfterSigningInAgain(t *testing.T) {
	revoked := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revoked <- r.URL.Path + " " + r.Header.Get("Authorization")
	}))
	defer srv.Close()
	home, dir := sigdekHome(t), repoDir(t)
	cli := func(args ...string) string {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home, "SIGDEK_APP_URL=http://127.0.0.1:9")
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	config.SaveDevice(home, config.Device{ID: "dev_cli", Token: "tok", User: "dev@example.com", OrgName: "Tekton", AppURL: srv.URL})
	out := cli("logout")
	if !strings.Contains(out, "server revoked") || !strings.Contains(out, "Agents here are still recorded") || !strings.Contains(out, "to Tekton again") {
		t.Errorf("logout:\n%s", out)
	}
	if got := <-revoked; got != "/api/devices/self/revoke Bearer tok" {
		t.Errorf("revoke went to %q", got)
	}

	config.SaveDevice(home, config.Device{ID: "dev_cli", Token: "tok2", User: "dev@example.com", OrgName: "Tekton", IngestURL: "http://127.0.0.1:9"})
	os.MkdirAll(config.StateDir(home), 0o700)
	health := filepath.Join(config.StateDir(home), "upload-health.json")
	at := time.Now().Add(-2 * time.Minute).UTC()
	stamp := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z07:00") }
	os.WriteFile(config.ErrorLog(home), []byte(`{"at":"`+stamp(at)+`","error":"upload: this device's token was rejected"}`+"\n"), 0o600)
	os.WriteFile(health, []byte(`{"signed_in":"`+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)+`"}`), 0o600)
	if out := cli("status"); !strings.Contains(out, "Errors      none") {
		t.Errorf("an error from before signing in again still shows:\n%s", out)
	}
	os.WriteFile(config.ErrorLog(home), []byte(`{"at":"`+stamp(at)+`","error":"upload: the server accepted 3 records, but its answer couldn't be read (EOF)"}`+"\n"), 0o600)
	os.WriteFile(health, []byte(`{"last_try":"`+at.Add(-300*time.Millisecond).Format(time.RFC3339Nano)+`","last_ok":"`+at.Add(time.Second).Format(time.RFC3339Nano)+`"}`), 0o600)
	if out := cli("status"); !strings.Contains(out, "answer couldn't be read") || !strings.Contains(out, "[x] Uploads work") {
		t.Errorf("a warning from the last good upload is hidden:\n%s", out)
	}
}

// QA3-REC-5: rm -rf ~/.sigdek right after the hook returns can't beat the upload of its
// record: the hook sends it before it exits, and within about a second.
func TestSetupChangeIsUploadedBeforeTheHookExits(t *testing.T) {
	got := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Records []struct {
				Outcome string `json:"outcome"`
				Rule    string `json:"rule"`
			} `json:"records"`
		}
		json.Unmarshal(b, &req)
		for _, rec := range req.Records {
			got <- rec.Outcome + " " + rec.Rule
		}
		w.Write([]byte(`{"accepted":` + fmt.Sprint(len(req.Records)) + `}`))
	}))
	defer srv.Close()
	home, dir := sigdekHome(t), repoDir(t)
	config.SaveDevice(home, config.Device{ID: "dev_rm", Token: "tok", IngestURL: srv.URL})
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(home), "flush.stamp"), nil, 0o600)
	r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"rm -rf ~/.sigdek"}`))
	select {
	case rec := <-got:
		if rec != "attempted sigdek-setup-shell" {
			t.Fatalf("uploaded %q", rec)
		}
	default:
		t.Fatalf("the hook exited (after %v) before its record reached the server", r.elapsed)
	}
	if r.code != 0 || r.out != "" || r.elapsed > 1500*time.Millisecond {
		t.Fatalf("hook: exit %d, stdout %q, %v", r.code, r.out, r.elapsed)
	}
	waitForUploader(t, home)
}
