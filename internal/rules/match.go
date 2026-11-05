package rules

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"

	"github.com/tektonsoftware/sigdek/internal/event"
)

// Match returns the first rule with the given effect that matches the action. Callers pass
// the action as the agent sent it, before redaction and before fields are cut to their
// storage limits.
//
// A command is matched in two forms, and a rule matches if it matches either: as sent with
// runs of blanks squeezed to one space, and as the shell reads its words (see Normalize), so
// `git push \<newline> --force`, `r\m -rf` and `rm "-rf"` read as what they run. Block rules
// read both forms whole, however long, so padding can't hide a command from them. Flag rules
// only label what is recorded, and read at most FlagScanLimit bytes of each (see View).
func (s Set) Match(a *event.Action, effect string) *Rule {
	return s.match(a, effect, nil)
}

// MatchSetup returns the first Flag rule about SigDek's own setup (an id starting
// "sigdek-setup") that matches the action.
func (s Set) MatchSetup(a *event.Action) *Rule {
	return s.match(a, Flag, func(r *Rule) bool { return IsSetupRule(r.ID) })
}

// IsSetupRule reports whether a rule flags changes to SigDek's own setup, which can switch
// the recorder off, so such an action is recorded before it runs.
func IsSetupRule(id string) bool { return strings.HasPrefix(id, "sigdek-setup") }

func (s Set) match(a *event.Action, effect string, only func(*Rule) bool) *Rule {
	if a == nil {
		return nil
	}
	sub := &subject{a: a, whole: effect == Deny}
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Effect == effect && (only == nil || only(r)) && r.matches(sub) {
			return r
		}
	}
	return nil
}

// FlagScanLimit is how much of each form of a long command or URL Flag rules read.
const FlagScanLimit = 16 << 10

// subject is an action being matched, with the forms of its long fields built once.
type subject struct {
	a     *event.Action
	whole bool // Block rules read whole values; Flag rules bounded views

	cmds, urls     []text
	cmdsOK, urlsOK bool
	path           string
	pathOK         bool
}

// text is one form of a field, with its lower-case copy for case-insensitive prefilters.
type text struct {
	s        string
	lower    string
	ascii    bool // s is all ASCII, so lower is exact for RE2's case folding
	hasLower bool
}

