// Package redact strips secrets from commands, paths and URLs before they are recorded.
// It is best effort: patterns catch the common credential formats, not every possible secret.
package redact

import (
	"regexp"
	"sort"
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
	// overlap looks for the next match from where a match's value starts rather than where
	// it ends, so a flag that another flag took as its value is judged on its own, as in
	// mysql --skip-password --password VALUE. The expression must not look behind its first
	// character (no ^ or \b there), since each search starts part way through the text.
	overlap bool
	// also, when set, is a second expression with the same groups whose matches in the same text
	// are judged by keep and stripped too, together with re's. The flag pattern uses it for 0.1.1's
	// own expression, matched as 0.1.1 matched it, so everything 0.1.1 stripped is still stripped:
	// a 0.1.1 flag inside the value of a flag only 0.1.2 matches, as in
	// --no-password "curl --token VALUE", is found although re's search went past it (QA6-REC-1).
	also *lazyRE
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
	{re: lazy(`\bhv[sbr]\.[A-Za-z0-9_\-]{20,}`), keep: lowerWord},                                  // HashiCorp Vault
	// A token given as a URL's user, with no password: https://<token>@github.com/...
	{re: lazy(`(?i)\bhttps?://([A-Za-z0-9_\-]{32,})@[^\s/@?#'"]+`), group: 1},
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
	{re: lazy(`\bredis-cli\b[^|;&\n]*?\s-a[ \t]+(?:'([^']*)'|"([^"]*)"|([^\s'"|;&]+))`), group: -1, heuristic: true},                     // redis-cli -a
	{re: lazy(mongoCommand + `\s(?:[^|;&\n]*?\s)?-p[ \t]+(?:'([^']*)'|"([^"]*)"|([^-\s'"|;&][^\s'"|;&]*))`), group: -1, heuristic: true}, // mongosh -p
	{re: lazy(`\b(?:mysql|mysqldump|mariadb)\b[^\n|;&]*\s-p([^\s'"]+)`), group: 1},
	{re: lazy(`(?i)(?:^|[^A-Za-z0-9])(?:_authToken|_auth|_password)(?:\s*=\s*|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"]+))`), group: -1},
	// Authorization and cookie headers.
	{re: lazy(`(?i)\b(?:authorization|proxy-authorization)\s*[:=]\s*(?:bearer|basic|token)?\s*([^\s'"]+)`), group: 1, heuristic: true},
	{re: lazy(`(?i)\b(?:set-)?cookie\s*:\s*([^'"\n]+)`), group: 1, heuristic: true},
	{re: lazy(`(?i)\b(?:bearer|token)\s+([A-Za-z0-9_\-\.=+/]{16,})`), group: 1, heuristic: true, keep: noDigit},
	// --password VALUE style flags, also with a word before the name: --http-password,
	// --db-password, --github-token. --no-password and the like take no value.
	{re: lazy(`(?i)--(?:[a-z]+-)?(?:password|passwd|secret|token|api-key|apikey|access-key|account-key|client-secret|auth-token)(?:=|[ \t]+)(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`), group: -1, heuristic: true, keep: flagWithoutValue, overlap: true,
		also: lazy(`(?i)--(?:password|passwd|secret|token|api-key|apikey|access-key|account-key|client-secret|auth-token)(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`)},
}

// mongoCommand finds a MongoDB tool run as a command: at the start of one (after ; & | ( or a
// backtick), after sudo or variable assignments, by path, or as what docker exec, kubectl exec or
// a "--" runs. So in docker run --name mongo -p 27017:27017, mongo is a name, not the program,
// and -p takes a port, not a password.
const mongoCommand = `(?:^|[;&|(\x60\n]|\s--\s|\bexec\s(?:[^|;&\n]*?\s)?)\s*(?:sudo\s+(?:-\S+\s+)*)?(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:[^\s;&|()\x60'"]*/)?mongo(?:sh|dump|restore|import|export)?`

// keyValue finds key=value and key: value pairs where the key names a secret, including
// JSON ("password": "x"). It is handled in code because three look-alikes must be told
// apart from the context: rg 'token=' (the quote after = closes the argument), sed
// s/password=old/password=new/ (an expression, not an assignment) and prose in messages.
//
// PASS, PWD and KEY name a secret only as a separate word after another (DB_PASS, MYSQL_PWD,
// SIGNING_KEY), so PWD=/tmp and OLDPWD don't. A key named only by a generic KEY is redacted
// only when its value looks generated (see generated): SORT_KEY=name stays.
var keyValue = lazy(`(?i)(^|[^A-Za-z0-9_\-])([A-Za-z0-9_\-]*(?:password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?)[A-Za-z0-9_\-]*|[A-Za-z0-9_\-]*[A-Za-z0-9][_\-](?:pass|pwd|key)(?:[_\-][A-Za-z0-9_\-]*)?)(["']?)\s*[=:]\s*(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`)

