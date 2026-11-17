# Security

SigDek runs on developer machines and records what coding agents do, so we treat bugs in it seriously.

## Reporting a vulnerability

Please don't open a public issue for a security problem. Report it privately, in either of these ways:

- **On GitHub:** [report a vulnerability](https://github.com/tektonsoftware/sigdek/security/advisories/new) with GitHub's private vulnerability reporting (the repository's Security tab, then "Report a vulnerability").
- **By email:** write to **security@sigdek.com**.

Include a description and, if you can, steps to reproduce. We'll acknowledge your report within two business days and keep you updated until it's fixed.

## What's in scope

- The recorder (`cmd/`, `internal/`), the launchers and the plugins in this repository
- Ways to make the recorder block an action it shouldn't, let through an action a Block rule should stop, leak a secret it should have stripped, or accept a tampered record as valid
- The hosted service at sigdek.com, app.sigdek.com and ingest.sigdek.com

## What SigDek does and doesn't promise

- **Anyone using a machine can switch the recorder off**, not only an admin: by removing the hooks, uninstalling the plugin, pointing `SIGDEK_BIN` at another program, or editing the agent's settings. SigDek makes this visible rather than impossible: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.
- **Records are tamper-evident once SigDek has received them.** The machine's local log can be rewritten by someone with access to it; the server catches any later change, gap or fork.
- **Block rules read a command the way the shell does**, whole, up to 8 MB of hook input: line continuations, quotes, backslashes, padding, an empty substitution (`r$()m`), `$IFS` in place of a space and a quoted `git -C 'my repo'` don't hide it. The hook reads no more than 8 MB of input and notes the cut in `~/.sigdek/errors.log`, so a rule can't see the rest of a longer command. They can't see a command put together at run time or by expansion: a variable (`X=rm; $X -rf`), `eval` of a string, a script piped from a decoder, brace expansion (`{rm,-rf,/x}`), a glob that names the program (`/bin/r? -rf`), or a command that a script in another language builds and runs. Rules are checks on what the agent asks for, not a sandbox.
- **Patterns are case-sensitive**, apart from the program name at the start of each command, which is also read in lower case. A rule `drop table` misses `DROP TABLE`, and on macOS and Windows, where file names ignore case, a path rule `**/prod-secrets.yml` misses `PROD-SECRETS.yml`. A pattern that starts with `re:(?i)` ignores case.
- **Command rules check the commands an agent runs through its shell tool:** Claude Code's Bash and Cursor's shell. From 0.1.2 they also check commands sent through Claude Code's Monitor and PowerShell tools, or any other of its tools (MCP tools aside) whose input is a `command`, and these are recorded with their command like Bash commands. Before 0.1.2, those two are recorded by tool name only, with no command, and no command rule sees them; a Block rule on the tool name `Monitor` or `PowerShell` stops them. From 0.1.2, a Monitor call that watches a WebSocket (its `ws` input) instead of running a command is a web action: URL rules judge its address, and the record keeps the address with secrets stripped. A PowerShell command is read the way sh would read it, and the built-in rules are written for sh, so they don't catch PowerShell cmdlets such as `Remove-Item -Recurse`.
- **Path rules judge the path a tool names**, not the files it goes on to read. From 0.1.2, a Grep or Glob search is also judged by its folder (the session's folder when it names none) joined with its glob, so a search with the glob `.env`, `**/.env`, `.env.*`, `*.pem` or `*.key` meets the built-in Secrets file rule. A glob that reaches a secret file only through wildcards or braces, such as `.env*`, `id_*` or `{.env,.npmrc}`, can get past that rule, and a search with no glob is judged by its folder alone, so it can return lines from a secrets file in that folder. Before 0.1.2, a search is judged by the folder it names, and one that names no folder meets no path rule.
- **Redaction is best effort.** It catches common credential formats, not every secret. From 0.1.2 it also catches passwords in variables named like `DB_PASS` or `MYSQL_PWD`, the value of a `*_KEY` variable when it looks generated, flags such as `--http-password`, the passwords given to `redis-cli -a` and `mongosh -p`, Vault tokens, and a long token used as a URL's user name.
- **The stored hashes can confirm a guess.** A record keeps each prompt's SHA-256 and, for each action, `input_hash`: the SHA-256 of the tool input exactly as the agent sent it, which also covers parts that are never stored, such as a file edit's contents. Someone with the record who can guess the whole input can confirm the guess, so a short or common prompt or command can be worked out. When a secret was stripped from an action's command, path or URL, `input_hash` is instead the SHA-256 of the stored, redacted action: the RFC 8785 canonical JSON of an object holding its `kind`, `name`, and `command`, `path` and `url` where present. That form holds no stripped secret, so it can't confirm a guessed one, and anyone holding the record can recompute it. A record whose `redactions` count is above 0 holds this form. A secret that redaction misses, or that sits past a field's size limit (and so is cut rather than stripped), stays covered by the hash of the input as sent.
- **Logout revokes the token on a best-effort basis**: it says when the server didn't confirm the revocation, and an owner or admin can revoke a machine in the dashboard.

## How releases are protected

- Each release publishes `checksums.txt` with the SHA-256 of every binary.
- Releases are immutable: once a release is published, its files and tag can't be changed, and GitHub attaches an attestation to each. `gh release verify <tag>` checks a release's attestation, and `gh release verify-asset <tag> <file>` checks a downloaded file against it.
- `install.sh` (macOS and Linux) refuses a binary whose SHA-256 differs from its line in the same release's `checksums.txt`. The checksum still comes from the same release as the binary, so it catches a damaged or wrong download, not a release that was bad when it was published. The script itself reaches you through sigdek.com's redirect to the release.
- The launchers download only the version pinned in `.sigdek/recorder.lock` (written by `sigdek init`) or, for a plugin, in the plugin's own `scripts/recorder.lock`, and refuse any binary whose SHA-256 differs.
- Plugin installs and updates, and the Scoop bucket, follow this repository's `main` branch: the plugin's hooks, its launcher and its `scripts/recorder.lock`, and Scoop's manifest (`bucket/sigdek.json`) with its download URLs and checksums, come from `main` as it is when you install or update, not from a release. So `main`'s protection is part of the supply chain.
- The recorder has no third-party Go dependencies.
