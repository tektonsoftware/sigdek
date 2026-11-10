// Package initcmd implements `sigdek init`: it adds SigDek's hooks to a repository so
// every developer who clones it is recorded, with no install step of their own.
package initcmd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/adapter"
)

//go:embed templates/launch.sh
var launchSh []byte

//go:embed templates/launch.ps1
var launchPs1 []byte

// ReleaseBase is where tagged releases are published.
// REHEARSAL ONLY (branch release-rehearsal, never merged): a var, so the rehearsal build can
// point it at a local server with -X. On main it is a const.
var ReleaseBase = "https://github.com/tektonsoftware/sigdek/releases/download"

// Targets are the platforms each release is built for.
var Targets = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"}

type Options struct {
	Root       string   // repository root
	Version    string   // this binary's version; "dev" for local builds
	Tools      []string // "claude-code", "cursor"
	PowerShell bool     // write Cursor hooks for PowerShell (Windows teams)
	Offline    bool     // don't fetch release checksums; pin only this machine's platform
	Remove     bool     // take SigDek out of the repository's hook files
	Client     *http.Client
	Out        io.Writer
}

// Hooks run the launcher through sh, so it works without the exec bit (lost when a
// repository is set up on Windows) and whatever the checkout did to it.
//
// The command itself must never fail, because both tools treat exit 2 as "block": sh exits
// 2 when it can't open the launcher (a branch made before SigDek was added, `.sigdek`
// ignored or deleted, an unreadable file) and on a syntax error before the launcher's trap
// (merge-conflict markers). Claude Code's command ends in `; exit 0`, which means the same
// in sh and in PowerShell: Claude Code runs hooks through PowerShell on Windows without Git
// Bash, where `sh` isn't found and any status other than 0 or 2 blocks the tool call. A
// missing command is a statement-terminating error there, so `exit 0` still runs.
const (
	claudeCommand = `sh "$CLAUDE_PROJECT_DIR/.sigdek/launch.sh" hook claude-code; exit 0`
	hookTimeout   = 10
)

// cursorCommand gives Cursor the launcher's own safe answer whenever the launcher gives none:
// Cursor blocks on exit 2 and on invalid output from permission hooks, and on no output at all
// under failClosed. `grep '^{'` passes on the launcher's answer (one JSON object) and fails
// when there is none, so `|| echo` answers for a launcher that failed, for one that printed
// nothing and exited 0 (an empty launch.sh, or a folder in its place), and for one replaced by
// something that prints other text (a Git LFS pointer runs `size`, which macOS has). Cursor runs commands through PowerShell on
// Windows, where Windows PowerShell 5.1 can't parse `||`; launch.ps1 (the --powershell form)
// catches its own errors and exits 0.
func cursorCommand(event string, ps bool) string {
	if ps {
		return "powershell -NoProfile -ExecutionPolicy Bypass -File .sigdek/launch.ps1 hook cursor " + event
	}
	return "sh .sigdek/launch.sh hook cursor " + event + " | grep '^{' || echo '" + strings.TrimSpace(string(adapter.CursorAllowFor(event))) + "'"
}

// Outdated names the agent tools whose SigDek hook commands in the repository at root aren't
// the ones this version writes (an older sigdek init wrote them), so `sigdek init` should run
// again. The --powershell form of the Cursor commands is current too.
func Outdated(root string) []string {
	var out []string
	for _, c := range sigdekCommands(filepath.Join(root, ".claude", "settings.json"), true) {
		if c.command != claudeCommand {
			out = append(out, "claude-code")
			break
		}
	}
	for _, c := range sigdekCommands(filepath.Join(root, ".cursor", "hooks.json"), false) {
		if c.command != cursorCommand(c.event, false) && c.command != cursorCommand(c.event, true) {
			out = append(out, "cursor")
			break
		}
	}
	return out
}

type hookCommand struct{ event, command string }

