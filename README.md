# <img src="plugins/claude-code/assets/icon.png" width="40" height="40" alt="" align="top"> SigDek recorder

[![Latest release](https://img.shields.io/github/v/release/tektonsoftware/sigdek)](https://github.com/tektonsoftware/sigdek/releases/latest)
[![CI on main](https://github.com/tektonsoftware/sigdek/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/tektonsoftware/sigdek/actions/workflows/ci.yml?query=branch%3Amain)
[![npm](https://img.shields.io/npm/v/sigdek)](https://www.npmjs.com/package/sigdek)
[![License](https://img.shields.io/github/license/tektonsoftware/sigdek)](LICENSE)

SigDek records what AI coding agents do on your team's machines (who ran which agent, which tool, the command or file, and whether it ran or was blocked) and turns that record into evidence an auditor or a customer can read. It never records prompt text, file contents or command output.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/activity-blocked-dark-2x.webp">
  <img src="docs/images/activity-blocked-light-2x.webp" width="640" alt="Three rows of SigDek's Activity page, filtered to actions a rule matched: Dave's force push, flagged; Ivan's terraform destroy -target=module.cache, marked Blocked by rule with the rule No terraform destroy; and Grace reading config/secrets.yaml, flagged.">
</picture>

<sub>The Activity page of the hosted dashboard, for a demo team with invented people.</sub>

## Quick start

```sh
# in your project's top folder
npx sigdek init
# start a new agent session there
npx sigdek login
```

- `init` sets up Claude Code and Cursor, and `login` connects this machine to your team. Needs Node.js 18 or later.
- **No account?** Skip `login`. Records stay on your machine: `npx sigdek log` shows them and `npx sigdek verify` checks the chain.
- **Or the Claude Code plugin** (in Claude Code in a terminal, no change to the repository): `/plugin marketplace add tektonsoftware/sigdek`, `/plugin install sigdek@sigdek`, then `/sigdek:connect`.
- **Did it work?** `npx sigdek status`. The [setup guide](docs/setup.md) covers a team, taking it out, and fixes.

## Free and paid

- **Free:** the recorder in this repository is open source (Apache-2.0). It records to a local log on your machine, with no account.
- **Paid:** the dashboard, your team's own Flag and Block rules, and reports come with a SigDek team. Team is $99 a month for up to 10 people and Business $199 for up to 50, after a 14-day trial with no card. [Pricing](https://sigdek.com/pricing/)

## Getting the sigdek command

`npx sigdek` needs nothing installed but Node.js. To install a `sigdek` command instead (npm, an install script for macOS and Linux, Scoop, Go, or a release binary), see [Installing the sigdek command](docs/install.md).

## Docs

- [Setup guide](docs/setup.md), [commands](docs/commands.md), [Cursor](docs/cursor.md) and [Windows](docs/windows.md)
- [What it records, and what it never does](docs/recording.md), [rules](docs/rules.md) and [how it works](docs/how-it-works.md)
- [What SigDek can't guarantee](SECURITY.md), and how to report a vulnerability
- [Developing SigDek](docs/develop.md), and [sigdek.com/docs](https://sigdek.com/docs/) for the hosted service

## License

Apache-2.0. Copyright 2026 Tekton Software LLC. SigDek is a product of Tekton Software.
