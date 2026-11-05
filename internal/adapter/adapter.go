// Package adapter turns each agent tool's hook payload into a SigDek record and the
// response the tool expects. All tool-specific knowledge lives in this package.
package adapter

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/gitinfo"
	"github.com/tektonsoftware/sigdek/internal/redact"
	"github.com/tektonsoftware/sigdek/internal/rules"
)

// Context is what a handler needs besides the payload.
type Context struct {
	Now    time.Time
	Device config.Device
	Rules  rules.Set
	// Partial is set when the input was cut off (over the size limit, or stdin never
	// closed); the handler then works with the fields that arrived.
	Partial bool
}

// Result is what the hook does: print Stdout, write Record (unsealed; nil for none), and
// start a background upload when Flush is set, or upload before returning when Urgent is set.
// Drop, when set, says why an event that arrived wrote nothing, for the error log. StandAside
// says the event isn't this hook's to handle (Cursor running SigDek's Claude Code hook): the
// hook then prints, writes, logs and uploads nothing.
type Result struct {
	Record     *event.Record
	Stdout     []byte
	Flush      bool
	Urgent     bool
	Drop       string
	StandAside bool
}

func newRecord(ctx Context, tool string, toolVersion *string, session string, promptID *string, cwd, evt string) *event.Record {
	repo := gitinfo.Find(cwd)
	var user *string
	if ctx.Device.User != "" {
		u := ctx.Device.User
		user = &u
	}
	if session == "" {
		session = "unknown"
	}
	return &event.Record{
		V: 1, ID: event.NewID(ctx.Now), TS: event.Timestamp(ctx.Now),
		Device: ctx.Device.ID, User: user, Tool: tool, ToolVersion: toolVersion,
		Session: session, PromptID: promptID,
		Repo:  event.Repo{Remote: repo.Remote, RootHash: repo.RootHash},
		Event: evt,
	}
}

// actionFields is the tool-independent description of one tool call.
type actionFields struct {
	Kind    string
	Name    string
	Command string
	Path    string
	URL     string
	MCP     *event.MCP
}

// buildAction returns the action to store and the action to match rules against.
func buildAction(f actionFields, rawInput json.RawMessage, cwd string) (stored, view *event.Action, redactions int) {
	view = actionView(f, cwd)
	stored, redactions = storedAction(f, view, rawInput)
	return stored, view, redactions
}

// actionView is the action as rules see it: the command, path and URL as the agent sent them,
// before anything is cut or redacted, so padding can't push a command past the cut and out of
// a rule's sight. Only the path is made repository-relative.
func actionView(f actionFields, cwd string) *event.Action {
	view := &event.Action{Kind: f.Kind, Name: f.Name, Command: f.Command, URL: f.URL, MCP: f.MCP}
	if f.Path != "" {
		view.Path = gitinfo.RelPath(gitinfo.Find(cwd).Root, f.Path)
	}
	return view
}

// storedAction is the action as recorded: cut to the schema's limits first and redacted after,
// which keeps redaction fast however long the input; the full input is kept only as a hash
// (actionInputHash).
func storedAction(f actionFields, view *event.Action, rawInput json.RawMessage) (stored *event.Action, redactions int) {
	stored = &event.Action{Kind: f.Kind, Name: event.Clean(f.Name, event.MaxActionName)}
	if f.Command != "" {
		c, n := storeText(f.Command, event.MaxCommand)
		stored.Command, redactions = c, redactions+n
	}
	if view.Path != "" {
		p, n := storeText(view.Path, event.MaxPath)
		stored.Path, redactions = p, redactions+n
	}
	if f.URL != "" {
		u, n := cleanURL(f.URL)
		stored.URL, redactions = u, redactions+n
	}
	if f.MCP != nil {
		stored.MCP = &event.MCP{Server: event.Clean(f.MCP.Server, event.MaxMCPName), Tool: event.Clean(f.MCP.Tool, event.MaxMCPName)}
	}
	stored.InputHash = actionInputHash(stored, redactions, rawInput)
	return stored, redactions
}

// storeText fits a value in a field of max bytes: no NUL, valid UTF-8, secrets redacted. A
// value that doesn't fit is cut and ends with a note saying so and how long it was.
func storeText(s string, max int) (string, int) {
	total := -1
	if len(s) > max+redact.Margin {
		total = utf8.RuneCountInString(s)
		s = event.Clean(s, max+redact.Margin) // redaction is slow, so cut first
	} else {
		s = event.Clean(s, 0)
	}
	out, n := redact.String(s)
	if total < 0 && len(out) <= max {
		return out, n
	}
	if total < 0 {
		total = utf8.RuneCountInString(s)
	}
	note := " … [cut from " + commas(total) + " characters]"
	return event.Clean(out, max-len(note)) + note, n
}

