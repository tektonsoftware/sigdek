package main

// `sigdek status`: did it work? A short checklist for this folder and this machine, with the
// next step for anything missing, then the details support asks for. It only reads local files:
// no network call, and it never creates the device or any other file.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/gitinfo"
	"github.com/tektonsoftware/sigdek/internal/initcmd"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
	"github.com/tektonsoftware/sigdek/internal/upload"
)

// statusEnv is what status looks at, so tests can point it anywhere.
type statusEnv struct {
	home      string // SIGDEK_HOME, or ~/.sigdek
	cwd       string
	claudeDir string // Claude Code's user settings folder
	goos      string
	bin       string // SIGDEK_BIN
	cache     string // SIGDEK_CACHE
	now       time.Time
	gitBash   func() bool // on Windows: whether Claude Code has a Git Bash to run hooks with
}

func runStatus(home string) error {
	cwd, _ := os.Getwd()
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	writeStatus(w, statusEnv{home: home, cwd: cwd, claudeDir: claudeUserDir(), goos: runtime.GOOS,
		bin: os.Getenv("SIGDEK_BIN"), cache: os.Getenv("SIGDEK_CACHE"), now: time.Now(), gitBash: gitBashFound})
	return nil
}

// A check is one line of the checklist: done ("x"), to do (" ") or not needed here ("-").
type check struct {
	mark string
	text string
	more []string // indented lines under it: the next step, or a note
}

func done(text string, more ...string) check  { return check{"x", text, more} }
func todo(text string, more ...string) check  { return check{" ", text, more} }
func aside(text string, more ...string) check { return check{"-", text, more} }

func writeStatus(w io.Writer, e statusEnv) {
	fmt.Fprintf(w, "sigdek %s (%s/%s)\n\n", version, runtime.GOOS, runtime.GOARCH)
	var checks []check
	f := lookAtFolder(e)
	checks = append(checks, f.checks...)
	if c, ok := recorderCheck(e, f); ok {
		checks = append(checks, c)
	}
	dev, devOK, devErr := config.ReadDevice(e.home)
	st := store.New(e.home)
	head, headErr := st.Head()
	health := upload.LoadHealth(e.home)
	checks = append(checks, connectedCheck(e, dev))
	if dev.SignedIn() {
		checks = append(checks, uploadCheck(e, dev, st, head, headErr, health))
	} else if headErr == nil && head.Seq == 0 && f.records {
		checks = append(checks, todo("No records on this machine yet",
			"Next: start Claude Code or Cursor here and give the agent a task."))
	}
	for _, c := range checks {
		fmt.Fprintf(w, "[%s] %s\n", c.mark, c.text)
		for _, m := range c.more {
			fmt.Fprintf(w, "    %s\n", m)
		}
	}
	fmt.Fprintln(w)

	// The details, for support and for anyone who wants them.
	set, rerr := rules.LoadChecked(config.RulesPath(e.home))
	problems := set.Problems()
	broken := map[string]bool{}
	for _, p := range problems {
		broken[p.Rule] = true
	}
	block, flag := 0, 0
	for _, r := range set.Rules {
		switch {
		case broken[r.ID]:
		case r.Effect == rules.Deny:
			block++
		default:
			flag++
		}
	}
	source := "your team's rules, version " + fmt.Sprint(set.Version)
	if set.Version == 0 {
		source = "built-in defaults"
	}
	fmt.Fprintf(w, "Rules       %d flag, %d block (%s)\n", flag, block, source)
	if rerr != nil {
		fmt.Fprintf(w, "            %v\n", rerr)
	}
	for _, p := range problems {
		fmt.Fprintf(w, "            Not working: %s. Fix it in the dashboard.\n", oneLine(p.Error(), 140))
	}
	if headErr != nil {
		fmt.Fprintf(w, "Records     unreadable: %v\n", headErr)
	} else {
		fmt.Fprintf(w, "Records     %d on this machine", head.Seq)
		if t := newestRecord(st); head.Seq > 0 && !t.IsZero() {
			fmt.Fprintf(w, ", the newest written %s", when(t, e.now))
		}
		fmt.Fprintln(w)
	}
	if at, msg := lastError(config.ErrorLog(e.home)); msg != "" && !(strings.HasPrefix(msg, "upload: ") && at.Before(health.ErrorsOverAt())) {
		// An upload error from before the last good upload, or before the last sign-in, is over;
		// anything else stays, such as a warning from that upload.
		fmt.Fprintf(w, "Last error  %s: %s\n", when(at, e.now), oneLine(msg, 160))
	} else {
		fmt.Fprintln(w, "Errors      none")
	}
	switch {
	case devErr != nil:
		fmt.Fprintf(w, "Device      unreadable: %v\n", devErr)
	case !devOK:
		fmt.Fprintln(w, "Device      not set up yet (it is created the first time an agent runs here)")
	case dev.SignedIn():
		fmt.Fprintf(w, "Device      %s, uploading to %s\n", dev.ID, dev.Ingest())
	default:
		fmt.Fprintf(w, "Device      %s\n", dev.ID)
	}
}