// sigdekCommands lists SigDek's hook commands in a hook file, by event. Claude Code groups
// hooks under a matcher; Cursor lists them directly.
func sigdekCommands(path string, grouped bool) []hookCommand {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	type hook struct {
		Command string `json:"command"`
	}
	var file struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(b, &file) != nil {
		return nil
	}
	var out []hookCommand
	for ev, list := range file.Hooks {
		for _, raw := range list {
			var hs []hook
			if grouped {
				var g struct {
					Hooks []hook `json:"hooks"`
				}
				_ = json.Unmarshal(raw, &g)
				hs = g.Hooks
			} else {
				var h hook
				_ = json.Unmarshal(raw, &h)
				hs = []hook{h}
			}
			for _, h := range hs {
				if isOurs(h.Command) {
					out = append(out, hookCommand{ev, h.Command})
				}
			}
		}
	}
	return out
}

// WindowsNote is what init says on Windows after writing Cursor hooks without --powershell:
// Cursor runs hooks through PowerShell there, where they can't run.
func WindowsNote(goos string, tools []string, ps bool) string {
	if goos != "windows" || ps {
		return ""
	}
	for _, t := range tools {
		if t == "cursor" {
			return "Note: Cursor on Windows runs hooks through PowerShell, and Windows PowerShell 5.1 can't run these\n" +
				"commands, so Cursor records nothing there (it isn't blocked). If your team uses Cursor on Windows, run\n" +
				"`sigdek init --powershell` instead; those Cursor hooks need PowerShell on every machine."
		}
	}
	return ""
}

func isOurs(cmd string) bool {
	return strings.Contains(cmd, ".sigdek/launch.") && strings.Contains(cmd, " hook ")
}

// ourFiles are what init puts in .sigdek, with their modes.
var ourFiles = []struct {
	name string
	mode os.FileMode
}{{"launch.sh", 0o755}, {"launch.ps1", 0o644}, {"recorder.lock", 0o644}, {"README.md", 0o644}, {".gitattributes", 0o644}}

// Run writes or removes the hook files and returns the paths it changed. Every change is
// worked out and every target checked before anything is written, so a bad settings file,
// a failed checksum fetch or an unwritable folder leaves the repository as it was. If a
// write still fails part-way, the paths already written are returned with the error.
func Run(o Options) ([]string, error) {
	tools, err := ParseTools(o.Tools)
	if err != nil {
		return nil, err
	}
	var plan []change
	after := map[string][]byte{} // hook file -> its content once the plan is applied
	for _, t := range tools {
		var c *change
		switch t {
		case "claude-code":
			c, err = claudeSettings(o.Root, o.Remove)
		case "cursor":
			c, err = cursorHooks(o.Root, o.PowerShell, o.Remove)
		}
		if err != nil {
			return nil, err
		}
		if c != nil {
			plan = append(plan, *c)
			after[c.path] = c.data
		}
	}

	dir := filepath.Join(o.Root, ".sigdek")
	if o.Remove {
		// The launchers go too once no hook file uses them any more.
		used := false
		for _, p := range []string{filepath.Join(o.Root, ".claude", "settings.json"), filepath.Join(o.Root, ".cursor", "hooks.json")} {
			b, planned := after[p]
			if !planned {
				b, _ = os.ReadFile(p)
			}
			used = used || bytes.Contains(b, []byte(".sigdek/launch."))
		}
		if !used {
			for _, f := range ourFiles {
				if _, err := os.Lstat(filepath.Join(dir, f.name)); err == nil {
					plan = append(plan, change{path: filepath.Join(dir, f.name), remove: true})
				}
			}
		}
	} else {
		lock, err := lockFile(o)
		if err != nil {
			return nil, err
		}
		data := map[string][]byte{"launch.sh": launchSh, "launch.ps1": launchPs1, "recorder.lock": lock, "README.md": []byte(readme), ".gitattributes": []byte(gitattributes)}
		for _, f := range ourFiles {
			plan = append(plan, change{path: filepath.Join(dir, f.name), data: data[f.name], mode: f.mode, forceMode: true})
		}
	}

	for _, c := range plan {
		if err := c.check(); err != nil {
			return nil, err
		}
	}
	var done []string
	for _, c := range plan {
		wrote, err := c.apply()
		if err != nil {
			return done, fmt.Errorf("%s: %w", c.path, err)
		}
		if wrote {
			done = append(done, c.path)
		}
	}
	if o.Remove {
		_ = os.Remove(dir) // only succeeds when empty
	}
	return done, nil
}

