package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/rules"
)

// secretsBlocked is the built-in rules with "Secrets file accessed" set to Block, as a team
// can do from the dashboard.
func secretsBlocked() Context {
	c := ctx()
	set := rules.Set{Version: c.Rules.Version}
	for _, r := range c.Rules.Rules {
		if r.ID == "secrets-file" {
			r.Effect = rules.Deny
		}
		set.Rules = append(set.Rules, r)
	}
	c.Rules = set
	return c
}

// BB-HOOK-2: a search with a glob is matched, and recorded, as its folder joined with the
// glob, so Grep --glob .env is refused like Read .env. A search with no folder searches the
// session's folder. A plain search of the repository stays allowed.
func TestSearchesWithAGlobMeetPathRules(t *testing.T) {
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("API_SECRET=x\n"), 0o600)
	abs := filepath.Join(dir, ".env")
	refused := []struct {
		tool  string
		input map[string]any
		path  string
	}{
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": ".env", "path": ".", "output_mode": "content"}, ".env"},
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": ".env", "path": dir, "output_mode": "content"}, ".env"},
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": "**/.env", "output_mode": "content"}, "**/.env"},
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": ".env.*"}, ".env.*"},
		{"Grep", map[string]any{"pattern": "PRIVATE KEY", "glob": "*.pem", "path": "certs"}, "certs/*.pem"},
		{"Grep", map[string]any{"pattern": "x", "glob": "*.key"}, "*.key"},
		// The folder alone still counts: a glob doesn't hide the file being searched.
		{"Grep", map[string]any{"pattern": "API_SECRET", "path": abs, "glob": "*.ts"}, ".env/*.ts"},
		{"Grep", map[string]any{"pattern": "API_SECRET", "path": abs}, ".env"},
		{"Glob", map[string]any{"pattern": ".env"}, ".env"},
		{"Glob", map[string]any{"pattern": "**/.env"}, "**/.env"},
		{"Glob", map[string]any{"pattern": "*.pem", "path": dir}, "*.pem"},
		{"Glob", map[string]any{"pattern": abs}, ".env"},
	}
	for _, c := range refused {
		res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), secretsBlocked())
		if !denied(t, res) {
			t.Errorf("%s %v: allowed", c.tool, c.input)
			continue
		}
		sealed(t, res.Record)
		if a := res.Record.Action; a.Kind != "file_read" || a.Path != c.path || res.Record.Rule != "secrets-file" {
			t.Errorf("%s %v: record %+v rule %q", c.tool, c.input, a, res.Record.Rule)
		}
		// With the built-in rules as they come (Flag), the same search is labelled, and the
		// record shows the glob.
		ran := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), ctx())
		sealed(t, ran.Record)
		if ran.Record.Rule != "secrets-file" || ran.Record.Action.Path != c.path {
			t.Errorf("%s %v: finished record %+v rule %q", c.tool, c.input, ran.Record.Action, ran.Record.Rule)
		}
	}

	allowed := []struct {
		tool  string
		input map[string]any
		path  string
	}{
		{"Grep", map[string]any{"pattern": "API_SECRET", "output_mode": "content"}, "."},
		{"Grep", map[string]any{"pattern": "TODO", "path": "."}, "."},
		{"Grep", map[string]any{"pattern": "TODO", "glob": "*.ts"}, "*.ts"},
		{"Grep", map[string]any{"pattern": "TODO", "glob": "**/*.go", "path": "src"}, "src/**/*.go"},
		{"Glob", map[string]any{"pattern": "**/*.ts"}, "**/*.ts"},
		{"Glob", map[string]any{"pattern": ".env.example"}, ".env.example"},
	}
	for _, c := range allowed {
		res := ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), secretsBlocked())
		if res.Stdout != nil || res.Record != nil {
			t.Errorf("%s %v: %+v", c.tool, c.input, res)
		}
		ran := ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input}), ctx())
		sealed(t, ran.Record)
		if a := ran.Record.Action; a.Kind != "file_read" || a.Path != c.path || ran.Record.Rule != "" {
			t.Errorf("%s %v: finished record %+v rule %q", c.tool, c.input, a, ran.Record.Rule)
		}
	}

	// A search in a subfolder of the repository, with no path, searches that subfolder.
	sub := filepath.Join(dir, "api")
	os.MkdirAll(sub, 0o755)
	res := ClaudeCode(payload(sub, "PostToolUse", map[string]any{"tool_name": "Grep", "tool_input": map[string]any{"pattern": "x", "glob": "*.go"}}), ctx())
	if p := res.Record.Action.Path; p != "api/*.go" {
		t.Errorf("subfolder search: %q", p)
	}
}

// BB-HOOK-2 for Cursor: its grep's glob (or include pattern) and its glob search's
// glob_pattern are matched the same way.
func TestCursorSearchesWithAGlobMeetPathRules(t *testing.T) {
	dir := repo(t)
	refused := []struct {
		tool  string
		input map[string]any
		path  string
	}{
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": ".env", "path": dir}, ".env"},
		{"Grep", map[string]any{"pattern": "API_SECRET", "glob": ".env"}, ".env"},
		{"grep_search", map[string]any{"query": "API_SECRET", "include_pattern": "*.pem"}, "*.pem"},
		{"glob_file_search", map[string]any{"glob_pattern": "**/.env", "target_directory": dir}, "**/.env"},
	}
	for _, c := range refused {
		res := Cursor(cursorPayloadJSON(dir, "preToolUse", map[string]any{"tool_name": c.tool, "tool_input": c.input, "cwd": dir}), secretsBlocked(), "")
		if !strings.Contains(string(res.Stdout), `"permission":"deny"`) {
			t.Errorf("%s %v: %s", c.tool, c.input, res.Stdout)
			continue
		}
		sealed(t, res.Record)
		if a := res.Record.Action; a.Kind != "file_read" || a.Path != c.path {
			t.Errorf("%s %v: %+v", c.tool, c.input, a)
		}
	}
	for _, in := range []map[string]any{{"pattern": "TODO"}, {"pattern": "TODO", "glob": "*.ts"}} {
		res := Cursor(cursorPayloadJSON(dir, "preToolUse", map[string]any{"tool_name": "Grep", "tool_input": in, "cwd": dir}), secretsBlocked(), "")
		if string(res.Stdout) != "{\"permission\":\"allow\"}\n" || res.Record != nil {
			t.Errorf("%v: %s %+v", in, res.Stdout, res.Record)
		}
	}
	// web search is still a web action, not a file search.
	web := Cursor(cursorPayloadJSON(dir, "postToolUse", map[string]any{"tool_name": "web_search", "tool_input": map[string]any{"query": "x", "glob": ".env"}, "cwd": dir}), ctx(), "")
	if web.Record.Action.Kind != "web" || web.Record.Action.Path != "" {
		t.Errorf("web search: %+v", web.Record.Action)
	}
}
