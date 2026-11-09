package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/rules"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:acme/api.git\n"), 0o644)
	return dir
}

func ctx() Context {
	set := rules.Defaults()
	set.Rules = append(set.Rules, rules.Rule{ID: "no-prod-db", Label: "Production database", Effect: rules.Deny,
		Kinds: []string{"shell"}, Command: "*--env=production*", Message: "Ask in #platform first."})
	return Context{Now: time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC), Device: config.Device{ID: "dev_abc", User: "dev@example.com"}, Rules: set}
}

// payload builds a Claude Code hook input with the fields seen in the phase 0 capture.
func payload(cwd, event string, extra map[string]any) []byte {
	m := map[string]any{
		"session_id": "sess-1", "transcript_path": "/tmp/t.jsonl", "cwd": cwd, "prompt_id": "prompt-1",
		"permission_mode": "default", "effort": map[string]any{"level": "high"}, "hook_event_name": event,
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func sealed(t *testing.T, r *event.Record) []byte {
	t.Helper()
	if r == nil {
		t.Fatal("expected a record")
	}
	if err := r.Seal(1, event.Genesis); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid record: %v", err)
	}
	b, _ := json.Marshal(r)
	return b
}

func TestClaudeCodeFinishedShellAction(t *testing.T) {
	dir := repo(t)
	secret := "ghp_" + strings.Repeat("Ab3", 12)
	res := ClaudeCode(payload(dir, "PostToolUse", map[string]any{
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": "GH_TOKEN=" + secret + " git push origin main --force", "description": "push"},
		"tool_response": map[string]any{"stdout": "THE-SECRET-OUTPUT", "stderr": ""},
		"tool_use_id":   "toolu_1", "duration_ms": 812.4,
	}), ctx())
	if res.Stdout != nil || res.Flush {
		t.Fatalf("unexpected stdout/flush: %+v", res)
	}
	r := res.Record
	line := sealed(t, r)
	if r.Event != "action" || r.Outcome != "ran" || r.Action.Kind != "shell" || r.Rule != "force-push" {
		t.Fatalf("got %+v / %+v", r, r.Action)
	}
	if strings.Contains(string(line), secret) || strings.Contains(string(line), "THE-SECRET-OUTPUT") {
		t.Fatalf("secret or output leaked: %s", line)
	}
	if r.Redactions < 1 || *r.DurationMS != 812 || *r.Repo.Remote != "github.com/acme/api" || *r.User != "dev@example.com" {
		t.Fatalf("fields: %s", line)
	}
	if *r.PromptID != "prompt-1" || r.Session != "sess-1" || **r.PermissionMode != "default" {
		t.Fatalf("ids: %s", line)
	}
}

func TestClaudeCodeFilePathsAreRepoRelative(t *testing.T) {
	dir := repo(t)
	res := ClaudeCode(payload(dir, "PostToolUse", map[string]any{
		"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(dir, ".env.local")}, "tool_response": "x",
	}), ctx())
	if res.Record.Action.Path != ".env.local" || res.Record.Action.Kind != "file_read" || res.Record.Rule != "secrets-file" {
		t.Fatalf("got %+v rule %q", res.Record.Action, res.Record.Rule)
	}
}

func TestClaudeCodeDenyRule(t *testing.T) {
	dir := repo(t)
	res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "npm run migrate -- --env=production"}, "tool_use_id": "toolu_2",
	}), ctx())
	var out struct {
		HookSpecificOutput struct {
			HookEventName, PermissionDecision, PermissionDecisionReason string
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		t.Fatalf("stdout not JSON: %q", res.Stdout)
	}
	h := out.HookSpecificOutput
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "deny" || !strings.Contains(h.PermissionDecisionReason, "Ask in #platform first.") {
		t.Fatalf("decision: %+v", h)
	}
	sealed(t, res.Record)
	if res.Record.Outcome != "denied_by_rule" || res.Record.Rule != "no-prod-db" {
		t.Fatalf("record: %+v", res.Record)
	}
}

