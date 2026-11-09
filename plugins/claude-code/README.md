# SigDek for Claude Code

SigDek records what Claude Code's agents do on your machine: who ran which agent, which tool, the command, file path or URL, and whether it ran, failed or was blocked. Your team gets one record across Claude Code and Cursor, and an evidence report that an auditor or a customer's security review can check. Prompt text, file contents and command output are never recorded.

The plugin is free and open source (Apache-2.0) and works on its own, keeping records on your machine. Connecting the machine to a team on sigdek.com, a separate paid service with a 14-day trial, adds the shared log, Block rules and reports.

## Install

- From this repository's marketplace: `/plugin marketplace add tektonsoftware/sigdek`, then `/plugin install sigdek@sigdek`.
- From Anthropic's plugin directory, once it's listed there: add SigDek in Customize > Plugins on claude.ai, or with `/plugin directory` in Claude Code. It loads in Claude Code as `sigdek@synced`.

SigDek uses Claude Code's hooks, so it records in Claude Code (the terminal, the IDE extensions and the desktop app's Code tab). It does nothing in claude.ai chat.

## Use it

1. Work in Claude Code as usual. Each action is recorded once it has run, failed, been stopped by a Block rule or been refused by auto mode. An action you decline at the permission prompt leaves no record, because Claude Code runs no hook after a decline.
2. Install the `sigdek` command (https://sigdek.com/docs/#command). Read this machine's records with `sigdek log`, and check that none was changed with `sigdek verify`.
3. To connect the machine to your team, run `sigdek login` and approve it in your browser.

## Try it

- Ask Claude Code to run your tests. `sigdek log` lists each command and whether it ran.
- In a throwaway repository, ask Claude Code to force-push a branch. The built-in rules flag it: `sigdek log` shows `[rule: force-push]`. Out of the box nothing is blocked.
- With the machine connected, an owner adds a Block rule in the dashboard, such as command `terraform destroy*` with a message. The machine picks it up on its next upload, at most a minute later while you work. Ask Claude Code to run `terraform destroy`, and it's refused before it runs, with your message. `sigdek log` shows it as `denied_by_rule`, and the dashboard as "Blocked by rule". Generate a report for the day, and anyone can check the PDF at https://app.sigdek.com/verify.

## What it runs, downloads, writes and sends

- **Runs:** on Claude Code's `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`, `SubagentStart`, `SubagentStop`, `Stop` and `ConfigChange` hooks, `scripts/launch.sh`, which runs the SigDek recorder. The hook always exits 0. It stops an action only when one of your team's Block rules matches.
- **Downloads:** on first use, the recorder binary for your platform from this repository's GitHub releases (github.com/tektonsoftware/sigdek/releases). It downloads only the version pinned in `scripts/recorder.lock`, and refuses the file unless its SHA-256 matches the one there. It's kept in the plugin's data folder. That first event waits up to a few seconds for the download. If the download fails, the launcher waits 1, 2, 4… up to 60 minutes before trying again, and records nothing in the meantime. The recorder's source is in the same repository (`cmd/`, `internal/`) and uses only Go's standard library.
- **Writes:** records to `~/.sigdek/log/` (one file per month), errors to `~/.sigdek/errors.log`, and the machine's settings and your team's rules to `~/.sigdek`. The local log isn't trimmed; delete `~/.sigdek` to remove it.
- **Sends:** nothing until you run `sigdek login`.
  - Sign-in happens in your browser at app.sigdek.com.
  - After that, records go over HTTPS to ingest.sigdek.com, run by Tekton Software. They're sent by a separate background process when the agent finishes each turn, when the session ends, and at most once a minute during a long turn, and each upload brings back your team's rules.
  - One exception: an action that changes SigDek's own setup (deleting `.sigdek`, `sigdek logout`, editing the hook files) is recorded as attempted, and the hook uploads that record before the action runs, waiting at most about a second; if that upload doesn't finish in time, the background upload takes over.

## What a record holds

The person (the signed-in email address, after `sigdek login`), the machine, the agent and its model, Claude Code's permission mode, the session and the repository. For an action, it also holds the kind (shell command, file read, file edit, web request, MCP tool, other), the tool's name, the command, file path or URL, the result, how long it took and which rule matched, plus a SHA-256 hash of the tool input. For a prompt, it holds only its length and a SHA-256 hash.

A hash can confirm a guess: someone with the record can check a guessed prompt or tool input against it, so a short or common one can be worked out. When a secret was stripped from an action, its hash covers the stored, redacted action instead of the input as sent, so it can't confirm the secret.

Secrets (cloud keys, tokens, passwords in URLs and flags, private keys) are stripped from commands, paths and URLs before anything is written. This is best effort, so treat commands as sensitive. Paths inside the repository are relative, and your home folder shows as `~`. Every field has a size limit; a command is cut at 4 KB. The record format is in the repository's `schema/agent-event.v1.schema.json`.

## Limits

- Anyone using the machine can switch SigDek off: uninstall or disable the plugin, turn off hooks, or set `SIGDEK_BIN`. SigDek doesn't hide that. The built-in rules flag an agent editing SigDek's own setup, and your team's recorder coverage shows a machine that goes silent for two or more working days.
- Block rules check what the agent asks for; they aren't a sandbox. A command put together at run time, such as `X=rm; $X -rf`, can't be seen through.
- Files attached to a prompt with @ aren't recorded.
- On Windows, Claude Code runs hooks through Git Bash. Without Git Bash it uses PowerShell, where SigDek records nothing (and blocks nothing).
- In a repository set up with `sigdek init`, the plugin steps aside in sessions started at the repository's root, so nothing is recorded twice.
- Cursor can run Claude Code's hooks too. When Cursor sends an event to SigDek's Claude Code hook, the recorder stands aside, so Cursor's actions are recorded only by SigDek's Cursor plugin or a repository's Cursor hooks, never as Claude Code.
- Recording adds a few milliseconds per action; CI fails the build above 50 ms.

## Privacy, terms and support

- Privacy policy: https://sigdek.com/privacy/
- Terms: https://sigdek.com/terms/
- How SigDek handles data: https://sigdek.com/security/
- Support: support@sigdek.com. Security reports: security@sigdek.com (see SECURITY.md).
- Source: https://github.com/tektonsoftware/sigdek

SigDek is a product of Tekton Software.
