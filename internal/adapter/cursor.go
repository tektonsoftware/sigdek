package adapter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/rules"
)

// CursorEvents are the hook events SigDek registers for Cursor. Cursor's general
// preToolUse and postToolUse hooks cover shell, file and MCP actions, including in its
// cloud agents, so the tool-specific hooks aren't needed.
var CursorEvents = []string{
	"sessionStart", "sessionEnd", "beforeSubmitPrompt", "preToolUse", "postToolUse",
	"postToolUseFailure", "subagentStart", "subagentStop", "stop",
}

var (
	cursorAllow    = []byte("{\"permission\":\"allow\"}\n")
	cursorContinue = []byte("{\"continue\":true}\n")
	cursorEmpty    = []byte("{}\n")
)

// CursorAllowFor is the safe answer for an event when anything goes wrong. For permission
// hooks Cursor blocks the action on invalid output, so this must always be valid.
func CursorAllowFor(hookEvent string) []byte {
	switch hookEvent {
	case "preToolUse", "subagentStart", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile":
		return cursorAllow
	case "beforeSubmitPrompt":
		return cursorContinue
	}
	return cursorEmpty
}

var eventNameRe = regexp.MustCompile(`"hook_event_name"\s*:\s*"([A-Za-z]+)"`)

// CursorEventName finds the event even in a payload that doesn't parse, falling back to
// the event name the hook command was registered with (`sigdek hook cursor <event>`).
func CursorEventName(raw []byte, hint string) string {
	var p struct {
		HookEventName string `json:"hook_event_name"`
	}
	if json.Unmarshal(raw, &p) == nil && p.HookEventName != "" {
		return p.HookEventName
	}
	if m := eventNameRe.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return hint
}

// Cursor handles one Cursor hook event. Every path prints valid JSON for that event.
// hint is the event name from the command line, used when the payload can't be read.
func Cursor(raw []byte, ctx Context, hint string) Result {
	p, err := decodePayload(raw, !ctx.Partial)
	if err != nil {
		return Result{Stdout: CursorAllowFor(CursorEventName(raw, hint)), Drop: err.Error()}
	}
	name := p.str("hook_event_name")
	if name == "" {
		name = hint
	}
	cwd := p.str("cwd")
	if roots := p.strs("workspace_roots"); cwd == "" && len(roots) > 0 {
		cwd = roots[0]
	}
	session := p.str("conversation_id", "session_id")
	rec := func(evt string) *event.Record {
		r := newRecord(ctx, "cursor", event.Str(p.str("cursor_version")), session, event.Str(p.str("generation_id")), cwd, evt)
		r.Model = event.Optional(modelName(p.value("model")))
		return r
	}
	toolInput := p["tool_input"]
	action := func(r *event.Record) *event.Action {
		a, view, n := buildAction(cursorAction(p.str("tool_name"), toolInput), toolInput, cwd)
		r.Action, r.Redactions = a, n
		return view
	}
	ok := CursorAllowFor(name)

	switch name {
	case "sessionStart":
		return Result{Record: rec("session_start"), Stdout: ok}
	case "sessionEnd":
		return Result{Record: rec("session_end"), Stdout: ok, Flush: true}
	case "beforeSubmitPrompt":
		r := rec("prompt")
		r.Prompt = promptOf(p.str("prompt"))
		return Result{Record: r, Stdout: ok}
	case "preToolUse":
		// As for Claude Code: the record is built only for a denial or a setup change.
		f := cursorAction(p.str("tool_name"), toolInput)
		view := actionView(f, cwd)
		deny := ctx.Rules.Match(view, rules.Deny)
		var setup *rules.Rule
		if deny == nil {
			if setup = ctx.Rules.MatchSetup(view); setup == nil {
				return Result{Stdout: ok}
			}
		}
		r := rec("action")
		r.Action, r.Redactions = storedAction(f, view, toolInput)
		if deny == nil {
			res := preRecord(r, setup)
			res.Stdout = ok
			return res
		}
		blocked(r, view, deny)
		msg := denyMessage(deny)
		out, _ := json.Marshal(map[string]string{"permission": "deny", "user_message": msg, "agent_message": msg})
		return Result{Record: r, Stdout: append(out, '\n')}
	case "postToolUse", "postToolUseFailure":
		r := rec("action")
		view := action(r)
		r.Outcome = "ran"
		if name == "postToolUseFailure" {
			r.Outcome = "failed"
			ft := strings.ToLower(p.str("failure_type"))
			if strings.Contains(ft, "denied") || strings.Contains(ft, "reject") || strings.Contains(ft, "permission") {
				r.Outcome = "denied"
			}
		}
		// A setup change was also recorded as attempted at preToolUse; this is its result.
		r.DurationMS = durationMS(p.num("duration_ms", "duration"))
		applyRule(r, view, ctx.Rules)
		return Result{Record: r, Stdout: ok}
	case "subagentStart":
		r := rec("subagent_start")
		r.Subagent = &event.Subagent{ID: p.str("subagent_id", "tool_call_id"), Type: p.str("subagent_type")}
		return Result{Record: r, Stdout: ok}
	case "subagentStop":
		// Cursor's subagentStop carries no subagent_id (only the type, status and duration), so
		// the record names the type and leaves the id empty rather than inventing one.
		r := rec("subagent_stop")
		r.Subagent = &event.Subagent{ID: p.str("subagent_id", "tool_call_id"), Type: p.str("subagent_type")}
		r.DurationMS = durationMS(p.num("duration_ms"))
		return Result{Record: r, Stdout: ok}
	case "stop":
		return Result{Stdout: ok} // nothing to record; an upload starts if one is due
	case "":
		return Result{Stdout: ok, Drop: "hook input has no hook_event_name"}
	}
	return Result{Stdout: ok, Drop: fmt.Sprintf("Cursor event %q is not one SigDek records", name)}
}