// Committed lists, relative to the repository root, the files a teammate needs in their
// clone for the tools set up: the hook files and the launchers they run.
func Committed(tools []string) []string {
	var out []string
	for _, t := range tools {
		switch t {
		case "claude-code":
			out = append(out, ".claude/settings.json")
		case "cursor":
			out = append(out, ".cursor/hooks.json")
		}
	}
	for _, f := range ourFiles {
		out = append(out, ".sigdek/"+f.name)
	}
	return out
}

// Ignored returns the paths (relative to root) that git would leave out of a commit: an
// ignore rule matches them and they aren't tracked. A clone then has hooks that point at a
// launcher it doesn't have, so nothing is recorded there. Nil when git can't say.
func Ignored(root string, paths []string) []string {
	cmd := exec.Command("git", append([]string{"-C", root, "check-ignore", "--"}, paths...)...)
	out, err := cmd.Output() // exit 1: nothing is ignored
	if err != nil && len(out) == 0 {
		return nil
	}
	var ignored []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ignored = append(ignored, filepath.ToSlash(l))
		}
	}
	return ignored
}

// ParseTools accepts "claude-code,cursor", "claude-code, cursor" and repeats.
func ParseTools(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range list {
		for _, t := range strings.Split(item, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" || seen[t] {
				continue
			}
			if t != "claude-code" && t != "cursor" {
				return nil, fmt.Errorf("unknown tool %q (use claude-code or cursor)", t)
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tools given (use --tools claude-code,cursor)")
	}
	return out, nil
}

// claudeSettings merges one hook per event into .claude/settings.json. Everything that isn't
// SigDek's stays exactly as written, keys in their order.
func claudeSettings(root string, remove bool) (*change, error) {
	path := filepath.Join(root, ".claude", "settings.json")
	obj, existed, err := readObjectFile(path)
	if err != nil {
		return nil, err
	}
	if remove && !existed {
		return nil, nil
	}
	ho, err := hookLists(obj, path)
	if err != nil {
		return nil, err
	}
	// Claude Code groups hooks under a matcher: {"matcher": ..., "hooks": [{"command": ...}]}.
	for _, k := range append([]string(nil), ho.keys...) {
		var groups []json.RawMessage
		_ = json.Unmarshal(ho.vals[k], &groups)
		var kept []json.RawMessage
		for _, raw := range groups {
			g, err := parseObject(raw)
			if err != nil {
				kept = append(kept, raw) // not an object: not ours to judge
				continue
			}
			hraw, ok := g.get("hooks")
			var hs []json.RawMessage
			if !ok || json.Unmarshal(hraw, &hs) != nil {
				kept = append(kept, raw)
				continue
			}
			keep := withoutOurs(hs)
			switch {
			case len(keep) == len(hs):
				kept = append(kept, raw)
			case len(keep) > 0:
				g.set("hooks", keep)
				b, _ := g.marshal()
				kept = append(kept, b)
			}
		}
		setList(ho, k, kept)
	}
	if !remove {
		for _, ev := range adapter.ClaudeCodeEvents {
			hook := newObject()
			hook.set("type", "command")
			hook.set("command", claudeCommand)
			hook.set("timeout", hookTimeout)
			hb, _ := hook.marshal()
			group := newObject()
			group.set("hooks", []json.RawMessage{hb})
			gb, _ := group.marshal()
			appendList(ho, ev, gb)
		}
	}
	if len(ho.keys) == 0 {
		obj.del("hooks")
	} else {
		raw, _ := ho.marshal()
		obj.set("hooks", json.RawMessage(raw))
	}
	return jsonChange(path, obj)
}

// cursorHooks merges one hook per event into .cursor/hooks.json, keeping everything else.
func cursorHooks(root string, ps, remove bool) (*change, error) {
	path := filepath.Join(root, ".cursor", "hooks.json")
	obj, existed, err := readObjectFile(path)
	if err != nil {
		return nil, err
	}
	if remove && !existed {
		return nil, nil
	}
	if _, ok := obj.get("version"); !ok {
		obj.set("version", 1)
	}
	ho, err := hookLists(obj, path)
	if err != nil {
		return nil, err
	}
	for _, k := range append([]string(nil), ho.keys...) {
		var list []json.RawMessage
		_ = json.Unmarshal(ho.vals[k], &list)
		setList(ho, k, withoutOurs(list))
	}
	if !remove {
		for _, ev := range adapter.CursorEvents {
			hook := newObject()
			hook.set("command", cursorCommand(ev, ps))
			hook.set("timeout", hookTimeout)
			hb, _ := hook.marshal()
			appendList(ho, ev, hb)
		}
	}
	raw, _ := ho.marshal()
	obj.set("hooks", json.RawMessage(raw))
	return jsonChange(path, obj)
}

// hookLists returns the "hooks" object, checking that every event holds a list.
func hookLists(obj *object, path string) (*object, error) {
	raw, ok := obj.get("hooks")
	if !ok {
		return newObject(), nil
	}
	ho, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: \"hooks\" is not an object; nothing was changed", path)
	}
	for _, k := range ho.keys {
		var list []json.RawMessage
		if err := json.Unmarshal(ho.vals[k], &list); err != nil {
			return nil, fmt.Errorf("%s: hooks.%s is not an array; nothing was changed", path, k)
		}
	}
	return ho, nil
}