func (t *text) lowered() (string, bool) {
	if !t.hasLower {
		t.lower, t.ascii, t.hasLower = asciiLower(t.s), isASCII(t.s), true
	}
	return t.lower, t.ascii
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func (s *subject) commands() []text {
	if !s.cmdsOK {
		forms := []string{Squeeze(s.a.Command)}
		if n := Normalize(s.a.Command); n != forms[0] {
			forms = append(forms, n)
		}
		for _, f := range forms {
			if !s.whole {
				f = View(f, FlagScanLimit)
			}
			s.cmds = append(s.cmds, text{s: f})
		}
		s.cmdsOK = true
	}
	return s.cmds
}

func (s *subject) urlForms() []text {
	if !s.urlsOK {
		u := s.a.URL
		if !s.whole {
			u = View(u, FlagScanLimit)
		}
		s.urls, s.urlsOK = []text{{s: u}}, true
	}
	return s.urls
}

func (s *subject) pathView() string {
	if !s.pathOK {
		s.path, s.pathOK = s.a.Path, true
		if !s.whole {
			s.path = View(s.path, FlagScanLimit)
		}
	}
	return s.path
}

func (r *Rule) matches(s *subject) bool {
	a := s.a
	if len(r.Kinds) > 0 && !contains(r.Kinds, a.Kind) {
		return false
	}
	if r.Tool != "" && !match(r.Tool, a.Name, modeName) {
		return false
	}
	if r.Command != "" && (a.Command == "" || !matchForms(r.Command, s.commands())) {
		return false
	}
	if r.Path != "" && (a.Path == "" || !match(r.Path, s.pathView(), modePath)) {
		return false
	}
	if r.URL != "" && (a.URL == "" || !matchForms(r.URL, s.urlForms())) {
		return false
	}
	if r.MCP != "" && (a.MCP == nil || !matchMCP(r.MCP, a.MCP)) {
		return false
	}
	// A rule with no conditions at all matches nothing, so a half-written rule can't deny everything.
	return len(r.Kinds) > 0 || r.Tool != "" || r.Command != "" || r.Path != "" || r.URL != "" || r.MCP != ""
}

// matchForms matches a command or URL pattern, found anywhere in any of the forms. A plain
// glob is a few substring searches, however long the text. A regular expression first checks
// for the literal text every match must contain, and runs only on forms that have it: RE2
// scans at roughly 10 MB/s per rule, so skipping is what keeps long commands fast.
func matchForms(pattern string, forms []text) bool {
	if segs, ok := literalGlob(pattern); ok {
		for i := range forms {
			if globContains(segs, forms[i].s) {
				return true
			}
		}
		return false
	}
	c := compile(pattern, modeText)
	for i := range forms {
		if c.mayMatch(&forms[i]) {
			if re := c.regexp(); re != nil && re.MatchString(forms[i].s) {
				return true
			}
		}
	}
	return false
}

// matchMCP matches "server/tool" globs part by part, each against the whole name, so
// github/merge_* matches neither server mygithub nor tool merge_x on another server. Cursor
// names only the tool (tool_name "MCP:<tool>"), so then only the tool part is matched. A glob
// without "/" names the tool. A regular expression is matched against "server/tool", or
// "/tool" when the server is unknown.
func matchMCP(pattern string, m *event.MCP) bool {
	if strings.HasPrefix(pattern, "re:") {
		return match(pattern, m.Server+"/"+m.Tool, modeText)
	}
	server, tool, hasServer := strings.Cut(pattern, "/")
	switch {
	case !hasServer:
		return match(pattern, m.Tool, modeName)
	case m.Server == "":
		return match(tool, m.Tool, modeName)
	}
	return match(server, m.Server, modeName) && match(tool, m.Tool, modeName)
}

// View returns s when it is at most limit bytes. A longer value is split into statements at
// newlines and ; & | (kept, so pipelines still read as pipelines); a statement over 1 KB is
// kept as its first and last 512 bytes, which hold the program name and the trailing flags;
// repeated statements are dropped. If that is still over limit, its first and last halves
// are kept.
func View(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	var b strings.Builder
	seen := map[string]bool{}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isSep(s[j]) {
			j++
		}
		k := j
		for k < len(s) && isSep(s[k]) {
			k++
		}
		stmt, sep := s[i:j], s[j:k]
		if len(stmt) > 1024 {
			stmt = stmt[:512] + " " + stmt[len(stmt)-512:]
		}
		if key := stmt + sep; !seen[key] {
			seen[key] = true
			b.WriteString(stmt)
			b.WriteString(sep)
		}
		i = k
	}
	v := b.String()
	if len(v) > limit {
		v = v[:limit/2] + "\n" + v[len(v)-limit/2:]
	}
	return v
}

func isSep(c byte) bool { return c == '\n' || c == ';' || c == '&' || c == '|' }

// Squeeze returns s with every run of spaces, tabs and carriage returns as one space and runs
// of newlines as one newline, without blanks around it.
func Squeeze(s string) string {
	if !needsSqueeze(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	blank := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\r':
			blank = true
		case '\n':
			blank = false
			if b.Len() > 0 && last(&b) != '\n' {
				b.WriteByte('\n')
			}
		default:
			if blank && b.Len() > 0 && last(&b) != '\n' {
				b.WriteByte(' ')
			}
			blank = false
			b.WriteByte(c)
		}
	}
	return b.String()
}

func needsSqueeze(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\t', '\r':
			return true
		case ' ', '\n':
			if i == 0 || i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n' || s[i+1] == '\t' || s[i+1] == '\r' {
				return true
			}
		}
	}
	return false
}

func last(b *strings.Builder) byte {
	s := b.String()
	return s[len(s)-1]
}

// Normalize returns a command the way the shell reads its words, for matching only (the
// record keeps the command as it ran): a backslash-newline joins the lines, a backslash
// outside single quotes stands for the next character, quotes only group (so 'rm', "-rf" and
// r""m read as rm, -rf and rm), blanks squeeze to one space, quoted newlines read as spaces,
// and the first word of each command reads in lower case, since macOS and Windows find RM as
// rm. An empty substitution ($() or two backquotes) reads as nothing, so r$()m is rm, and an
// unquoted $IFS as a blank, so rm${IFS}-rf is rm -rf. What it can't see through is a command
// built at run time, such as X=rm; $X -rf, brace expansion ({rm,-rf,/x}) or a glob that
// names the program (/bin/r? -rf).
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var quote byte
	blank, cmdPos, inWord := false, true, false
	emit := func(c byte) {
		if blank && b.Len() > 0 && last(&b) != '\n' {
			b.WriteByte(' ')
		}
		blank = false
		if cmdPos && 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		inWord = true
		b.WriteByte(c)
	}
	endWord := func() {
		if inWord {
			cmdPos, inWord = false, false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c == '$' || c == '`') && quote != '\'' {
			if strings.HasPrefix(s[i:], "$()") { // an empty substitution
				i += 2
				continue
			}
			if strings.HasPrefix(s[i:], "``") {
				i++
				continue
			}
			if n := ifsRef(s[i:]); n > 0 && quote == 0 {
				blank = true
				endWord()
				i += n - 1
				continue
			}
		}
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
				continue
			}
		case c == '\\':
			if i+1 == len(s) {
				continue
			}
			i++
			if c = s[i]; c == '\n' {
				continue // line continuation
			}
			emit(c)
			continue
		case c == '\'' && quote == 0, c == '"' && quote == 0:
			quote = c
			continue
		case c == '"':
			quote = 0
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r' || (c == '\n' && quote != 0):
			blank = true
			endWord()
		case c == '\n':
			blank, cmdPos, inWord = false, true, false
			if b.Len() > 0 && last(&b) != '\n' {
				b.WriteByte('\n')
			}
		case quote == 0 && (c == ';' || c == '|' || c == '&' || c == '(' || c == '`'):
			emit(c)
			cmdPos, inWord = true, false
		default:
			emit(c)
		}
	}
	return b.String()
}