func TestClaudeCodePreToolUseAllowedIsSilent(t *testing.T) {
	res := ClaudeCode(payload(repo(t), "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}}), ctx())
	if res.Record != nil || res.Stdout != nil {
		t.Fatalf("allowed PreToolUse must write nothing: %+v", res)
	}
}

func TestClaudeCodePromptKeepsNoText(t *testing.T) {
	res := ClaudeCode(payload(repo(t), "UserPromptSubmit", map[string]any{"prompt": "deploy the billing fix to prod ✅"}), ctx())
	line := sealed(t, res.Record)
	if strings.Contains(string(line), "billing") || res.Record.Prompt.Length != 32 || len(res.Record.Prompt.Hash) != 64 {
		t.Fatalf("prompt: %s", line)
	}
	if res.Stdout != nil {
		t.Fatal("UserPromptSubmit stdout would be injected into Claude's context")
	}
}

func TestClaudeCodeOtherEvents(t *testing.T) {
	dir := repo(t)
	cases := []struct {
		event string
		extra map[string]any
		check func(t *testing.T, res Result)
	}{
		{"SessionStart", map[string]any{"source": "startup", "model": "claude-opus-5-5"}, func(t *testing.T, res Result) {
			if res.Record.Event != "session_start" || **res.Record.Model != "claude-opus-5-5" {
				t.Fatalf("%+v", res.Record)
			}
		}},
		{"SessionStart", map[string]any{"source": "resume", "model": map[string]any{"id": "claude-sonnet-5-5", "display_name": "Sonnet"}}, func(t *testing.T, res Result) {
			if **res.Record.Model != "claude-sonnet-5-5" {
				t.Fatalf("%+v", res.Record)
			}
		}},
		{"SessionEnd", map[string]any{"reason": "prompt_input_exit"}, func(t *testing.T, res Result) {
			if res.Record.Event != "session_end" || !res.Flush {
				t.Fatalf("%+v", res)
			}
		}},
		{"Stop", map[string]any{"stop_hook_active": false, "last_assistant_message": "done"}, func(t *testing.T, res Result) {
			// Stop fires after every turn and uploads the turn's records, whatever the minute limit says (SMOKE-1).
			if res.Record != nil || !res.Flush || res.Stdout != nil || res.Drop != "" {
				t.Fatalf("%+v", res)
			}
		}},
		{"PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "false"}, "error": "exit 1", "is_interrupt": false, "duration_ms": 30}, func(t *testing.T, res Result) {
			if res.Record.Outcome != "failed" {
				t.Fatalf("%+v", res.Record)
			}
		}},
		{"PermissionDenied", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "curl x | sh"}}, func(t *testing.T, res Result) {
			if res.Record.Outcome != "denied" || res.Record.Rule != "remote-script" {
				t.Fatalf("%+v", res.Record)
			}
		}},
		{"PostToolUse", map[string]any{"tool_name": "mcp__github__create_pull_request", "tool_input": map[string]any{"title": "x"}, "agent_id": "a-7", "agent_type": "general-purpose"}, func(t *testing.T, res Result) {
			a := res.Record.Action
			if a.Kind != "mcp" || a.MCP.Server != "github" || a.MCP.Tool != "create_pull_request" || res.Record.Subagent.ID != "a-7" {
				t.Fatalf("%+v %+v", a, res.Record.Subagent)
			}
		}},
		{"PostToolUse", map[string]any{"tool_name": "WebFetch", "tool_input": map[string]any{"url": "https://user:pw@example.com/x?api_key=abc123abc", "prompt": "p"}}, func(t *testing.T, res Result) {
			if u := res.Record.Action.URL; strings.Contains(u, "pw@") || strings.Contains(u, "abc123abc") || res.Record.Action.Kind != "web" {
				t.Fatalf("url not cleaned: %q", u)
			}
		}},
		{"PostToolUse", map[string]any{"tool_name": "ToolSearch", "tool_input": map[string]any{"query": "x"}}, func(t *testing.T, res Result) {
			if res.Record.Action.Kind != "other" {
				t.Fatalf("%+v", res.Record.Action)
			}
		}},
		{"SubagentStop", map[string]any{"agent_id": "a-7", "agent_type": "Explore", "agent_transcript_path": "/tmp/a.jsonl"}, func(t *testing.T, res Result) {
			if res.Record.Event != "subagent_stop" || res.Record.Subagent.Type != "Explore" {
				t.Fatalf("%+v", res.Record)
			}
		}},
		{"SubagentStart", map[string]any{}, func(t *testing.T, res Result) {
			if res.Record.Event != "subagent_start" || res.Record.Subagent == nil {
				t.Fatalf("a start without ids still records: %+v", res.Record)
			}
		}},
		{"ConfigChange", map[string]any{"source": "user_settings", "file_path": filepath.Join(dir, ".claude", "settings.json")}, func(t *testing.T, res Result) {
			if res.Record.Event != "config_change" || *res.Record.Config.Path != ".claude/settings.json" {
				t.Fatalf("%+v", res.Record.Config)
			}
		}},
		{"PreCompact", map[string]any{"trigger": "manual"}, func(t *testing.T, res Result) {
			if res.Record != nil || res.Stdout != nil {
				t.Fatalf("%+v", res)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.event, func(t *testing.T) {
			res := ClaudeCode(payload(dir, c.event, c.extra), ctx())
			if res.Record != nil {
				sealed(t, res.Record)
			}
			c.check(t, res)
		})
	}
}

func TestClaudeCodeGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", "[]", `{"hook_event_name":42}`} {
		if res := ClaudeCode([]byte(in), ctx()); res.Record != nil || res.Stdout != nil {
			t.Fatalf("%q: %+v", in, res)
		}
	}
}

