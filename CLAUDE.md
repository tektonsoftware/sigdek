# SigDek recorder

The `sigdek` command and the Claude Code and Cursor plugins. It records what AI coding agents do on a developer's machine, as a hash chain per machine, applies the team's Flag and Block rules, and uploads to the hosted service once signed in. `README.md` is the user guide; `SECURITY.md` says what the recorder can and can't guarantee.

**This repository is, or will be, public.** Keep private business details, account IDs, customer data and captured records out of commits, issues and this file. Raw capture data stays on the machine that made it.

## Commands

```sh
go vet ./...
go test ./...          # unit tests plus end-to-end tests of the compiled binary
go test -race ./...
go build -o sigdek ./cmd/sigdek
```

- **CI:** a push to `main` runs Linux only. Before merging anything that touches files, paths, processes, permissions or the launchers, run the full Linux, macOS and Windows matrix: `gh workflow run ci.yml --ref main`, then `gh run watch`.
- **Windows failures:** read them from the check run's annotations. The raw log download can be blocked.
- **Trying a build without touching your real setup:**
  - `SIGDEK_BIN=$PWD/sigdek` makes launchers use your build;
  - `SIGDEK_HOME=<temp dir>` keeps the log and config away from `~/.sigdek`;
  - `SIGDEK_APP_URL` and `SIGDEK_INGEST_URL` point at a non-production service.

## The hook contract (never break it)

A hook must never block or break the agent by accident.

- **Always exit 0.** Write to stdout only for a deny, or for Cursor's JSON answer.
- **Hook commands, in `internal/initcmd/initcmd.go` and in both plugins' hook files:**
  - Claude Code: `sh "$CLAUDE_PROJECT_DIR/.sigdek/launch.sh" hook claude-code; exit 0`
  - Cursor: `sh .sigdek/launch.sh hook cursor <event> | grep '^{' || echo '<safe answer>'`. Cursor always gets an answer, even from a missing, empty, directory or Git LFS-pointer launcher.
  - The plugins use the same forms with `${CLAUDE_PLUGIN_ROOT}` / `${CURSOR_PLUGIN_ROOT}`.
  - `sigdek status` must recognise the current forms and tell older repositories to re-run `sigdek init`.
  - Windows PowerShell 5.1 has no `||`, so Windows Cursor teams use `--powershell`.
- **Launchers:**
  - The sources are `internal/initcmd/templates/launch.sh` and `launch.ps1`. Each is pinned to a recorder version and SHA-256 by `.sigdek/recorder.lock`.
  - After editing one, run `scripts/sync-plugins.sh` to copy it into both plugins (CI fails on drift).
  - `.sigdek/.gitattributes` keeps Git LFS and filters off the launchers.
- **Speed:** the budget is 50 ms p95 from start to exit (120 ms on Windows CI); typical is about 6–8 ms.
  - Keep work off the hook path: build a record only when a rule matches or one will be written.
  - Uploads run in a separate process. The one exception is the early record of an action that changes SigDek's own setup, uploaded in the hook within about 1 s.
- **Outcomes:** `ran`, `failed`, `denied_by_rule`, `denied`, `attempted`. "Attempted" is recorded before an action that changes SigDek's own setup; the later hook, if any, adds the result.

## Code rules

- **Standard library only:** add no third-party Go modules.
- **Schema:** `schema/agent-event.v1.schema.json` is the source of the record format. The hosted service copies it and regenerates its validator, so tell the maintainer when it changes. Adding an enum value keeps version 1.
- **Built-in rules** (`schema/default-rules.json`) are RE2 patterns.
  - The everyday-command corpus must stay unflagged, even with every rule set to Block, and every true positive must stay caught.
  - Add test cases rather than loosening patterns.
- **Redaction** (`internal/redact`): its tests list what it catches and what it leaves alone. Add both kinds of case with each change. Timing checks are skipped under the race detector (`race_on_test.go` / `race_off_test.go`).
- **Windows file access:** go through `internal/fsretry`, which retries sharing violations and reads with FILE_SHARE_DELETE. Permission comparisons on Windows look only at the write bit.

## Releases

- Pushing a tag like `v0.1.0` runs the release workflow. It builds six targets, publishes npm packages, pins the plugins and the Homebrew and Scoop manifests, and submits to winget.
- **Never tag, publish to npm or change this repository's visibility without the maintainer's explicit go-ahead.** v0.1.0 waits until the hosted service is live in production, because the recorder's default addresses point there.

## Commits

- Author and committer here are `Tekton Software <291360551+tekton-software@users.noreply.github.com>`, never a person's name. This overrides the author line in `../CLAUDE.md`. The repository's local git config is set to it; check `git config user.name` before committing.

How the maintainer likes to work, including commit message style, is in `../CLAUDE.md` on the maintainer's machine (not in this repository).
