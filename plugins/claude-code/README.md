# SigDek for Claude Code

Records what Claude Code's agents do in your repositories (who, which tool, the command or file, and whether it ran or was blocked) for your team's audit evidence. Prompt text, file contents and command output are never recorded, and secrets are stripped from commands.

```
/plugin marketplace add tektonsoftware/sigdek
/plugin install sigdek@sigdek
```

On first use the plugin downloads the recorder version pinned in `scripts/recorder.lock` and checks its SHA-256. Run `npx sigdek login` to connect to your team; until then, records stay on your machine. In a repository set up with `sigdek init`, the plugin steps aside when a session starts at the repository's root, so nothing is recorded twice; in a session started in a subdirectory, the repository's hooks don't run, so the plugin records.

More: https://github.com/tektonsoftware/sigdek