func cursorPayloadJSON(cwd, event string, extra map[string]any) []byte {
	m := map[string]any{
		"conversation_id": "conv-1", "generation_id": "gen-1", "model": "gpt-5", "hook_event_name": event,
		"cursor_version": "2.4.0", "workspace_roots": []string{cwd}, "user_email": "dev@example.com", "transcript_path": nil,
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func TestCursorEveryEventPrintsValidJSON(t *testing.T) {
	dir := repo(t)
	for _, e := range append(CursorEvents, "afterFileEdit", "unknownFutureEvent") {
		res := Cursor(cursorPayloadJSON(dir, e, map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "ls"}, "cwd": dir}), ctx(), "")
		var v map[string]any
		if err := json.Unmarshal(res.Stdout, &v); err != nil {
			t.Errorf("%s: stdout %q is not a JSON object", e, res.Stdout)
		}
		if res.Record != nil {
			sealed(t, res.Record)
		}
	}
}

func TestCursorPermissions(t *testing.T) {
	dir := repo(t)
	allow := Cursor(cursorPayloadJSON(dir, "preToolUse", map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "npm test"}, "cwd": dir}), ctx(), "")
	if string(allow.Stdout) != "{\"permission\":\"allow\"}\n" || allow.Record != nil {
		t.Fatalf("allow: %q %+v", allow.Stdout, allow.Record)
	}
	deny := Cursor(cursorPayloadJSON(dir, "preToolUse", map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "db push --env=production"}, "cwd": dir}), ctx(), "")
	var out map[string]string
	json.Unmarshal(deny.Stdout, &out)
	if out["permission"] != "deny" || !strings.Contains(out["agent_message"], "no-prod-db") || deny.Record.Outcome != "denied_by_rule" {
		t.Fatalf("deny: %q", deny.Stdout)
	}
	if sub := Cursor(cursorPayloadJSON(dir, "subagentStart", map[string]any{"subagent_id": "s1", "subagent_type": "explore"}), ctx(), ""); string(sub.Stdout) != "{\"permission\":\"allow\"}\n" {
		t.Fatalf("subagentStart must allow: %q", sub.Stdout)
	}
	if p := Cursor(cursorPayloadJSON(dir, "beforeSubmitPrompt", map[string]any{"prompt": "hello"}), ctx(), ""); string(p.Stdout) != "{\"continue\":true}\n" || p.Record.Prompt.Length != 5 {
		t.Fatalf("prompt: %q", p.Stdout)
	}
	// Broken input for a permission hook still allows.
	if g := Cursor([]byte(`{"hook_event_name":"preToolUse","tool_input":`), ctx(), ""); string(g.Stdout) != "{\"permission\":\"allow\"}\n" {
		t.Fatalf("truncated payload: %q", g.Stdout)
	}
	if g := Cursor([]byte(`garbage`), ctx(), "subagentStart"); string(g.Stdout) != "{\"permission\":\"allow\"}\n" {
		t.Fatalf("unreadable payload falls back to the command-line event: %q", g.Stdout)
	}
	if g := Cursor([]byte(`garbage`), ctx(), "beforeSubmitPrompt"); string(g.Stdout) != "{\"continue\":true}\n" {
		t.Fatalf("unreadable prompt payload: %q", g.Stdout)
	}
}

func TestCursorActions(t *testing.T) {
	dir := repo(t)
	cases := []struct {
		tool  string
		input map[string]any
		event string
		extra map[string]any
		kind  string
		check func(r *event.Record) bool
	}{
		{"Shell", map[string]any{"command": "go test ./..."}, "postToolUse", map[string]any{"duration": 1500.0}, "shell", func(r *event.Record) bool { return *r.DurationMS == 1500 && r.Outcome == "ran" }},
		{"Write", map[string]any{"file_path": filepath.Join(dir, "src/a.ts")}, "postToolUse", nil, "file_edit", func(r *event.Record) bool { return r.Action.Path == "src/a.ts" }},
		{"Read", map[string]any{"target_file": filepath.Join(dir, "README.md")}, "postToolUse", nil, "file_read", func(r *event.Record) bool { return r.Action.Path == "README.md" }},
		{"MCP:linear:create_issue", map[string]any{}, "postToolUse", nil, "mcp", func(r *event.Record) bool {
			return r.Action.MCP.Server == "linear" && r.Action.MCP.Tool == "create_issue"
		}},
		{"web_search", map[string]any{"query": "x"}, "postToolUse", nil, "web", func(r *event.Record) bool { return true }},
		{"Shell", map[string]any{"command": "rm -rf /tmp/x"}, "postToolUseFailure", map[string]any{"failure_type": "permission_denied", "error_message": "user rejected"}, "shell", func(r *event.Record) bool { return r.Outcome == "denied" && r.Rule == "recursive-delete" }},
		{"Shell", map[string]any{"command": "make"}, "postToolUseFailure", map[string]any{"failure_type": "error"}, "shell", func(r *event.Record) bool { return r.Outcome == "failed" }},
	}
	for _, c := range cases {
		extra := map[string]any{"tool_name": c.tool, "tool_input": c.input, "cwd": dir}
		for k, v := range c.extra {
			extra[k] = v
		}
		res := Cursor(cursorPayloadJSON(dir, c.event, extra), ctx(), "")
		sealed(t, res.Record)
		if res.Record.Action.Kind != c.kind || !c.check(res.Record) || *res.Record.ToolVersion != "2.4.0" || *res.Record.PromptID != "gen-1" || res.Record.Session != "conv-1" {
			t.Errorf("%s: %+v %+v", c.tool, res.Record, res.Record.Action)
		}
	}
}

