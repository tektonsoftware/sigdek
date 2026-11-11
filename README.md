# SigDek recorder

SigDek records what AI coding agents do on your team's machines (who ran which agent, which tool, the command or file, and whether it ran or was blocked) and turns that record into evidence an auditor or a customer can read.

This repository is the open-source recorder that runs on developer machines, plus the Claude Code and Cursor plugins that install it. The hosted team log and reports live at [sigdek.com](https://sigdek.com).

SigDek is a product of Tekton Software.

## Quick start

Pick the path that fits. Each one is three steps.

- **[Just me, in Claude Code](#just-me-in-claude-code):** one person, in every project, with SigDek's Claude Code plugin.
- **[Our team's project](#our-teams-project):** everyone who clones a repository, in Claude Code and in Cursor. Nothing to install for each teammate but one sign-in.
- **[I use Cursor](#i-use-cursor):** today, the same steps as a team's project, because SigDek's Cursor plugin isn't listed on Cursor's marketplace yet.

Every command says where it goes:

- **In Claude Code's chat:** type it where you talk to Claude Code, then press Enter.
- **In a terminal:** on macOS, press Command and Space, type Terminal, press Enter. On Windows, open Start, type PowerShell, press Enter. On Linux, open Terminal. In VS Code or Cursor, choose Terminal, then New Terminal: it opens in your project's folder already. Elsewhere, go to your project's folder first with `cd`, for example `cd ~/code/our-app`.

**Do you have Node.js?** The `npx` commands need it. In a terminal, run `node -v`. `v18` or anything later is fine. If you see `command not found` or `is not recognized`, install the LTS version from [nodejs.org](https://nodejs.org/), then open a new terminal; or [install `sigdek` without Node.js](#other-ways-to-install-the-command) and type `sigdek` where these steps say `npx sigdek`.

The first time you run `npx sigdek`, npx asks to download it, naming the current version:

```
Need to install the following packages:
sigdek@0.1.0
Ok to proceed? (y)
```

Press Enter to say yes.

### Just me, in Claude Code

1. **Add the recorder.** In Claude Code's chat:

   ```
   /plugin marketplace add tektonsoftware/sigdek
   ```

   You should see `Successfully added marketplace: sigdek`. Then, in Claude Code's chat:

   ```
   /plugin install sigdek@sigdek
   ```

   Claude Code shows what the plugin adds. Choose **Install for you (user scope)**. The summary ends with `Plugin is now active.`, or with `Run /reload-plugins to apply.`, and Claude Code then reloads for you.

2. **Connect your machine.** In Claude Code's chat:

   ```
   /sigdek:connect
   ```

   Claude shows you a link and a code, and your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If the page shows the same code, choose **Connect machine**. Claude then tells you what SigDek printed, such as:

   ```
   Signed in as ana@larkspur.example to Larkspur Labs. This machine's records now upload in the background.
   ```

   You can do the same in a terminal with `npx sigdek login`, as in [Our team's project](#our-teams-project).

3. **Watch Activity.** Use Claude Code as usual. When the agent finishes its turn, what it did uploads, and shows on the Activity page of your team's dashboard at [app.sigdek.com](https://app.sigdek.com). To check this machine, see [Did it work?](#did-it-work)

### Our team's project

1. **Add the recorder.** In a terminal, in your project's folder (the top folder, with the `.git` folder in it):

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

   Then commit the files and push them, in the same terminal:

   ```
   git add .claude .cursor .sigdek
   git commit -m "Record agent activity with SigDek"
   git push
   ```

2. **Connect your machine.** In a terminal:

   ```
   npx sigdek login
   ```

   You should see:

   ```
   Your code: ABCD-EFGH
   Approve it at: https://app.sigdek.com/device?user_code=ABCDEFGH

   Waiting for approval…
   ```

   Your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If it doesn't open, open the link yourself. If the page shows the same code, choose **Connect machine**. The terminal then says:

   ```
   Signed in as ana@larkspur.example to Larkspur Labs. This machine's records now upload in the background.
   ```

3. **Invite your team.** In the dashboard, an owner or admin opens People and chooses **Invite people**. Each teammate accepts, pulls the commit from step 1, and runs `npx sigdek login` once on their own machine. From then on, Claude Code and Cursor record for them whenever they start in the project's top folder, with nothing else to install.

Then **watch Activity**: when an agent finishes its turn, what it did uploads and shows on the dashboard's Activity page.

### I use Cursor

SigDek's Cursor plugin isn't listed on Cursor's marketplace yet, so Cursor records through a project's own hooks: follow [Our team's project](#our-teams-project). The commands go in Cursor's terminal (Terminal, then New Terminal), which opens in your project's folder. Open that top folder as Cursor's workspace, because Cursor reads `.cursor/hooks.json` only from there. The same hooks work on macOS, Linux and Windows.

### Did it work?

In a terminal, in your project's folder:

```
npx sigdek status
```

It answers as a checklist, without going online, and gives the next step for anything missing. When everything is set up, it looks like this:

```
sigdek 0.1.1 (darwin/arm64)

[x] Claude Code records in this project, through its hooks in .claude/settings.json
[x] Cursor records in this project, through its hooks in .cursor/hooks.json
[x] SigDek 0.1.1, the recorder this project pins, is downloaded and matches its checksum
[x] Connected to Larkspur Labs as ana@larkspur.example
[x] Uploads work: the last one finished just now, and nothing is waiting
```

`[ ]` marks something missing, with the next step under it. `[-]` marks something you may not need, such as Cursor for someone who uses only Claude Code.

### If something goes wrong

| What you see | What to do |
|---|---|
| `command not found: npx` (macOS, Linux), or `'npx' is not recognized` or `The term 'npx' is not recognized` (Windows) | Node.js isn't installed: install it from [nodejs.org](https://nodejs.org/), then open a new terminal. Or [install `sigdek` without Node.js](#other-ways-to-install-the-command). |
| Claude Code doesn't know `/plugin` | Your Claude Code is too old for plugins: update it (`claude update` in a terminal), then start it again. |
| The browser didn't open during "Connect your machine" | Open the `Approve it at:` link that `sigdek login` printed, or that Claude showed you. |
| PowerShell says `running scripts is disabled on this system` | PowerShell's execution policy blocks npm's `npx.ps1`. Type `npx.cmd` instead of `npx`, or allow scripts for your account with `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`. |
| Nothing shows in Activity | Run `npx sigdek status`: it names what's missing. Records upload when the agent finishes its turn, so wait for that, or run `npx sigdek flush`. |
| Cursor on Windows records nothing | Run `npx sigdek status` in the project's folder. A project set up with SigDek 0.1.0 has Cursor hooks that Windows can't run: run `npx sigdek init` again there and commit the change. |

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
- **Never blocks your work by accident.** Any error inside the recorder or the launcher lets the action go ahead and is written to `~/.sigdek/errors.log`, and the hook commands can't fail even when the launcher is missing. The only time SigDek stops an action is a Block rule your team wrote.
- **Can be switched off by anyone using the machine:** removing the hooks, uninstalling the plugin or setting `SIGDEK_BIN` stops recording. SigDek doesn't hide that: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.
- **Fast:** a few milliseconds per event; CI fails the build above 50 ms. Two waits are longer. The first event on each machine, and the first after a new recorder version is pinned, waits up to 8 seconds while the launcher downloads the pinned recorder. On a slower link, `launch.sh` finishes the download in the background, while `launch.ps1` (Windows PowerShell) picks it up where it stopped in each following event, each waiting up to 7 seconds; recording starts once the download is complete. Until then nothing is recorded, and after a failed download the launcher tries again after 1, 2, 4… up to 60 minutes. An action that changes SigDek's own setup waits up to about a second for its record to upload (see above).
- **No third-party code:** the recorder uses only Go's standard library.

## Commands

| Command | What it does |
|---|---|
| `sigdek init` | Add SigDek's hooks to the current repository. `--remove` takes them out, and `.sigdek/` once no hooks use it |
| `sigdek login` / `logout` | Connect or disconnect this machine; sign-in happens in the browser. Logout asks the server that signed it in to revoke the machine's token and says if it couldn't. Agents are still recorded after logout, on the machine only; those records upload if it signs in to the same team again, and login says how many |
| `sigdek status` | A checklist of what is set up and working here, and the next step for anything that isn't. It reads only local files |
| `sigdek log` | Recent records from this machine |
| `sigdek verify` | Check that no local record was changed or removed |
| `sigdek flush` | Upload now (normally automatic, in the background), without waiting out a back-off |

## Rules

Teams set rules in the dashboard. A **Flag** rule marks matching actions in the report; a **Block** rule (`deny` in the API) stops them before they run, and tells the agent and the developer why. Until a machine signs in, 12 built-in rules ([schema/default-rules.json](schema/default-rules.json)) flag common risky actions (force pushes, recursive deletes, secrets files, infrastructure changes, destructive SQL, piping a download into a shell, publishing packages, privileged commands, history rewrites, and changes to SigDek's own setup) and block nothing. Patterns are globs (`*` matches anything; in paths `**` crosses `/`) or, with `re:`, RE2 regular expressions. Command and URL patterns are found anywhere in the text, path patterns match the whole path or any part after a `/`, and tool and MCP globs match the whole name: `Write` matches Write but not TodoWrite, and `github/merge_*` matches the server github but not mygithub. An MCP glob without `/` names the tool. Cursor's general `preToolUse` and `postToolUse` hooks, which SigDek uses, don't name the MCP server, so for Cursor an MCP pattern like `github/merge_*` matches on the tool part. Cursor's MCP-specific hooks do name it, but SigDek doesn't use them yet.

## How it works

### Plugin and project hooks together

With the plugin installed and `sigdek init` in a repository, the plugin steps aside when the agent starts at the repository's root, so each action is recorded once. Claude Code reads `.claude/settings.json` only from the folder a session starts in, and Cursor reads `.cursor/hooks.json` from the workspace root, so in a session started in a subdirectory the repository's hooks don't run and the plugin records instead. Only hook events step aside: a command you run through the plugin, such as `/sigdek:connect`, runs everywhere.

### Cursor

Cursor also runs Claude Code's hooks by default (its Third-Party Imports setting loads `.claude/settings.json`, `.claude/settings.local.json` and `~/.claude/settings.json`), so in a repository set up with `sigdek init` each Cursor action reaches SigDek's Claude Code hook as well as its Cursor hook. SigDek's Claude Code hook stands aside when Cursor runs it, so each Cursor action is recorded once, by the Cursor hook; Cursor's actions are recorded only through SigDek's Cursor hooks (`sigdek init` or the Cursor plugin).

SigDek's Cursor plugin isn't listed on the Cursor Marketplace yet (Cursor reviews each plugin by hand). Until it is, set up each repository with `sigdek init`, or, on Cursor's Teams and Enterprise plans, add it to a team marketplace with **Import from Repo** and `https://github.com/tektonsoftware/sigdek`, which lists the plugin in `.cursor-plugin/marketplace.json`.

### Windows

On Windows, Claude Code runs hooks through Git Bash; without Git Bash it uses PowerShell, where SigDek records nothing but still doesn't block. `sigdek status` says when it can't find Git Bash.

Cursor runs hooks through PowerShell on Windows. The Cursor commands `sigdek init` writes read as one program to sh, which runs `.sigdek/launch.sh` (macOS and Linux), and as another to PowerShell, which runs `.sigdek/launch.ps1` (Windows PowerShell 5.1 and PowerShell 7), so the one committed `.cursor/hooks.json` works for everyone. CI checks this by running each command inside the PowerShell wrapper Cursor uses on Windows; it hasn't been tried in the Cursor app on Windows yet. A repository set up with SigDek 0.1.0 has sh-only Cursor commands, which Windows PowerShell 5.1 can't run (it has no `||`): there Cursor records nothing on Windows, without blocking, until someone runs `sigdek init` again and commits the change; `sigdek status` says so. `sigdek init --powershell` still writes PowerShell-only Cursor commands, which then need PowerShell on every machine that uses Cursor. The Cursor plugin's hooks are sh-only, so the plugin records nothing on Windows.

### Line endings, and a missing launcher

`.sigdek/.gitattributes` keeps the launchers' line endings right on Windows checkouts, and hooks run the launcher with `sh` (or PowerShell), so it needs no exec bit.

A hook command never stops the agent on its own: when the launcher is missing or broken (a branch made before SigDek was added, `.sigdek` deleted or ignored by git, merge-conflict markers in `launch.sh`, an empty file), the command does nothing but give Cursor the answer it expects, and that checkout records nothing until the launcher is back. `sigdek init` warns when git ignores any file it wrote, `sigdek status` says when `.sigdek/launch.sh` is missing, and in a repository set up by an older version, `sigdek status` asks you to run `sigdek init` again for these commands.

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