// cursorAction maps Cursor's tool names, which are less settled than Claude Code's, by
// what they contain. Unknown tools are recorded as "other" with their name.
func cursorAction(name string, rawInput json.RawMessage) actionFields {
	in := decodeObject(rawInput)
	f := actionFields{Kind: "other", Name: name}
	n := strings.ToLower(name)
	if server, tool, ok := splitMCP(name); ok {
		f.Kind, f.MCP = "mcp", &event.MCP{Server: server, Tool: tool}
		return f
	}
	// Cursor names MCP tools "MCP:<tool>" in preToolUse and postToolUse, without the server.
	// Rules then match on the tool alone. "MCP:<server>:<tool>" is read as both, in case a
	// version adds the server.
	if strings.HasPrefix(n, "mcp:") {
		rest := name[len("mcp:"):]
		server, tool, both := strings.Cut(rest, ":")
		if !both {
			server, tool = "", rest
		}
		f.Kind, f.MCP = "mcp", &event.MCP{Server: server, Tool: tool}
		return f
	}
	switch {
	case strings.Contains(n, "shell") || strings.Contains(n, "terminal") || n == "bash" || strings.Contains(n, "run_command"):
		f.Kind, f.Command = "shell", str(in, "command", "cmd")
	case strings.Contains(n, "edit") || strings.Contains(n, "write") || strings.Contains(n, "delete") ||
		strings.Contains(n, "replace") || strings.Contains(n, "patch") || strings.Contains(n, "create_file"):
		f.Kind, f.Path = "file_edit", str(in, "file_path", "path", "target_file", "filename")
	case strings.Contains(n, "read") || strings.Contains(n, "grep") || strings.Contains(n, "glob") ||
		strings.Contains(n, "list_dir") || strings.Contains(n, "search"):
		f.Kind, f.Path = "file_read", str(in, "file_path", "path", "target_file", "target_directory")
		if strings.Contains(n, "web") {
			f.Kind, f.Path = "web", ""
		}
	case strings.Contains(n, "web") || strings.Contains(n, "fetch") || strings.Contains(n, "browser"):
		f.Kind, f.URL = "web", str(in, "url")
	}
	return f
}
