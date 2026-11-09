package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/rules"
)

// ClaudeCodeEvents are the hook events SigDek registers for Claude Code.
var ClaudeCodeEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PostToolUseFailure", "PermissionDenied", "SubagentStart", "SubagentStop", "Stop", "ConfigChange",
}

// ClaudeCode handles one Claude Code hook event. Claude Code adds stdout to the model's
// context on some events, so Stdout is only ever set for a PreToolUse denial.
func ClaudeCode(raw []byte, ctx Context) Result {
	p, err := decodePayload(raw, !ctx.Partial)
	if err != nil {
		return Result{Drop: err.Error()}
	}
	if fromCursor(p) {
		// Before anything else, so no path records it: not a denial, not an attempted setup change.
		return Result{StandAside: true}
	}
	cwd := p.str("cwd")
	rec := func(evt string) *event.Record {
		r := newRecord(ctx, "claude-code", nil, p.str("session_id"), event.Str(p.str("prompt_id")), cwd, evt)
		r.PermissionMode = p.nullable("permission_mode")
		if id := p.str("agent_id"); id != "" {
			r.Subagent = &event.Subagent{ID: id, Type: p.str("agent_type")}
		}
		return r
	}
	toolInput := p["tool_input"]
	action := func(r *event.Record) *event.Action {
		a, view, n := buildAction(claudeCodeAction(p.str("tool_name"), toolInput), toolInput, cwd)
		r.Action, r.Redactions = a, n
		return view
	}

	switch name := p.str("hook_event_name"); name {
	case "SessionStart":
		r := rec("session_start")
		r.Model = event.Optional(modelName(p.value("model")))
		return Result{Record: r}
	case "SessionEnd":
		return Result{Record: rec("session_end"), Flush: true}
	case "UserPromptSubmit":
		r := rec("prompt")
		r.Prompt = promptOf(p.str("prompt"))
		return Result{Record: r}
	case "PreToolUse":
		// Most of these write nothing: an allowed action is recorded by PostToolUse once it has
		// run, so the record (and its redaction) is built only for a denial or a setup change.
		f := claudeCodeAction(p.str("tool_name"), toolInput)
		view := actionView(f, cwd)
		deny := ctx.Rules.Match(view, rules.Deny)
		var setup *rules.Rule
		if deny == nil {
			// A change to SigDek's own setup may leave nothing to record it afterwards.
			if setup = ctx.Rules.MatchSetup(view); setup == nil {
				return Result{}
			}
		}
		r := rec("action")
		r.Action, r.Redactions = storedAction(f, view, toolInput)
		if deny == nil {
			return preRecord(r, setup)
		}
		blocked(r, view, deny, toolInput)
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": denyMessage(deny),
		}})
		return Result{Record: r, Stdout: append(out, '\n')}
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		r := rec("action")
		view := action(r)
		switch name {
		case "PostToolUse":
			r.Outcome = "ran"
		case "PostToolUseFailure":
			r.Outcome = "failed"
		default:
			r.Outcome = "denied" // auto mode refused the call
		}
		// A setup change was also recorded as attempted at PreToolUse; this is its result.
		r.DurationMS = durationMS(p.num("duration_ms"))
		applyRule(r, view, ctx.Rules)
		return Result{Record: r}
	case "SubagentStart", "SubagentStop":
		evt := "subagent_start"
		if name == "SubagentStop" {
			evt = "subagent_stop"
		}
		r := rec(evt)
		if r.Subagent == nil {
			r.Subagent = &event.Subagent{Type: p.str("agent_type")}
		}
		return Result{Record: r}
	case "ConfigChange":
		r := rec("config_change")
		path, _, n := buildAction(actionFields{Kind: "other", Path: p.str("file_path")}, nil, cwd)
		r.Config = &event.Config{Source: event.Str(p.str("source")), Path: event.Str(path.Path)}
		r.Redactions = n
		return Result{Record: r}
	case "Stop":
		// Nothing to record. The turn is over, so its records go up now: an agent can sit idle
		// for hours after a turn, and no later hook would send them until the next prompt or
		// the session's end (SMOKE-1). One upload per turn is well under ingest's limit.
		return Result{Flush: true}
	case "":
		return Result{Drop: "hook input has no hook_event_name"}
	default:
		return Result{Drop: fmt.Sprintf("Claude Code event %q is not one SigDek records", name)}
	}
}

// fromCursor reports whether input sent to SigDek's Claude Code hook came from Cursor, which
// runs Claude Code's hooks as well as its own: its Third-Party Imports setting, on by default,
// loads hooks from .claude/settings.local.json, .claude/settings.json and
// ~/.claude/settings.json, so in a repository set up with `sigdek init` every Cursor action
// reaches both of SigDek's hooks. The Cursor hook records it; this one stands aside, so it is
// recorded once and as Cursor. Every Cursor hook input carries cursor_version,
// conversation_id and generation_id, and Claude Code's carries none of them, so the test is
// a top-level cursor_version field, or conversation_id and generation_id together. It reads
// only the input, never the environment: Claude Code run in Cursor's terminal inherits
// CURSOR_VERSION and CURSOR_PROJECT_DIR, and must still be recorded.
func fromCursor(p hookInput) bool {
	if _, ok := p["cursor_version"]; ok {
		return true
	}
	_, conversation := p["conversation_id"]
	_, generation := p["generation_id"]
	return conversation && generation
}

func claudeCodeAction(name string, rawInput json.RawMessage) actionFields {
	in := decodeObject(rawInput)
	f := actionFields{Kind: "other", Name: name}
	switch name {
	case "Bash":
		f.Kind, f.Command = "shell", str(in, "command")
	case "Read", "NotebookRead":
		f.Kind, f.Path = "file_read", str(in, "file_path", "notebook_path")
	case "Grep", "Glob", "LS":
		f.Kind, f.Path = "file_read", str(in, "path")
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		f.Kind, f.Path = "file_edit", str(in, "file_path", "notebook_path")
	case "WebFetch":
		f.Kind, f.URL = "web", str(in, "url")
	case "WebSearch":
		f.Kind = "web"
	default:
		if server, tool, ok := splitMCP(name); ok {
			f.Kind, f.MCP = "mcp", &event.MCP{Server: server, Tool: tool}
		}
	}
	return f
}

// splitMCP parses "mcp__<server>__<tool>".
func splitMCP(name string) (string, string, bool) {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", "", false
	}
	server, tool, ok := strings.Cut(rest, "__")
	return server, tool, ok && server != "" && tool != ""
}
