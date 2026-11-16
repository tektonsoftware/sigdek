package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

// BB-RULE-1 through the binary: a command Block rule refuses the same command sent through
// Bash, Monitor and PowerShell, and changing SigDek's setup through Monitor is recorded as
// attempted before it runs. BB-HOOK-2: Grep with a glob meets a path Block rule.
func TestCommandToolsAndSearchesMeetBlockRules(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	set := rules.Defaults()
	for i := range set.Rules {
		if set.Rules[i].ID == "secrets-file" {
			set.Rules[i].Effect = rules.Deny
		}
	}
	set.Rules = append(set.Rules, rules.Rule{ID: "bbv-block", Label: "No bbv", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "*bbv-block-me*"})
	rules.Save(config.RulesPath(home), set)
	deny := func(r run) bool {
		var out struct {
			HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
		}
		return r.code == 0 && json.Unmarshal([]byte(r.out), &out) == nil && out.HookSpecificOutput["permissionDecision"] == "deny"
	}
	for _, tool := range []string{"Bash", "Monitor", "PowerShell"} {
		r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"`+tool+`","tool_input":{"command":"echo bbv-block-me","description":"d","timeout_ms":20000}`))
		if !deny(r) || !strings.Contains(r.out, "bbv-block") {
			t.Errorf("%s: %+v", tool, r)
		}
		if r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"`+tool+`","tool_input":{"command":"echo hello"}`)); r.code != 0 || r.out != "" {
			t.Errorf("%s allowed: %+v", tool, r)
		}
	}
	for _, in := range []string{
		`{"pattern":"API_SECRET","glob":".env","path":".","output_mode":"content"}`,
		`{"pattern":"API_SECRET","glob":"**/.env"}`,
	} {
		if r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Grep","tool_input":`+in)); !deny(r) || !strings.Contains(r.out, "secrets-file") {
			t.Errorf("Grep %s: %+v", in, r)
		}
	}
	if r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Grep","tool_input":{"pattern":"TODO"}`)); r.code != 0 || r.out != "" {
		t.Errorf("plain Grep: %+v", r)
	}
	if r := runHook(t, home, []string{"claude-code"}, cc(dir, "PreToolUse", `,"tool_name":"Monitor","tool_input":{"command":"rm -rf .sigdek && export SIGDEK_BIN=/usr/bin/true"}`)); r.code != 0 || r.out != "" {
		t.Errorf("setup change: %+v", r)
	}
	type rec struct {
		Outcome string `json:"outcome"`
		Rule    string `json:"rule"`
		Action  struct {
			Kind, Name, Command, Path string
		} `json:"action"`
	}
	var recs []rec
	store.New(home).ReadFrom(store.Cursor{}, func(l store.Line) bool {
		var r rec
		json.Unmarshal(l.Raw, &r)
		recs = append(recs, r)
		return true
	})
	var got []string
	for _, r := range recs {
		got = append(got, fmt.Sprintf("%s %s %s %s %q %q", r.Outcome, r.Rule, r.Action.Kind, r.Action.Name, r.Action.Command, r.Action.Path))
	}
	want := []string{
		`denied_by_rule bbv-block shell Bash "echo bbv-block-me" ""`,
		`denied_by_rule bbv-block shell Monitor "echo bbv-block-me" ""`,
		`denied_by_rule bbv-block shell PowerShell "echo bbv-block-me" ""`,
		`denied_by_rule secrets-file file_read Grep "" ".env"`,
		`denied_by_rule secrets-file file_read Grep "" "**/.env"`,
		`attempted sigdek-setup-shell shell Monitor "rm -rf .sigdek && export SIGDEK_BIN=/usr/bin/true" ""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