// QA REC-7: one field of an unexpected type costs that field, not the event.
func TestClaudeCodeLenientFields(t *testing.T) {
	dir := repo(t)
	raw := []byte(`{"session_id":12345,"prompt_id":"p","cwd":` + strconvQuote(dir) + `,"permission_mode":null,"hook_event_name":"PostToolUse",` +
		`"tool_name":"Bash","tool_input":{"command":"ls"},"duration_ms":"5","agent_id":7}`)
	res := ClaudeCode(raw, ctx())
	r := res.Record
	if r == nil {
		t.Fatalf("event dropped: %+v", res)
	}
	sealed(t, r)
	if r.Session != "12345" || *r.DurationMS != 5 || r.PermissionMode == nil || *r.PermissionMode != nil || r.Subagent.ID != "7" {
		t.Fatalf("fields: %+v", r)
	}
	huge := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "duration_ms": 1e300}), ctx())
	if *huge.Record.DurationMS != event.MaxSafeInteger {
		t.Fatalf("duration not clamped: %d", *huge.Record.DurationMS)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// QA REC-7, REC-16: input that can't become a record says why, for the error log.
func TestDropsSayWhy(t *testing.T) {
	for _, in := range []string{"", "null", "[]", "42", "not json", `{"hook_event_name":42}`, `{"hook_event_name":"PreCompact"}`} {
		res := ClaudeCode([]byte(in), ctx())
		if res.Record != nil || res.Stdout != nil || res.Drop == "" {
			t.Errorf("claude-code %q: %+v", in, res)
		}
	}
	// A null payload to beforeSubmitPrompt used to write a prompt record for session "unknown".
	res := Cursor([]byte("null"), ctx(), "beforeSubmitPrompt")
	if res.Record != nil || string(res.Stdout) != "{\"continue\":true}\n" || res.Drop == "" {
		t.Fatalf("cursor null: %+v", res)
	}
}

// QA REC-6: rules see the command as sent, so text after the 4096-byte storage limit, or a
// secret-looking value, can't hide it; the stored copy is still cut and redacted.
func TestRulesMatchTheRawCommand(t *testing.T) {
	dir := repo(t)
	secret := "ghp_" + strings.Repeat("Ab3", 12)
	cmd := "echo " + strings.Repeat("x", 6000) + " && GH_TOKEN=" + secret + " npm run migrate -- --env=production"
	res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}), ctx())
	if res.Record == nil || res.Record.Rule != "no-prod-db" || res.Stdout == nil {
		t.Fatalf("padded command dodged the deny rule: %+v", res)
	}
	line := sealed(t, res.Record)
	if len(res.Record.Action.Command) > event.MaxCommand || strings.Contains(string(line), secret) {
		t.Fatalf("stored command: %d bytes", len(res.Record.Action.Command))
	}
}

// QA REC-6: input cut off at the size limit (or by stdin never closing) still gets the deny
// check, and is recorded with the fields that arrived.
func TestPartialInputStillChecked(t *testing.T) {
	dir := repo(t)
	// Field order as Claude Code sends it: tool_name before tool_input.
	full := `{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm run migrate -- --env=production ` + strings.Repeat("y", 5000) + `"}}`
	c := ctx()
	c.Partial = true
	res := ClaudeCode([]byte(full[:len(full)-2000]), c)
	if res.Record == nil || res.Record.Rule != "no-prod-db" || res.Stdout == nil {
		t.Fatalf("cut-off PreToolUse not denied: %+v", res)
	}
	sealed(t, res.Record)

	write := `{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":` + strconvQuote(filepath.Join(dir, "big.bin")) + `,"content":"` + strings.Repeat("z", 10000) + `"}}`
	res = ClaudeCode([]byte(write[:len(write)/2]), c)
	if res.Record == nil || res.Record.Action.Path != "big.bin" || res.Record.Outcome != "ran" {
		t.Fatalf("cut-off Write: %+v", res)
	}
	sealed(t, res.Record)
}

func TestPartialObject(t *testing.T) {
	p := partialObject([]byte(`{"a":"x\"y","n":12,"o":{"k":[1,2,{"z":true}],"s":"cut \u00e9 her`))
	if p.str("a") != `x"y` || p.str("n") != "12" {
		t.Fatalf("%v", p)
	}
	var o map[string]any
	json.Unmarshal(p["o"], &o)
	if o["s"] != "cut é her" || len(o["k"].([]any)) != 3 {
		t.Fatalf("%v", o)
	}
	if partialObject([]byte(`[1,2`)) != nil || len(partialObject([]byte(strings.Repeat("{\"a\":", 10000)))) > 1 {
		t.Fatal("non-objects and deep nesting must not explode")
	}
}

