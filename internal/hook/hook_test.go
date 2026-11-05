package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

func opts(home string, spawned *int) Options {
	return Options{Home: home, Now: time.Now, Spawn: func() error { *spawned++; return nil }}
}

func repo(t *testing.T) string {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	return dir
}

func cc(dir, event, extra string) string {
	return fmt.Sprintf(`{"session_id":"s1","cwd":%q,"hook_event_name":%q%s}`, dir, event, extra)
}

func errorLog(home string) string { b, _ := os.ReadFile(config.ErrorLog(home)); return string(b) }

// QA REC-6: a tool that writes the JSON but never closes stdin still gets an answer at once,
// deny check included.
func TestStdinLeftOpen(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write([]byte(cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)))
	var out bytes.Buffer
	var n int
	start := time.Now()
	Run("claude-code", "", pr, &out, opts(home, &n))
	if el := time.Since(start); el > time.Second || !strings.Contains(out.String(), `"deny"`) {
		t.Fatalf("after %v: %q", el, out.String())
	}
}

// QA REC-6: input cut off by the deadline is still checked against deny rules.
func TestIncompleteInputAfterDeadline(t *testing.T) {
	StdinDeadline = 200 * time.Millisecond
	defer func() { StdinDeadline = 2 * time.Second }()
	home, dir := t.TempDir(), repo(t)
	rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
	pr, pw := io.Pipe()
	defer pw.Close()
	cwd, _ := json.Marshal(dir) // a Windows path has backslashes
	go pw.Write([]byte(`{"session_id":"s1","cwd":` + string(cwd) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf / ` + strings.Repeat("x", 100)))
	var out bytes.Buffer
	var n int
	Run("claude-code", "", pr, &out, opts(home, &n))
	if !strings.Contains(out.String(), `"deny"`) || !strings.Contains(errorLog(home), "stdin not closed") {
		t.Fatalf("out %q log %q", out.String(), errorLog(home))
	}
}

// QA REC-12: the decision is printed even when the record can't be written, and a stuck
// writer costs about a second, not forever.
func TestDenyPrintedEvenWhenTheLogIsLocked(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	store.LockWait = 200 * time.Millisecond
	defer func() { store.LockWait = time.Second }()
	config.LoadDevice(home)
	rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
	held, err := lockfile.Acquire(config.ChainLock(home))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var out bytes.Buffer
	var n int
	start := time.Now()
	Run("claude-code", "", strings.NewReader(cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"rm -rf /"}`)), &out, opts(home, &n))
	if !strings.Contains(out.String(), `"deny"`) || time.Since(start) > 2*time.Second {
		t.Fatalf("out %q after %v", out.String(), time.Since(start))
	}
	if !strings.Contains(errorLog(home), "not recorded") {
		t.Fatalf("lost record not logged: %s", errorLog(home))
	}
}

// QA REC-7: events that arrive but can't be recorded are logged, not dropped silently.
func TestDropsAreLogged(t *testing.T) {
	home := t.TempDir()
	var n int
	for _, in := range []string{"null", "not json", `{"hook_event_name":"Notification"}`} {
		Run("claude-code", "", strings.NewReader(in), io.Discard, opts(home, &n))
	}
	Run("future-tool", "", strings.NewReader("{}"), io.Discard, opts(home, &n))
	log := errorLog(home)
	for _, want := range []string{"not JSON", "null", "Notification", "unknown agent tool", "future-tool"} {
		if !strings.Contains(log, want) {
			t.Errorf("error log lacks %q:\n%s", want, log)
		}
	}
	if h, _ := store.New(home).Head(); h.Seq != 0 {
		t.Fatalf("wrote %d records", h.Seq)
	}
}

