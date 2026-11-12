[Back to the README](../README.md)

# How it works

## From hook to upload

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

The launcher runs only the recorder version pinned next to it: in `.sigdek/recorder.lock` for a repository set up with `sigdek init`, or in the plugin's own `scripts/recorder.lock`. It refuses a download whose SHA-256 doesn't match the pin. [Fast](recording.md#fast) says how long the first download may take, and what happens on a slow link or after a failed download.

The one upload that happens inside a hook is the record of an action that changes SigDek's own setup (see [What it records](recording.md)), since that action may delete the log as soon as the hook is done.

Uploads back off when the service asks:

- After it rejects the machine's token, they are tried once an hour until `sigdek login` succeeds.
- After a rate limit or a server error, they wait as long as the service's `Retry-After` says, or longer each time, up to an hour.
- A batch the service finds too large is split; a single record it still refuses is skipped, stays on the machine and shows as a break in the report.

Each upload brings back the team's current rules; a rule the recorder can't use is written to `~/.sigdek/errors.log` when it arrives.

## Plugin and project hooks together

With the plugin installed and `sigdek init` in a repository, the plugin steps aside when the agent starts at the repository's root, so each action is recorded once. Claude Code reads `.claude/settings.json` only from the folder a session starts in, and Cursor reads `.cursor/hooks.json` from the workspace root, so in a session started in a subdirectory the repository's hooks don't run and the plugin records instead.

Only hook events step aside. A command you run through the plugin, such as `/sigdek:connect`, runs in a repository set up with `sigdek init` too, wherever the plugin can run (on Windows, only with Git Bash).

## Line endings, and a missing launcher

`.sigdek/.gitattributes` keeps the launchers' line endings right on Windows checkouts, and keeps filters such as Git LFS off them. Hooks run the launcher with `sh` (or PowerShell), so it needs no exec bit.

A hook command never stops the agent on its own: when the launcher is missing or broken (a branch made before SigDek was added, `.sigdek` deleted or ignored by git, merge-conflict markers in `launch.sh`, an empty file), the command does nothing but give Cursor the answer it expects, and that checkout records nothing until the launcher is back. The PowerShell-only Cursor form, `sigdek init --powershell`, is the exception: with launch.ps1 missing, empty or broken, it gives Cursor no answer.

`sigdek init` warns when git ignores any file it wrote. `sigdek status` says when `.sigdek/launch.sh` is missing, and, in a repository set up by an older version, asks you to run `sigdek init` again for these commands.

## Cursor and Windows

[Cursor](cursor.md) covers how Cursor runs Claude Code's hooks too, and the Cursor plugin. [Windows](windows.md) covers the hook commands for Windows.
