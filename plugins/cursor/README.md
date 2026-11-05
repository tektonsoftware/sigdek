# SigDek for Cursor

Records what Cursor's agents do in your repositories (who, which tool, the command or file, and whether it ran or was blocked) for your team's audit evidence. Prompt text, file contents and command output are never recorded, and secrets are stripped from commands.

Install it from the Cursor Marketplace, or add it to your team marketplace from github.com/tektonsoftware/sigdek (plugins/cursor).

On first use the plugin downloads the recorder version pinned in `scripts/recorder.lock` and checks its SHA-256. Run `sigdek login` to connect to your team; until then, records stay on your machine. In a repository set up with `sigdek init`, the plugin steps aside when the workspace is the repository's root, so nothing is recorded twice; in a workspace opened on a subdirectory, the repository's hooks don't run, so the plugin records.

On Windows, Cursor runs hooks through PowerShell, and Windows PowerShell 5.1 can't run this plugin's hooks (it has no `||`), so the plugin records nothing there, without blocking anything. To record Cursor on Windows, set up the repository with `sigdek init --powershell`.

More: https://github.com/tektonsoftware/sigdek
