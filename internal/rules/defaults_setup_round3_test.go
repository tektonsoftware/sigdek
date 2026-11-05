package rules

import "testing"

// QA3-REC-4: switching SigDek off in everyday ways is flagged, and so recorded before it runs:
// a script whose quoted code holds a ;, cd into the folder first, globs, and the agents' whole
// settings folders. Mentioning SigDek's files or variables, or reading them, is not.
func TestSetupChangesFromTheShellRound3(t *testing.T) {
	d := Defaults()
	cases := []struct{ cmd, want string }{
		{`python3 -c "import os; os.remove('.sigdek/launch.sh')"`, "sigdek-setup-shell"},
		{`node -e "const fs=require('fs'); fs.rmSync('.sigdek',{recursive:true})"`, "sigdek-setup-shell"},
		{`python3 -c "import shutil; shutil.rmtree('.sigdek')"`, "sigdek-setup-shell"},
		{`python3 -c "import json; json.dump({}, open('.claude/settings.json','w'))"`, "sigdek-setup-shell"},
		{`perl -pi -e 's/hook//' .claude/settings.json`, "sigdek-setup-shell"},
		{"cd .sigdek && rm launch.sh", "sigdek-setup-shell"},
		{"cd .sigdek; rm launch.sh", "sigdek-setup-shell"},
		{`cd "$PWD/.sigdek" && echo 'exit 0' > launch.sh`, "sigdek-setup-shell"},
		{"cd .claude && rm settings.json", "sigdek-setup-shell"},
		{"cd .cursor\nmv hooks.json hooks.json.off", "sigdek-setup-shell"},
		{"rm -rf .sig*", "sigdek-setup-shell"},
		{"rm -rf ./.s?gdek", "sigdek-setup-shell"},
		{"rm -rf .[s]igdek", "sigdek-setup-shell"},
		{"rm -rf .??*", "sigdek-setup-shell"},
		{"rm -rf .claude", "sigdek-setup-shell"},
		{"rm -rf ~/.claude/", "sigdek-setup-shell"},
		{`rm -rf ".cursor"`, "sigdek-setup-shell"},
		{"mv .cursor .cursor.off", "sigdek-setup-shell"},
		{"git rm -r --cached .claude", "sigdek-setup-shell"},
		{"find . -maxdepth 1 -name .claude -exec rm -rf {} +", "sigdek-setup-shell"},
		{"unset SIGDEK_HOME", "sigdek-setup-shell"},
		{"export SIGDEK_BIN", "sigdek-setup-shell"},
		{"SIGDEK_BIN=/bin/true claude", "sigdek-setup-shell"},
		{"set -gx SIGDEK_BIN /usr/bin/true", "sigdek-setup-shell"},
		{"cd .sigdek && ls", ""},
		{"cd .sigdek && cat README.md 2>/dev/null", ""},
		{"cd .claude && ls", ""},
		{"cd .claude && cat settings.json", ""},
		{"cd .claude && rm commands/old.md", ""},
		{`python3 -c "import json; print(json.load(open('.claude/settings.json')))"`, ""},
		{`node -e "console.log(require('fs').readFileSync('.cursor/hooks.json','utf8'))"`, ""},
		{"ls -d .sig* .claude", ""},
		{"cp -r .claude ../backup/", ""},
		{"rm -f .claude-old .cache .next node_modules/.cache", ""},
		{"rm -f .*.swp .*~", ""},
		{"sed -i -e 's/ .* / /' notes.txt", ""},
		{"grep -rn SIGDEK_HOME internal/", ""},
		{"echo $SIGDEK_HOME", ""},
	}
	for _, c := range cases {
		got := ""
		if r := d.Match(shell(c.cmd), Flag); r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("%q: matched %q, want %q", c.cmd, got, c.want)
		}
	}
}
