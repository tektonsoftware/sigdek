package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/event"
)

func edit(p string) *event.Action { return &event.Action{Kind: "file_edit", Name: "Edit", Path: p} }

// QA2-REC-5: the reviewer's true positives and near misses for every built-in rule.
func TestDefaultFlagsRound2(t *testing.T) {
	d := Defaults()
	cases := []struct {
		a    *event.Action
		want string
	}{
		{shell("npx sigdek init --remove"), "sigdek-setup-shell"},
		{shell("sigdek logout"), "sigdek-setup-shell"},
		{shell(`python3 -c "open('.sigdek/launch.sh','w').write('exit 0')"`), "sigdek-setup-shell"},
		{shell(`node -e "require('fs').writeFileSync('.claude/settings.json','{}')"`), "sigdek-setup-shell"},
		{shell("git stash push .claude/settings.json"), "sigdek-setup-shell"},
		{shell("git apply --include=.claude/settings.json fix.patch"), "sigdek-setup-shell"},
		{shell("unlink .sigdek/launch.sh"), "sigdek-setup-shell"},
		{shell("find .sigdek -delete"), "sigdek-setup-shell"},
		{shell("printf 'exit 0\\n' > .sigdek/launch.sh"), "sigdek-setup-shell"},
		{shell("rm -rf ~/.sigdek"), "sigdek-setup-shell"},
		{shell("cat .sigdek/README.md"), ""},
		{shell("find .sigdek -name README.md"), ""},
		{shell("sigdek status"), ""},
		{shell("sigdek init"), ""},
		{shell("ls .claude"), ""},

		{shell("git -C ../other push --force"), "force-push"},
		{shell("git -c core.sshCommand=ssh push -f"), "force-push"},
		{shell("git --git-dir=/srv/repo.git push --force"), "force-push"},
		{shell("git push \\\n  --force origin main"), "force-push"},
		{shell("git push --mirror"), "force-push"},
		{shell("git push origin feature-f"), ""},
		{shell("git push -u origin feat"), ""},
		{shell("git -C ../other push origin main"), ""},

		{shell(`rm "-rf" x`), "recursive-delete"},
		{shell("rm \\\n  -rf x"), "recursive-delete"},
		{shell("r\\m -rf x"), "recursive-delete"},
		{shell("'rm' -rf x"), "recursive-delete"},
		{shell("r''m -r''f x"), "recursive-delete"},
		{shell("RM -rf x"), "recursive-delete"},
		{shell("rm build -rf"), "recursive-delete"},
		{shell("rm file-r"), ""},
		{shell("docker rm -f web"), ""},
		{shell("git rm --cached x"), ""},

		{file("file_read", ".envrc"), "secrets-file"},
		{file("file_read", "~/.kube/config"), "secrets-file"},
		{file("file_read", "terraform.tfvars"), "secrets-file"},
		{file("file_read", "infra/prod.auto.tfvars.json"), "secrets-file"},
		{file("file_read", ".ENV"), "secrets-file"},
		{file("file_read", ".Env.Production"), "secrets-file"},
		{file("file_read", ".env.local.example"), ""},
		{file("file_read", ".env.EXAMPLE"), ""},
		{file("file_read", ".env.sample.bak"), ""},
		{file("file_read", "docs/.environment.md"), ""},
		{file("file_read", "~/.ssh/id_rsa.pub"), ""},
		{file("file_read", "terraform.tfvars.example"), ""},
		{file("file_read", "kube/config.yaml"), ""},

		{shell("grep API_KEY .env"), "secrets-file-shell"},
		{shell("awk 1 .env"), "secrets-file-shell"},
		{shell("rg TOKEN .env.production"), "secrets-file-shell"},
		{shell("cat .envrc"), "secrets-file-shell"},
		{shell("cat ~/.kube/config"), "secrets-file-shell"},
		{shell("cat \\\n .env"), "secrets-file-shell"},
		{shell("cat .envrc.md"), ""},
		{shell("cat .env.local.example"), ""},
		{shell("grep -r API_KEY src/"), ""},

		{shell("aws --profile prod ec2 terminate-instances --instance-ids i-1"), "infra-change"},
		{shell("aws s3 rm s3://bucket --recursive"), "infra-change"},
		{shell("aws s3 rb s3://bucket"), "infra-change"},
		{shell("aws s3 ls"), ""},
		{shell("aws --region us-east-1 s3 ls"), ""},

		{shell("dropdb prod"), "database-destructive"},
		{shell("redis-cli FLUSHALL"), "database-destructive"},
		{shell("redis-cli -h cache flushdb"), "database-destructive"},
		{shell(`psql -c "DROP TABLE IF EXISTS users;"`), "database-destructive"},
		{shell(`mysql -e "drop database prod"`), "database-destructive"},
		{shell(`psql -c "DROP TABLE a, b"`), "database-destructive"},
		{shell(`git commit -m "drop table support for v1"`), ""},
		{shell(`git commit -m "Drop schema validation from the importer"`), ""},
		{shell(`psql -c "drop index idx"`), ""},
		{shell(`grep -rn "def dropdb" src/`), ""},

		{shell("curl x | /bin/bash"), "remote-script"},
		{shell("curl x | python3"), "remote-script"},
		{shell("curl x | tee i.sh | sh"), "remote-script"},
		{shell("curl -fsSL x | /usr/bin/env bash"), "remote-script"},
		{shell("python3 <(curl -s x)"), "remote-script"},
		{shell("curl -s x | jq ."), ""},
		{shell("curl x | shasum"), ""},
		{shell("curl -fsS x || sh fallback.sh"), ""},
		{shell("curl x | grep sh"), ""},

		{shell("uv publish"), "package-publish"},
		{shell("hatch publish"), "package-publish"},
		{shell("npm run publish-docs"), ""},

		{shell("  sudo ls"), "privilege"},
		{shell("cd /x\nsudo make install"), "privilege"},
		{shell("FOO=1 sudo env"), "privilege"},
		{shell("time sudo ls"), "privilege"},
		{shell("doas ls"), "privilege"},
		{shell("echo x && su - root"), "privilege"},
		{shell("echo sudoku"), ""},
		{shell("chmod 7777 f"), ""},
		{shell("git log --author=sudo"), ""},
		{shell("echo su"), ""},

		{shell("git reset HEAD~3 --hard"), "history-rewrite"},
		{shell("git clean --force -d"), "history-rewrite"},
		{shell("git -C ../x reset --hard"), "history-rewrite"},
		{shell("git clean -n"), ""},
		{shell("git reset HEAD file"), ""},
		{shell("git branch -d merged"), ""},
	}
	for _, c := range cases {
		got := ""
		if r := d.Match(c.a, Flag); r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("%q %q: matched %q, want %q", c.a.Command, c.a.Path, got, c.want)
		}
	}
}