// ifsRef returns the length of a reference to IFS at the start of s ($IFS or ${IFS}), or 0.
func ifsRef(s string) int {
	if strings.HasPrefix(s, "${IFS}") {
		return len("${IFS}")
	}
	if strings.HasPrefix(s, "$IFS") && (len(s) == 4 || !isIdent(s[4])) {
		return len("$IFS")
	}
	return 0
}

func isIdent(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// literalGlob splits a command glob into its literal parts, when it has no "?" or newline
// (which need the regular expression).
func literalGlob(pattern string) ([]string, bool) {
	if strings.HasPrefix(pattern, "re:") || strings.ContainsAny(pattern, "?\n") {
		return nil, false
	}
	var segs []string
	for _, p := range strings.Split(pattern, "*") {
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs, true
}

// globContains reports whether some line of s contains segs in order, which is what the
// glob's regular expression (unanchored, "*" as ".*") matches. Taking each part's leftmost
// occurrence is enough, so this is a few substring searches per line.
func globContains(segs []string, s string) bool {
	if len(segs) == 0 {
		return true
	}
	for {
		i := strings.Index(s, segs[0])
		if i < 0 {
			return false
		}
		line := s[i+len(segs[0]):]
		nl := strings.IndexByte(s[i:], '\n')
		if nl >= 0 {
			line = s[i+len(segs[0]) : i+nl]
		}
		ok := true
		for _, seg := range segs[1:] {
			j := strings.Index(line, seg)
			if j < 0 {
				ok = false
				break
			}
			line = line[j+len(seg):]
		}
		if ok {
			return true
		}
		if nl < 0 {
			return false
		}
		s = s[i+nl+1:]
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// How a pattern is matched.
type mode int

const (
	modeText mode = iota // commands and URLs: found anywhere
	modePath             // paths: the whole path or any part after a "/"; "**" crosses "/"
	modeName             // tool, MCP server and MCP tool names: the whole name
)

// compiled is a pattern ready to match: the expression (compiled when first needed), or why
// it couldn't be compiled, and the literal text every match contains (lits, compared ignoring
// case when fold is set). For the built-in patterns the literals are known ahead of time
// (knownLiterals), so a rule whose literals a command lacks is skipped without parsing it:
// compiling the built-in rules costs about a millisecond, a good share of a hook's time.
type compiled struct {
	expr    string
	reOnce  sync.Once
	re      *regexp.Regexp
	err     error
	litOnce sync.Once
	lits    []string
	fold    bool
	known   bool
}

// regexp returns the compiled expression, or nil when it doesn't compile (see error).
func (c *compiled) regexp() *regexp.Regexp {
	c.reOnce.Do(func() { c.re, c.err = regexp.Compile(c.expr) })
	return c.re
}

func (c *compiled) error() error {
	c.regexp()
	return c.err
}

// prefilterMin is the shortest text worth a prefilter when the literals aren't known ahead of
// time: below it the expression runs at once, as working out the literals costs about as much
// as compiling the expression.
var prefilterMin = 1 << 10

// mayMatch is the cheap test before the regular expression: false only when t lacks every
// string some part of each match must be.
func (c *compiled) mayMatch(t *text) bool {
	if !c.known {
		if len(t.s) < prefilterMin {
			return true
		}
		c.litOnce.Do(func() {
			if p, err := syntax.Parse(c.expr, syntax.Perl); err == nil {
				c.lits, c.fold = requiredLiterals(p.Simplify())
			}
		})
	}
	if len(c.lits) == 0 {
		return true
	}
	s := t.s
	if c.fold {
		lower, ascii := t.lowered()
		if !ascii {
			return true // RE2 folds k and s with non-ASCII runes (the Kelvin sign, long s)
		}
		s = lower
	}
	for _, l := range c.lits {
		if strings.Contains(s, l) {
			return true
		}
	}
	return false
}

type cacheKey struct {
	pattern string
	mode    mode
}

var (
	cacheMu sync.Mutex
	cache   = map[cacheKey]*compiled{}
)

// compile builds a pattern once per process.
func compile(pattern string, m mode) *compiled {
	k := cacheKey{pattern, m}
	cacheMu.Lock()
	c, ok := cache[k]
	cacheMu.Unlock()
	if ok {
		return c
	}
	expr := ""
	if strings.HasPrefix(pattern, "re:") {
		expr = jsEscapes(pattern[3:])
	} else {
		expr = globToRegexp(pattern, m)
	}
	c = &compiled{expr: expr}
	if l, ok := knownLiterals[pattern]; ok && strings.HasPrefix(pattern, "re:") {
		c.lits, c.fold, c.known = l.lits, l.fold, true
	}
	cacheMu.Lock()
	cache[k] = c
	cacheMu.Unlock()
	return c
}

// match reports whether pattern matches s; an invalid pattern never matches (Problems names it).
func match(pattern, s string, m mode) bool {
	c := compile(pattern, m)
	if !c.mayMatch(&text{s: s}) {
		return false
	}
	re := c.regexp()
	return re != nil && re.MatchString(s)
}

// jsEscapes rewrites \uXXXX, which JavaScript regular expressions (and so the dashboard)
// accept and RE2 doesn't, as RE2's \x{XXXX}.
func jsEscapes(expr string) string {
	if !strings.Contains(expr, `\u`) {
		return expr
	}
	var b strings.Builder
	for i := 0; i < len(expr); i++ {
		if expr[i] == '\\' && i+1 < len(expr) {
			if expr[i+1] == 'u' && i+6 <= len(expr) && isHex(expr[i+2:i+6]) {
				b.WriteString(`\x{` + expr[i+2:i+6] + `}`)
				i += 5
				continue
			}
			b.WriteString(expr[i : i+2])
			i++
			continue
		}
		b.WriteByte(expr[i])
	}
	return b.String()
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune("0123456789abcdefABCDEF", rune(s[i])) {
			return false
		}
	}
	return true
}

// globToRegexp: in paths "**" crosses "/" and "*" does not; elsewhere "*" matches anything.
// Command globs match anywhere in the text, path globs the whole path or any suffix after
// "/", and name globs the whole name.
func globToRegexp(g string, m mode) string {
	var b strings.Builder
	switch m {
	case modePath:
		b.WriteString(`(^|/)`)
	case modeName:
		b.WriteString(`^`)
	}
	isPath := m == modePath
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && isPath && i+1 < len(g) && g[i+1] == '*':
			i++
			if i+1 < len(g) && g[i+1] == '/' {
				i++
				b.WriteString(`(.*/)?`)
			} else {
				b.WriteString(`.*`)
			}
		case c == '*':
			if isPath {
				b.WriteString(`[^/]*`)
			} else {
				b.WriteString(`.*`)
			}
		case c == '?':
			if isPath {
				b.WriteString(`[^/]`)
			} else {
				b.WriteString(`.`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if m != modeText {
		b.WriteString(`$`)
	}
	return b.String()
}

// litSet is a set of strings, one of which every match of an expression contains.
type litSet struct {
	lits []string
	fold bool // compare ignoring ASCII case
}

func (l *litSet) minLen() int {
	n := -1
	for _, s := range l.lits {
		if n < 0 || len(s) < n {
			n = len(s)
		}
	}
	return n
}

// requiredLiterals returns strings one of which every match of re contains, lower-cased when
// fold is set; nil when re promises nothing worth checking (some match could hold no literal,
// the literals are single characters, or a case-folded one isn't ASCII).
func requiredLiterals(re *syntax.Regexp) ([]string, bool) {
	l := need(re)
	if l == nil || l.minLen() < 2 {
		return nil, false
	}
	if l.fold {
		for i, s := range l.lits {
			if !isASCII(s) {
				return nil, false
			}
			l.lits[i] = asciiLower(s)
		}
	}
	return l.lits, l.fold
}

func need(re *syntax.Regexp) *litSet {
	switch re.Op {
	case syntax.OpLiteral:
		return &litSet{lits: []string{string(re.Rune)}, fold: re.Flags&syntax.FoldCase != 0}
	case syntax.OpCapture, syntax.OpPlus:
		return need(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return need(re.Sub[0])
		}
	case syntax.OpConcat:
		// Every part must match: runs of literal parts join into one string, and the most
		// selective part's set is the expression's.
		var best *litSet
		var run []rune
		fold := false
		flush := func() {
			if len(run) > 0 {
				best = better(best, &litSet{lits: []string{string(run)}, fold: fold})
				run, fold = nil, false
			}
		}
		for _, sub := range re.Sub {
			if sub.Op == syntax.OpLiteral {
				run = append(run, sub.Rune...)
				fold = fold || sub.Flags&syntax.FoldCase != 0
				continue
			}
			flush()
			best = better(best, need(sub))
		}
		flush()
		return best
	case syntax.OpAlternate:
		out := &litSet{}
		for _, sub := range re.Sub {
			l := need(sub)
			if l == nil {
				return nil
			}
			out.lits = append(out.lits, l.lits...)
			out.fold = out.fold || l.fold
		}
		return out
	}
	return nil
}

// better picks the more selective set: the longer shortest string, then fewer strings.
func better(a, b *litSet) *litSet {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.minLen() > a.minLen(), b.minLen() == a.minLen() && len(b.lits) < len(a.lits):
		return b
	}
	return a
}

// Problem is a rule the recorder can't use.
type Problem struct {
	Rule  string // the rule's id
	Field string // which pattern
	Err   error
}

func (p Problem) Error() string {
	return fmt.Sprintf("rule %s: %s pattern can't be used (%v)", p.Rule, p.Field, p.Err)
}

// Problems lists the rules with a pattern that doesn't compile; they never match.
func (s Set) Problems() []Problem {
	var out []Problem
	for _, r := range s.Rules {
		for _, f := range []struct {
			name, pattern string
			mode          mode
		}{{"tool", r.Tool, modeName}, {"command", r.Command, modeText}, {"path", r.Path, modePath}, {"url", r.URL, modeText}, {"mcp", r.MCP, modeText}} {
			if f.pattern == "" {
				continue
			}
			if f.name == "mcp" && !strings.HasPrefix(f.pattern, "re:") {
				f.mode = modeName
			}
			if err := compile(f.pattern, f.mode).error(); err != nil {
				out = append(out, Problem{Rule: r.ID, Field: f.name, Err: err})
				break
			}
		}
	}
	return out
}

// Excerpt returns the text of the command (or URL) that rule r matched, from the form it
// matched in, at most max bytes; "" when r has no command or URL pattern. A record keeps the
// first part of a long command, so a blocked one carries this too, to show what was blocked.
func Excerpt(r *Rule, a *event.Action, max int) string {
	if r == nil || a == nil {
		return ""
	}
	sub := &subject{a: a, whole: true}
	pattern, forms := r.Command, []text(nil)
	switch {
	case r.Command != "" && a.Command != "":
		forms = sub.commands()
	case r.URL != "" && a.URL != "":
		pattern, forms = r.URL, sub.urlForms()
	default:
		return ""
	}
	for _, f := range forms {
		lo, hi := -1, -1
		if segs, ok := literalGlob(pattern); ok {
			lo, hi = globSpan(segs, f.s)
		} else if re := compile(pattern, modeText).regexp(); re != nil {
			if m := re.FindStringIndex(f.s); m != nil {
				lo, hi = m[0], m[1]
			}
		}
		if lo < 0 {
			continue
		}
		m := f.s[lo:hi]
		if len(m) > max {
			half := max/2 - 2
			m = cutRunes(m, half) + " … " + m[len(m)-half:]
		}
		return strings.ToValidUTF8(m, "�")
	}
	return ""
}

// globSpan is globContains returning where the match starts and ends.
func globSpan(segs []string, s string) (int, int) {
	if len(segs) == 0 {
		return 0, 0
	}
	base := 0
	for {
		i := strings.Index(s, segs[0])
		if i < 0 {
			return -1, -1
		}
		end := i + len(segs[0])
		line := s[end:]
		if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
			line = s[end : i+nl]
		}
		pos, ok := end, true
		for _, seg := range segs[1:] {
			j := strings.Index(line, seg)
			if j < 0 {
				ok = false
				break
			}
			pos += j + len(seg)
			line = line[j+len(seg):]
		}
		if ok {
			return base + i, base + pos
		}
		nl := strings.IndexByte(s[i:], '\n')
		if nl < 0 {
			return -1, -1
		}
		base += i + nl + 1
		s = s[i+nl+1:]
	}
}

func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
