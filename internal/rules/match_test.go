package rules

import (
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/event"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"git push \\\n  --force":        "git push --force",
		"r\\m -rf x":                    "rm -rf x",
		`'rm' -rf "a b"`:                "rm -rf a b",
		"r''m -r\"\"f":                  "rm -rf",
		`echo 'a\b'`:                    `echo a\b`,
		"RM -RF X; LS":                  "rm -RF X; ls",
		"a   b\t\tc  \n\n  SUDO x":      "a b c\nsudo x",
		"echo \"line1\nline2\" && RM x": "echo line1 line2 && rm x",
		"(CD /x)":                       "(cd /x)",
		"echo $(RM -rf /)":              "echo $(rm -rf /)",
		"trailing\\":                    "trailing",
		"  lead":                        "lead",
		"echo 'unterminated ; RM -rf":   "echo unterminated ; RM -rf",
		"ls | GREP x":                   "ls | grep x",
		"r$()m -rf x":                   "rm -rf x",
		"r``m -rf x":                    "rm -rf x",
		"rm${IFS}-rf${IFS}/x":           "rm -rf /x",
		"rm$IFS-rf x":                   "rm -rf x",
		`echo "$IFS" $IFSX '$IFS'`:      "echo $IFS $IFSX $IFS",
		"echo '$()'":                    "echo $()",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"a  b": "a b", " a ": "a", "a\t\r\nb": "a\nb", "a\n\n b": "a\nb", "ab": "ab",
	} {
		if got := Squeeze(in); got != want {
			t.Errorf("Squeeze(%q) = %q, want %q", in, got, want)
		}
	}
}

// QA2-REC-6: tool and MCP patterns name the whole tool, server or MCP tool; command
// patterns are still found anywhere.
func TestToolAndMCPPatternsMatchWholeNames(t *testing.T) {
	s := Set{Rules: []Rule{
		{ID: "write", Effect: Deny, Tool: "Write"},
		{ID: "merge", Effect: Deny, Tool: "mcp__github__merge_pull_request"},
		{ID: "gh", Effect: Deny, MCP: "github/merge_*"},
		{ID: "bare", Effect: Deny, MCP: "delete_*"},
	}}
	mcp := func(name, server, tool string) *event.Action {
		return &event.Action{Kind: "mcp", Name: name, MCP: &event.MCP{Server: server, Tool: tool}}
	}
	for _, c := range []struct {
		a    *event.Action
		want string
	}{
		{&event.Action{Kind: "file_edit", Name: "Write"}, "write"},
		{&event.Action{Kind: "other", Name: "TodoWrite"}, ""},
		{mcp("mcp__github__merge_pull_request", "github", "merge_pull_request"), "merge"},
		{mcp("mcp__github__merge_pull_request_status", "github", "merge_pull_request_status"), "gh"},
		{mcp("mcp__mygithub__merge_pr", "mygithub", "merge_pr"), ""},
		{mcp("mcp__github__automerge_x", "github", "automerge_x"), ""},
		{mcp("MCP:merge_pull_request", "", "merge_pull_request"), "gh"},
		{mcp("mcp__db__delete_rows", "db", "delete_rows"), "bare"},
		{mcp("mcp__db__undelete_rows", "db", "undelete_rows"), ""},
	} {
		got := ""
		if r := s.Match(c.a, Deny); r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("%s: matched %q, want %q", c.a.Name, got, c.want)
		}
	}
}

// QA2-REC-15: a pattern the recorder can't compile is named; JavaScript's \uXXXX works.
func TestProblemsNameUnusableRules(t *testing.T) {
	s := Set{Rules: []Rule{
		{ID: "ok", Effect: Deny, Command: `re:\brm\b`},
		{ID: "lookahead", Effect: Deny, Command: `re:rm(?!x)`},
		{ID: "unicode", Effect: Deny, Kinds: []string{"shell"}, Command: `re:drop-everything`},
		{ID: "bad-path", Effect: Flag, Path: `re:(`},
	}}
	ps := s.Problems()
	if len(ps) != 2 || ps[0].Rule != "lookahead" || ps[1].Rule != "bad-path" || !strings.Contains(ps[0].Error(), "command pattern") {
		t.Fatalf("problems %v", ps)
	}
	if r := s.Match(shell("drop-everything now"), Deny); r == nil || r.ID != "unicode" {
		t.Fatalf("\\u0064 should read as d: %+v", r)
	}
	if len(Defaults().Problems()) != 0 {
		t.Fatalf("built-in rules: %v", Defaults().Problems())
	}
}