// folder is what status found about recording in the current folder.
type folder struct {
	checks     []check
	root       string // the repository's top folder, when cwd is in one
	repoHooks  bool   // the repository has SigDek's hooks and its launcher
	plugin     string // the enabledPlugins key that turns SigDek's plugin on here, if any
	pluginOn   bool
	usesPlugin bool // Claude Code records here through the plugin
	records    bool // something records here, as far as status can tell
}

func lookAtFolder(e statusEnv) folder {
	var f folder
	root := gitinfo.Find(e.cwd).Root
	inRepo := isRepo(root)
	if inRepo {
		f.root = root
	}
	cc := inRepo && fileHas(filepath.Join(root, ".claude", "settings.json"), "hook claude-code")
	cu := inRepo && fileHas(filepath.Join(root, ".cursor", "hooks.json"), "hook cursor")
	atRoot := inRepo && samePath(e.cwd, root)
	launcher := fileExists(filepath.Join(root, ".sigdek", "launch.sh"))
	f.repoHooks = (cc || cu) && launcher
	var where string
	f.pluginOn, where, f.plugin = pluginState(e.claudeDir, f.root)
	user := e.claudeDir != "" && fileHas(filepath.Join(e.claudeDir, "settings.json"), "hook claude-code")
	top := tildePath(root)

	// Claude Code.
	switch {
	case cc && atRoot:
		f.checks = append(f.checks, done("Claude Code records in this project, through its hooks in .claude/settings.json"))
	case cc && f.pluginOn:
		f.usesPlugin = true
		f.checks = append(f.checks, done("Claude Code records here, through SigDek's plugin (turned on in "+where+")",
			"Started in "+top+", it uses the project's own hooks instead."))
	case cc:
		f.checks = append(f.checks, todo("Claude Code records this project only when it starts in the project's top folder",
			"Next: start Claude Code in "+top+"."))
	case f.pluginOn:
		f.usesPlugin = true
		f.checks = append(f.checks, done("Claude Code records here, through SigDek's plugin (turned on in "+where+")"))
	case user:
		f.checks = append(f.checks, done("Claude Code records here, through hooks in "+tildePath(filepath.Join(e.claudeDir, "settings.json"))))
	case where != "":
		f.checks = append(f.checks, todo("SigDek's Claude Code plugin is turned off here ("+where+")",
			"Next: turn it on in Claude Code with /plugin, or remove that setting."))
	}
	ccRecords := (cc && (atRoot || f.pluginOn)) || (!cc && (f.pluginOn || user))
	if ccRecords && e.goos == "windows" && e.gitBash != nil && !e.gitBash() {
		f.checks = append(f.checks, todo("Claude Code on Windows runs hooks through Git Bash, and it isn't installed here, so Claude",
			"Code records nothing (and blocks nothing).",
			"Next: install Git for Windows from https://git-scm.com, or set CLAUDE_CODE_GIT_BASH_PATH."))
	}

	// Cursor.
	switch form := initcmd.CursorForm(root); {
	case cu && !atRoot:
		f.checks = append(f.checks, todo("Cursor records this project only when the project's top folder is its workspace",
			"Next: open "+top+" in Cursor."))
	case cu && form == initcmd.CursorSh && e.goos == "windows":
		f.checks = append(f.checks, todo("Cursor records nothing on this machine: Cursor on Windows can't run this project's hooks",
			"Next: run `sigdek init` again in "+top+" and commit the change."))
	case cu && form == initcmd.CursorPowerShell && e.goos != "windows":
		f.checks = append(f.checks, todo("Cursor records nothing on this machine: this project's Cursor hooks need PowerShell",
			"Next: run `sigdek init` again in "+top+", without --powershell, and commit the change."))
	case cu && form == initcmd.CursorSh:
		f.checks = append(f.checks, done("Cursor records in this project, through its hooks in .cursor/hooks.json",
			"Cursor on Windows can't run these hooks. If teammates use Cursor on Windows, run `sigdek init`",
			"again and commit the change."))
	case cu:
		f.checks = append(f.checks, done("Cursor records in this project, through its hooks in .cursor/hooks.json"))
	case ccRecords && inRepo:
		f.checks = append(f.checks, aside("Cursor: this project has no SigDek hooks for it",
			"If you use Cursor here, run `sigdek init` in "+top+" and commit the files."))
	case ccRecords:
		f.checks = append(f.checks, aside("Cursor: this folder isn't in a git repository, so it has no SigDek hooks for Cursor"))
	}
	f.records = ccRecords || (cu && atRoot)
	if len(f.checks) == 0 {
		next := "Next: for your team's project, run `sigdek init` in its top folder and commit the files; for"
		f.checks = append(f.checks, todo("Nothing records Claude Code or Cursor in this folder yet",
			next, "Claude Code on your own, add SigDek's plugin. See https://sigdek.com/start/"))
	}

	// What can stop the project's hooks from recording.
	if (cc || cu) && !launcher {
		f.checks = append(f.checks, todo("This project's hooks run .sigdek/launch.sh, which is missing, so they record nothing",
			"Next: bring the .sigdek folder back from git, or run `sigdek init` again and commit it."))
	}
	if inRepo && len(initcmd.Outdated(root)) > 0 {
		// Older hook commands passed on sh's status 2 ("block") when the launcher was missing, or
		// left Cursor without an answer when it printed nothing.
		f.checks = append(f.checks, todo("The hook commands here were written by an older sigdek init: a missing or broken launcher",
			"(an old branch, a deleted .sigdek) could block the agent.",
			"Next: run `sigdek init` again in "+top+" and commit the change."))
	}
	return f
}

