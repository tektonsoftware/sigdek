[Back to the README](../README.md)

# What it records, and what it never does

SigDek writes one record (see [the schema](../schema/agent-event.v1.schema.json)) for each action an agent takes, once it has run, failed or been refused, and for each session start and end, prompt, subagent and settings change.

A record holds the person, device, agent tool, session and repository; the agent's model and version where the agent reports them (Cursor reports both; Claude Code reports only the model); and, for an action, its kind, the tool's name, the command or file path, the outcome, how long it took, and which of your team's rules matched.

Some hook events write nothing on purpose: the check before a tool runs (unless a rule blocks it, the action is recorded after it runs) and the end of each turn. So an action someone declines at the agent's permission prompt leaves no record: Claude Code runs no hook after a decline, and Cursor documents none (if Cursor does report a decline, as a `permission_denied` failure, SigDek records the action as refused).

The exception is an action that changes SigDek's own setup, such as deleting `.sigdek`, `sigdek logout` or editing the hook files. It may leave nothing behind to record it afterwards, so it is recorded as Attempted when the agent asks to run it (it may still be declined, which leaves no later trace) and, on a signed-in machine, uploaded before the agent goes ahead, waiting at most about a second. If a hook still runs once it is done, a second record says whether it ran, failed or was refused.

Events that arrive but can't be recorded are listed in `~/.sigdek/errors.log`.

## Never recorded

Prompt text (only a hash and its length), file contents, command output.

The full tool input is kept only as a SHA-256 hash of the input exactly as the agent sent it. Like the prompt hash, it can confirm a guess at the whole input, a file edit's contents included. When a secret was stripped from the action's command, path or URL, the hash is instead of the stored, redacted action (the RFC 8785 canonical JSON of its `kind`, `name`, `command`, `path` and `url`), so it can't confirm a guessed secret; a nonzero `redactions` count in the record says it holds this form. [SECURITY.md](../SECURITY.md) has the details.

## Secrets are stripped

Secrets are stripped from commands, paths and URLs before anything is written: cloud keys, tokens, passwords in URLs and flags, cookies, private keys, JWTs. It is best effort, and [the tests](../internal/redact/redact_test.go) list what it catches and what it leaves alone.

## Bounded

Every field is cut to the schema's limit (a command to 4 KB), and a cut field ends with a note of its full length; a blocked command too long to keep also keeps the part the rule matched.

Rules are checked before anything is cut or redacted. Block rules read the whole command, both as the agent sent it and as the shell reads it (line continuations joined, quotes and backslashes removed, blanks squeezed), so neither padding nor quoting hides a command from them; Flag rules read up to 16 KB of each. A command put together at run time or by expansion, such as `X=rm; $X -rf`, `{rm,-rf,/x}` or `/bin/r? -rf`, can't be seen through; [SECURITY.md](../SECURITY.md) lists what can and can't.

## Paths are repository-relative

Paths inside the repository are relative to it, and your home directory shows as `~`.

## Tamper-evident once SigDek has received them

Each record carries the hash of the one before it, per device, and the hosted service checks the chain on every upload. The machine's local log can be rewritten by someone with access to it; the server catches any later change, gap or fork. `sigdek verify` recomputes the local chain and lists every line that doesn't hold.

## Never blocks your work by accident

Any error inside the recorder or the launcher lets the action go ahead and is written to `~/.sigdek/errors.log`, and the hook commands can't fail even when the launcher is missing. The only time SigDek stops an action is a Block rule your team wrote.

Two exceptions:

- **The PowerShell-only Cursor form,** `sigdek init --powershell`: when launch.ps1 is missing, empty or refused by PowerShell, it gives Cursor no answer. Cursor's docs say it lets a failed hook through unless its failClosed setting is on, and don't say how it treats an empty answer. The sh and sh-and-PowerShell forms always answer.
- **One isn't SigDek's:** Cursor's CLI on Windows, started from Git Bash, reports every hook as "Hook blocked" (see [Windows](windows.md#where-cursor-on-windows-records-nothing)).

## Can be switched off by anyone using the machine

Removing the hooks, uninstalling the plugin or setting `SIGDEK_BIN` stops recording. SigDek doesn't hide that: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.

## Fast

A few milliseconds per event; CI fails the build when the slowest 5% of hook runs take over 50 ms (120 ms on Windows).

Two waits are longer:

- **The first event on each machine,** and the first after a new recorder version is pinned, waits while the launcher downloads the pinned recorder. Nothing is recorded until the download is complete and its SHA-256 checked. How long it waits depends on the launcher:
  - **`launch.sh` with curl** (macOS, Linux, and Git Bash on Windows) waits up to 8 seconds, or 6 under Git Bash on Windows. On a slower link it carries on in a background process, for up to 5 minutes, and the next event after that finds the recorder.
  - **`launch.sh` with only wget** has no overall limit: it gives up when connecting or receiving stalls for 8 seconds. It has no background download, so a download that stalls counts as failed.
  - **`launch.ps1`** (Cursor on Windows, under PowerShell) waits up to 7 seconds from the start of each event. It keeps what arrived and asks for the rest in the following events, each waiting up to 7 seconds.

  After a failed download, the launcher tries again after 1, 2, 4… up to 60 minutes. A new pin in `recorder.lock` tries again at once.
- **An action that changes SigDek's own setup** waits up to about a second for its record to upload (see above).

## No third-party code

The recorder uses only Go's standard library.