// keyValue011 is 0.1.1's key=value expression: keyValue without the names made only of a generic
// KEY, PASS or PWD. Its matches in the same text are judged as 0.1.1 judged them (settingOf011)
// and stripped with keyValue's, so a pair 0.1.1 stripped is still stripped where a wider match, or
// one read as a setting only since 0.1.2, went past it without stripping, as in
// --set-env-vars "CACHE_KEY=prod,DB_PASSWORD=VALUE" (QA7-REC-1) or
// --set-env-vars SECRET_KEY_FILE=/k,DB_PASSWORD=VALUE (QA8-REC-1).
var keyValue011 = lazy(`(?i)(^|[^A-Za-z0-9_\-])([A-Za-z0-9_\-]*(?:password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?)[A-Za-z0-9_\-]*)(["']?)\s*[=:]\s*(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`)

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
	idx := matches(s, p)
	if p.also != nil {
		idx = append(idx, p.also.get().FindAllStringSubmatchIndex(s, -1)...)
		byValue(idx, 1)
	}
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
		if v == Marker || varRef.get().MatchString(v) || (p.heuristic && inSpans(spans, m[0])) {
			continue
		}
		if p.keep != nil && p.keep(s, m, v) {
			continue
		}
		if start < last {
			// A value that starts inside one already stripped, as a flag inside another flag's
			// value can: the stripped text runs on to its end.
			last = max(last, end)
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

// byValue sorts matches from two expressions by where their value starts (the first group from
// group first on that took part), so text before a stripped value is written out only once it's
// known that no other match strips it.
func byValue(idx [][]int, first int) {
	at := func(m []int) int {
		for g := first; 2*g < len(m); g++ {
			if m[2*g] >= 0 {
				return m[2*g]
			}
		}
		return m[0]
	}
	sort.SliceStable(idx, func(i, j int) bool { return at(idx[i]) < at(idx[j]) })
}

// matches returns p's matches in s, as FindAllStringSubmatchIndex does. With p.overlap, a match
// whose bare value (its last group) follows a blank and starts with "-" may be another flag taken
// as this one's value, as in --skip-password --password VALUE, so the next search starts at that
// value; replace skips a value that starts inside one it stripped. Any other match ends the
// search there, as without overlap. A value after "=" is the flag's own, and a value after a blank
// is one word, so each byte is searched at most twice and the search stays linear.
func matches(s string, p pattern) [][]int {
	re := p.re.get()
	if !p.overlap {
		return re.FindAllStringSubmatchIndex(s, -1)
	}
	var all [][]int
	for at := 0; at < len(s); {
		m := re.FindStringSubmatchIndex(s[at:])
		if m == nil {
			break
		}
		for i := range m {
			if m[i] >= 0 {
				m[i] += at
			}
		}
		all = append(all, m)
		at = max(m[1], m[0]+1)
		if v, e := m[len(m)-2], m[len(m)-1]; v > m[0] && s[v-1] != '=' && strings.HasPrefix(s[v:e], "-") {
			at = v
		}
	}
	return all
}

func replaceKeyValues(s string, spans [][2]int, total *int) string {
	idx := keyValue.get().FindAllStringSubmatchIndex(s, -1)
	for _, m := range keyValue011.get().FindAllStringSubmatchIndex(s, -1) {
		idx = append(idx, append(m, from011)) // an odd length marks 0.1.1's match, judged as 0.1.1 judged it
	}
	if idx == nil {
		return s
	}
	byValue(idx, 4)
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
		v, key := s[start:end], s[keyStart:m[5]]
		setting := settingOf(key)
		if len(m)%2 == 1 {
			setting = settingOf011(key)
		}
		if v == Marker || varRef.get().MatchString(v) || setting || placeholder[strings.ToLower(v)] {
			continue
		}
		if !namedSecret.get().MatchString(key) && !generated(v) {
			continue // SORT_KEY=name: only a generic KEY names it, and the value isn't a key's
		}
		if start < last {
			last = max(last, end) // inside a value already stripped: the stripped text runs on to its end
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

// secretWord finds the words that make a key name a secret, for settingOf: DB_PASS_FILE and
// SSH_KEY_PATH are settings, as DB_PASSWORD_FILE is.
var secretWord = lazy(`(?i)[_\-](?:pass|pwd|key)\b|[_\-](?:pass|pwd|key)[_\-]|password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?`)

// secretWord011 and settingOf011 are 0.1.1's: a key is a setting about a secret only when the word
// after one of these names (not a generic _KEY, _PASS or _PWD) is in settings. replaceKeyValues
// judges 0.1.1's own matches with them, so SECRET_KEY_FILE=/k is stripped as 0.1.1 stripped it.
var secretWord011 = lazy(`(?i)password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?`)

func settingOf011(key string) bool {
	loc := secretWord011.get().FindAllStringIndex(key, -1)
	if loc == nil {
		return false
	}
	rest := strings.TrimLeft(key[loc[len(loc)-1][1]:], "_-")
	if i := strings.IndexAny(rest, "_-"); i >= 0 {
		rest = rest[:i]
	}
	return settings[strings.ToLower(rest)]
}

// from011 is appended to a match of keyValue011, giving it an odd length, so replaceKeyValues can
// tell it from keyValue's after sorting.
const from011 = -1

// namedSecret finds the words that name a secret on their own, which a generic KEY doesn't.
var namedSecret = lazy(`(?i)password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|account[_\-]?key|private[_\-]?key|client[_\-]?secret|auth[_\-]?token|credentials?|[_\-](?:pass|pwd)(?:$|[_\-])`)

// generated reports a value that looks like a generated key rather than a name, a path or a
// number: base64:..., 32 or more hex digits, or 16 or more characters of a key's alphabet that
// mix upper case, lower case and digits.
func generated(v string) bool {
	if strings.HasPrefix(v, "base64:") || hexKey.get().MatchString(v) {
		return true
	}
	return keyAlphabet.get().MatchString(v) && !notMixed("", nil, v)
}

var (
	hexKey      = lazy(`^[0-9a-fA-F]{32,}$`)
	keyAlphabet = lazy(`^[A-Za-z0-9+=_\-][A-Za-z0-9+/=_\-]{15,}$`)
)

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

// lowerWord vetoes a Vault-looking match with no upper case and no digit, such as a config key
// in a namespace named hvs (--set hvs.database_password_value=...), which a token never is (QA9-REC-3).
func lowerWord(_ string, _ []int, v string) bool {
	return !strings.ContainsFunc(v, func(r rune) bool { return unicode.IsUpper(r) || unicode.IsDigit(r) })
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

// flagWithoutValue vetoes a redirection in place of a value, docker build --secret id=…,src=…
// (see dockerSecretSpec), and a lone "-" or "--" after a space, none of which holds a secret.
//
// 0.1.1's own flags (secretFlags) otherwise always take the next word as their value, as 0.1.1
// did, even when it starts with "-": --password -hunter22 (QA4-REC-1). mysql --password -u root
// loses -u to that, as it did in 0.1.1.
//
// A flag with a word before the secret's name may take no value: --no-password, --ask-password,
// --prompt-password, gh auth login --with-token < file, and one followed by another flag, as in
// mysql --skip-password -u root. Its value given with = or in quotes is its own, even when it
// starts with "-", and so is a bare value that starts with "-" but doesn't look like a flag, as
// a generated key can: --db-password -Hk3pQ9xZ2mLw7Rt5Vb8N (QA3-REC-3).
func flagWithoutValue(s string, m []int, v string) bool {
	// m[6] is where the pattern's third group, a value without quotes, starts.
	spaced := m[6] > 0 && s[m[6]-1] != '='
	if strings.Trim(v, "<>") == "" || dockerSecretSpec(s, m, v) || spaced && (v == "-" || v == "--") {
		return true
	}
	name := flagName(s, m[0])
	for _, f := range secretFlags {
		if strings.EqualFold(name, f) {
			return false
		}
	}
	for _, p := range []string{"no-", "ask-", "prompt-", "with-"} {
		if strings.HasPrefix(strings.ToLower(name), p) {
			return true
		}
	}
	return spaced && looksLikeFlag(v)
}

// secretFlags are the flag names 0.1.1 matched, with no word before the secret's name.
var secretFlags = []string{"password", "passwd", "secret", "token", "api-key", "apikey", "access-key", "account-key", "client-secret", "auth-token"}

// flagName returns the name of the flag that starts at s[at], without its dashes: up to the =
// or blank that the pattern puts between a flag and its value.
func flagName(s string, at int) string {
	name := s[at+2:]
	if i := strings.IndexAny(name, "= \t\n\f\r"); i >= 0 {
		name = name[:i]
	}
	return name
}

// looksLikeFlag reports a word that reads as a command-line flag rather than a key: "-", "--",
// -u, -uroot, -P3306, --verbose or --host=db.example.com, but not a generated value such as
// -Hk3pQ9xZ2mLw7Rt5Vb8N, whose letters after the first mix upper and lower case.
func looksLikeFlag(v string) bool {
	return flagWord.get().MatchString(v) && !generated(strings.TrimLeft(v, "-"))
}

var flagWord = lazy(`^(?:--?|--?[A-Za-z][a-z0-9-]*|--[A-Za-z][a-z0-9-]*=.*)$`)

// dockerSecretSpec vetoes docker build --secret id=…,src=…, which names a secret file.
func dockerSecretSpec(_ string, _ []int, v string) bool {
	for _, p := range []string{"id=", "src=", "source=", "type=", "env=", "target=", "required"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}