// Cursor runs Claude Code's hooks as well as its own. On a signed-in machine, its input to the
// Claude Code hook writes, prints, logs and uploads nothing, even for a setup change (which
// would be uploaded in the hook) or a SessionEnd (which always starts an upload); Claude
// Code's own input is handled as usual.
func TestClaudeCodeHookStandsAsideForCursor(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	config.SaveDevice(home, config.Device{ID: "dev_x", Token: "tok"})
	set := rules.Defaults() // with the setup rules
	set.Rules = append(set.Rules, rules.Rule{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf /srv*"})
	rules.Save(config.RulesPath(home), set)
	var spawned, uploads int
	o := opts(home, &spawned)
	o.Upload = func(time.Duration) error { uploads++; return nil }
	cwd, _ := json.Marshal(dir)
	for _, in := range []string{
		`{"conversation_id":"c","generation_id":"g","cursor_version":"2.4.0","cwd":` + string(cwd) + `,"hook_event_name":"preToolUse","tool_name":"Shell","tool_input":{"command":"rm -rf ~/.sigdek"}}`,
		`{"conversation_id":"c","generation_id":"g","cursor_version":"2.4.0","cwd":` + string(cwd) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /srv/data"}}`,
		`{"conversation_id":"c","generation_id":"g","cursor_version":"2.4.0","session_id":"c","hook_event_name":"sessionEnd"}`,
		`{"conversation_id":"c","generation_id":"g","cursor_version":"2.4.0","hook_event_name":"afterAgentThought"}`,
	} {
		var out bytes.Buffer
		Run("claude-code", "", strings.NewReader(in), &out, o)
		if out.Len() != 0 {
			t.Fatalf("%s: printed %q", in, out.String())
		}
	}
	if h, _ := store.New(home).Head(); h.Seq != 0 || spawned != 0 || uploads != 0 || errorLog(home) != "" {
		t.Fatalf("records %d, spawned %d, uploads %d, log %q", h.Seq, spawned, uploads, errorLog(home))
	}
	Run("claude-code", "", strings.NewReader(cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"rm -rf ~/.sigdek"}`)), io.Discard, o)
	if h, _ := store.New(home).Head(); h.Seq != 1 || uploads != 1 {
		t.Fatalf("Claude Code's own setup change: records %d, uploads %d", h.Seq, uploads)
	}
}

// QA REC-16: Stop fires after every turn and follows the once-a-minute upload limit; only
// SessionEnd uploads regardless.
func TestOnlySessionEndBypassesTheUploadLimit(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	config.SaveDevice(home, config.Device{ID: "dev_x", Token: "tok"})
	var n int
	o := opts(home, &n)
	Run("claude-code", "", strings.NewReader(cc(dir, "Stop", "")), io.Discard, o)
	if n != 1 {
		t.Fatalf("first event should start an upload (none ran yet): %d", n)
	}
	Run("claude-code", "", strings.NewReader(cc(dir, "Stop", "")), io.Discard, o)
	Run("cursor", "stop", strings.NewReader(`{"hook_event_name":"stop","conversation_id":"c"}`), io.Discard, o)
	if n != 1 {
		t.Fatalf("Stop within the minute started an upload: %d", n)
	}
	Run("claude-code", "", strings.NewReader(cc(dir, "SessionEnd", "")), io.Discard, o)
	if n != 2 {
		t.Fatalf("SessionEnd should always start an upload: %d", n)
	}
}

// QA REC-16: a corrupt cached rule set is logged.
func TestCorruptRulesAreLogged(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	os.WriteFile(config.RulesPath(home), []byte("{oops"), 0o600)
	var n int
	Run("claude-code", "", strings.NewReader(cc(dir, "UserPromptSubmit", `,"prompt":"x"`)), io.Discard, opts(home, &n))
	if !strings.Contains(errorLog(home), "rules.json is not a rule set") {
		t.Fatalf("log: %s", errorLog(home))
	}
}

// QA2-REC-13: input nested deeper than encoding/json reads still reaches the Block rules,
// even when the deep part comes first and the input is larger than the decoder's buffer.
func TestDeeplyNestedInputIsStillChecked(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	in := `{"x":` + deep + `,"pad":"` + strings.Repeat("p", 200000) + `","session_id":"s","cwd":"` + dir + `","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /srv"}}`
	var out bytes.Buffer
	Run("claude-code", "", strings.NewReader(in), &out, opts(home, nil))
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("not denied: %q\n%s", out.String(), errorLog(home))
	}
}

// QA2-REC-14: once the server has rejected the token, hooks don't start an upload, not even
// at the end of a session, until the hourly retry is due.
func TestNoUploadWhileTheTokenIsRejected(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	config.SaveDevice(home, config.Device{ID: "dev_x", Token: "tok"})
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(config.TokenRejected(home), nil, 0o600)
	var n int
	o := opts(home, &n)
	Run("claude-code", "", strings.NewReader(cc(dir, "SessionEnd", "")), io.Discard, o)
	if n != 0 {
		t.Fatalf("started %d uploads with a rejected token", n)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(config.TokenRejected(home), old, old)
	Run("claude-code", "", strings.NewReader(cc(dir, "SessionEnd", "")), io.Discard, o)
	if n != 1 {
		t.Fatalf("the hourly retry didn't start: %d", n)
	}
}

// QA2-REC-17: a corrupt device.json doesn't send records out as another device: they keep
// the machine's own id when it can be found, and none are written when it can't.
func TestCorruptDeviceKeepsTheChain(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	os.WriteFile(config.DevicePath(home), []byte(`{"id": "dev_0123456789abcdef01234567", "token": "t`), 0o600)
	Run("claude-code", "", strings.NewReader(cc(dir, "UserPromptSubmit", `,"prompt":"x"`)), io.Discard, opts(home, nil))
	var got []string
	store.New(home).ReadFrom(store.Cursor{}, func(l store.Line) bool {
		var r struct{ Device string }
		json.Unmarshal(l.Raw, &r)
		got = append(got, r.Device)
		return true
	})
	if fmt.Sprint(got) != "[dev_0123456789abcdef01234567]" {
		t.Fatalf("records went out as %v", got)
	}
	os.WriteFile(config.DevicePath(home), []byte(`{oops`), 0o600)
	Run("claude-code", "", strings.NewReader(cc(dir, "UserPromptSubmit", `,"prompt":"y"`)), io.Discard, opts(home, nil))
	if n, _, _ := store.New(home).Verify(); n != 1 || !strings.Contains(errorLog(home), "nothing is recorded until it is fixed") {
		t.Fatalf("records %d, log %s", n, errorLog(home))
	}
}

// QA3-REC-5: the record of an action that changes SigDek's own setup is uploaded before the
// hook returns (bounded), since the action may delete the log right after; a failed try falls
// back to the background upload. Other records, and machines not signed in, work as before.
func TestSetupChangesUploadBeforeTheHookReturns(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	config.SaveDevice(home, config.Device{ID: "dev_x", Token: "tok"})
	var spawned int
	var waits []time.Duration
	var fail error
	o := opts(home, &spawned)
	o.Upload = func(d time.Duration) error { waits = append(waits, d); return fail }
	pre := cc(dir, "PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"rm -rf ~/.sigdek"}`)
	Run("claude-code", "", strings.NewReader(pre), io.Discard, o)
	if len(waits) != 1 || waits[0] != UrgentUpload || waits[0] > time.Second || spawned != 0 {
		t.Fatalf("uploads %v, spawned %d", waits, spawned)
	}
	fail = fmt.Errorf("timed out")
	Run("claude-code", "", strings.NewReader(pre), io.Discard, o)
	if len(waits) != 2 || spawned != 1 {
		t.Fatalf("a failed upload should leave it to the background: uploads %v, spawned %d", waits, spawned)
	}
	Run("claude-code", "", strings.NewReader(cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"}`)), io.Discard, o)
	Run("claude-code", "", strings.NewReader(cc(dir, "SessionEnd", "")), io.Discard, o)
	if len(waits) != 2 {
		t.Fatalf("an ordinary record was uploaded in the hook: %v", waits)
	}
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(config.TokenRejected(home), nil, 0o600)
	Run("claude-code", "", strings.NewReader(pre), io.Discard, o)
	if len(waits) != 2 {
		t.Fatalf("uploaded with a rejected token: %v", waits)
	}
	signedOut := t.TempDir()
	o = opts(signedOut, &spawned)
	o.Upload = func(d time.Duration) error { waits = append(waits, d); return nil }
	Run("claude-code", "", strings.NewReader(pre), io.Discard, o)
	if len(waits) != 2 {
		t.Fatalf("uploaded while signed out: %v", waits)
	}
}