// SMOKE-1: Cursor's stop ends a turn, so it records nothing, answers, and uploads the turn's records.
func TestCursorStopUploads(t *testing.T) {
	res := Cursor(cursorPayloadJSON(repo(t), "stop", map[string]any{"status": "completed", "loop_count": 0}), ctx(), "")
	if res.Record != nil || !res.Flush || res.Urgent || string(res.Stdout) != "{}\n" || res.Drop != "" {
		t.Fatalf("%+v", res)
	}
}

// QA REC-11: subagentStop has no subagent_id in Cursor; the record keeps the type and duration.
func TestCursorSubagentStop(t *testing.T) {
	res := Cursor(cursorPayloadJSON(repo(t), "subagentStop", map[string]any{"subagent_type": "explore", "status": "completed", "duration_ms": 4200}), ctx(), "")
	r := res.Record
	sealed(t, r)
	if r.Event != "subagent_stop" || r.Subagent.Type != "explore" || r.Subagent.ID != "" || *r.DurationMS != 4200 || string(res.Stdout) != "{}\n" {
		t.Fatalf("%+v %+v", r, r.Subagent)
	}
}

// QA REC-11: an MCP deny rule matches a Cursor MCP call, which names no server.
func TestCursorMCPDeny(t *testing.T) {
	c := ctx()
	c.Rules.Rules = append(c.Rules.Rules, rules.Rule{ID: "no-merge", Effect: rules.Deny, MCP: "github/merge_*"})
	res := Cursor(cursorPayloadJSON(repo(t), "preToolUse", map[string]any{"tool_name": "MCP:merge_pull_request", "tool_input": map[string]any{"pr": 1}}), c, "")
	if res.Record == nil || res.Record.Rule != "no-merge" || !strings.Contains(string(res.Stdout), `"deny"`) {
		t.Fatalf("%+v", res)
	}
}

// QA2-REC-8, QA3-REC-3: an action that switches SigDek off is recorded as attempted when the
// agent asks to run it, so it is on the chain before the launcher or the log is gone. A user
// may still decline it, which fires no later hook, so it doesn't read as having run. When a
// later hook does fire, it records the result as usual: attempted, then ran, failed or denied.
func TestSetupChangesAreRecordedBeforeTheyRun(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{"rm -rf .sigdek", "printf 'exit 0\\n' > .sigdek/launch.sh", "sigdek logout", "npx sigdek init --remove", "rm -rf ~/.sigdek", "unlink .sigdek/launch.sh"} {
		in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
		pre := ClaudeCode(payload(dir, "PreToolUse", in), ctx())
		if pre.Stdout != nil || pre.Record == nil || pre.Record.Outcome != "attempted" || pre.Record.Rule != "sigdek-setup-shell" || !pre.Flush {
			t.Fatalf("%s: PreToolUse should record it as attempted: %+v", cmd, pre)
		}
		sealed(t, pre.Record)
		for ev, want := range map[string]string{"PostToolUse": "ran", "PostToolUseFailure": "failed", "PermissionDenied": "denied"} {
			if r := ClaudeCode(payload(dir, ev, in), ctx()); r.Record == nil || r.Record.Outcome != want || r.Record.Rule != "sigdek-setup-shell" || r.Flush {
				t.Fatalf("%s: %s should record the result %q: %+v", cmd, ev, want, r)
			}
		}
		cur := []byte(`{"hook_event_name":"preToolUse","cwd":` + strconvQuote(dir) + `,"tool_name":"Shell","tool_input":{"command":` + strconvQuote(cmd) + `}}`)
		if r := Cursor(cur, ctx(), "preToolUse"); string(r.Stdout) != "{\"permission\":\"allow\"}\n" || r.Record == nil || r.Record.Outcome != "attempted" || r.Record.Rule != "sigdek-setup-shell" {
			t.Fatalf("%s: Cursor preToolUse: %+v", cmd, r)
		}
		post := []byte(strings.Replace(string(cur), "preToolUse", "postToolUse", 1))
		if r := Cursor(post, ctx(), "postToolUse"); r.Record == nil || r.Record.Outcome != "ran" || string(r.Stdout) != "{}\n" {
			t.Fatalf("%s: Cursor postToolUse should record that it ran: %+v", cmd, r)
		}
	}
	edit := map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(dir, ".claude", "settings.json")}}
	if pre := ClaudeCode(payload(dir, "PreToolUse", edit), ctx()); pre.Record == nil || pre.Record.Rule != "sigdek-setup" {
		t.Fatalf("editing the hook file: %+v", pre)
	}
	// Ordinary allowed actions still write nothing before they run.
	if pre := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "cat .sigdek/README.md"}}), ctx()); pre.Record != nil {
		t.Fatalf("reading SigDek's files is not a change: %+v", pre.Record)
	}
}

