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
  sigdek init [--tools claude-code,cursor] [--windows | --powershell] [--offline] [--remove]
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
		fmt.Fprintf(os.Stderr, "sigdek: %s\n", named(err.Error()))
		os.Exit(1)
	}
}

// command is how the person runs SigDek: "npx sigdek" when npm's launcher says npx started it
// (npm/sigdek/bin/sigdek.js sets SIGDEK_VIA), otherwise "sigdek". Next steps are printed the same
// way, so they work when copied.
func command() string {
	if os.Getenv("SIGDEK_VIA") == "npx" {
		return "npx sigdek"
	}
	return "sigdek"
}

// named writes the `sigdek …` commands in a message the way the person runs SigDek.
func named(s string) string {
	if c := command(); c != "sigdek" {
		return strings.ReplaceAll(s, "`sigdek ", "`"+c+" ")
	}
	return s
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
	windows := fs.Bool("windows", false, "write Cursor hooks that run under sh and under PowerShell, so Cursor records on Windows too\n(the default when init runs on Windows)")
	ps := fs.Bool("powershell", false, "write PowerShell-only Cursor hooks; they need a command named \"powershell\" (Windows PowerShell),\nso Cursor on macOS and Linux records nothing with them")
	offline := fs.Bool("offline", false, "pin only this machine's platform instead of fetching release checksums")
	remove := fs.Bool("remove", false, "remove SigDek's hooks from this repository")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *windows && *ps {
		return errors.New("init: --windows and --powershell write different Cursor hooks; choose one")
	}
	cwd, _ := os.Getwd()
	info := gitinfo.Find(cwd)
	if _, err := os.Stat(filepath.Join(info.Root, ".git")); err != nil {
		return errors.New("run `sigdek init` inside a git repository")
	}
	flagForm := ""
	switch {
	case *windows:
		flagForm = initcmd.CursorBoth
	case *ps:
		flagForm = initcmd.CursorPowerShell
	}
	form := initcmd.ChooseCursorForm(info.Root, runtime.GOOS, flagForm)
	changed, err := initcmd.Run(initcmd.Options{
		Root: info.Root, Version: version, Tools: []string{*tools},
		CursorForm: form, Offline: *offline, Remove: *remove, Out: os.Stdout,
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
	for _, t := range toolList {
		if note := initcmd.FormNote(form, command()); t == "cursor" && note != "" {
			fmt.Println("\n" + note)
		}
	}
	if version == "dev" {
		fmt.Println("\nThis is a development build, so the launcher can't download it. Set SIGDEK_BIN to this binary to test.")
	} else if lock, err := initcmd.ReadLock(info.Root); err == nil && len(lock.Sums) < len(initcmd.Targets) {
		fmt.Printf("\nWarning: recorder.lock pins SigDek %s for %s only, because of --offline. Teammates on any other\n"+
			"platform won't be recorded. Run `"+command()+" init` again with network access to pin every platform.\n",
			version, strings.ReplaceAll(initcmd.Platform(), "_", "/"))
	}
	fmt.Println("\nNext: run `" + command() + " login` to connect this machine to your team. Until then, records stay on this machine.")
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
		fmt.Println(named("No records yet. Run `sigdek init` in a repository, then use Claude Code or Cursor there."))
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

func fileHas(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(needle))
}
