# sigdek

The command-line recorder for [SigDek](https://sigdek.com): it records what AI coding agents (Claude Code, Cursor) do on your machine and sends it to your team's audit log.

```sh
npx sigdek init     # in a repository: record every agent session for everyone who clones it
npx sigdek login    # connect this machine to your team
npx sigdek status   # what is set up and working
```

This package holds a small launcher; the recorder itself is a single Go binary built for your platform, installed from one of the `@tektonsoftware/sigdek-*` packages. Prompt text, file contents and command output are never recorded, and secrets are stripped from commands before anything is written.

Source, schema and security notes: https://github.com/tektonsoftware/sigdek

SigDek is a product of Tekton Software LLC.
