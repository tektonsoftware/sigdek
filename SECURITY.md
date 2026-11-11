# Security

SigDek runs on developer machines and records what coding agents do, so we treat bugs in it seriously.

## Reporting a vulnerability

Email **security@sigdek.com** with a description and, if you can, steps to reproduce. Please don't open a public issue for a security problem. We'll acknowledge your report within two business days and keep you updated until it's fixed.

## What's in scope

- The recorder (`cmd/`, `internal/`), the launchers and the plugins in this repository
- Ways to make the recorder block an action it shouldn't, let through an action a Block rule should stop, leak a secret it should have stripped, or accept a tampered record as valid
- The hosted service at sigdek.com, app.sigdek.com and ingest.sigdek.com

## What SigDek does and doesn't promise

- **Anyone using a machine can switch the recorder off**, not only an admin: by removing the hooks, uninstalling the plugin, pointing `SIGDEK_BIN` at another program, or editing the agent's settings. SigDek makes this visible rather than impossible: the built-in rules flag an agent editing SigDek's own setup, and the hosted Coverage report shows a machine that goes silent.
- **Records are tamper-evident once SigDek has received them.** The machine's local log can be rewritten by someone with access to it; the server catches any later change, gap or fork.
- **Block rules read a command the way the shell does**, whole and however long: line continuations, quotes, backslashes, padding, an empty substitution (`r$()m`), `$IFS` in place of a space and a quoted `git -C 'my repo'` don't hide it. They can't see a command put together at run time or by expansion: a variable (`X=rm; $X -rf`), `eval` of a string, a script piped from a decoder, brace expansion (`{rm,-rf,/x}`), a glob that names the program (`/bin/r? -rf`), or a command that a script in another language builds and runs. Rules are checks on what the agent asks for, not a sandbox.
- **Redaction is best effort.** It catches common credential formats, not every secret.
- **The stored hashes can confirm a guess.** A record keeps each prompt's SHA-256 and, for each action, `input_hash`: the SHA-256 of the tool input exactly as the agent sent it, which also covers parts that are never stored, such as a file edit's contents. Someone with the record who can guess the whole input can confirm the guess, so a short or common prompt or command can be worked out. When a secret was stripped from an action's command, path or URL, `input_hash` is instead the SHA-256 of the stored, redacted action: the RFC 8785 canonical JSON of an object holding its `kind`, `name`, and `command`, `path` and `url` where present. That form holds no stripped secret, so it can't confirm a guessed one, and anyone holding the record can recompute it. A record whose `redactions` count is above 0 holds this form. A secret that redaction misses, or that sits past a field's size limit (and so is cut rather than stripped), stays covered by the hash of the input as sent.
- **Logout revokes the token on a best-effort basis**: it says when the server didn't confirm the revocation, and an owner or admin can revoke a machine in the dashboard.

## How releases are protected

- Each release publishes `checksums.txt` with the SHA-256 of every binary.
- Releases are immutable: once a release is published, its files and tag can't be changed, and GitHub attaches an attestation to each. `gh release verify <tag>` checks a release's attestation, and `gh release verify-asset <tag> <file>` checks a downloaded file against it.
- `install.sh` (macOS and Linux) refuses a binary whose SHA-256 differs from its line in the same release's `checksums.txt`. The checksum still comes from the same release as the binary, so it catches a damaged or wrong download, not a release that was bad when it was published. The script itself reaches you through sigdek.com's redirect to the release.
- The launchers download only the version pinned in `.sigdek/recorder.lock` (written by `sigdek init`) or, for a plugin, in the plugin's own `scripts/recorder.lock`, and refuse any binary whose SHA-256 differs.
- The recorder has no third-party Go dependencies.