// blockedText is storeText for a command a Block rule stopped: when the command doesn't fit,
// the note also holds the part the rule matched (match), which padding may have pushed past
// the cut.
func blockedText(s, match string, max int) (string, int) {
	if match == "" || len(s) <= max {
		return storeText(s, max)
	}
	m, n := redact.String(event.Clean(match, 0))
	m = event.Clean(m, max/2)
	note := " … [cut from " + commas(utf8.RuneCountInString(s)) + " characters; the rule matched: " + m + "]"
	head, k := storeText(s, max-len(note))
	if i := strings.LastIndex(head, " … [cut from "); i >= 0 {
		head = head[:i]
	}
	return head + note, n + k
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// actionInputHash is a stored action's input_hash. With nothing stripped from the action, it is
// the SHA-256 of the tool input exactly as the agent sent it. With a secret stripped
// (redactions > 0), that hash would let a guess at the secret be confirmed, so it is the hash of
// the stored, redacted action instead (event.RedactedInputHash). Only the one used is computed.
func actionInputHash(a *event.Action, redactions int, raw json.RawMessage) string {
	if redactions > 0 {
		return event.RedactedInputHash(a)
	}
	return inputHash(raw)
}

func inputHash(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("null")
	}
	return event.SHA256Hex(raw)
}

// cleanURL removes user info and redacts secrets in the query.
func cleanURL(raw string) (string, int) {
	n := 0
	if len(raw) > event.MaxURL+redact.Margin {
		raw = event.Clean(raw, event.MaxURL+redact.Margin)
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		raw = u.String()
		n++
	}
	s, m := storeText(raw, event.MaxURL)
	return s, n + m
}

func promptOf(text string) *event.Prompt {
	return &event.Prompt{Hash: event.SHA256Hex([]byte(text)), Length: utf8.RuneCountInString(text)}
}

// preRecord records an allowed action before it runs when it changes SigDek's own setup
// (rm -rf .sigdek, sigdek logout, editing the hook files): once it has run, the launcher or
// the log may be gone, so the after-the-fact record would never be written. The record reads
// Attempted: the user may still decline it, and a decline fires no later hook. When a later
// hook does fire for the same call, it records the result as usual (ran, failed, denied).
// On a signed-in machine the hook uploads it before returning, for at most about a second,
// since the action may delete the log as soon as the hook is done; if that upload doesn't
// finish in time, a background upload takes over, which the deletion can still beat.
func preRecord(r *event.Record, setup *rules.Rule) Result {
	r.Outcome, r.Rule = "attempted", setup.ID
	return Result{Record: r, Flush: true, Urgent: true}
}

// blocked marks a record as stopped by a Block rule. A command too long for the record keeps
// its first part and, in the note at the end, the part the rule matched; its input_hash is
// then worked out again, since a redacted action's hash covers the stored command.
func blocked(r *event.Record, view *event.Action, deny *rules.Rule, rawInput json.RawMessage) {
	r.Outcome, r.Rule = "denied_by_rule", deny.ID
	if len(view.Command) > event.MaxCommand {
		if m := rules.Excerpt(deny, view, event.MaxCommand/2); m != "" {
			// Only shell actions have a command, and no path or URL, so its count is the record's.
			r.Action.Command, r.Redactions = blockedText(view.Command, m, event.MaxCommand)
			r.Action.InputHash = actionInputHash(r.Action, r.Redactions, rawInput)
		}
	}
}

// applyRule tags a finished action with the first matching sensitive-action rule.
func applyRule(r *event.Record, view *event.Action, set rules.Set) {
	if m := set.Match(view, rules.Flag); m != nil {
		r.Rule = m.ID
	}
}

// durationMS rounds a duration to whole milliseconds, within what the server can hold.
func durationMS(v *float64) *int64 {
	if v == nil || math.IsNaN(*v) || *v < 0 {
		return nil
	}
	ms := int64(event.MaxSafeInteger)
	if *v < event.MaxSafeInteger {
		ms = int64(*v + 0.5)
	}
	return &ms
}

// decodeObject decodes a tool's input. Some send it as a JSON string that holds the object,
// which is decoded too, so a Block rule still sees the command.
func decodeObject(raw json.RawMessage) map[string]any {
	var m map[string]any
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = bytes.TrimSpace([]byte(s))
		}
	}
	if len(raw) > 0 && json.Unmarshal(raw, &m) != nil {
		if p := partialObject(raw); len(p) > 0 { // too deep for encoding/json, or cut off
			m = map[string]any{}
			for k, v := range p {
				var x any
				if json.Unmarshal(v, &x) == nil {
					m[k] = x
				}
			}
		}
	}
	return m
}

// str reads a string field from a decoded JSON object.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// modelName accepts a model given as a string or as {"id": ..., "display_name": ...}.
func modelName(v any) *string {
	switch t := v.(type) {
	case string:
		return event.Str(t)
	case map[string]any:
		return event.Str(str(t, "id", "name", "display_name"))
	}
	return nil
}

func denyMessage(r *rules.Rule) string {
	label := r.Label
	if label == "" {
		label = r.ID
	}
	msg := "Blocked by your team's SigDek rule \"" + label + "\" (" + r.ID + ")."
	if r.Message != "" {
		msg += " " + strings.TrimSpace(r.Message)
	}
	return msg
}