// QA2-REC-13: tool_input sent as a JSON string, and input nested deeper than encoding/json
// allows, still reach the Block rules.
func TestOddToolInputStillChecked(t *testing.T) {
	dir := repo(t)
	c := ctx()
	c.Rules.Rules = append(c.Rules.Rules, rules.Rule{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"},
		rules.Rule{ID: "no-merge", Effect: rules.Deny, MCP: "github/merge_*"})
	asString, _ := json.Marshal(`{"command":"rm -rf /srv/data"}`)
	in := `{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":` + string(asString) + `}`
	if r := ClaudeCode([]byte(in), c); r.Record == nil || r.Record.Rule != "no-rm" || r.Record.Action.Command != "rm -rf /srv/data" {
		t.Fatalf("string tool_input: %+v", r)
	}
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	for _, in := range []string{
		`{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PreToolUse","tool_name":"mcp__github__merge_pull_request","tool_input":{"pull_number":1,"x":` + deep + `}}`,
		`{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"x":` + deep + `,"command":"rm -rf /srv/data"}}`,
		`{"x":` + deep + `,"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /srv/data"}}`,
	} {
		r := ClaudeCode([]byte(in), c)
		if r.Record == nil || r.Record.Outcome != "denied_by_rule" {
			t.Fatalf("deep input: %+v (drop %q)", r.Record, r.Drop)
		}
	}
}

// QA2-REC-7 through the adapter: a NUL in a command is stored as U+FFFD.
func TestNULNeverReachesTheRecord(t *testing.T) {
	res := ClaudeCode(payload(repo(t), "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "printf 'a\x00b'"}}), ctx())
	line := sealed(t, res.Record)
	if strings.Contains(string(line), `\u0000`) || res.Record.Action.Command != "printf 'a�b'" {
		t.Fatalf("%s", line)
	}
}

// A command cut to the schema's limit says so; a blocked one also keeps the part that
// matched, which padding may have pushed past the cut.
func TestCutFieldsAreMarked(t *testing.T) {
	dir := repo(t)
	long := "echo " + strings.Repeat("a", 10000)
	r := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": long}}), ctx())
	c := r.Record.Action.Command
	if len(c) > event.MaxCommand || !strings.HasSuffix(c, " … [cut from 10,005 characters]") || !strings.HasPrefix(c, "echo aaa") {
		t.Fatalf("%d bytes, ends %q", len(c), c[len(c)-40:])
	}
	if short := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}), ctx()); short.Record.Action.Command != "ls" {
		t.Fatalf("short command changed: %q", short.Record.Action.Command)
	}
	x := ctx()
	x.Rules.Rules = append(x.Rules.Rules, rules.Rule{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: `re:\brm\s+(\S+\s+)*-rf\b`})
	padded := "rm /srv/data" + strings.Repeat(" ", 600) + "-rf" + strings.Repeat(" ", 70000) + "/srv/other GH=ghp_" + strings.Repeat("Ab3", 12)
	d := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": padded}}), x)
	line := sealed(t, d.Record)
	got := d.Record.Action.Command
	if d.Record.Rule != "no-rm" || len(got) > event.MaxCommand || !strings.Contains(got, "the rule matched: rm /srv/data -rf") || strings.Contains(string(line), "ghp_") {
		t.Fatalf("blocked command: %d bytes: %q", len(got), got[len(got)-200:])
	}
	// The secret was past the cut, so nothing was stripped and input_hash is the raw input's.
	if d.Record.Redactions != 0 || d.Record.Action.InputHash != event.SHA256Hex([]byte(`{"command":`+strconvQuote(padded)+`}`)) {
		t.Fatalf("blocked command's input_hash: redactions %d, %s", d.Record.Redactions, d.Record.Action.InputHash)
	}
	// With the secret inside the part kept, the blocked command's stored form is redacted and
	// replaced after the record was built, and input_hash follows it.
	secretFirst := "GH=ghp_" + strings.Repeat("Ab3", 12) + " rm /srv/data" + strings.Repeat(" ", 70000) + "-rf /srv/other"
	d = ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": secretFirst}}), x)
	line = sealed(t, d.Record)
	if d.Record.Rule != "no-rm" || d.Record.Redactions < 1 || strings.Contains(string(line), "ghp_") || !strings.Contains(d.Record.Action.Command, "the rule matched:") {
		t.Fatalf("blocked command with a secret: %+v", d.Record.Action)
	}
	if d.Record.Action.InputHash != event.RedactedInputHash(d.Record.Action) {
		t.Fatalf("blocked command's input_hash doesn't cover its stored form: %s", d.Record.Action.InputHash)
	}
}

// reread parses a sealed record line the way a reader of the log or an upload would.
func reread(t *testing.T, line []byte) event.Record {
	t.Helper()
	var r event.Record
	if err := json.Unmarshal(line, &r); err != nil || r.Action == nil {
		t.Fatalf("record line: %v: %s", err, line)
	}
	return r
}

