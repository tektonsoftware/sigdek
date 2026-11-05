# SigDek recorder

SigDek records what AI coding agents do on your team's machines (who ran which agent, which tool, the command or file, and whether it ran or was blocked) and turns that record into evidence an auditor or a customer can read.

This repository is the open-source recorder that runs on developer machines, plus the Claude Code and Cursor plugins that install it. The hosted team log and reports live at [sigdek.com](https://sigdek.com).

SigDek is a product of Tekton Software LLC.

## Install

**In a repository, for the whole team.** Every developer who clones the repository is covered, with nothing to install:

```sh
npx sigdek init        # writes .claude/settings.json, .cursor/hooks.json and .sigdek/
git add .claude .cursor .sigdek && git commit -m "Record agent activity with SigDek"
```

**As a Claude Code plugin**, for one person across all their repositories:

```
/plugin marketplace add tektonsoftware/sigdek
/plugin install sigdek@sigdek
```

**As a Cursor plugin**: it's submitted to the Cursor Marketplace, where Cursor reviews each plugin by hand, and isn't listed yet. Until it is, set up each repository with `sigdek init` (above), or, on Cursor's Teams and Enterprise plans, add it to a team marketplace with **Import from Repo** and `https://github.com/tektonsoftware/sigdek`, which lists the plugin in `.cursor-plugin/marketplace.json`.

Then connect the machine to your team: `npx sigdek login`. Until you do, records stay on the machine.

With the plugin installed and `sigdek init` in a repository, the plugin steps aside when the agent starts at the repository's root, so each action is recorded once. Claude Code reads `.claude/settings.json` only from the folder a session starts in, and Cursor reads `.cursor/hooks.json` from the workspace root, so in a session started in a subdirectory the repository's hooks don't run and the plugin records instead. Cursor also runs Claude Code's hooks by default (its Third-Party Imports setting loads `.claude/settings.json`, `.claude/settings.local.json` and `~/.claude/settings.json`), so in a repository set up with `sigdek init` each Cursor action reaches SigDek's Claude Code hook as well as its Cursor hook. SigDek's Claude Code hook stands aside when Cursor runs it, so each Cursor action is recorded once, by the Cursor hook; Cursor's actions are recorded only through SigDek's Cursor hooks (`sigdek init` or the Cursor plugin). `.sigdek/.gitattributes` keeps the launchers' line endings right on Windows checkouts, and hooks run the launcher with `sh`, so it needs no exec bit.

A hook command never stops the agent on its own: when the launcher is missing or broken (a branch made before SigDek was added, `.sigdek` deleted or ignored by git, merge-conflict markers in `launch.sh`, an empty file), the command does nothing but give Cursor the answer it expects, and that checkout records nothing until the launcher is back. `sigdek init` warns when git ignores any file it wrote, and in a repository set up by an older version, `sigdek status` asks you to run `sigdek init` again for these commands. On Windows, Claude Code runs hooks through Git Bash; without Git Bash it uses PowerShell, where SigDek records nothing but still doesn't block. Cursor runs hooks through PowerShell on Windows, and Windows PowerShell 5.1 can't run the default commands (it has no `||`), so with the default `sigdek init` Cursor records nothing there, without blocking; `sigdek init` on Windows says so. The Cursor plugin's hooks use the same commands, so the plugin records nothing on Windows either. For Cursor on Windows, run `sigdek init --powershell`, whose hooks need PowerShell on every machine that uses Cursor.

### Getting the `sigdek` command

`npx sigdek …` works anywhere Node.js is installed. To install the command instead:

| Where | Install |
|---|---|
| Any OS, with Node.js | `npm install -g sigdek` |
| macOS or Linux, Homebrew | `brew tap tektonsoftware/sigdek https://github.com/tektonsoftware/sigdek` then `brew install sigdek` |
| Windows, Scoop | `scoop bucket add sigdek https://github.com/tektonsoftware/sigdek` then `scoop install sigdek` |
| Windows, winget | `winget install TektonSoftware.SigDek`, available a few days after each release, once Microsoft merges its pull request |
| Go 1.24 or later | `go install github.com/tektonsoftware/sigdek/cmd/sigdek@latest` |
| Anything else | Download the binary for your platform from [GitHub releases](https://github.com/tektonsoftware/sigdek/releases) and check it against `checksums.txt` |

Every channel except `go install`, which compiles from source, installs the same binaries, built once per release. The npm package is a small launcher that runs the one for your platform from an `@tektonsoftware/sigdek-*` package. The Homebrew formula (`Formula/`) and Scoop manifest (`bucket/`) live in this repository and are updated by the release workflow, which also opens the winget pull request.

## What it records, and what it never does

SigDek writes one record (see [the schema](schema/agent-event.v1.schema.json)) for each action an agent takes, once it has run, failed or been refused, and for each session start and end, prompt, subagent and settings change. A record holds the person, device, agent tool, session and repository; the agent's model and version where the agent reports them (Cursor reports both; Claude Code reports only the model); and, for an action, its kind, the tool's name, the command or file path, the outcome, how long it took, and which of your team's rules matched. Some hook events write nothing on purpose: the check before a tool runs (unless a rule blocks it, the action is recorded after it runs) and the end of each turn. So an action someone declines at the agent's permission prompt leaves no record: Claude Code runs no hook after a decline, and Cursor documents none (if Cursor does report a decline, as a `permission_denied` failure, SigDek records the action as refused). The exception is an action that changes SigDek's own setup, such as deleting `.sigdek`, `sigdek logout` or editing the hook files: it may leave nothing behind to record it afterwards, so it is recorded as Attempted when the agent asks to run it (it may still be declined, which leaves no later trace) and, on a signed-in machine, uploaded before the agent goes ahead, waiting at most about a second. If a hook still runs once it is done, a second record says whether it ran, failed or was refused. Events that arrive but can't be recorded are listed in `~/.sigdek/errors.log`.

- **Never recorded:** prompt text (only a hash and its length), file contents, command output. The full tool input is kept only as a SHA-256 hash of the input exactly as the agent sent it. Like the prompt hash, it can confirm a guess at the whole input, a file edit's contents included. When a secret was stripped from the action's command, path or URL, the hash is instead of the stored, redacted action (the RFC 8785 canonical JSON of its `kind`, `name`, `command`, `path` and `url`), so it can't confirm a guessed secret; a nonzero `redactions` count in the record says it holds this form. [SECURITY.md](SECURITY.md) has the details.
- **Secrets are stripped** from commands, paths and URLs before anything is written: cloud keys, tokens, passwords in URLs and flags, cookies, private keys, JWTs. It is best effort, and [the tests](internal/redact/redact_test.go) list what it catches and what it leaves alone.
- **Bounded:** every field is cut to the schema's limit (a command to 4 KB), and a cut field ends with a note of its full length; a blocked command too long to keep also keeps the part the rule matched. Rules are checked before anything is cut or redacted. Block rules read the whole command, both as the agent sent it and as the shell reads it (line continuations joined, quotes and backslashes removed, blanks squeezed), so neither padding nor quoting hides a command from them; Flag rules read up to 16 KB of each. A command put together at run time or by expansion, such as `X=rm; $X -rf`, `{rm,-rf,/x}` or `/bin/r? -rf`, can't be seen through; [SECURITY.md](SECURITY.md) lists what can and can't.
- **Paths are repository-relative**, and your home directory shows as `~`.
- **Tamper-evident once SigDek has received them:** each record carries the hash of the one before it, per device, and the hosted service checks the chain on every upload. The machine's local log can be rewritten by someone with access to it; the server catches any later change, gap or fork. `sigdek verify` recomputes the local chain and lists every line that doesn't hold.
- **Never blocks your work by accident.** Any error inside the recorder or the launcher lets the action go ahead and is written to `~/.sigdek/errors.log`, and the hook commands can't fail even when the launcher is missing. The only time SigDek stops an action is a Block rule your team wrote.
- **Can be switched off by anyone using the machine:** removing the hooks, uninstalling the plugin or setting `SIGDEK_BIN` stops recording. SigDek doesn't hide that: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.
- **Fast:** a few milliseconds per event; CI fails the build above 50 ms. Two waits are longer. The first event on each machine, and the first after a new recorder version is pinned, waits up to a few seconds while the launcher downloads the pinned recorder; until that download succeeds nothing is recorded, and after a failure the launcher tries again after 1, 2, 4… up to 60 minutes. An action that changes SigDek's own setup waits up to about a second for its record to upload (see above).
- **No third-party code:** the recorder uses only Go's standard library.

## Commands

| Command | What it does |
|---|---|
| `sigdek init` | Add SigDek's hooks to the current repository. `--remove` takes them out, and `.sigdek/` once no hooks use it |
| `sigdek login` / `logout` | Connect or disconnect this machine; sign-in happens in the browser. Logout asks the server that signed it in to revoke the machine's token and says if it couldn't. Agents are still recorded after logout, on the machine only; those records upload if it signs in to the same team again, and login says how many |
| `sigdek status` | What is set up and working, and what isn't |
| `sigdek log` | Recent records from this machine |
| `sigdek verify` | Check that no local record was changed or removed |
| `sigdek flush` | Upload now (normally automatic, in the background), without waiting out a back-off |

## Rules

Teams set rules in the dashboard. A **Flag** rule marks matching actions in the report; a **Block** rule (`deny` in the API) stops them before they run, and tells the agent and the developer why. Until a machine signs in, 12 built-in rules ([schema/default-rules.json](schema/default-rules.json)) flag common risky actions (force pushes, recursive deletes, secrets files, infrastructure changes, destructive SQL, piping a download into a shell, publishing packages, privileged commands, history rewrites, and changes to SigDek's own setup) and block nothing. Patterns are globs (`*` matches anything; in paths `**` crosses `/`) or, with `re:`, RE2 regular expressions. Command and URL patterns are found anywhere in the text, path patterns match the whole path or any part after a `/`, and tool and MCP globs match the whole name: `Write` matches Write but not TodoWrite, and `github/merge_*` matches the server github but not mygithub. An MCP glob without `/` names the tool. Cursor's general `preToolUse` and `postToolUse` hooks, which SigDek uses, don't name the MCP server, so for Cursor an MCP pattern like `github/merge_*` matches on the tool part. Cursor's MCP-specific hooks do name it, but SigDek doesn't use them yet.

## How it runs

```
agent tool ──hook event──▶ launcher (.sigdek/launch.sh, or the plugin's)
                              │ first use: download the pinned version, check SHA-256
                              ▼
                          sigdek hook <tool>
                              │ adapter → match rules → answer the agent → cut and redact → seal into the hash chain
                              ▼
                          ~/.sigdek/log/events-YYYY-MM.jsonl
                              │ at the end of each session, and at most once a minute while working, in a separate process
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

Releases: push a tag like `v0.3.0`. The release workflow builds six targets, publishes them with `checksums.txt`, publishes the npm packages with provenance (when the `NPM_TOKEN` secret is set), pins both plugins, the Homebrew formula and the Scoop manifest to the new version, and submits it to winget (when the `WINGET_TOKEN` secret is set).

## License

Apache-2.0. Copyright 2026 Tekton Software LLC. Report security issues as described in [SECURITY.md](SECURITY.md).
