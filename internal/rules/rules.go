// Package rules matches actions against sensitive-action ("flag") and deny rules.
// Teams edit rules in the dashboard; the ingest service returns the current set with every
// upload, and the recorder caches it. Before sign-in, the built-in defaults apply.
package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tektonsoftware/sigdek/internal/fsretry"
)

const (
	Flag = "flag" // record which rule matched; the action still runs
	Deny = "deny" // block the action before it runs
)

// Rule fields other than ID and Effect are optional; an empty field matches anything.
// Patterns are globs by default ("*" any run of characters; in Path, "**" crosses "/").
// Prefix with "re:" for an RE2 regular expression.
type Rule struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Effect  string   `json:"effect"`
	Kinds   []string `json:"kinds,omitempty"`
	Tool    string   `json:"tool,omitempty"`    // the tool's own action name, e.g. Bash or mcp__github__merge_pr
	Command string   `json:"command,omitempty"` // shell actions
	Path    string   `json:"path,omitempty"`    // file actions, repository-relative
	URL     string   `json:"url,omitempty"`     // web actions
	MCP     string   `json:"mcp,omitempty"`     // "server/tool"
	Message string   `json:"message,omitempty"` // shown to the agent and the user when denied
}

type Set struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Defaults flag common risky actions and deny nothing. They are exported to
// schema/default-rules.json, which the hosted service seeds into every new team. Commands are
// matched as sent and as the shell reads them (see Normalize), so the patterns needn't allow
// for quotes, backslashes or line continuations. They are RE2 throughout (no lookarounds), so
// "but not" is spelled out with notPrefixed, notSuffixed and exceptWords.
func Defaults() Set {
	// .env files (any case: macOS and Windows file systems ignore it), but not the templates
	// committed next to them, whatever comes before the template's name: .env.example,
	// .env.local.example and .env.sample.bak are all templates.
	const templates = "exam|samp|templ"
	fileEnv := `(?i:\.env(\.` + notPrefixed(`/.`, templates) + `)*|\.envrc)`
	shellEnd := `([\s'"|;&<>()]|$)`
	shellWord := `\s/'"|;&<>()` // what ends a file name in a command
	shellEnv := `(?i:\.env(\.` + notPrefixed(shellWord+`.`, templates) + `)*|\.envrc)` + shellEnd
	// A .pem file, unless its name says it holds a certificate: ca.pem, cert.pem, fullchain.pem.
	const certs = "ca|cert|certs|certificate|certificates|crt|chain|bundle|pub|public"
	filePem := `(` + notSuffixed(`/`, certs) + `)?\.pem`
	shellPem := `[` + shellWord + `=](` + notSuffixed(shellWord, certs) + `)?\.pem\b`
	// An SSH private key, not its .pub or -cert.pub.
	shellSSH := `id_(rsa|dsa|ecdsa|ed25519)(_sk)?(\.` + notPrefixed(shellWord, "pub") + `)?` + shellEnd
	flagValue := `(?:-{1,2}[a-zA-Z][\w-]*(?:=\S+|\s+[^-\s]\S*)?\s+)*` // global flags before a verb, with or without values
	// git's own options before the subcommand: git -C dir, -c key=value, --git-dir=x, with a
	// value that may be quoted (git -C 'my repo').
	git := `\bgit\s+(?:(?:-C|-c|--git-dir|--work-tree|--namespace|--exec-path|--config-env)(?:=|\s+)(?:'[^']*'|"[^"]*"|\S+)\s+|-{1,2}[a-zA-Z][\w-]*(?:=\S+)?\s+)*`
	stmt := `[^|;&\n]*` // the rest of one statement
	// The rest of one statement, reading quoted strings whole, so a ; inside quotes doesn't end
	// it, and possibly into a quoted string that is still open: python3 -c "import os;
	// os.remove('.sigdek/launch.sh')".
	stmtQ := `(?:[^|;&\n'"\\]|\\[\s\S]|'[^']*'|"(?:[^"\\]|\\[\s\S])*")*(?:'[^']*|"(?:[^"\\]|\\[\s\S])*)?`
	// Where a command starts: the beginning, a new line or statement, or $( ` ( ; then any
	// VAR=value assignments and wrappers such as time, nohup or env.
	cmdStart := `(^|[;&|(\n` + "`" + `]|\$\()\s*([A-Za-z_][A-Za-z0-9_]*=\S*\s+|(time|nohup|nice|exec|command|env)\s+(-\S+\s+)*)*`
	// Where a program runs: the start of a command; after -c (sh -c "rm …"), find's -exec or a
	// "--"; or after a word that runs the rest of the statement as a command (sudo, xargs, ssh,
	// docker exec, timeout …). Quotes don't matter, as the shell's reading of the command has
	// none. A program named in prose, such as a commit message, isn't run.
	runs := `(` + cmdStart + `|\s(-[a-zA-Z]*c|--|-exec|-execdir|-ok|-okdir)\s+` +
		`|\b(sudo|doas|xargs|exec|eval|nohup|time|nice|ionice|timeout|watch|ssh|command|builtin|parallel|chroot|runuser|flock|then|do|else)\b` + stmt + `\s` +
		`|\b(docker|podman|nerdctl)\b` + stmt + `\brun\b` + stmt + `\s)(\S*/)?`
	// The rest of a statement whose words don't ask only for a preview or for help
	// (--dry-run, --dry-run=client or =server, --help, -h): the command then changes nothing.
	changes := `([ \t]+` + exceptWords(`\s|;&`, "", "--dry-run", "--dry-run=c*", "--dry-run=s*", "--dry-run=t*", "--help", "-h") + `)*[ \t]*($|[|;&\n)])`

	// SigDek's own setup: its folder, the agents' hook files, and the variables that point the
	// launchers elsewhere (set or unset, not merely mentioned).
	setup := `(\.sigdek\b|\.claude/settings[^/\s]*\.json|\.cursor/hooks\.json)`
	setupVar := `\bSIGDEK_(BIN|HOME)\s*=|\b(export|unset|setx|set|readonly|declare|typeset|local)\s+(-\S+\s+)*SIGDEK_(BIN|HOME)\b`
	// The agents' whole settings folders, and globs that can match them or .sigdek (rm -rf
	// .sig*, ./.s?gdek), as a word of their own.
	setupDir := `[\s/'"=(]\.(claude/?|cursor/?|([sc][sigdeklaucro]*)?([*?]|\[[^\]\s]*\])([sigdeklaucro*?]|\[[^\]\s]*\])*)` + shellEnd
	changeVerbs := `(>|\btee\b|\bsed\s+-i|\b(rm|mv|cp|ln|truncate|chmod|chown|unlink|shred|dd|rsync)\b|\bgit\s` + stmt + `\b(checkout|restore|stash|apply|reset|clean|update-index)\b)`
	// A script run by an interpreter changes a setup file only if it writes, deletes, moves or
	// edits in place; reading .claude/settings.json is no change.
	interp := `\b(python[0-9.]*|node|deno|bun|ruby|perl|php)\b[^\n]*`
	writes := `(\b(rm|unlink|remove|write|rename|replace|truncate|chmod|move|copy|dump)\w*|\s-[a-zA-Z]*i\b|['"][wa]b?\+?['"])`
	// cd into .sigdek and change something there in a later statement; or into .claude or
	// .cursor and change the hook file.
	cdInto := func(dir string) string {
		return `\b(cd|pushd)\s+(-\S+\s+)?["']?([^\s'"|;&]*/)?\.` + dir + `/?["']?(\s[^;&|\n]*)?[;&|\n]([\s\S]*[;&|\n])?\s*` +
			`((sudo\s+(-\S+\s+)*)?(rm|rmdir|unlink|shred|truncate|mv|cp|ln|chmod|chown|tee|dd|sed\s+(-\S+\s+)*-i|git\s` + stmt + `\b(rm|mv|checkout|restore))\b|[^;&|\n]*[^0-9&>]>)`
	}

	ident := `[\w."` + "`" + `\[\]]+`
	sqlDrop := `(drop\s+(table|database|schema)\s+(if\s+exists\s+)?` + ident + `|truncate\s+(table\s+)?(only\s+)?` + ident + `)`
	// A statement ending at the end of the text, a comma or a closing quote counts only where
	// SQL starts (the text, a quote, ; ( or a new line, or an option such as -c): otherwise it
	// is prose, as in a commit message "Remove the drop table helper".
	sqlStart := `(^|[\n;(]|["'` + "`" + `]|\s-{1,2}[A-Za-z][\w-]*[\s=])\s*`
	sqlEnd := `\s*(;|\bcascade\b|\brestrict\b|\brestart\b)`
	sqlStartEnd := `\s*(;|,|$|["'` + "`" + `]|\bcascade\b|\brestrict\b|\brestart\b)`
	// A shell or interpreter reads the piped script only when it is given no script of its
	// own: a shell with no operand or with -s, -, --; python, node and the rest with no
	// arguments or -. (python3 -m json.tool and python3 -c '…' read data, not code.)
	shellReads := `(\s*($|[|;&\n)` + "`" + `])|\s+(-|--|-[abd-zA-Z][a-zA-Z]*)(\s|$))`
	interpReads := `(\s*($|[|;&\n)` + "`" + `])|\s+-(\s|$))`
	return Set{Version: 0, Rules: []Rule{
		{ID: "sigdek-setup", Label: "SigDek's own setup changed", Effect: Flag, Kinds: []string{"file_edit"},
			Path: `re:(^|/)(\.sigdek/.*|\.claude/settings[^/]*\.json|\.cursor/hooks\.json)$`},
		{ID: "sigdek-setup-shell", Label: "SigDek's own setup changed from the shell", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:` + setupVar + `|\bsigdek(\.exe)?\s+(-\S+\s+)*(logout\b|init\b` + stmt + `\s--remove\b)` +
				`|` + changeVerbs + stmtQ + setup +
				`|\b(rm|rmdir|mv|unlink|shred|chmod|chown)\b` + stmtQ + setupDir +
				`|` + interp + `(` + writes + `[^\n]*` + setup + `|` + setup + `[^\n]*` + writes + `)` +
				`|\bfind\b` + stmt + `(` + setup + `|` + setupDir + `)(` + stmt + `\s)?-(delete|exec)\b` +
				`|` + cdInto("sigdek") + `|` + cdInto("(claude|cursor)") + stmtQ + `\b(settings[^/\s]*|hooks)\.json`},
		{ID: "force-push", Label: "Force push", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:` + git + `push\b` + stmt + `\s(--force(-with-lease|-if-includes)?\b|-[a-zA-Z]*f[a-zA-Z]*\b|\+\S|--mirror\b)`},
		{ID: "infra-change", Label: "Infrastructure change", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:(\b(terraform|tofu)\s+(-\S+\s+)*(apply|destroy|import|state\s+(rm|mv|push))\b|\bkubectl\s+` + flagValue + `(apply|delete|replace|patch|scale|drain|create|edit|rollout\s+undo)\b|\bhelm\s+` + flagValue + `(install|upgrade|uninstall|rollback|delete)\b|\bpulumi\s+(-\S+\s+)*(up|destroy)\b` +
				`|\baws\s+` + flagValue + `\S+\s+` + flagValue + `(delete|terminate|put|create|update|remove|modify)-[^\s|;&]*|\baws\s+` + flagValue + `s3\s+(rm|rb)\b|\bgcloud\s+` + stmt + `\b(delete|deploy)\b|\baz\s+` + stmt + `\b(delete|create)\b)` + changes},
		{ID: "recursive-delete", Label: "Recursive delete", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:(` + runs + `|` + git + `)rm\s+(` + stmt + `\s)?(-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)\b`},
		{ID: "secrets-file", Label: "Secrets file accessed", Effect: Flag, Kinds: []string{"file_read", "file_edit"},
			Path: `re:(^|/)(` + fileEnv + `|` + filePem + `|[^/]*\.key|id_(rsa|dsa|ecdsa|ed25519)|\.npmrc|\.netrc|\.pgpass|\.git-credentials|credentials(\.json)?|secrets?\.(ya?ml|json)|[^/]*\.tfvars(\.json)?|\.kube/config|\.sigdek/device\.json)$`},
		{ID: "secrets-file-shell", Label: "Secrets file read from the shell", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:(\b(cat|less|more|head|tail|bat|cp|scp|base64|xxd|od|hexdump|strings|source|grep|egrep|fgrep|rg|ag|awk|gawk|sed|cut|sort|tac|nl|jq|yq|vi|vim|nvim|nano|dotenv)\b|(^|[;&|(\n]\s*)\.\s)` + stmt +
				`(` + shellEnv + `|` + shellPem + `|\.key\b|` + shellSSH + `|\.npmrc\b|\.netrc\b|\.pgpass\b|\.git-credentials\b|\.aws/credentials\b|\.kube/config\b|\.tfvars\b|\.sigdek/device\.json)`},
		{ID: "database-destructive", Label: "Destructive database statement", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:(?i)\b` + sqlDrop + sqlEnd + `|` + sqlStart + sqlDrop + sqlStartEnd +
				`|\bdelete\s+from\s+\S+\s*;|` + sqlStart + `delete\s+from\s+\S+\s*($|["'` + "`" + `])|` + cmdStart + `(sudo\s+(-\S+\s+)*)?dropdb\s+[\w-]|\bredis-cli\b` + stmt + `\bflush(all|db)\b`},
		{ID: "remote-script", Label: "Remote script piped to a shell", Effect: Flag, Kinds: []string{"shell"},
			// The pipe may come after other stages (curl x | tee i.sh | sh), but not as ||.
			Command: `re:\b(curl|wget)\b[^;&\n]*[^|;&\n]\|\s*(sudo\s+(-\S+\s+)*)?(\S*/)?(env\s+)?((ba|z|da|k|fi)?sh` + shellReads + `|(python[0-9.]*|perl|ruby|node|php)` + interpReads + `)` +
				`|(\b((ba|z|da|k)?sh|python[0-9.]*|perl|ruby|node|source|eval)\b|(^|[;&|\n]\s*)\.\s)` + stmt + `(\$\(|<\(|` + "`" + `)\s*(curl|wget)\b`},
		{ID: "package-publish", Label: "Package or release published", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:(\b(npm|pnpm|yarn|bun)\s+` + flagValue + `(npm\s+)?publish\b|\b(cargo|poetry|uv|hatch|flit)\s+` + flagValue + `publish\b|\btwine\s+upload\b|\bgh\s+release\s+create\b|\bdocker\s+(image\s+)?push\b|\bgem\s+push\b|\bdotnet\s+nuget\s+push\b)` + changes},
		{ID: "privilege", Label: "Privileged command", Effect: Flag, Kinds: []string{"shell"},
			// sudo -n true only checks that sudo works without a password.
			Command: `re:` + cmdStart + `(sudo|doas|pkexec|run0)(\s+-\S+)*(\s+` + exceptWords(`\s|;&()`, "-", "true") + `([\s|;&()]|$)|\s*($|[|;&\n)]))|` + cmdStart + `su(\s|$)|\bchmod\s+(-\S+\s+)*(0?777|a\+rwx|ugo\+rwx)\b`},
		{ID: "history-rewrite", Label: "Git history rewritten", Effect: Flag, Kinds: []string{"shell"},
			Command: `re:` + git + `(reset\b` + stmt + `\s--hard\b|rebase\b|filter-(branch|repo)\b|push\b` + stmt + `\s(--delete\b|-d\b|:[^\s:]+)|branch\b` + stmt + `\s-[a-zA-Z]*D\b|clean\b` + stmt + `\s(-[a-zA-Z]*f[a-zA-Z]*|--force)\b)`},
	}}
}

// notPrefixed returns an RE2 fragment matching any non-empty run of characters from the
// class [^stop] that doesn't start with one of the |-separated prefixes. RE2 has no negative
// lookahead, so this spells out the complement: at each step, either stop short of a prefix,
// take a character no prefix continues with, or follow a prefix one more character.
func notPrefixed(stop, prefixes string) string {
	type node struct {
		next map[byte]*node
		end  bool
	}
	root := &node{next: map[byte]*node{}}
	for _, w := range strings.Split(prefixes, "|") {
		n := root
		for i := 0; i < len(w); i++ {
			c := n.next[w[i]]
			if c == nil {
				c = &node{next: map[byte]*node{}}
				n.next[w[i]] = c
			}
			n = c
		}
		n.end = true
	}
	var walk func(n *node, top bool) string
	walk = func(n *node, top bool) string {
		var alts []string
		if !top {
			alts = append(alts, "")
		}
		keys := make([]byte, 0, len(n.next))
		for k := range n.next {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		alts = append(alts, `[^`+stop+string(keys)+`][^`+stop+`]*`)
		for _, k := range keys {
			if c := n.next[k]; !c.end {
				alts = append(alts, regexp.QuoteMeta(string(k))+walk(c, false))
			}
		}
		return "(" + strings.Join(alts, "|") + ")"
	}
	return walk(root, true)
}

// trie holds words character by character, for the complements below. A word ending in *
// stands for every word with that prefix.
type trie struct {
	next   map[byte]*trie
	end    bool
	prefix bool
}

func newTrie(words []string, reverse bool) *trie {
	root := &trie{next: map[byte]*trie{}}
	for _, w := range words {
		w, prefix := strings.CutSuffix(w, "*")
		n := root
		for i := 0; i < len(w); i++ {
			c := w[i]
			if reverse {
				c = w[len(w)-1-i]
			}
			if n.next[c] == nil {
				n.next[c] = &trie{next: map[byte]*trie{}}
			}
			n = n.next[c]
		}
		n.end, n.prefix = true, prefix
	}
	return root
}

func (n *trie) keys() []byte {
	keys := make([]byte, 0, len(n.next))
	for k := range n.next {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// classChars escapes characters for use inside a character class.
func classChars(cs []byte) string {
	var b strings.Builder
	for _, c := range cs {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// exceptWords returns an RE2 fragment matching a non-empty run of characters from the class
// [^stop], not starting with a character of notFirst, that is not one of words (a word ending
// in * excludes every word it begins). A caller follows it with what ends a word, so the
// fragment can't stop part way through one.
func exceptWords(stop, notFirst string, words ...string) string {
	var walk func(n *trie, top bool) string
	walk = func(n *trie, top bool) string {
		var alts []string
		if !top && !n.end {
			alts = append(alts, "") // the word ends here, short of every listed one
		}
		keys := n.keys()
		other := classChars(keys)
		if top {
			other += classChars([]byte(notFirst))
		}
		alts = append(alts, `[^`+stop+other+`][^`+stop+`]*`)
		for _, k := range keys {
			if n.next[k].prefix || top && strings.Contains(notFirst, string(k)) {
				continue
			}
			alts = append(alts, regexp.QuoteMeta(string(k))+walk(n.next[k], false))
		}
		return "(" + strings.Join(alts, "|") + ")"
	}
	return walk(newTrie(words, false), true)
}

// notSuffixed returns an RE2 fragment matching a non-empty run of characters from the class
// [^stop] that doesn't end with one of the |-separated suffixes, in any case. Read from its
// end, such a run either leaves every suffix at some character, or is shorter than the
// suffixes it agrees with.
func notSuffixed(stop, suffixes string) string {
	var leave, short []string
	var walk func(n *trie, tail string)
	walk = func(n *trie, tail string) {
		leave = append(leave, `[^`+stop+classChars(n.keys())+`]`+regexp.QuoteMeta(tail))
		if tail != "" {
			short = append(short, regexp.QuoteMeta(tail))
		}
		for _, k := range n.keys() {
			if c := n.next[k]; !c.end {
				walk(c, string(k)+tail)
			}
		}
	}
	walk(newTrie(strings.Split(suffixes, "|"), true), "")
	alts := append([]string{`[^` + stop + `]*(` + strings.Join(leave, "|") + `)`}, short...)
	return `(?i:` + strings.Join(alts, "|") + `)`
}

// Load reads a cached rule set, falling back to the defaults.
func Load(path string) Set {
	s, _ := LoadChecked(path)
	return s
}

// LoadChecked is Load, with an error when a cached rule set exists but can't be used (the
// built-in defaults apply until the next upload brings a fresh copy).
func LoadChecked(path string) (Set, error) {
	b, err := fsretry.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), fmt.Errorf("reading %s: %w; using the built-in rules", path, err)
	}
	var s Set
	if err := json.Unmarshal(b, &s); err != nil || s.Rules == nil {
		if err == nil {
			err = errors.New(`no "rules" list`)
		}
		return Defaults(), fmt.Errorf("%s is not a rule set (%v); using the built-in rules until the next upload", path, err)
	}
	return s, nil
}

// Save writes the set atomically.
func Save(path string, s Set) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return fsretry.Rename(tmp, path)
}