// recorderCheck says whether the recorder version that the hooks here run is downloaded and
// matches its pinned SHA-256, as the launcher would find it. ok is false when nothing here
// runs a pinned recorder.
func recorderCheck(e statusEnv, f folder) (check, bool) {
	if e.bin != "" {
		if st, err := os.Stat(e.bin); err == nil && !st.IsDir() {
			return done("SIGDEK_BIN is set, so hooks started from here run " + e.bin + " instead of a pinned recorder"), true
		}
	}
	var lock initcmd.Lock
	var lockErr error
	var cache, whose, first string
	switch {
	case f.repoHooks && !f.usesPlugin:
		lock, lockErr = initcmd.ReadLock(f.root)
		cache = filepath.Join(e.home, "bin")
		if e.cache != "" {
			cache = e.cache
		}
		whose = "this project pins"
		first = "the first time Claude Code or Cursor runs here"
	case f.usesPlugin:
		cache = filepath.Join(e.claudeDir, "plugins", "data", pluginDataID(f.plugin))
		whose = "SigDek's plugin pins"
		first = "the first time Claude Code runs with the plugin on"
		path := pluginLockPath(e.claudeDir, f.plugin)
		if path == "" {
			// Claude Code's list of installed plugins couldn't be read: say what's downloaded.
			if v := downloadedVersions(cache); len(v) > 0 {
				return done("SigDek " + strings.Join(v, ", ") + ", the recorder SigDek's plugin runs, is downloaded"), true
			}
			return todo("The recorder SigDek's plugin runs isn't downloaded yet", "It downloads, and is checked, "+first+"."), true
		}
		lock, lockErr = initcmd.ReadLockFile(path)
	default:
		return check{}, false
	}
	if lockErr != nil || lock.Version == "" {
		return todo("Can't tell which recorder "+whose+": its recorder.lock can't be read",
			"Next: run `sigdek init` again and commit .sigdek/recorder.lock."), true
	}
	v := lock.Version
	if v == "dev" {
		return todo("This project pins a development build of SigDek, which the launcher can't download",
			"Next: set SIGDEK_BIN to your build, or run `sigdek init` with a released sigdek and commit it."), true
	}
	want := lock.Sums[e.goos+"_"+runtime.GOARCH]
	if want == "" {
		return todo(fmt.Sprintf("recorder.lock has no build of SigDek %s for %s/%s, so nothing is recorded on this machine", v, e.goos, runtime.GOARCH),
			"Next: run `sigdek init` with network access, then commit .sigdek/recorder.lock."), true
	}
	dir := filepath.Join(cache, v)
	bin := filepath.Join(dir, "sigdek")
	if e.goos == "windows" {
		bin += ".exe"
	}
	if fileExists(bin) {
		if sum, err := fileSHA256(bin); err == nil && sum == want {
			return done("SigDek " + v + ", the recorder " + whose + ", is downloaded and matches its checksum"), true
		}
		return todo("The downloaded SigDek "+v+" doesn't match the checksum "+whose+", yet the launcher runs it",
			"Next: delete "+tildePath(dir)+"; the launcher downloads it again and checks it."), true
	}
	failed := filepath.Join(dir, ".download-failed")
	switch partial, _ := filepath.Glob(filepath.Join(dir, "*.partial")); {
	case recent(filepath.Join(dir, ".downloading"), e.now, 7*time.Minute) || len(partial) > 0:
		return todo("SigDek "+v+", the recorder "+whose+", is downloading",
			"Recording starts once it's complete and checked. Check again in a minute."), true
	case fileExists(failed):
		tries, at := 1, time.Time{}
		if b, err := os.ReadFile(failed); err == nil {
			if n, err := strconv.Atoi(strings.Fields(string(b) + " 1")[0]); err == nil && n > 0 {
				tries = n
			}
		}
		if st, err := os.Stat(failed); err == nil {
			at = st.ModTime()
		}
		failures := "the download failed " + when(at, e.now)
		if tries > 1 {
			failures = fmt.Sprintf("%d downloads failed, the last %s", tries, when(at, e.now))
		}
		return todo("SigDek "+v+", the recorder "+whose+", isn't downloaded: "+failures,
			"Next: check that this machine can reach github.com. The launcher tries again after a wait of up",
			"to an hour."), true
	}
	return todo("SigDek "+v+", the recorder "+whose+", isn't downloaded yet", "It downloads, and is checked, "+first+"."), true
}

