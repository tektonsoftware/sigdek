// Command sigdek records what AI coding agents do on this machine.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/gitinfo"
	"github.com/tektonsoftware/sigdek/internal/hook"
	"github.com/tektonsoftware/sigdek/internal/initcmd"
	"github.com/tektonsoftware/sigdek/internal/login"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/spawn"
	"github.com/tektonsoftware/sigdek/internal/store"
	"github.com/tektonsoftware/sigdek/internal/upload"
	"regexp"
	"runtime/debug"
)

// version is set at release time with -ldflags "-X main.version=1.2.3".
var version = "dev"

// pseudoVersion matches Go's untagged-commit versions, like v0.0.0-20261028153000-abcdef123456.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}(\+.*)?$|\+dirty$`)

// A binary from `go install github.com/tektonsoftware/sigdek/cmd/sigdek@v1.2.3` has no
// ldflags, but Go records the module version; use it so `sigdek init` pins that release.
func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Version, "v") && !pseudoVersion.MatchString(bi.Main.Version) {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

const usage = `sigdek records what AI coding agents do on this machine, for your team's evidence log.

Usage:
  sigdek init [--tools claude-code,cursor] [--powershell] [--offline] [--remove]
                     Add SigDek's hooks to the current repository
  sigdek login       Connect this machine to your team (opens the browser)
  sigdek logout      Disconnect this machine
  sigdek status      What is set up and working, and what isn't
  sigdek log [-n 20] [--json]
                     Show recent records from this machine
  sigdek verify      Check that no local record was changed or removed
  sigdek flush       Upload unsent records now
  sigdek version

Called by the agent tools, not by people:
  sigdek hook claude-code
  sigdek hook cursor <event>
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		return
	}
	home := config.Home()
	cmd, rest := args[0], args[1:]

	// The hot path: nothing else runs first, and it always exits 0.
	if cmd == "hook" {
		tool, hint := "", ""
		if len(rest) > 0 {
			tool = rest[0]
		}
		if len(rest) > 1 {
			hint = rest[1]
		}
		hook.Run(tool, hint, os.Stdin, os.Stdout, hook.Options{Home: home, Now: time.Now, Spawn: spawn.Flush, Upload: func(d time.Duration) error {
			_, err := upload.FlushWith(home, userAgent(), upload.Options{Background: true, Within: d, Log: func(err error) { hook.LogError(home, err) }})
			return err
		}})
		os.Exit(0)
	}

	var err error
	switch cmd {
	case "init":
		err = runInit(home, rest)
	case "login":
		err = runLogin(home, rest)
	case "logout":
		if err = noArgs("logout", rest); err == nil {
			err = runLogout(home)
		}
	case "status":
		if err = noArgs("status", rest); err == nil {
			err = runStatus(home)
		}
	case "log":
		err = runLog(home, rest)
	case "verify":
		if err = noArgs("verify", rest); err == nil {
			err = runVerify(home)
		}
	case "flush":
		err = runFlush(home, rest)
	case "version", "--version", "-v":
		if err = noArgs(cmd, rest); err == nil {
			fmt.Printf("sigdek %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		}
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "sigdek: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sigdek: %v\n", err)
		os.Exit(1)
	}
}

// noArgs is parseFlags for a command that takes no options or arguments.
func noArgs(name string, args []string) error {
	return parseFlags(flag.NewFlagSet(name, flag.ContinueOnError), args)
}

// parseFlags reports a bad flag once (the flag package would print it, then the usage, then
// main would print it again) and prints the command's flags for -h.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(os.Stdout)
		fmt.Printf("Usage of sigdek %s:\n", fs.Name())
		fs.PrintDefaults()
		return err
	}
	if err != nil {
		return fmt.Errorf("%s: %v (run `sigdek %s -h` for its options)", fs.Name(), err, fs.Name())
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s: unexpected argument %q (run `sigdek %s -h` for its options)", fs.Name(), fs.Arg(0), fs.Name())
	}
	return nil
}

func runLogout(home string) error {
	res, err := login.Logout(home, nil)
	if err != nil {
		return err
	}
	if !res.WasSignedIn {
		fmt.Println("This machine wasn't signed in; nothing changed.")
		return nil
	}
	if res.RevokeErr != nil {
		fmt.Printf("Signed out on this machine, but the server didn't confirm it revoked this machine's token (%v).\n"+
			"An owner or admin can revoke the machine in the dashboard.\n", res.RevokeErr)
	} else {
		fmt.Println("Signed out, and the server revoked this machine's token.")
	}
	team := "the same team"
	if res.Org != "" {
		team = res.Org
	}
	fmt.Printf("Agents here are still recorded, on this machine only. Those records upload if this machine signs in\nto %s again.\n", team)
	return nil
}

func userAgent() string {
	return "sigdek/" + version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

func runInit(home string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	tools := fs.String("tools", "claude-code,cursor", "which agent tools to record")
	ps := fs.Bool("powershell", false, "write Cursor hooks that run under PowerShell (Windows teams)")
	offline := fs.Bool("offline", false, "pin only this machine's platform instead of fetching release checksums")
	remove := fs.Bool("remove", false, "remove SigDek's hooks from this repository")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	info := gitinfo.Find(cwd)
	if _, err := os.Stat(filepath.Join(info.Root, ".git")); err != nil {
		return errors.New("run `sigdek init` inside a git repository")
	}
	changed, err := initcmd.Run(initcmd.Options{
		Root: info.Root, Version: version, Tools: []string{*tools},
		PowerShell: *ps, Offline: *offline, Remove: *remove, Out: os.Stdout,
	})
	rel := func(p string) string {
		r, _ := filepath.Rel(info.Root, p)
		return filepath.ToSlash(r)
	}
	if err != nil {
		if len(changed) > 0 {
			fmt.Fprintln(os.Stderr, "These files were changed before the error:")
			for _, p := range changed {
				fmt.Fprintln(os.Stderr, "  "+rel(p))
			}
		}
		return err
	}
	if *remove {
		if len(changed) == 0 {
			fmt.Println("SigDek's hooks weren't in this repository; nothing changed.")
			return nil
		}
		if _, err := os.Stat(filepath.Join(info.Root, ".sigdek", "launch.sh")); err == nil {
			fmt.Println("Removed SigDek's hooks for " + *tools + ". The .sigdek folder stays: the other tool's hooks still use it.")
		} else {
			fmt.Println("Removed SigDek's hooks and the .sigdek folder.")
		}
		fmt.Println("Commit the change so it applies to everyone:")
		for _, p := range changed {
			fmt.Println("  " + rel(p))
		}
		return nil
	}
	if len(changed) == 0 {
		fmt.Println("SigDek is already set up in this repository; nothing changed.")
	} else {
		fmt.Println("SigDek is set up in this repository. Commit these files:")
		for _, p := range changed {
			fmt.Println("  " + rel(p))
		}
	}
	toolList, _ := initcmd.ParseTools([]string{*tools})
	if ignored := initcmd.Ignored(info.Root, initcmd.Committed(toolList)); len(ignored) > 0 {
		fmt.Println("\nWarning: git ignores these files, so a commit would leave them out and teammates who clone")
		fmt.Println("the repository wouldn't be recorded:")
		var add []string
		for _, p := range ignored {
			fmt.Println("  " + p)
			if strings.HasPrefix(p, ".sigdek/") {
				p = ".sigdek"
			}
			if len(add) == 0 || add[len(add)-1] != p {
				add = append(add, p)
			}
		}
		fmt.Printf("Add them anyway with `git add -f %s`, or change the rule that ignores them\n(`git check-ignore -v %s` names it).\n", strings.Join(add, " "), ignored[0])
	}
	if note := initcmd.WindowsNote(runtime.GOOS, toolList, *ps); note != "" {
		fmt.Println("\n" + note)
	}
	if version == "dev" {
		fmt.Println("\nThis is a development build, so the launcher can't download it. Set SIGDEK_BIN to this binary to test.")
	} else if lock, err := initcmd.ReadLock(info.Root); err == nil && len(lock.Sums) < len(initcmd.Targets) {
		fmt.Printf("\nWarning: recorder.lock pins SigDek %s for %s only, because of --offline. Teammates on any other\n"+
			"platform won't be recorded. Run `sigdek init` again with network access to pin every platform.\n",
			version, strings.ReplaceAll(initcmd.Platform(), "_", "/"))
	}
	fmt.Println("\nNext: run `sigdek login` to connect this machine to your team. Until then, records stay on this machine.")
	return nil
}

func runLogin(home string, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	noBrowser := fs.Bool("no-browser", false, "print the link instead of opening it")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	reg, err := login.Login(login.Options{Home: home, Version: version, Out: os.Stdout, OpenBrowser: !*noBrowser})
	if err != nil {
		return err
	}
	org := reg.OrgName
	if org == "" {
		org = reg.OrgID
	}
	fmt.Printf("\nSigned in as %s to %s. This machine's records now upload in the background.\n", reg.User, org)
	if reg.Pending > 0 {
		fmt.Printf("%s made on this machine since its last upload to %s, including while it was signed out, upload now.\n", plural(reg.Pending, "record"), org)
	}
	if reg.Kept > 0 {
		fmt.Printf("%s made before this sign-in stay on this machine.\n", plural(reg.Kept, "record"))
	}
	// Start uploading at once rather than at the next agent event.
	if err := spawn.Flush(); err != nil {
		hook.LogError(home, fmt.Errorf("starting upload: %w", err))
	}
	return nil
}

func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func runFlush(home string, args []string) error {
	background := len(args) == 1 && args[0] == "--background"
	if !background {
		fs := flag.NewFlagSet("flush", flag.ContinueOnError)
		if err := parseFlags(fs, args); err != nil {
			return err
		}
	}
	var warnings []error
	logf := func(err error) {
		hook.LogError(home, err)
		warnings = append(warnings, err)
	}
	sum, err := upload.FlushWith(home, userAgent(), upload.Options{Background: background, Log: logf})
	if background {
		if err != nil && !errors.Is(err, upload.ErrBusy) && !errors.Is(err, upload.ErrNotSignedIn) && !errors.Is(err, upload.ErrPaused) {
			hook.LogError(home, fmt.Errorf("upload: %w", err))
		}
		return nil
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "sigdek: %v\n", w)
	}
	if errors.Is(err, upload.ErrBusy) {
		fmt.Println("An upload is already running.")
		return nil
	}
	if err != nil {
		if sum.Sent > 0 {
			fmt.Printf("Uploaded %d records before the error.\n", sum.Sent)
		}
		return err
	}
	if sum.Sent == 0 && sum.Skipped == 0 {
		fmt.Println("Nothing to upload.")
		return nil
	}
	fmt.Printf("Uploaded %d records in %d batch(es).", sum.Sent, sum.Batches)
	if sum.Duplicates > 0 {
		fmt.Printf(" %d were already on the server.", sum.Duplicates)
	}
	if sum.Invalid > 0 {
		fmt.Printf(" %d were refused as malformed; they appear as breaks in your report.", sum.Invalid)
	}
	if sum.Skipped > 0 {
		fmt.Printf(" %d were too large for the server; they stay on this machine and appear as breaks.", sum.Skipped)
	}
	if sum.ChainBreaks > 0 {
		fmt.Printf(" The server noted %d break(s) in this machine's chain; they appear in your report.", sum.ChainBreaks)
	}
	fmt.Println()
	return nil
}

func runVerify(home string) error {
	n, breaks, err := store.New(home).Verify()
	if err != nil {
		return err
	}
	if len(breaks) == 0 {
		fmt.Printf("All %d records on this machine are intact.\n", n)
		return nil
	}
	fmt.Printf("%d records checked, %d problem(s):\n", n, len(breaks))
	for _, b := range breaks {
		if b.Seq > 0 {
			fmt.Printf("  %s line %d (record %d): %s\n", b.File, b.Line, b.Seq, b.Reason)
		} else {
			fmt.Printf("  %s line %d: %s\n", b.File, b.Line, b.Reason)
		}
	}
	os.Exit(1)
	return nil
}

func runLog(home string, args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 20, "how many records")
	asJSON := fs.Bool("json", false, "print raw records")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *n < 1 {
		return fmt.Errorf("log: -n must be at least 1")
	}
	var lines [][]byte
	err := store.New(home).ReadFrom(store.Cursor{}, func(l store.Line) bool {
		lines = append(lines, l.Raw)
		if len(lines) > *n {
			lines = lines[1:]
		}
		return true
	})
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Println("No records yet. Run `sigdek init` in a repository, then use Claude Code or Cursor there.")
		return nil
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, raw := range lines {
		if *asJSON {
			w.Write(raw)
			w.WriteByte('\n')
			continue
		}
		var r event.Record
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, r.TS)
		fmt.Fprintf(w, "%5d  %s  %-11s  %-14s  %s\n", r.Seq, ts.Local().Format("Jan 02 15:04:05"), r.Tool, r.Event, summary(&r))
	}
	return nil
}

func summary(r *event.Record) string {
	var b strings.Builder
	switch {
	case r.Action != nil:
		b.WriteString(r.Outcome)
		b.WriteString("  ")
		b.WriteString(r.Action.Name)
		switch {
		case r.Action.Command != "":
			b.WriteString(": " + oneLine(r.Action.Command, 80))
		case r.Action.Path != "":
			b.WriteString(": " + r.Action.Path)
		case r.Action.URL != "":
			b.WriteString(": " + oneLine(r.Action.URL, 80))
		}
	case r.Prompt != nil:
		fmt.Fprintf(&b, "%d characters", r.Prompt.Length)
	case r.Subagent != nil && r.Subagent.Type != "":
		b.WriteString(r.Subagent.Type)
	case r.Config != nil && r.Config.Path != nil:
		b.WriteString(*r.Config.Path)
	}
	if r.Rule != "" {
		b.WriteString("  [rule: " + r.Rule + "]")
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func runStatus(home string) error {
	now := time.Now()
	fmt.Printf("Recorder    sigdek %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	// Read only: status never creates the device or any other file.
	dev, exists, err := config.ReadDevice(home)
	switch {
	case err != nil:
		fmt.Printf("Device      unreadable: %v\n", err)
	case !exists:
		fmt.Println("Device      not set up yet (it is created the first time an agent runs here)")
	default:
		fmt.Printf("Device      %s\n", dev.ID)
	}
	st := store.New(home)
	head, herr := st.Head()
	cur := st.LoadCursor()
	set, rerr := rules.LoadChecked(config.RulesPath(home))
	health := upload.LoadHealth(home)

	if dev.SignedIn() {
		org := dev.OrgName
		if org == "" {
			org = dev.Org
		}
		if info, err := os.Stat(config.TokenRejected(home)); err == nil {
			fmt.Printf("Signed in   %s, %s, but the server rejected this machine's token (%s): run `sigdek login` again\n", dev.User, org, when(info.ModTime(), now))
		} else {
			fmt.Printf("Signed in   %s, %s\n", dev.User, org)
		}
		fmt.Printf("Uploads to  %s\n", dev.Ingest())
		switch paused, _ := upload.Paused(home, now); {
		case health.LastError != "" && paused && now.Before(health.RetryAfter):
			fmt.Printf("Uploads     waiting until %s after: %s\n", health.RetryAfter.Local().Format("Jan 02 15:04"), oneLine(health.LastError, 120))
		case health.LastError != "":
			fmt.Printf("Uploads     the last try (%s) failed: %s\n", when(health.LastTry, now), oneLine(health.LastError, 120))
		case !health.LastOK.IsZero():
			fmt.Printf("Uploads     working; the last one finished %s\n", when(health.LastOK, now))
		}
	} else {
		fmt.Println("Signed in   no (records stay on this machine; run `sigdek login`)")
	}
	if herr != nil {
		fmt.Printf("Records     unreadable: %v\n", herr)
	} else {
		unsent := head.Seq - cur.Seq
		if !dev.SignedIn() {
			unsent = 0
		}
		fmt.Printf("Records     %d on this machine, %d waiting to upload\n", head.Seq, max(unsent, 0))
		if health.Refused > 0 || health.Skipped > 0 {
			fmt.Printf("            The server refused %d as malformed and %d as too large; they show as breaks in your report.\n", health.Refused, health.Skipped)
		}
	}
	problems := set.Problems()
	broken := map[string]bool{}
	for _, p := range problems {
		broken[p.Rule] = true
	}
	deny, flag := 0, 0
	for _, r := range set.Rules {
		switch {
		case broken[r.ID]:
		case r.Effect == rules.Deny:
			deny++
		default:
			flag++
		}
	}
	source := "your team's rules, version " + fmt.Sprint(set.Version)
	if set.Version == 0 {
		source = "built-in defaults"
	}
	fmt.Printf("Rules       %d flag, %d deny (%s)\n", flag, deny, source)
	if rerr != nil {
		fmt.Printf("            %v\n", rerr)
	}
	for _, p := range problems {
		fmt.Printf("            Not working: %s. Fix it in the dashboard.\n", oneLine(p.Error(), 140))
	}

	cwd, _ := os.Getwd()
	if root := gitinfo.Find(cwd).Root; isRepo(root) {
		claudeFile, cursorFile := filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".cursor", "hooks.json")
		fmt.Printf("This repo   Claude Code hooks: %s; Cursor hooks: %s\n", yesNo(fileHas(claudeFile, "hook claude-code")), yesNo(fileHas(cursorFile, "hook cursor")))
		// Older hook commands passed on sh's status 2 ("block") when the launcher was missing, or
		// left Cursor without an answer when it printed nothing; `sigdek init` writes current ones.
		if len(initcmd.Outdated(root)) > 0 {
			fmt.Println("            The hook commands were written by an older sigdek init. Run `sigdek init` again and commit the\n" +
				"            change, so a missing or broken launcher (an old branch, a deleted .sigdek) can't block the agent.")
		}
		if lock, err := initcmd.ReadLock(root); err == nil && lock.Version != "" && lock.Sums[initcmd.Platform()] == "" {
			fmt.Printf("            .sigdek/recorder.lock has no build of SigDek %s for %s, so nothing is recorded on this\n"+
				"            machine in this repo. Run `sigdek init` with network access, then commit .sigdek/recorder.lock.\n",
				lock.Version, strings.ReplaceAll(initcmd.Platform(), "_", "/"))
		}
	}
	user := claudeUserDir()
	if user != "" && fileHas(filepath.Join(user, "settings.json"), "hook claude-code") {
		fmt.Println("User level  Claude Code hooks found in " + tildePath(filepath.Join(user, "settings.json")))
	}
	if on, where := pluginState(user, gitinfo.Find(cwd).Root); where != "" {
		if on {
			fmt.Println("Plugin      SigDek's Claude Code plugin is on (" + where + ")")
		} else {
			fmt.Println("Plugin      SigDek's Claude Code plugin is turned off here (" + where + ")")
		}
	}
	if at, msg := lastError(config.ErrorLog(home)); msg != "" && !(strings.HasPrefix(msg, "upload: ") && at.Before(health.ErrorsOverAt())) {
		// An upload error from before the last good upload, or before the last sign-in, is over;
		// anything else stays, such as a warning from that upload.
		fmt.Printf("Last error  %s: %s\n", when(at, now), oneLine(msg, 160))
	} else {
		fmt.Println("Errors      none")
	}
	return nil
}

func isRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// when says when t was, the way a person reads it.
func when(t time.Time, now time.Time) string {
	switch d := now.Sub(t); {
	case t.IsZero():
		return "at an unknown time"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case t.Local().YearDay() == now.Local().YearDay() && t.Year() == now.Year():
		return "today at " + t.Local().Format("15:04")
	}
	return t.Local().Format("Jan 02 15:04")
}

// lastError reads the newest entry of the error log.
func lastError(path string) (time.Time, string) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		f.Seek(st.Size()-64<<10, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var e struct{ At, Error string }
	if json.Unmarshal([]byte(last), &e) != nil || e.Error == "" {
		return time.Time{}, last
	}
	at, _ := time.Parse(time.RFC3339, e.At)
	return at, e.Error
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// claudeUserDir is where Claude Code keeps its user settings: CLAUDE_CONFIG_DIR, or ~/.claude.
func claudeUserDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".claude")
	}
	return ""
}

// tildePath writes a path under the home folder as ~/…, the way people read it.
func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if rel, err := filepath.Rel(h, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return p
}

// pluginState says whether Claude Code has SigDek's plugin on here, as Claude Code decides it:
// the repository's .claude/settings.local.json, then its .claude/settings.json, then the user's
// settings, the first that sets "sigdek@<any marketplace>" in enabledPlugins. where names that
// file, and is empty when none does (the plugin isn't installed, or isn't SigDek's concern here).
func pluginState(userDir, repoRoot string) (on bool, where string) {
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
		if set, enabled := pluginSetting(f[0]); set {
			return enabled, f[1]
		}
	}
	return false, ""
}

// pluginSetting reads one Claude Code settings file's enabledPlugins entry for SigDek's plugin,
// from any marketplace ("sigdek@sigdek" from SigDek's own). An entry that turns it on wins over
// one that turns it off, in the rare file that lists it from two marketplaces.
func pluginSetting(path string) (set, enabled bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var s struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false, false
	}
	for k, v := range s.EnabledPlugins {
		if !strings.HasPrefix(k, "sigdek@") {
			continue
		}
		var on bool
		if json.Unmarshal(v, &on) != nil {
			continue
		}
		set = true
		enabled = enabled || on
	}
	return set, enabled
}

func fileHas(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(needle))
}
