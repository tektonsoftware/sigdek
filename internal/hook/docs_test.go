package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// BB-HOOK-7: Block rules see only what the hook reads, so the docs that say they read a command
// whole must give the same limit as MaxStdin, and so must the matcher's own comment (QA2-L4-9).
func TestDocsGiveTheHookInputLimit(t *testing.T) {
	want := fmt.Sprintf("up to %d MB of hook input", MaxStdin>>20)
	for _, rel := range []string{"SECURITY.md", "docs/rules.md", "docs/recording.md"} {
		b := repoFile(t, rel)
		if !strings.Contains(b, want) {
			t.Errorf("%s doesn't say %q", rel, want)
		}
		if strings.Contains(b, "however long") {
			t.Errorf("%s says rules read a command however long; the hook reads at most %d MB", rel, MaxStdin>>20)
		}
	}
	match := strings.Join(strings.Fields(repoFile(t, "internal/rules/match.go")), " ")
	if strings.Contains(match, "whole, however long") || !strings.Contains(match, fmt.Sprintf("whole, up to the hook's %d MB of input (hook.MaxStdin)", MaxStdin>>20)) {
		t.Errorf("internal/rules/match.go's Match comment doesn't give the hook's %d MB limit", MaxStdin>>20)
	}
}

// SECURITY.md: "The hook reads no more than 8 MB of input and notes the cut in
// ~/.sigdek/errors.log, so a rule can't see the rest of a longer command" (QA2-L4-5). A Block
// rule sees a command whole up to the limit, sees what comes before the cut, and doesn't see
// what comes after it.
func TestHookInputOverTheLimit(t *testing.T) {
	notes := fmt.Sprintf("The hook reads no more than %d MB of input and notes the cut in `~/.sigdek/errors.log`, so a rule can't see the rest of a longer command.", MaxStdin>>20)
	if !strings.Contains(strings.Join(strings.Fields(repoFile(t, "SECURITY.md")), " "), notes) {
		t.Fatalf("SECURITY.md no longer says %q; update this test with it", notes)
	}
	dir := repo(t)
	cwd, _ := json.Marshal(dir)
	run := func(command string) (string, string) {
		home := t.TempDir()
		rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
		head := `{"session_id":"s1","cwd":` + string(cwd) + `,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"`
		var out bytes.Buffer
		Run("claude-code", "", strings.NewReader(head+command+`"}}`), &out, opts(home, nil))
		return out.String(), errorLog(home)
	}
	fill := func(n int) string { return "echo " + strings.Repeat("x", n) }
	room := MaxStdin - 1024 // leaves room for the rest of the hook input around the command

	// Whole, up to the limit: a command that ends just inside it is stopped by what ends it.
	if out, log := run(fill(room-100) + "; rm -rf /tmp/tail"); !strings.Contains(out, `"deny"`) || log != "" {
		t.Errorf("a command inside the limit wasn't stopped: out %q log %q", out, log)
	}
	// Past the limit: what comes before the cut is seen, and the cut is noted.
	if out, log := run("rm -rf /tmp/head; " + fill(MaxStdin)); !strings.Contains(out, `"deny"`) || !strings.Contains(log, "input is over 8 MB") {
		t.Errorf("a command whose start is inside the limit wasn't stopped: out %q log %q", out, log)
	}
	// What comes after the cut isn't seen.
	if out, log := run(fill(MaxStdin) + "; rm -rf /tmp/tail"); out != "" || !strings.Contains(log, "input is over 8 MB") {
		t.Errorf("past the limit: out %q log %q", out, log)
	}
}

// Every text that says what is never recorded also says that text written inside a shell
// command is part of the command (BB-SITE-2, QA2-L4-1, QA2-L4-6), in the same paragraph:
// the READMEs, the plugins' descriptions, the winget description made at each release, and
// the README `sigdek init` writes.
func TestNeverRecordedTextsGiveTheCommandCaveat(t *testing.T) {
	caveat := func(s string) bool {
		s = strings.Join(strings.Fields(s), " ")
		return strings.Contains(s, "inside a shell command") || strings.Contains(s, "written inside one, such as a heredoc")
	}
	for _, rel := range []string{
		"README.md", "npm/sigdek/README.md", "plugins/claude-code/README.md", "plugins/cursor/README.md",
		"scripts/package-managers.mjs", "internal/initcmd/initcmd.go",
	} {
		found := false
		for _, para := range strings.Split(repoFile(t, rel), "\n\n") {
			for _, line := range strings.Split(para, "\n") {
				if !strings.Contains(strings.ToLower(line), "never record") {
					continue
				}
				found = true
				// A Markdown or YAML line stands alone; the README init writes is a wrapped paragraph.
				if text := line; !caveat(text) && (!strings.HasSuffix(rel, ".go") || !caveat(para)) {
					t.Errorf("%s says what is never recorded without the inside-a-command caveat: %q", rel, strings.TrimSpace(line))
				}
			}
		}
		if !found {
			t.Errorf("%s no longer says what is never recorded; update this test", rel)
		}
	}
	for _, rel := range []string{"plugins/claude-code/.claude-plugin/plugin.json", "plugins/cursor/.cursor-plugin/plugin.json"} {
		var m struct{ Description string }
		if err := json.Unmarshal([]byte(repoFile(t, rel)), &m); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(m.Description), "never record") || !caveat(m.Description) {
			t.Errorf("%s's description: %q", rel, m.Description)
		}
	}
}

// The record the hook writes for a heredoc holds the text written inside it, which is why the
// texts above give the caveat.
func TestAHeredocIsInTheRecord(t *testing.T) {
	home, dir := t.TempDir(), repo(t)
	cwd, _ := json.Marshal(dir)
	command, _ := json.Marshal("cat > notes.txt <<'EOF'\nFILE-CONTENT-LINE\nEOF")
	in := `{"session_id":"s1","cwd":` + string(cwd) + `,"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":` + string(command) + `}}`
	Run("claude-code", "", strings.NewReader(in), &bytes.Buffer{}, opts(home, nil))
	var line string
	store.New(home).ReadFrom(store.Cursor{}, func(l store.Line) bool { line = string(l.Raw); return true })
	if !strings.Contains(line, "FILE-CONTENT-LINE") {
		t.Fatalf("record: %s", line)
	}
}