func connectedCheck(e statusEnv, dev config.Device) check {
	if !dev.SignedIn() {
		return todo("This machine isn't connected to a team, so its records stay on it",
			"Next: run `sigdek login`.")
	}
	org := dev.OrgName
	if org == "" {
		org = dev.Org
	}
	if info, err := os.Stat(config.TokenRejected(e.home)); err == nil {
		return todo(fmt.Sprintf("Connected to %s as %s, but the server rejected this machine's token (%s)", org, dev.User, when(info.ModTime(), e.now)),
			"Next: run `sigdek login` again.")
	}
	return done(fmt.Sprintf("Connected to %s as %s", org, dev.User))
}

func uploadCheck(e statusEnv, dev config.Device, st *store.Store, head store.Head, headErr error, h upload.Health) check {
	if headErr != nil {
		return todo("Can't tell how many records are waiting: the local log can't be read", "Next: run `sigdek verify`.")
	}
	waiting := max(head.Seq-st.LoadCursor().Seq, 0)
	left := "nothing is waiting"
	if waiting > 0 {
		left = plural(waiting, "record") + " " + map[bool]string{true: "is", false: "are"}[waiting == 1] + " waiting"
	}
	c := func() check {
		switch paused, _ := upload.Paused(e.home, e.now); {
		case h.LastError != "" && paused && e.now.Before(h.RetryAfter):
			return todo("Uploads wait until "+h.RetryAfter.Local().Format("Jan 02 15:04")+" after an error: "+oneLine(h.LastError, 120),
				"Next: `sigdek flush` tries now; "+left+".")
		case h.LastError != "":
			return todo("The last upload, "+when(h.LastTry, e.now)+", failed: "+oneLine(h.LastError, 120),
				"Next: run `sigdek flush` to try again now; "+left+".")
		case !h.LastOK.IsZero():
			if waiting > 0 {
				return done("Uploads work: the last one finished "+when(h.LastOK, e.now)+", and "+left,
					"They go up when the agent finishes its turn, or now with `sigdek flush`.")
			}
			return done("Uploads work: the last one finished " + when(h.LastOK, e.now) + ", and " + left)
		case waiting > 0:
			return todo("Nothing has uploaded yet, and "+left,
				"They go up when the agent finishes its turn, or now with `sigdek flush`.")
		case head.Seq > 0:
			return todo("Nothing has uploaded yet: the records on this machine were made before it connected",
				"and stay on it. Next: start Claude Code or Cursor here and give the agent a task.")
		}
		return todo("Nothing has uploaded yet, because there are no records yet",
			"Next: start Claude Code or Cursor here and give the agent a task. Records upload when it",
			"finishes its turn.")
	}()
	if h.Refused > 0 || h.Skipped > 0 {
		c.more = append(c.more, fmt.Sprintf("The server refused %d as malformed and %d as too large; they show as breaks in your report.", h.Refused, h.Skipped))
	}
	return c
}