// The literal prefilter never changes an answer, only skips work: for every built-in pattern
// and many commands, prefiltered matching agrees with the plain regular expression.
func TestPrefilterAgreesWithTheRegex(t *testing.T) {
	words := []string{"rm", "-rf", "git", "push", "--force", "sudo", "DROP", "table", "users", ";", "|", "&&", "\n", "curl", "sh",
		"terraform", "apply", "cat", ".env", ".sigdek", "x", "Kubectl", "delete", "TRUNCATE", "redis-cli", "FLUSHALL", "\"", "'", "K", "ſudo",
		"SIGDEK_BIN=", ".claude", "cd", ".sig*", "python3", "-c", "os.remove('.sigdek')", "--dry-run", "npm", "publish", "id_rsa.pub", "ca.pem",
		"key.pem", "true", "-n", "-h", "kubectl", "exec", "--", "-m", "psql", "(", "$(", "-s"}
	r := rand.New(rand.NewSource(1))
	var inputs []string
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for j := 0; j < 1+r.Intn(8); j++ {
			b.WriteString(words[r.Intn(len(words))])
			b.WriteByte(" \t"[r.Intn(2)])
		}
		inputs = append(inputs, b.String())
	}
	defer func(n int) { prefilterMin = n }(prefilterMin)
	prefilterMin = 0 // check every input, however short
	for _, rule := range Defaults().Rules {
		for _, p := range []string{rule.Command, rule.Path} {
			if !strings.HasPrefix(p, "re:") {
				continue
			}
			plain := regexp.MustCompile(p[3:])
			c := compile(p, modeText)
			for _, in := range inputs {
				if got, want := c.mayMatch(&text{s: in}) && c.regexp().MatchString(in), plain.MatchString(in); got != want {
					t.Fatalf("%s on %q: prefiltered %v, regex %v (literals %q)", rule.ID, in, got, want, c.lits)
				}
			}
		}
	}
	for expr, want := range map[string]string{
		`\bgit\s+push\b`:       "push",
		`(?i)\bdrop\s+table`:   "table",
		`terraform|tofu`:       "",
		`foo|bar`:              "bar foo",
		`a*b`:                  "",
		`(?i)KUBECTL\s+delete`: "kubectl",
	} {
		p, _ := syntax.Parse(expr, syntax.Perl)
		lits, _ := requiredLiterals(p.Simplify())
		got := strings.Join(sortedCopy(lits), " ")
		if expr == `terraform|tofu` {
			if len(lits) == 0 {
				t.Errorf("%s: no literals", expr)
			}
			continue
		}
		if got != want {
			t.Errorf("%s: literals %q, want %q", expr, got, want)
		}
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// QA2-REC-11: Flag rules on a long command stay fast; the built-in set on a 64 KB heredoc.
func TestFlagRulesOnLongCommandsAreFast(t *testing.T) {
	var b strings.Builder
	b.WriteString("cat > src/data.ts <<'EOF'\n")
	for i := 0; b.Len() < 64<<10; i++ {
		b.WriteString("export const item")
		b.WriteString(strings.Repeat("1", i%5+1))
		b.WriteString(` = { id: 1, name: "item-1", tags: ["a", "b"], push: () => format(x) };` + "\n")
	}
	b.WriteString("EOF")
	d := Defaults()
	a := shell(b.String())
	d.Match(a, Flag)
	start := time.Now()
	for i := 0; i < 5; i++ {
		d.Match(shell(b.String()), Flag)
	}
	if el := time.Since(start) / 5; el > 25*time.Millisecond && !raceDetector {
		t.Errorf("built-in Flag rules on a %d-byte heredoc took %v", b.Len(), el)
	} else {
		t.Logf("built-in Flag rules on a %d-byte heredoc: %v", b.Len(), el)
	}
}

// A hook checks the built-in rules without compiling those whose literals the command lacks:
// compiling them all costs about a millisecond, a good share of a hook's time.
func TestBuiltInRulesCompileOnlyWhenNeeded(t *testing.T) {
	cacheMu.Lock()
	cache = map[cacheKey]*compiled{}
	cacheMu.Unlock()
	d := Defaults()
	if r := d.Match(shell("npm test -- --coverage"), Flag); r != nil {
		t.Fatalf("matched %s", r.ID)
	}
	for _, r := range d.Rules {
		if r.Command != "" && compile(r.Command, modeText).re != nil {
			t.Errorf("%s was compiled for a command without its literals", r.ID)
		}
	}
	if r := d.Match(shell("git push --force"), Flag); r == nil || r.ID != "force-push" {
		t.Fatalf("force push: %v", r)
	}
	for _, r := range d.Rules {
		if p := r.Command + r.Path; !compile(p, modeText).known {
			t.Errorf("%s: literals not known ahead of time", r.ID)
		}
	}
}
