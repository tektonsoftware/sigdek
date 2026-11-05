// Package redact strips secrets from commands, paths and URLs before they are recorded.
// It is best effort: patterns catch the common credential formats, not every possible secret.
package redact

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Marker replaces each stripped value.
const Marker = "[redacted]"

// lazyRE is an expression compiled the first time it is used. Compiling them all takes about
// a millisecond, which a hook that redacts nothing (most checks before a tool runs) shouldn't
// pay at start-up.
type lazyRE struct {
	expr string
	once sync.Once
	re   *regexp.Regexp
}

func lazy(expr string) *lazyRE { return &lazyRE{expr: expr} }

func (l *lazyRE) get() *regexp.Regexp {
	l.once.Do(func() { l.re = regexp.MustCompile(l.expr) })
	return l.re
}

type pattern struct {
	re *lazyRE
	// group is the submatch to replace; 0 replaces the whole match, -1 the first non-empty group.
	group int
	// heuristic patterns guess from a keyword rather than a token's format; they are not
	// applied inside commit and tag messages, where "token: refresh" is prose.
	heuristic bool
	// keep, when set, vetoes a match that only looks like a secret.
	keep func(s string, m []int, value string) bool
}

var patterns = []pattern{
	// Private key blocks, PEM and PGP. An unterminated block runs to the end.
	{re: lazy(`-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----|$)`)},
	// Provider tokens with recognizable prefixes.
	{re: lazy(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA|AIPA)[A-Z0-9]{16}\b`)},                      // AWS access key ids
	{re: lazy(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`)},                                                // GitHub
	{re: lazy(`\bgithub_pat_[A-Za-z0-9_]{22,255}\b`)},                                              // GitHub fine-grained
	{re: lazy(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},                                                    // GitLab
	{re: lazy(`\b(?:xox[abprsoe]|xapp)-[A-Za-z0-9-]{10,}\b`)},                                      // Slack bot, user and app tokens
	{re: lazy(`\bhooks\.slack\.com/(?:services|workflows|triggers)/([A-Za-z0-9/_\-]+)`), group: 1}, // Slack webhooks
	{re: lazy(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}\b`)},                                  // Stripe
	{re: lazy(`\bwhsec_[A-Za-z0-9]{16,}\b`)},                                                       // Stripe webhook
	{re: lazy(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`), keep: notMixed},                                   // Anthropic
	{re: lazy(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{20,}\b`), keep: notMixed},                    // OpenAI style
	{re: lazy(`\bAIza[0-9A-Za-z_\-]{30,40}\b`)},                                                    // Google API key
	{re: lazy(`\bnpm_[A-Za-z0-9]{36}\b`)},                                                          // npm
	{re: lazy(`\bpypi-[A-Za-z0-9_\-]{50,}\b`)},                                                     // PyPI
	{re: lazy(`\bSG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}\b`)},                                 // SendGrid
	{re: lazy(`\bhf_[A-Za-z0-9]{30,}\b`)},                                                          // Hugging Face
	{re: lazy(`\bdop_v1_[a-f0-9]{64}\b`)},                                                          // DigitalOcean
	{re: lazy(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\b`)},             // JWT
	// Credentials embedded in URLs: scheme://user:password@host, where the user may be empty
	// (redis://:pw@host) and the password may itself hold @ or / (the last @ ends it).
	{re: lazy(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@'"]*:([^\s'"]+)@[^\s/@?#'"]+`), group: 1, keep: portNotPassword},
	// Signatures and keys in URL queries: Azure SAS (sig=), AWS and Google signed URLs, ?key=.
	{re: lazy(`(?i)[?&](?:sig|signature|x-amz-signature|x-amz-security-token|x-amz-credential|x-goog-signature|x-goog-credential|key|apikey|api-key)=([^&\s'"#]+)`), group: 1},
	// Command-line credentials. Those named by a program's option are heuristic, so a commit
	// message that mentions "docker login -p" stays as written.
	{re: lazy(`\b(?:docker|podman|nerdctl|buildah)\s+login\b[^|;&\n]*?\s-p[\s=]*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},
	{re: lazy(`\becho\s+(?:-\S+\s+)*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))\s*\|\s*(?:docker|podman|nerdctl|buildah|helm|oras|skopeo|crane)\b[^|;&\n]*--password-stdin`), group: -1, heuristic: true},
	{re: lazy(`\bcurl\b[^|;&\n]*?\s(?:-u|--user)[\s=]*['"]?[^\s:'"]*:([^\s'"]+)`), group: 1},
	{re: lazy(`\bcurl\b[^|;&\n]*?\s(?:-b|--cookie)[\s=]*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true, keep: cookieJar},
	{re: lazy(`(?i)-pass(?:in|out|word)?\s+(?:'pass:([^']*)'|"pass:([^"]*)"|pass:(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+)))`), group: -1, heuristic: true}, // openssl
	{re: lazy(`\bopenssl\b[^|;&\n]*?\s-[kK]\s+(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                                     // openssl enc -k
	{re: lazy(`\b(?:zip|unzip|zipcloak)\b[^|;&\n]*?\s-P\s*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                         // zip -P
	{re: lazy(`\b7za?\b[^|;&\n]*?\s-p(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                                              // 7z -pSECRET
	{re: lazy(`\baz\s+login\b[^|;&\n]*?\s-p\s+(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                                     // az login -p
	{re: lazy(`\b(?:sqlcmd|bcp|osql|isql)\b[^|;&\n]*?\s-P\s*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                       // SQL Server tools
	{re: lazy(`\bgh\s+secret\s+set\b[^|;&\n]*?\s(?:--body|-b)[\s=]*(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                // gh secret set
	{re: lazy(`\baws\s+configure\s+set\s+(?:\S+\.)?(?:aws_secret_access_key|aws_session_token)\s+(?:'([^']*)'|"([^"]*)"|([^\s'"]+))`), group: -1},
	{re: lazy(`\bsshpass\s+(?:-\S+\s+)*?-p\s*(?:'([^']*)'|"([^"]*)"|([^\s'"]+))`), group: -1},
	{re: lazy(`\bhtpasswd\s+(?:-\w+\s+)*-\w*b\w*\s+(?:-\w+\s+)*(?:\S+\s+)??\S+\s+(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))\s*(?:$|[|;&\n])`), group: -1},
	{re: lazy(`\b(?:mysql|mysqldump|mariadb)\b[^\n|;&]*\s-p([^\s'"]+)`), group: 1},
	{re: lazy(`(?i)(?:^|[^A-Za-z0-9])(?:_authToken|_auth|_password)(?:\s*=\s*|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"]+))`), group: -1},
	// Authorization and cookie headers.
	{re: lazy(`(?i)\b(?:authorization|proxy-authorization)\s*[:=]\s*(?:bearer|basic|token)?\s*([^\s'"]+)`), group: 1, heuristic: true},
	{re: lazy(`(?i)\b(?:set-)?cookie\s*:\s*([^'"\n]+)`), group: 1, heuristic: true},
	{re: lazy(`(?i)\b(?:bearer|token)\s+([A-Za-z0-9_\-\.=+/]{16,})`), group: 1, heuristic: true, keep: noDigit},
	// --password VALUE style flags.
	{re: lazy(`(?i)--(?:password|passwd|secret|token|api-key|apikey|access-key|account-key|client-secret|auth-token)(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`), group: -1, heuristic: true, keep: dockerSecretSpec},
}

// keyValue finds key=value and key: value pairs where the key names a secret, including
// JSON ("password": "x"). It is handled in code because three look-alikes must be told
// apart from the context: rg 'token=' (the quote after = closes the argument), sed
// s/password=old/password=new/ (an expression, not an assignment) and prose in messages.
var keyValue = lazy(`(?i)(^|[^A-Za-z0-9_\-])([A-Za-z0-9_\-]*(?:password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?)[A-Za-z0-9_\-]*)(["']?)\s*[=:]\s*(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`)

// messageArg finds the text of -m/--message arguments (git commit -am "...", git tag -m '...').
var messageArg = lazy(`(?:^|\s)(?:-[a-zA-Z]*m|--message)(?:\s+|=)("(?:[^"\\]|\\.)*"|'[^']*')`)

// varRef is a reference that names a secret without containing it: a shell variable
// ($TOKEN, ${TOKEN:-x}), a command substitution, or a template such as GitHub Actions'
// ${{ secrets.TOKEN }} or Helm's {{ .Values.token }}.
var varRef = lazy(`^(?:\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][^}]*\}|\$\{\{.*|\{\{.*|\$\(.*)$`)

// String returns s with secrets replaced by Marker, and how many were replaced.
func String(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	total := 0
	for _, p := range patterns {
		s = replace(s, p, &total)
	}
	return replaceKeyValues(s, prose(s), &total), total
}

// Margin is how far past a field's limit Cut keeps text for redaction, so a secret that
// straddles the limit is still recognized before the cut.
const Margin = 1024

// Cut redacts s and cuts it to at most max bytes. It cuts first, leaving Margin bytes of
// room, because redaction is far slower than the cut: a 1 MB command took over a second.
func Cut(s string, max int) (string, int) {
	if len(s) > max+Margin {
		s = cutUTF8(s, max+Margin)
	}
	out, n := String(s)
	return cutUTF8(out, max), n
}

func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// prose returns the byte ranges of message arguments in s.
func prose(s string) [][2]int {
	var spans [][2]int
	for _, m := range messageArg.get().FindAllStringSubmatchIndex(s, -1) {
		spans = append(spans, [2]int{m[2], m[3]})
	}
	return spans
}

func inSpans(spans [][2]int, i int) bool {
	for _, sp := range spans {
		if i >= sp[0] && i < sp[1] {
			return true
		}
	}
	return false
}

func replace(s string, p pattern, total *int) string {
	idx := p.re.get().FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var spans [][2]int
	if p.heuristic {
		spans = prose(s)
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		start, end := m[0], m[1]
		switch {
		case p.group == -1:
			// First non-empty group among the alternatives.
			found := false
			for g := 1; g*2+1 < len(m); g++ {
				if m[g*2] >= 0 && m[g*2+1] > m[g*2] {
					start, end, found = m[g*2], m[g*2+1], true
					break
				}
			}
			if !found {
				continue
			}
		case p.group > 0:
			if m[p.group*2] < 0 || m[p.group*2+1] <= m[p.group*2] {
				continue
			}
			start, end = m[p.group*2], m[p.group*2+1]
		}
		v := s[start:end]
		// A value that is only a variable or a template names a secret without holding it.
		if v == Marker || start < last || varRef.get().MatchString(v) || (p.heuristic && inSpans(spans, m[0])) {
			continue
		}
		if p.keep != nil && p.keep(s, m, v) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Marker)
		last = end
		*total++
	}
	b.WriteString(s[last:])
	return b.String()
}

func replaceKeyValues(s string, spans [][2]int, total *int) string {
	idx := keyValue.get().FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		// Groups: 1 the character before the key, 2 the key, 3 a quote closing the key (JSON),
		// then the value as 4 'single-quoted', 5 "double-quoted" or 6 bare.
		pre, keyStart, keyQuoted := s[m[2]:m[3]], m[4], m[7] > m[6]
		start, end, quote := -1, -1, byte(0)
		switch {
		case m[8] >= 0:
			start, end, quote = m[8], m[9], '\''
		case m[10] >= 0:
			start, end, quote = m[10], m[11], '"'
		case m[12] >= 0:
			start, end = m[12], m[13]
		}
		if start < 0 || end <= start {
			continue
		}
		sep := s[m[7]:start] // from the end of the key (and its closing quote) to the value
		switch {
		case pre == "/" || inSpans(spans, keyStart):
			continue // s/password=old/.../ or a commit message
		case quote != 0 && !keyQuoted && len(pre) == 1 && pre[0] == quote:
			continue // 'token=' ...: the quote after = closes the argument the key is in
		case len(sep) > 1 && sep[0] == '=' && (sep[1] == ' ' || sep[1] == '\t'):
			continue // grep password= config/: the word ends at =, and the next word isn't its value
		}
		v := s[start:end]
		if v == Marker || varRef.get().MatchString(v) || settingOf(s[keyStart:m[5]]) || placeholder[strings.ToLower(v)] {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Marker)
		last = end
		*total++
	}
	b.WriteString(s[last:])
	return b.String()
}

// secretWord finds the words that make a key name a secret, for settingOf.
var secretWord = lazy(`(?i)password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?`)

// settings are words that, after the secret word, make a key name a setting about the secret
// rather than the secret: PASSWORD_MIN_LENGTH, TOKEN_TTL, DB_PASSWORD_FILE.
var settings = map[string]bool{
	"min": true, "max": true, "length": true, "len": true, "size": true, "count": true, "rounds": true, "iterations": true,
	"ttl": true, "timeout": true, "expiry": true, "expires": true, "lifetime": true, "age": true, "file": true, "path": true,
	"dir": true, "url": true, "endpoint": true, "header": true, "name": true, "type": true, "policy": true, "enabled": true,
	"required": true, "prompt": true, "hint": true, "field": true, "env": true, "var": true,
}

// settingOf reports whether a key names a setting about a secret, not the secret itself.
func settingOf(key string) bool {
	loc := secretWord.get().FindAllStringIndex(key, -1)
	if loc == nil {
		return false
	}
	rest := strings.TrimLeft(key[loc[len(loc)-1][1]:], "_-")
	if i := strings.IndexAny(rest, "_-"); i >= 0 {
		rest = rest[:i]
	}
	return settings[strings.ToLower(rest)]
}

// placeholder values say something about a secret, not what it is: password: required.
var placeholder = map[string]bool{
	"required": true, "optional": true, "none": true, "null": true, "nil": true, "true": true, "false": true,
	"yes": true, "no": true, "empty": true, "unset": true, "missing": true, "invalid": true,
}

// cookieJar vetoes curl -b cookies.txt, which names a file of cookies rather than holding one.
func cookieJar(_ string, _ []int, v string) bool { return !strings.Contains(v, "=") }

// notMixed vetoes sk-… matches that aren't a key's random mix of upper case, lower case and
// digits, such as a branch named feature/sk-update-billing.
func notMixed(_ string, _ []int, v string) bool {
	var upper, lower, digit bool
	for _, r := range v {
		upper = upper || unicode.IsUpper(r)
		lower = lower || unicode.IsLower(r)
		digit = digit || unicode.IsDigit(r)
	}
	return !(upper && lower && digit)
}

// noDigit vetoes "token <word>" matches without a digit: prose, not a credential.
func noDigit(_ string, _ []int, v string) bool { return !strings.ContainsAny(v, "0123456789") }

// portNotPassword vetoes http://host:8080/path@x, where the "password" is a port and a path.
func portNotPassword(_ string, _ []int, v string) bool {
	i := 0
	for i < len(v) && i < 6 && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	return i > 0 && i < len(v) && v[i] == '/'
}

// dockerSecretSpec vetoes docker build --secret id=…,src=…, which names a secret file.
func dockerSecretSpec(_ string, _ []int, v string) bool {
	for _, p := range []string{"id=", "src=", "source=", "type=", "env=", "target=", "required"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}