// QA-SITE-3: when a secret is stripped from an action, input_hash is the hash of the stored,
// redacted action, so it can't confirm a guessed password the way the hash of the raw input
// could. A record says which kind it holds: redactions above 0 means the redacted form.
func TestInputHashNeverCoversAStrippedSecret(t *testing.T) {
	dir := repo(t)
	const cmd = "mysql -u root -phunter2 -h db.internal"
	sum := func(s string) string { return event.SHA256Hex([]byte(s)) }
	// Inputs holding the secret, as the agent might have sent them. None may match.
	guesses := []string{
		`{"command":"mysql -u root -phunter2 -h db.internal"}`,
		`{"command":"mysql -u root -phunter2 -h db.internal","description":"Connect to the database"}`,
		`{"command":"mysql -u root -phunter2 -h db.internal","kind":"shell","name":"Bash"}`,
		`{"command":"mysql -u root -phunter2 -h db.internal","kind":"shell","name":"Shell"}`,
		cmd,
	}
	block := ctx()
	block.Rules.Rules = append(block.Rules.Rules, rules.Rule{ID: "no-db", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "mysql*db.internal*"})
	bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	shell := map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": cmd}, "cwd": dir}
	cases := []struct {
		name string
		res  Result
		// The stored, redacted action's canonical JSON, hashed with shasum outside Go.
		want string
	}{
		{"Claude Code PostToolUse", ClaudeCode(payload(dir, "PostToolUse", bash), ctx()), "7745210b0b4768c2aeded97f68ebc360469779291ca9ec4288e0134c0afd9b21"},
		{"Claude Code blocked", ClaudeCode(payload(dir, "PreToolUse", bash), block), "7745210b0b4768c2aeded97f68ebc360469779291ca9ec4288e0134c0afd9b21"},
		{"Cursor postToolUse", Cursor(cursorPayloadJSON(dir, "postToolUse", shell), ctx(), ""), "d49a841076864328a3586bafc2e6b3a51b5b80fffa41755133a70395d4b97fa2"},
		{"Cursor blocked", Cursor(cursorPayloadJSON(dir, "preToolUse", shell), block, ""), "d49a841076864328a3586bafc2e6b3a51b5b80fffa41755133a70395d4b97fa2"},
	}
	for _, c := range cases {
		line := sealed(t, c.res.Record)
		r := reread(t, line)
		if strings.Contains(string(line), "hunter2") || r.Action.Command != "mysql -u root -p[redacted] -h db.internal" || r.Redactions != 1 {
			t.Fatalf("%s: not redacted as expected: %s", c.name, line)
		}
		if r.Action.InputHash != c.want {
			t.Errorf("%s: input_hash %s, want the hash of the redacted action %s", c.name, r.Action.InputHash, c.want)
		}
		// A reader recomputes it from the record alone.
		if event.RedactedInputHash(r.Action) != r.Action.InputHash {
			t.Errorf("%s: input_hash can't be recomputed from the record", c.name)
		}
		for _, g := range guesses {
			if r.Action.InputHash == sum(g) {
				t.Errorf("%s: input_hash confirms the secret: it equals sha256(%s)", c.name, g)
			}
		}
	}

	// Credentials in a URL: the hash covers the cleaned URL.
	web := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "WebFetch", "tool_input": map[string]any{"url": "https://dev:s3cretpass@example.com/x?api_key=abc123abc", "prompt": "p"}}), ctx())
	r := reread(t, sealed(t, web.Record))
	if r.Redactions < 1 || r.Action.InputHash != event.RedactedInputHash(r.Action) || strings.Contains(r.Action.URL, "s3cretpass") || strings.Contains(r.Action.URL, "abc123abc") {
		t.Fatalf("web: %+v redactions %d", r.Action, r.Redactions)
	}
	// So does a setup change recorded as attempted before it runs.
	setup := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "PGPASSWORD=hunter2hunter2 sigdek logout"}}), ctx())
	r = reread(t, sealed(t, setup.Record))
	if r.Outcome != "attempted" || r.Redactions < 1 || r.Action.InputHash != event.RedactedInputHash(r.Action) || strings.Contains(r.Action.Command, "hunter2hunter2") {
		t.Fatalf("attempted setup change: %+v redactions %d", r.Action, r.Redactions)
	}
}

