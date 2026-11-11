# SigDek recorder

SigDek records what AI coding agents do on your team's machines (who ran which agent, which tool, the command or file, and whether it ran or was blocked) and turns that record into evidence an auditor or a customer can read.

This repository is the open-source recorder that runs on developer machines, plus the Claude Code and Cursor plugins that install it. The hosted team log and reports live at [sigdek.com](https://sigdek.com).

SigDek is a product of Tekton Software.

## Quick start

Using Cursor? Pick [I use Cursor](#i-use-cursor). Otherwise pick [Our team's project](#our-teams-project) if you can change the project and want everyone who works in it recorded, or [Just me, in Claude Code](#just-me-in-claude-code) to record only yourself.

> This README describes the released SigDek 0.1.0. Blocks that start **From SigDek 0.1.1** describe what changes with the next release, which isn't out yet.

Every command says where it goes:

- **In Claude Code's chat:** type it where you talk to Claude Code, then press Return (Enter on Windows).
- **In a terminal:** on macOS, press Command and Space, type Terminal, and press Return. On Windows, open Start, type PowerShell, and choose Windows PowerShell. On Linux, open Terminal from your apps. In VS Code or Cursor, open your project's folder, then choose Terminal, then New Terminal: it opens in the project's folder already. Elsewhere, go to the folder first: type `cd` and a space, drag the folder onto the window, and press Return.

**Do you have Node.js?** The `npx` commands need it. In a terminal, run `node -v`. You should see a version such as `v24.6.0`; v18 or later is fine. If it says `command not found: node` (macOS), `node: command not found` (Linux) or `The term 'node' is not recognized` (Windows), install Node.js from [nodejs.org](https://nodejs.org/en/download): under "Or get a prebuilt Node.js", choose the macOS Installer (.pkg) or the Windows Installer (.msi), not the commands above it, then open a new terminal. Or [install the `sigdek` command](#other-ways-to-install-the-command) (the install script and Scoop need no administrator), and type `sigdek` wherever these steps say `npx sigdek`.

The first time you run `npx sigdek`, npx asks before it downloads SigDek:

```
Need to install the following packages:
sigdek@0.1.0
Ok to proceed? (y)
```

Press Return (Enter on Windows) to say yes. npm may also suggest updating npm; you can ignore that. On Windows, if PowerShell says running scripts is disabled, type `npx.cmd` instead of `npx`.

### Just me, in Claude Code

The Claude Code plugin records you in every project you open in Claude Code on this machine. It's for Claude Code only.

1. **Add the recorder.** This step depends on where you use Claude Code.

   **In a terminal.** Start Claude Code: type `claude` and press Return. Then, in Claude Code's chat:

   ```
   /plugin marketplace add tektonsoftware/sigdek
   ```

   You should see `Successfully added marketplace: sigdek`. Then, in Claude Code's chat:

   ```
   /plugin install sigdek@sigdek
   ```

   Claude Code shows what the plugin adds. Choose **Install for you (user scope)**. The summary starts with `✓ Installed sigdek.` and ends with `Plugin is now active.`, or with `Run /reload-plugins to apply.`, and in that case Claude Code reloads for you. (These two outputs are as Claude Code's plugin docs give them, not captured from a chat here. `claude plugin marketplace add tektonsoftware/sigdek`, the same step typed in a terminal, printed `✔ Successfully added marketplace: sigdek (declared in user settings)`.)

   **In VS Code.** In the Claude Code panel, type `/plugins`. Manage plugins opens. On its Marketplaces tab, enter `tektonsoftware/sigdek`. Then on the Plugins tab, search for **sigdek**, choose **Install**, then **Install for you**.

   **In the desktop app.** Claude Code's docs describe the desktop app's plugin browser (the **+** button, then **Add plugins**) but not adding a marketplace from a GitHub repository there. So do this step once in Claude Code in a terminal or in VS Code. After that the plugin works in the desktop app too: on one computer, Claude Code in a terminal, in VS Code and in the desktop app share the plugins you install for yourself. Cloud sessions, including claude.ai/code, don't load plugins installed on your machine.

2. **Connect your machine.** In a terminal, run `npx sigdek login`, as in [Our team's project](#our-teams-project), step 2.

   > **From SigDek 0.1.1** (with the plugin update that comes with it): type `/sigdek:connect` in Claude Code's chat instead. Claude starts SigDek's sign-in and shows you a link and a code, and your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If the page shows the same code, choose **Connect machine**. Claude then passes on what SigDek prints, such as `Signed in as ana@larkspur.example to Larkspur Labs. This machine's records now upload in the background.` Claude's own words vary; SigDek's line doesn't. If Claude Code doesn't know `/sigdek:connect`, or says to use a terminal, use `npx sigdek login`.

3. **Watch Activity.** Use Claude Code as usual. When the agent finishes its turn, what it did uploads and shows on the Activity page of your team's dashboard at [app.sigdek.com](https://app.sigdek.com). To check this machine, see [Did it work?](#did-it-work)

On Windows, SigDek records Claude Code only where Git for Windows is installed ([git-scm.com](https://git-scm.com/downloads/win)). To check, run `git --version` in PowerShell; if it isn't recognized, install Git for Windows.

### Our team's project

Set it up once in the project, and everyone who works in it is recorded in Claude Code and Cursor, with nothing to install but one sign-in each. This changes the project for everyone who works in it: check with whoever looks after it first.

1. **Add the recorder.** In a terminal, in your project's folder (the folder you open in VS Code or Cursor for this project; it has a `.git` folder in it, which is hidden):

   ```
   npx sigdek init
   ```

   You should see:

   ```
   SigDek is set up in this repository. Commit these files:
     .claude/settings.json
     .cursor/hooks.json
     .sigdek/launch.sh
     .sigdek/launch.ps1
     .sigdek/recorder.lock
     .sigdek/README.md
     .sigdek/.gitattributes

   Next: run `sigdek login` to connect this machine to your team. Until then, records stay on this machine.
   ```

   It says `sigdek login`: in the next step you'll type `npx sigdek login`.

   > **From SigDek 0.1.1:** run through npx, it prints its next steps as `npx sigdek …`. On macOS and Linux, one more line names `--windows` (see the box under step 3).

   Then commit the files it added, in the same terminal:

   ```
   git add .claude/settings.json .cursor/hooks.json .sigdek
   git commit -m "Record agent activity with SigDek"
   ```

   You should see a line like ` 7 files changed, 567 insertions(+)` (the numbers can differ). If SigDek warned that git ignores a file, run the `git add -f` command it printed, then commit again. Then share the change with your team. Most teams share changes with a pull request; ask if you're not sure.

2. **Connect your machine.** In a terminal, in any folder:

   ```
   npx sigdek login
   ```

   You should see, with your own code:

   ```
   Your code: ABCD-EFGH
   Approve it at: https://app.sigdek.com/device?user_code=ABCDEFGH

   Waiting for approval…
   ```

   Your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If it doesn't open, open the link yourself. If the page shows the same code, choose **Connect machine**. The terminal then says:

   ```
   Signed in as ana@larkspur.example to Larkspur Labs. This machine's records now upload in the background.
   ```

   If the machine recorded anything before you connected it, one more line says what happens to those records. Nothing to do.

3. **Invite your team.** In the dashboard, an owner or admin opens People and chooses **Invite people**. Each teammate accepts, gets the change from step 1, and runs `npx sigdek login` once on their own machine. From then on, Claude Code and Cursor record for them whenever they start in the project's top folder.

Then **watch Activity**: when an agent finishes its turn, what it did uploads and shows on the dashboard's Activity page.

**Cursor on Windows.** Cursor on Windows runs hooks through PowerShell, which can't run the hooks `npx sigdek init` writes, so Cursor records nothing there (it isn't blocked). If everyone who uses Cursor in the project is on Windows, run `npx sigdek init --powershell` instead and commit the change; those hooks need PowerShell on every machine that uses Cursor in the project, so they record nothing for Cursor on macOS and Linux. Where some people use Cursor on macOS or Linux, keep the default: Cursor on Windows isn't recorded in that project yet. Claude Code isn't affected either way.

> **From SigDek 0.1.1:** `npx sigdek init --windows` writes Cursor hooks that run under sh and under PowerShell, so Cursor records on Windows, macOS and Linux; `npx sigdek init` run on Windows writes them too. On macOS and Linux, plain `npx sigdek init` still writes the sh-only hooks, and says so. A project set up without them can be fixed later by running `npx sigdek init --windows` and committing the change; on Windows, `npx sigdek status` says when a project needs it. See [Windows](#windows).

### I use Cursor

SigDek's Cursor plugin isn't on Cursor's Marketplace yet. Until it is, Cursor users set up the project itself: follow [Our team's project](#our-teams-project), in Cursor. In Cursor, open your project's folder (File, then Open Folder), then choose Terminal, then New Terminal; it opens in the project's folder. Open the project's folder, not a folder inside it: Cursor reads SigDek's setup from the folder it opened. This records Claude Code in the project too, for everyone who works in it.

On Windows, Cursor isn't recorded yet unless everyone using Cursor in this project is on Windows; ask whoever looks after the project. See **Cursor on Windows** under [Our team's project](#our-teams-project).

### Did it work?

In a terminal, in your project's folder:

```
npx sigdek status
```

Among its lines, you should see:

```
Signed in   ana@larkspur.example, Larkspur Labs
Uploads     working; the last one finished just now
```

No Uploads line yet? Nothing has uploaded: use the agent once and wait for its reply. In a project set up with `npx sigdek init`, status also says `This repo   Claude Code hooks: yes; Cursor hooks: yes`.

> **From SigDek 0.1.1:** status answers as a checklist, without going online, and gives the next step for anything missing. When everything is set up, it looks like this:
>
> ```
> sigdek 0.1.1 (darwin/arm64)
>
> [x] Claude Code records in this project, through its hooks in .claude/settings.json
> [x] Cursor records in this project, through its hooks in .cursor/hooks.json
> [x] SigDek 0.1.1, the recorder this project pins, is downloaded and matches its checksum
> [x] Connected to Larkspur Labs as ana@larkspur.example
> [x] Uploads work: the last one finished just now, and nothing is waiting
> ```
>
> `[ ]` marks something missing, with the next step under it. `[-]` marks something you may not need, such as Cursor for someone who uses only Claude Code.

### If something goes wrong

| What you see | What to do |
|---|---|
| `command not found: npx` (macOS), `npx: command not found` (Linux) or `The term 'npx' is not recognized` (Windows) | Node.js isn't installed, or the terminal was opened before it was. Install it from [nodejs.org](https://nodejs.org/en/download), then open a new terminal. Or [install the `sigdek` command](#other-ways-to-install-the-command) and drop the `npx`. |
| `command not found: sigdek` (macOS), `sigdek: command not found` (Linux) or `The term 'sigdek' is not recognized` (Windows) | The `sigdek` command isn't installed, so type `npx sigdek …` instead, for example `npx sigdek login`. Or [install the command](#other-ways-to-install-the-command). |
| `running scripts is disabled on this system` (PowerShell) | Windows is blocking `npx`'s PowerShell script. Type `npx.cmd` instead of `npx`, for example `npx.cmd sigdek login`. Or allow such scripts for your account, once, with `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`; if it asks whether to change the execution policy, answer `Y` and press Enter. |
| `command not found: claude` (macOS), `claude: command not found` (Linux) or `The term 'claude' is not recognized` (Windows) | Claude Code isn't installed for the terminal. If you use it in VS Code or the desktop app, follow the steps for those in [Just me, in Claude Code](#just-me-in-claude-code) instead. Or install it for the terminal with [Claude Code's setup guide](https://code.claude.com/docs/en/setup). |
| `/plugin isn't available in this environment` | You're in Claude Code's desktop app or its VS Code panel (or a cloud session), where the `/plugin` commands don't run. Use the steps for VS Code or the desktop app in [Just me, in Claude Code](#just-me-in-claude-code). |
| `Unknown command: /plugin` in Claude Code | Your Claude Code is too old for plugins. In a terminal, run `claude update`, then start Claude Code again. In the desktop app or VS Code, update the app or the extension. |
| `no such file or directory: /plugin` (macOS), `/plugin: No such file or directory` (Linux) or `The term '/plugin' is not recognized` (Windows) | The `/plugin` commands go in Claude Code's chat, not in a terminal. Type `claude` and press Return to start Claude Code, then type them there. |
| ``run `sigdek init` inside a git repository`` | The terminal isn't in your project's folder. Open it from VS Code or Cursor with the project open, or go to the folder with `cd`. The project setup needs a project kept in git. |
| `The following paths are ignored by one of your .gitignore files` | The project tells git to leave out some of SigDek's files, so teammates wouldn't get them. `sigdek init` warned about this and printed a `git add -f` command: check with whoever looks after the project, then run that command and commit. |
| The browser didn't open when connecting your machine | Copy the link after `Approve it at:` into your browser, sign in, check the code and choose **Connect machine**. The terminal finishes on its own. |
| Nothing shows in Activity | Records upload when the agent finishes its turn, so wait until its reply is done. Then run `npx sigdek status`: its `Signed in` line should show your email and team. If it says `no`, connect your machine again. With the project setup, start Claude Code or Cursor in the project's folder. **From SigDek 0.1.1:** status is a checklist, and each line marked `[ ]` says what's missing and the next step. |
| Cursor on Windows records nothing | See **Cursor on Windows** under [Our team's project](#our-teams-project). **From SigDek 0.1.1:** run `npx sigdek status` in the project's folder; if it says Cursor on Windows can't run this project's hooks, run `npx sigdek init --windows` there and commit the change. |

## Other ways to install the command

`npx sigdek …` works anywhere Node.js 18 or later is installed. To install the `sigdek` command instead, and type `sigdek` where these steps say `npx sigdek`:

| Where | Install |
|---|---|
| Any OS, with Node.js | `npm install -g sigdek` |
| macOS and Linux, without Node.js | `curl -fsSL https://sigdek.com/install.sh \| sh`, or without curl, `wget -qO- https://sigdek.com/install.sh \| sh` (wget's certificate checking depends on how it was built; prefer curl) |
| Windows, Scoop | `scoop bucket add sigdek https://github.com/tektonsoftware/sigdek` then `scoop install sigdek` |
| Windows, winget | `winget install TektonSoftware.SigDek`, available a few days after each release, once Microsoft merges its pull request |
| Go 1.24 or later | `go install github.com/tektonsoftware/sigdek/cmd/sigdek@latest` |
| Anything else | Download the binary for your platform from [GitHub releases](https://github.com/tektonsoftware/sigdek/releases) and check it against `checksums.txt` |

The install script ([`install.sh`](install.sh)) puts `sigdek` in `~/.local/bin`, without sudo, and tells you the line to add to your shell profile if that folder isn't on your `PATH`. Run it again to update. It checks the binary's SHA-256 against the release's `checksums.txt`. Releases are immutable: once a release is published, its files and tag can't be changed, and GitHub attaches an attestation to each. The checksum still comes from the same release as the binary, so it catches a damaged or wrong download, not a release that was bad when it was published. The script itself reaches you through sigdek.com's redirect to the release. To read the script before you run it:

```sh
curl -fsSLo install.sh https://sigdek.com/install.sh
less install.sh
sh install.sh
```

Settings go after the pipe, on the `sh` that runs the script: `curl -fsSL https://sigdek.com/install.sh | SIGDEK_VERSION=0.1.0 sh` installs a given version, and `SIGDEK_INSTALL_DIR=<folder>` installs into another folder.

Every channel except `go install`, which compiles from source, installs the same binaries, built once per release. The npm package is a small launcher that runs the one for your platform from an `@tektonsoftware/sigdek-*` package. The Scoop manifest (`bucket/`) lives in this repository and is updated by the release workflow, which also opens the winget pull request. The workflow writes a Homebrew formula (`Formula/`) too, but Homebrew isn't an install option yet: without prebuilt bottles, Homebrew on macOS refuses it when the Command Line Tools are out of date.

## What it records, and what it never does

SigDek writes one record (see [the schema](schema/agent-event.v1.schema.json)) for each action an agent takes, once it has run, failed or been refused, and for each session start and end, prompt, subagent and settings change. A record holds the person, device, agent tool, session and repository; the agent's model and version where the agent reports them (Cursor reports both; Claude Code reports only the model); and, for an action, its kind, the tool's name, the command or file path, the outcome, how long it took, and which of your team's rules matched. Some hook events write nothing on purpose: the check before a tool runs (unless a rule blocks it, the action is recorded after it runs) and the end of each turn. So an action someone declines at the agent's permission prompt leaves no record: Claude Code runs no hook after a decline, and Cursor documents none (if Cursor does report a decline, as a `permission_denied` failure, SigDek records the action as refused). The exception is an action that changes SigDek's own setup, such as deleting `.sigdek`, `sigdek logout` or editing the hook files: it may leave nothing behind to record it afterwards, so it is recorded as Attempted when the agent asks to run it (it may still be declined, which leaves no later trace) and, on a signed-in machine, uploaded before the agent goes ahead, waiting at most about a second. If a hook still runs once it is done, a second record says whether it ran, failed or was refused. Events that arrive but can't be recorded are listed in `~/.sigdek/errors.log`.

- **Never recorded:** prompt text (only a hash and its length), file contents, command output. The full tool input is kept only as a SHA-256 hash of the input exactly as the agent sent it. Like the prompt hash, it can confirm a guess at the whole input, a file edit's contents included. When a secret was stripped from the action's command, path or URL, the hash is instead of the stored, redacted action (the RFC 8785 canonical JSON of its `kind`, `name`, `command`, `path` and `url`), so it can't confirm a guessed secret; a nonzero `redactions` count in the record says it holds this form. [SECURITY.md](SECURITY.md) has the details.
- **Secrets are stripped** from commands, paths and URLs before anything is written: cloud keys, tokens, passwords in URLs and flags, cookies, private keys, JWTs. It is best effort, and [the tests](internal/redact/redact_test.go) list what it catches and what it leaves alone.
- **Bounded:** every field is cut to the schema's limit (a command to 4 KB), and a cut field ends with a note of its full length; a blocked command too long to keep also keeps the part the rule matched. Rules are checked before anything is cut or redacted. Block rules read the whole command, both as the agent sent it and as the shell reads it (line continuations joined, quotes and backslashes removed, blanks squeezed), so neither padding nor quoting hides a command from them; Flag rules read up to 16 KB of each. A command put together at run time or by expansion, such as `X=rm; $X -rf`, `{rm,-rf,/x}` or `/bin/r? -rf`, can't be seen through; [SECURITY.md](SECURITY.md) lists what can and can't.
- **Paths are repository-relative**, and your home directory shows as `~`.
- **Tamper-evident once SigDek has received them:** each record carries the hash of the one before it, per device, and the hosted service checks the chain on every upload. The machine's local log can be rewritten by someone with access to it; the server catches any later change, gap or fork. `sigdek verify` recomputes the local chain and lists every line that doesn't hold.
- **Never blocks your work by accident.** Any error inside the recorder or the launcher lets the action go ahead and is written to `~/.sigdek/errors.log`, and the hook commands can't fail even when the launcher is missing. The only time SigDek stops an action is a Block rule your team wrote. One exception isn't SigDek's: Cursor's CLI on Windows, started from Git Bash, reports every hook as "Hook blocked" (see [Windows](#windows)).
- **Can be switched off by anyone using the machine:** removing the hooks, uninstalling the plugin or setting `SIGDEK_BIN` stops recording. SigDek doesn't hide that: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.
- **Fast:** a few milliseconds per event; CI fails the build above 50 ms. Two waits are longer. The first event on each machine, and the first after a new recorder version is pinned, waits up to 8 seconds while the launcher downloads the pinned recorder. On a slower link, `launch.sh` finishes the download in the background, while `launch.ps1` (Windows PowerShell) picks it up where it stopped in each following event, each waiting up to 7 seconds; recording starts once the download is complete. Until then nothing is recorded, and after a failed download the launcher tries again after 1, 2, 4… up to 60 minutes. An action that changes SigDek's own setup waits up to about a second for its record to upload (see above).
- **No third-party code:** the recorder uses only Go's standard library.

## Commands

| Command | What it does |
|---|---|
| `sigdek init` | Add SigDek's hooks to the current repository. `--remove` takes them out, and `.sigdek/` once no hooks use it |
| `sigdek login` / `logout` | Connect or disconnect this machine; sign-in happens in the browser. Logout asks the server that signed it in to revoke the machine's token and says if it couldn't. Agents are still recorded after logout, on the machine only; those records upload if it signs in to the same team again, and login says how many |
| `sigdek status` | What is set up and working, and what isn't. **From SigDek 0.1.1:** a checklist, with the next step for anything that isn't, that reads only local files |
| `sigdek log` | Recent records from this machine |
| `sigdek verify` | Check that no local record was changed or removed |
| `sigdek flush` | Upload now (normally automatic, in the background), without waiting out a back-off |

## Rules

Teams set rules in the dashboard. A **Flag** rule marks matching actions in the report; a **Block** rule (`deny` in the API) stops them before they run, and tells the agent and the developer why. Until a machine signs in, 12 built-in rules ([schema/default-rules.json](schema/default-rules.json)) flag common risky actions (force pushes, recursive deletes, secrets files, infrastructure changes, destructive SQL, piping a download into a shell, publishing packages, privileged commands, history rewrites, and changes to SigDek's own setup) and block nothing. Patterns are globs (`*` matches anything; in paths `**` crosses `/`) or, with `re:`, RE2 regular expressions. Command and URL patterns are found anywhere in the text, path patterns match the whole path or any part after a `/`, and tool and MCP globs match the whole name: `Write` matches Write but not TodoWrite, and `github/merge_*` matches the server github but not mygithub. An MCP glob without `/` names the tool. Cursor's general `preToolUse` and `postToolUse` hooks, which SigDek uses, don't name the MCP server, so for Cursor an MCP pattern like `github/merge_*` matches on the tool part. Cursor's MCP-specific hooks do name it, but SigDek doesn't use them yet.

## How it works

### Plugin and project hooks together

With the plugin installed and `sigdek init` in a repository, the plugin steps aside when the agent starts at the repository's root, so each action is recorded once. Claude Code reads `.claude/settings.json` only from the folder a session starts in, and Cursor reads `.cursor/hooks.json` from the workspace root, so in a session started in a subdirectory the repository's hooks don't run and the plugin records instead.

> **From SigDek 0.1.1** (with the plugin update): only hook events step aside. A command you run through the plugin, such as `/sigdek:connect`, runs in a repository set up with `sigdek init` too, wherever the plugin can run (on Windows, only with Git Bash).

### Cursor

Cursor also runs Claude Code's hooks by default (its Third-Party Imports setting loads `.claude/settings.json`, `.claude/settings.local.json` and `~/.claude/settings.json`), so in a repository set up with `sigdek init` each Cursor action reaches SigDek's Claude Code hook as well as its Cursor hook. SigDek's Claude Code hook stands aside when Cursor runs it, so each Cursor action is recorded once, by the Cursor hook; Cursor's actions are recorded only through SigDek's Cursor hooks (`sigdek init` or the Cursor plugin).

SigDek's Cursor plugin isn't listed on the Cursor Marketplace yet (Cursor reviews each plugin by hand). Until it is, set up each repository with `sigdek init`, or, on Cursor's Teams and Enterprise plans, add it to a team marketplace with **Import from Repo** and `https://github.com/tektonsoftware/sigdek`, which lists the plugin in `.cursor-plugin/marketplace.json`.

### Windows

On Windows, Claude Code runs hooks through Git Bash; without Git Bash it uses PowerShell, where SigDek records nothing but still doesn't block.

Cursor runs hooks through PowerShell on Windows, and Windows PowerShell 5.1 can't run the default commands (it has no `||`), so with the default `sigdek init` Cursor records nothing there, without blocking; `sigdek init` on Windows says so. The Cursor plugin's hooks use the same commands, so the plugin records nothing on Windows either. For Cursor on Windows, run `sigdek init --powershell`, whose hooks need PowerShell on every machine that uses Cursor: they need a command named `powershell`, so Cursor on macOS and Linux records nothing with them.

> **From SigDek 0.1.1:**
>
> - `sigdek init --windows` writes Cursor commands that sh and PowerShell each read as their own program: sh runs `.sigdek/launch.sh` (macOS and Linux), and PowerShell runs `.sigdek/launch.ps1` (Windows PowerShell 5.1 and PowerShell 7), so the one committed `.cursor/hooks.json` works for everyone. `.sigdek/README.md` shows both programs as each shell reads them. `sigdek init` writes them when it runs on Windows, and keeps them when it runs again anywhere. On macOS and Linux, plain `sigdek init` still writes the sh-only commands and says so.
> - CI runs each command inside the PowerShell wrapper Cursor is reported to use on Windows (Cursor's docs say only that it pipes the input into the command), and with the input on stdin instead. It hasn't been tried in the Cursor app on Windows.
> - The PowerShell commands run launch.ps1 with `-ExecutionPolicy RemoteSigned`, for that one process only (0.1.0 wrote `Bypass`, which still works). launch.ps1 isn't signed, and Windows' default policy on a PC refuses every script. RemoteSigned still refuses a launch.ps1 marked as downloaded from the internet, such as one unpacked from a downloaded ZIP file; a git clone isn't marked.
> - `sigdek status` on Windows says when a project's Cursor commands can't run there and names `--windows`, and says when it can't find Git Bash for Claude Code.

**Where Cursor on Windows records nothing**, whatever the form:

- **Cursor's CLI started from Git Bash.** When Git Bash is anywhere above `cursor-agent` in its process tree, Cursor hands its PowerShell wrapper to bash, every hook fails, and Cursor reports each tool call as "Hook blocked". That's Cursor's own bug, reported on its forum, and it hits any project's hooks. Start `cursor-agent` from PowerShell or Command Prompt instead.
- **Machines whose policies are managed.** An execution policy set by Group Policy (such as AllSigned) overrides the one the command sets, and AppLocker or WDAC can run PowerShell in Constrained Language Mode, which stops launch.ps1. Then the commands give Cursor its safe answer and record nothing, and `sigdek status` can't tell.

### Line endings, and a missing launcher

`.sigdek/.gitattributes` keeps the launchers' line endings right on Windows checkouts, and hooks run the launcher with `sh` (or PowerShell), so it needs no exec bit.

A hook command never stops the agent on its own: when the launcher is missing or broken (a branch made before SigDek was added, `.sigdek` deleted or ignored by git, merge-conflict markers in `launch.sh`, an empty file), the command does nothing but give Cursor the answer it expects, and that checkout records nothing until the launcher is back. `sigdek init` warns when git ignores any file it wrote, and in a repository set up by an older version, `sigdek status` asks you to run `sigdek init` again for these commands. **From SigDek 0.1.1,** `sigdek status` also says when `.sigdek/launch.sh` is missing.

### From hook to upload

```
agent tool ──hook event──▶ launcher (.sigdek/launch.sh, or the plugin's)
                              │ first use: download the pinned version, check SHA-256
                              ▼
                          sigdek hook <tool>
                              │ adapter → match rules → answer the agent → cut and redact → seal into the hash chain
                              ▼
                          ~/.sigdek/log/events-YYYY-MM.jsonl
                              │ at the end of each turn and each session, and at most once a minute during a turn, in a separate process
                              ▼
                          ingest.sigdek.com  (only after `sigdek login`)
```

The launcher runs only the recorder version pinned next to it: in `.sigdek/recorder.lock` for a repository set up with `sigdek init`, or in the plugin's own `scripts/recorder.lock`. It refuses a download whose SHA-256 doesn't match the pin. The one upload that happens inside a hook is the record of an action that changes SigDek's own setup (see above), since that action may delete the log as soon as the hook is done. Uploads back off when the service asks: after it rejects the machine's token they are tried once an hour until `sigdek login` succeeds, and after a rate limit or a server error they wait as long as the service's `Retry-After` says, or longer each time, up to an hour. A batch the service finds too large is split; a single record it still refuses is skipped, stays on the machine and shows as a break in the report. Each upload brings back the team's current rules; a rule the recorder can't use is written to `~/.sigdek/errors.log` when it arrives.

## Develop

Go 1.24 or later.

```sh
go test ./...                    # unit tests plus end-to-end tests of the compiled binary
go build -o sigdek ./cmd/sigdek
SIGDEK_BIN=$PWD/sigdek           # makes launchers use your build instead of downloading
```

`SIGDEK_BIN` works for anyone, which is also a way to switch the recorder off; see "Can be switched off" above.

Releases: push a tag like `v0.3.0`. The release workflow builds six targets, publishes them with `checksums.txt` and `install.sh`, publishes the npm packages with provenance (when the `NPM_TOKEN` secret is set), and, when the tag is the highest published version, makes it the latest release, pins both plugins, the Homebrew formula and the Scoop manifest to it, and submits it to winget (when the `WINGET_TOKEN` secret is set).

## License

Apache-2.0. Copyright 2026 Tekton Software LLC. Report security issues as described in [SECURITY.md](SECURITY.md).
