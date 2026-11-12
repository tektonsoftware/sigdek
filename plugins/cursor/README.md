# SigDek for Cursor

SigDek records what Cursor's agents do on your machine: who ran which agent, which tool, the command, file path or URL, and whether it ran, failed or was blocked. Your team gets one record across Cursor and Claude Code, and an evidence report that an auditor or a customer's security review can check. Prompt text, file contents and command output are never recorded.

The plugin is free and open source (Apache-2.0) and works on its own, keeping records on your machine. Connecting the machine to a team on sigdek.com, a separate paid service with a 14-day trial, adds the shared log, Block rules and reports.

## Use it

1. Install the plugin. It isn't listed on the Cursor Marketplace yet. Until it is, an admin on Cursor's Teams or Enterprise plans can add it to a team marketplace with Import from Repo and github.com/tektonsoftware/sigdek, or a repository can be set up with `sigdek init`, which records Cursor without the plugin.
2. Use Cursor's agent as usual. Each action is recorded once it has run, failed or been refused. Cursor documents no hook after you decline an action at its approval prompt, so a declined action may leave no record.
3. Install the `sigdek` command (https://sigdek.com/docs/#command). Read this machine's records with `sigdek log`, and check that none was changed with `sigdek verify`.
4. To connect the machine to your team, run `sigdek login` and approve it in your browser.

## Try it

- Ask the agent to run your tests. `sigdek log` lists the command and whether it ran.
- In a throwaway repository, ask the agent to force-push a branch. The built-in rules flag it: `sigdek log` shows `[rule: force-push]`. Out of the box nothing is blocked.
- With the machine connected, an owner adds a Block rule in the dashboard, such as command `terraform destroy*` with a message. The machine picks it up on its next upload, at most a minute later while you work. Ask the agent to run `terraform destroy`, and Cursor refuses it before it runs, showing your message. `sigdek log` shows it as `denied_by_rule`, and the dashboard as "Blocked by rule".

## What it runs, downloads, writes and sends

- **Runs:** on Cursor's `sessionStart`, `sessionEnd`, `beforeSubmitPrompt`, `preToolUse`, `postToolUse`, `postToolUseFailure`, `subagentStart`, `subagentStop` and `stop` hooks, `scripts/launch.sh`, which runs the SigDek recorder. Every hook gives Cursor a valid answer, and it allows the action unless one of your team's Block rules matches.
- **Downloads:** on first use, the recorder binary for your platform from this repository's GitHub releases (github.com/tektonsoftware/sigdek/releases). It downloads only the version pinned in `scripts/recorder.lock`, and refuses the file unless its SHA-256 matches the one there. It's kept in `~/.sigdek/bin`. That first event waits up to 8 seconds for the download (6 on Windows). On a slower link, the launcher finishes it in a separate background process that keeps running after the hook ends, for up to 5 minutes, and records nothing until the download is done. If the download fails, the launcher waits 1, 2, 4… up to 60 minutes before trying again, and records nothing in the meantime. The recorder's source is in the same repository (`cmd/`, `internal/`) and uses only Go's standard library.
- **Writes:** records to `~/.sigdek/log/` (one file per month), errors to `~/.sigdek/errors.log`, and the machine's settings and your team's rules to `~/.sigdek`. The local log isn't trimmed; delete `~/.sigdek` to remove it.
- **Sends:** nothing until you run `sigdek login`.
  - Sign-in happens in your browser at app.sigdek.com.
  - After that, records go over HTTPS to ingest.sigdek.com, run by Tekton Software. They're sent by a separate background process when the agent finishes each turn, when the session ends, and at most once a minute during a long turn, and each upload brings back your team's rules.
  - One exception: an action that changes SigDek's own setup (deleting `.sigdek`, `sigdek logout`, editing the hook files) is recorded as attempted, and the hook uploads that record before the action runs, waiting at most about a second; if that upload doesn't finish in time, the background upload takes over.

## What a record holds

The person (the signed-in email address, after `sigdek login`), the machine, Cursor's version and model, the session and the repository. For an action, it also holds the kind (shell command, file read, file edit, web request, MCP tool, other), the tool's name, the command, file path or URL, the result, how long it took and which rule matched, plus a SHA-256 hash of the tool input. For a prompt, it holds only its length and a SHA-256 hash.

A hash can confirm a guess: someone with the record can check a guessed prompt or tool input against it, so a short or common one can be worked out. When a secret was stripped from an action, its hash covers the stored, redacted action instead of the input as sent, so it can't confirm the secret.

Secrets (cloud keys, tokens, passwords in URLs and flags, private keys) are stripped from commands, paths and URLs before anything is written. This is best effort, so treat commands as sensitive. Paths inside the repository are relative, and your home folder shows as `~`. Every field has a size limit; a command is cut at 4 KB. The record format is in the repository's `schema/agent-event.v1.schema.json`.

## Limits

- Anyone using the machine can switch SigDek off: uninstall or disable the plugin, turn off hooks, or set `SIGDEK_BIN`. SigDek doesn't hide that. The built-in rules flag an agent editing SigDek's own setup, and your team's recorder coverage shows a machine that goes silent for two or more working days.
- Block rules check what the agent asks for; they aren't a sandbox. A command put together at run time, such as `X=rm; $X -rf`, can't be seen through.
- Not recorded: Cursor's inline Tab completions. For MCP tools, SigDek records the tool's name, not yet which server it came from: Cursor's general `preToolUse` and `postToolUse` hooks, which SigDek uses, don't name the server.
- Records reach your team only from a machine signed in with `sigdek login`. An agent running in the cloud or on CI, such as one of Cursor's cloud agents, records only on its own machine unless that machine is signed in, and your team's Block rules don't apply there.
- On Windows, Cursor runs hooks through PowerShell, and Windows PowerShell 5.1 can't run this plugin's hook commands, so the plugin records nothing there (and blocks nothing). To record Cursor on Windows, set up the repository with `sigdek init --windows` (or run `sigdek init` on Windows): its hooks run under sh and under PowerShell, so they record on all three systems. They haven't been tried in the Cursor app on Windows yet.
- In a repository set up with `sigdek init`, the plugin steps aside when the workspace is the repository's root, so nothing is recorded twice.
- Cursor also runs Claude Code's hooks by default, including a repository's `sigdek init` hooks for Claude Code. SigDek's Claude Code hook stands aside for Cursor, so each Cursor action is recorded once, as Cursor.
- Recording adds a few milliseconds per action; CI fails the build when the slowest 5% of hook runs take over 50 ms (120 ms on Windows).

## Privacy, terms and support

- Privacy policy: https://sigdek.com/privacy/
- Terms: https://sigdek.com/terms/
- How SigDek handles data: https://sigdek.com/security/
- Support: support@sigdek.com. Security reports: security@sigdek.com (see SECURITY.md).
- Source: https://github.com/tektonsoftware/sigdek

SigDek is a product of Tekton Software.