// QA-SITE-3: with nothing stripped, input_hash is still the SHA-256 of the tool input exactly
// as the agent sent it, byte for byte, as before the change.
func TestInputHashWithoutASecretIsUnchanged(t *testing.T) {
	dir := repo(t)
	// Spacing and key order as sent, which the hash keeps.
	input := `{ "command": "npm test",  "description": "Run the tests" }`
	raw := []byte(`{"session_id":"s","cwd":` + strconvQuote(dir) + `,"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":` + input + `}`)
	r := reread(t, sealed(t, ClaudeCode(raw, ctx()).Record))
	if r.Redactions != 0 || r.Action.InputHash != event.SHA256Hex([]byte(input)) {
		t.Fatalf("input_hash %s, want sha256 of the input as sent", r.Action.InputHash)
	}
	cur := Cursor(cursorPayloadJSON(dir, "postToolUse", map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "npm test"}, "cwd": dir}), ctx(), "")
	// {"command":"npm test"}, hashed with shasum outside Go.
	if r := reread(t, sealed(t, cur.Record)); r.Redactions != 0 || r.Action.InputHash != "9178ac939ad908d8fd89e97596cdc0e5e28dee903ccb18dc79d0fb98698cbd14" {
		t.Fatalf("Cursor input_hash %s", r.Action.InputHash)
	}
	// An MCP call has no command, path or URL, so nothing is stripped and the raw hash stays.
	mcp := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "mcp__github__create_issue", "tool_input": map[string]any{"title": "x"}}), ctx())
	if r := reread(t, sealed(t, mcp.Record)); r.Redactions != 0 || r.Action.InputHash != event.SHA256Hex([]byte(`{"title":"x"}`)) {
		t.Fatalf("MCP input_hash %s", r.Action.InputHash)
	}
}

// Cursor runs Claude Code's hooks too (Third-Party Imports, on by default). Input from Cursor
// to the Claude Code hook writes nothing, prints nothing and logs nothing, on every path:
// a Block rule match, a setup change that would be recorded as attempted, an unknown event.
func TestClaudeCodeHookStandsAsideForCursor(t *testing.T) {
	dir := repo(t)
	c := ctx()
	c.Rules.Rules = append(c.Rules.Rules, rules.Rule{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"})
	var inputs [][]byte
	for _, ev := range append(append([]string{}, CursorEvents...), ClaudeCodeEvents...) {
		for _, cmd := range []string{"rm -rf /srv/data", "rm -rf .sigdek", "npm run migrate -- --env=production", "ls"} {
			for _, tool := range []string{"Shell", "Bash"} {
				// As Cursor sends its own hooks, and as it might if it translated names to Claude Code's.
				inputs = append(inputs, cursorPayloadJSON(dir, ev, map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": cmd}, "cwd": dir}))
			}
		}
	}
	inputs = append(inputs,
		// Cursor's sessionStart also carries session_id, the same as conversation_id.
		cursorPayloadJSON(dir, "sessionStart", map[string]any{"session_id": "conv-1"}),
		// Only cursor_version, even null; or only conversation_id and generation_id.
		[]byte(`{"cursor_version":null,"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf .sigdek"}}`),
		[]byte(`{"conversation_id":"c","generation_id":"g","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /srv/data"}}`),
		[]byte(`{"cursor_version":"2.4.0","hook_event_name":"workspaceOpen"}`),
	)
	for _, in := range inputs {
		res := ClaudeCode(in, c)
		if !res.StandAside || res.Record != nil || res.Stdout != nil || res.Flush || res.Urgent || res.Drop != "" {
			t.Fatalf("%s: %+v", in, res)
		}
	}
	// A cut-off Cursor input stands aside too, when the field arrived.
	p := c
	p.Partial = true
	cut := `{"cursor_version":"2.4.0","conversation_id":"c","hook_event_name":"preToolUse","tool_name":"Shell","tool_input":{"command":"rm -rf /srv/data ` + strings.Repeat("y", 100)
	if res := ClaudeCode([]byte(cut), p); !res.StandAside || res.Record != nil || res.Stdout != nil {
		t.Fatalf("cut-off Cursor input: %+v", res)
	}
}

// Claude Code's own input never stands aside: it has none of Cursor's fields, and the
// environment (Cursor's terminal sets CURSOR_VERSION) plays no part.
func TestClaudeCodeInputIsNeverMistakenForCursor(t *testing.T) {
	dir := repo(t)
	c := ctx()
	c.Rules.Rules = append(c.Rules.Rules, rules.Rule{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"})
	// Values that mention Cursor, nested or in strings, are not Cursor's fields.
	in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /srv/data", "cursor_version": "2.4.0", "conversation_id": "c", "generation_id": "g"}, "model": "cursor_version"}
	if res := ClaudeCode(payload(dir, "PreToolUse", in), c); res.StandAside || res.Record == nil || res.Record.Outcome != "denied_by_rule" || res.Stdout == nil {
		t.Fatalf("Claude Code deny: %+v", res)
	}
	for _, ev := range ClaudeCodeEvents {
		res := ClaudeCode(payload(dir, ev, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "prompt": "x"}), ctx())
		if res.StandAside {
			t.Fatalf("%s stood aside for Claude Code", ev)
		}
	}
	// One of conversation_id and generation_id alone, at the top level, isn't enough.
	for _, field := range []string{"conversation_id", "generation_id"} {
		in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, field: "x"}
		if res := ClaudeCode(payload(dir, "PostToolUse", in), ctx()); res.StandAside || res.Record == nil {
			t.Fatalf("%s alone: %+v", field, res)
		}
	}
}