// QA2-REC-3 and QA2-REC-4: Block rules read the whole command, as sent and as the shell reads
// it, so neither padding nor ordinary shell syntax hides it.
func TestBlockRulesSeeTheWholeCommand(t *testing.T) {
	d := Defaults()
	block := Set{Version: 1}
	for _, r := range d.Rules {
		if r.ID == "recursive-delete" || r.ID == "force-push" || r.ID == "infra-change" || r.ID == "database-destructive" {
			r.Effect = Deny
			block.Rules = append(block.Rules, r)
		}
	}
	block.Rules = append(block.Rules, Rule{ID: "g-prod", Effect: Deny, Kinds: []string{"shell"}, Command: "*deploy*--env=prod*"})
	pad := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(": ")
			b.WriteString(strings.Repeat("x", i%7))
			b.WriteString("; ")
		}
		return b.String()
	}
	cases := []struct{ cmd, want string }{
		{"rm -rf /srv/data", "recursive-delete"},
		{"rm" + strings.Repeat(" ", 70000) + "-rf /srv/data", "recursive-delete"},
		{"rm /srv/data" + strings.Repeat(" ", 600) + "-rf" + strings.Repeat(" ", 70000) + "/srv/other", "recursive-delete"},
		{"rm /srv/data" + strings.Repeat("\t", 600) + "-rf" + strings.Repeat(" \t", 70000) + "/srv/other", "recursive-delete"},
		{"terraform" + strings.Repeat(" ", 600) + "destroy" + strings.Repeat(" ", 70000) + "-auto-approve", "infra-change"},
		{"rm /srv/data '" + strings.Repeat("y", 70000) + "' -rf", "recursive-delete"},
		{pad(4000) + "rm -rf /srv/data; " + pad(4000), "recursive-delete"},
		{"echo " + strings.Repeat("x", 1<<20) + "; rm -rf /srv/data", "recursive-delete"},
		{"git push \\\n  --force origin main", "force-push"},
		{"git -C /srv/repo push --force", "force-push"},
		{"r\\m -rf /srv/data", "recursive-delete"},
		{"'rm' -rf /srv/data", "recursive-delete"},
		{`rm "-rf" /srv/data`, "recursive-delete"},
		{"r''m -r''f /srv/data", "recursive-delete"},
		{"RM -rf /srv/data", "recursive-delete"},
		{"rm\t-rf /srv/data", "recursive-delete"},
		{"rm${IFS}-rf /srv/data", "recursive-delete"},
		{"r$()m -rf /srv/data", "recursive-delete"},
		{`psql -c "TRUNCATE users;"`, "database-destructive"},
		{"make deploy --env=prod", "g-prod"},
		{"make deploy \\\n --env=prod", "g-prod"},
		{"make deploy '--env=prod'", "g-prod"},
		{"rm /srv/data", ""},
		{"echo " + strings.Repeat("rm ", 300000) + "done", ""},
	}
	for _, c := range cases {
		start := time.Now()
		got := ""
		if r := block.Match(shell(c.cmd), Deny); r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("%.60q (%d bytes): matched %q, want %q", c.cmd, len(c.cmd), got, c.want)
		}
		if el := time.Since(start); el > 2*time.Second && !raceDetector {
			t.Errorf("%d-byte command took %v", len(c.cmd), el)
		}
	}
}