// withoutOurs drops SigDek's entries from a list of hooks, keeping the others as written.
func withoutOurs(list []json.RawMessage) []json.RawMessage {
	var keep []json.RawMessage
	for _, raw := range list {
		var h struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &h) == nil && isOurs(h.Command) {
			continue
		}
		keep = append(keep, raw)
	}
	return keep
}

// setList replaces an event's list, dropping the event when the list is empty.
func setList(ho *object, k string, list []json.RawMessage) {
	if len(list) == 0 {
		ho.del(k)
		return
	}
	ho.set(k, list)
}

func appendList(ho *object, k string, item json.RawMessage) {
	var list []json.RawMessage
	if raw, ok := ho.get(k); ok {
		_ = json.Unmarshal(raw, &list)
	}
	ho.set(k, append(list, item))
}

func jsonChange(path string, obj *object) (*change, error) {
	b, err := obj.marshal()
	if err != nil {
		return nil, err
	}
	return &change{path: path, data: b, mode: 0o644}, nil
}

// change is one file init will write or remove.
type change struct {
	path      string
	data      []byte
	mode      os.FileMode // for a new file, or always with forceMode
	forceMode bool
	remove    bool
}

// target is where the bytes go: through a symlink to the file it points at, so a settings
// file linked from a dotfiles repository stays a link.
func (c change) target() string {
	if st, err := os.Lstat(c.path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		if t, err := filepath.EvalSymlinks(c.path); err == nil {
			return t
		}
	}
	return c.path
}

// check makes sure the change can be made: the folder exists or can be created, and a
// temporary file can be written there (that is how the file is replaced).
func (c change) check() error {
	if c.remove {
		return nil
	}
	p := c.target()
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return fmt.Errorf("%s is a folder; nothing was changed", p)
	}
	dir := filepath.Dir(p)
	for {
		st, err := os.Stat(dir)
		if err == nil {
			if !st.IsDir() {
				return fmt.Errorf("%s is not a folder, so %s can't be written; nothing was changed", dir, p)
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
	f, err := os.CreateTemp(dir, ".sigdek-check-*")
	if err != nil {
		return fmt.Errorf("can't write in %s (%v); nothing was changed", dir, err)
	}
	f.Close()
	return os.Remove(f.Name())
}

// apply makes the change and reports whether anything changed. An existing file keeps its
// permissions unless forceMode is set.
func (c change) apply() (bool, error) {
	if c.remove {
		err := os.Remove(c.path)
		return err == nil, err
	}
	p := c.target()
	mode := c.mode
	if st, err := os.Stat(p); err == nil {
		if !c.forceMode {
			mode = st.Mode().Perm()
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, c.data) && samePerm(st.Mode(), mode) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".sigdek-*")
	if err != nil {
		return false, err
	}
	_, werr := tmp.Write(c.data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), mode)); err != nil {
		os.Remove(tmp.Name())
		return false, err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return false, err
	}
	return true, nil
}