// pluginState says whether Claude Code has SigDek's plugin on here, as Claude Code decides it:
// the repository's .claude/settings.local.json, then its .claude/settings.json, then the user's
// settings, the first that sets "sigdek@<any marketplace>" in enabledPlugins. where names that
// file, and is empty when none does (the plugin isn't installed, or isn't SigDek's concern here);
// key is the enabledPlugins entry, such as "sigdek@sigdek".
func pluginState(userDir, repoRoot string) (on bool, where, key string) {
	var files [][2]string
	if repoRoot != "" && isRepo(repoRoot) {
		files = append(files,
			[2]string{filepath.Join(repoRoot, ".claude", "settings.local.json"), "this repo's .claude/settings.local.json"},
			[2]string{filepath.Join(repoRoot, ".claude", "settings.json"), "this repo's .claude/settings.json"})
	}
	if userDir != "" {
		p := filepath.Join(userDir, "settings.json")
		files = append(files, [2]string{p, tildePath(p)})
	}
	for _, f := range files {
		if set, enabled, k := pluginSetting(f[0]); set {
			return enabled, f[1], k
		}
	}
	return false, "", ""
}

// pluginSetting reads one Claude Code settings file's enabledPlugins entry for SigDek's plugin,
// from any marketplace ("sigdek@sigdek" from SigDek's own). An entry that turns it on wins over
// one that turns it off, in the rare file that lists it from two marketplaces.
func pluginSetting(path string) (set, enabled bool, key string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false, ""
	}
	var s struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false, false, ""
	}
	for k, v := range s.EnabledPlugins {
		if !strings.HasPrefix(k, "sigdek@") {
			continue
		}
		var on bool
		if json.Unmarshal(v, &on) != nil {
			continue
		}
		if !set || (on && !enabled) {
			key = k
		}
		set = true
		enabled = enabled || on
	}
	return set, enabled, key
}

var notIDChar = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// pluginDataID is the folder Claude Code keeps a plugin's data in, under plugins/data: the
// plugin's id with every character but a letter, digit, _ or - made a -.
func pluginDataID(key string) string { return notIDChar.ReplaceAllString(key, "-") }

// pluginLockPath finds the installed plugin's scripts/recorder.lock through Claude Code's list
// of installed plugins; "" when it can't.
func pluginLockPath(claudeDir, key string) string {
	b, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return ""
	}
	var list struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(b, &list) != nil {
		return ""
	}
	for _, p := range list.Plugins[key] {
		if lock := filepath.Join(p.InstallPath, "scripts", "recorder.lock"); p.InstallPath != "" && fileExists(lock) {
			return lock
		}
	}
	return ""
}

// downloadedVersions lists the recorder versions in a launcher's cache folder.
func downloadedVersions(cache string) []string {
	entries, _ := os.ReadDir(cache)
	var out []string
	for _, d := range entries {
		if fileExists(filepath.Join(cache, d.Name(), "sigdek")) || fileExists(filepath.Join(cache, d.Name(), "sigdek.exe")) {
			out = append(out, d.Name())
		}
	}
	return out
}

// newestRecord is when the local log last changed.
func newestRecord(st *store.Store) time.Time {
	files, err := st.Files()
	if err != nil || len(files) == 0 {
		return time.Time{}
	}
	info, err := os.Stat(files[len(files)-1])
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// gitBashFound looks for a Git Bash where Claude Code on Windows finds one: the path in
// CLAUDE_CODE_GIT_BASH_PATH, next to git, in Git for Windows' usual folder, or on the PATH.
func gitBashFound() bool {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		return fileExists(p)
	}
	var places []string
	if git, err := exec.LookPath("git"); err == nil {
		top := filepath.Dir(filepath.Dir(git))
		places = append(places, filepath.Join(top, "bin", "bash.exe"), filepath.Join(top, "usr", "bin", "bash.exe"))
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if d := os.Getenv(env); d != "" {
			places = append(places, filepath.Join(d, "Git", "bin", "bash.exe"), filepath.Join(d, "Programs", "Git", "bin", "bash.exe"))
		}
	}
	for _, p := range places {
		if fileExists(p) {
			return true
		}
	}
	_, err := exec.LookPath("bash")
	return err == nil
}

func recent(path string, now time.Time, within time.Duration) bool {
	st, err := os.Stat(path)
	return err == nil && now.Sub(st.ModTime()) < within
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// samePath compares two folders the way the file system does, links and case included.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
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
