package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/event"
)

// The pattern examples in docs/rules.md and the plugins' READMEs say what a pattern catches and
// what it misses. Each one is checked here against the matcher, so a change to matching that
// makes one untrue fails until the docs say what is true.

const terraformDestroy = `re:\b(terraform|tofu)\s+(-\S+\s+)*(destroy\b|apply\b[^|;&\n]*\s-destroy\b)`

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDocumentedPatternExamples(t *testing.T) {
	doc := readDoc(t, "docs/rules.md")
	for _, s := range []string{
		"`" + terraformDestroy + "`",
		"`terraform destroy*`", "`rm -rf*`", "`drop table`", "`**/prod-secrets.yml`",
		"`re:(?i)(^|/)prod-secrets\\.yml$`", "`github/merge_*`", "`*/merge_*`", "`re:(?i)github/merge_`",
		"up to 8 MB of hook input",
	} {
		if !strings.Contains(doc, s) {
			t.Errorf("docs/rules.md no longer has %s; update this test with the docs", s)
		}
	}
	for _, rel := range []string{"plugins/claude-code/README.md", "plugins/cursor/README.md"} {
		if !strings.Contains(readDoc(t, rel), "`"+terraformDestroy+"`") {
			t.Errorf("%s no longer has the terraform destroy pattern; update this test with the README", rel)
		}
	}

	deny := func(r Rule) Set { r.ID, r.Effect = "doc", Deny; return Set{Rules: []Rule{r}} }
	hits := func(s Set, a *event.Action) bool { return s.Match(a, Deny) != nil }

	// A glob matches its text in that order; the documented regex catches the other spellings.
	glob, re := deny(Rule{Command: "terraform destroy*"}), deny(Rule{Command: terraformDestroy})
	for _, c := range []string{"terraform -chdir=infra destroy", "terraform apply -destroy -auto-approve", "tofu destroy"} {
		if hits(glob, shell(c)) {
			t.Errorf("the docs say terraform destroy* misses %q, and it matched", c)
		}
		if !hits(re, shell(c)) {
			t.Errorf("the docs' terraform pattern misses %q", c)
		}
	}
	for _, c := range []string{"terraform destroy", "cd infra && terraform destroy -auto-approve"} {
		if !hits(glob, shell(c)) || !hits(re, shell(c)) {
			t.Errorf("both terraform patterns should match %q", c)
		}
	}
	for _, c := range []string{"terraform apply", "terraform plan -destroy", "terraform -chdir=infra apply"} {
		if hits(re, shell(c)) {
			t.Errorf("the docs' terraform pattern matched %q, which isn't a destroy", c)
		}
	}
	rmGlob := deny(Rule{Command: "rm -rf*"})
	for _, c := range []string{"rm -fr build", "rm -r -f build"} {
		if hits(rmGlob, shell(c)) {
			t.Errorf("the docs say rm -rf* misses %q, and it matched", c)
		}
	}

	// Patterns are case-sensitive, apart from the program name at the start of each command.
	if hits(deny(Rule{Command: "drop table"}), shell(`psql -c "DROP TABLE users"`)) {
		t.Error("the docs say drop table misses DROP TABLE, and it matched")
	}
	if !hits(rmGlob, shell("RM -rf build")) {
		t.Error("the docs say a command's program name is also read in lower case")
	}
	read := func(p string) *event.Action { return file("file_read", p) }
	if !hits(deny(Rule{Path: "**/prod-secrets.yml"}), read("config/prod-secrets.yml")) || hits(deny(Rule{Path: "**/prod-secrets.yml"}), read("config/PROD-SECRETS.yml")) {
		t.Error("the docs say **/prod-secrets.yml matches only that case")
	}
	fold := deny(Rule{Path: `re:(?i)(^|/)prod-secrets\.yml$`})
	if !hits(fold, read("config/prod-secrets.yml")) || !hits(fold, read("config/PROD-SECRETS.yml")) {
		t.Error("the docs' re:(?i) path example should match both cases")
	}

	// How Claude Code names claude.ai connectors and plugin servers.
	mcp := func(server string) *event.Action {
		return &event.Action{Kind: "mcp", MCP: &event.MCP{Server: server, Tool: "merge_pull_request"}}
	}
	plain := deny(Rule{MCP: "github/merge_*"})
	for _, server := range []string{"claude_ai_GitHub", "plugin_github_github"} {
		if hits(plain, mcp(server)) {
			t.Errorf("the docs say github/merge_* misses server %s, and it matched", server)
		}
	}
	for _, p := range []string{"*/merge_*", "re:(?i)github/merge_"} {
		for _, server := range []string{"github", "claude_ai_GitHub", "plugin_github_github"} {
			if !hits(deny(Rule{MCP: p}), mcp(server)) {
				t.Errorf("the docs say %s matches server %s", p, server)
			}
		}
	}
}

// A record carries the first matching rule, and rules are tried in the order they were added,
// so a team's Flag rule added after the built-in rules loses an action both match.
func TestARecordCarriesTheFirstMatchingRule(t *testing.T) {
	s := Defaults()
	s.Rules = append(s.Rules, Rule{ID: "team-psql-prod", Effect: Flag, Kinds: []string{"shell"}, Command: "psql*prod*"})
	if r := s.Match(shell(`psql -h prod.db.example -c "drop table users;"`), Flag); r == nil || r.ID != "database-destructive" {
		t.Errorf("got %+v, want the built-in database-destructive", r)
	}
	if r := s.Match(shell("psql -h prod.db.example"), Flag); r == nil || r.ID != "team-psql-prod" {
		t.Errorf("got %+v, want the team's rule", r)
	}
}