// samePerm compares permissions the way the file system keeps them. Windows only has a
// read-only flag (a file reads back as 0666 or 0444), so there only the owner's write bit
// counts; otherwise every run would rewrite launch.sh to "fix" its 0755.
func samePerm(have, want os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return have&0o200 == want&0o200
	}
	return have.Perm() == want.Perm()
}

// lockFile pins the recorder version and the SHA-256 of every platform's binary.
func lockFile(o Options) ([]byte, error) {
	sums := map[string]string{}
	url := ""
	if o.Version != "dev" && !o.Offline {
		url = ReleaseBase + "/v" + o.Version
		var err error
		sums, err = fetchChecksums(o.Client, url+"/checksums.txt", o.Version)
		if err != nil {
			return nil, fmt.Errorf("fetching release checksums: %w (use --offline to pin only this machine's platform)", err)
		}
	} else {
		if o.Version != "dev" {
			url = ReleaseBase + "/v" + o.Version
		}
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		sum, err := fileSHA256(exe)
		if err != nil {
			return nil, err
		}
		sums[runtime.GOOS+"_"+runtime.GOARCH] = sum
	}
	var b strings.Builder
	b.WriteString("# Pinned SigDek recorder. Written by `sigdek init`; commit this file.\n")
	b.WriteString("# The launcher downloads this exact version and refuses any binary whose SHA-256 differs.\n")
	fmt.Fprintf(&b, "version=%s\nurl=%s\n", o.Version, url)
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, sums[k])
	}
	return []byte(b.String()), nil
}

// Lock is what .sigdek/recorder.lock pins.
type Lock struct {
	Version, URL string
	Sums         map[string]string // "<os>_<arch>" -> SHA-256
}

// Platform is this machine's key in Sums, such as darwin_arm64.
func Platform() string { return runtime.GOOS + "_" + runtime.GOARCH }

// ReadLock reads a repository's .sigdek/recorder.lock.
func ReadLock(root string) (Lock, error) {
	b, err := os.ReadFile(filepath.Join(root, ".sigdek", "recorder.lock"))
	if err != nil {
		return Lock{}, err
	}
	l := Lock{Sums: map[string]string{}}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		switch {
		case !ok || strings.HasPrefix(k, "#"):
		case k == "version":
			l.Version = v
		case k == "url":
			l.URL = v
		default:
			l.Sums[k] = v
		}
	}
	return l, nil
}

// fetchChecksums reads a release's checksums.txt ("<sha256>  sigdek_<v>_<os>_<arch>[.exe]").
func fetchChecksums(c *http.Client, url, version string) (map[string]string, error) {
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", url, res.StatusCode)
	}
	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	prefix := "sigdek_" + version + "_"
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(f[1], "*"), ".exe")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		sums[strings.TrimPrefix(name, prefix)] = strings.ToLower(f[0])
	}
	for _, t := range Targets {
		if sums[t] == "" {
			return nil, fmt.Errorf("checksums.txt has no entry for %s", t)
		}
	}
	return sums, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gitattributes keeps the launchers' line endings whatever a developer's checkout settings
// (core.autocrlf would give launch.sh CRLF line endings on Windows).
const gitattributes = `# Written by sigdek init: keep the launchers' line endings on every checkout, and keep
# filters such as Git LFS off them (a pointer file in place of launch.sh can't run).
* -filter
*.sh text eol=lf
*.ps1 text eol=crlf
recorder.lock text eol=lf
`

const readme = `# .sigdek

This folder makes Claude Code and Cursor record what their agents do in this repository,
for your team's SigDek evidence log. It was added by ` + "`sigdek init`" + `.

- **launch.sh / launch.ps1** run the recorder. On first use they download the version
  pinned in recorder.lock and refuse it unless its SHA-256 matches. If anything fails,
  they exit quietly and your agent carries on.
- **recorder.lock** pins the version and checksums. Update it with ` + "`sigdek init`" + ` from a newer release.
- **.gitattributes** keeps the launchers' line endings the same on every checkout.

What is recorded: who ran which agent, which tool, the command or file path, and whether
it ran or was blocked. Prompt text, file contents and command output are never recorded;
secrets in commands are stripped. Nothing leaves your machine until you run ` + "`sigdek login`" + `.

Source and documentation: https://github.com/tektonsoftware/sigdek
`
