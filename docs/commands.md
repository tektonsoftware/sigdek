[Back to the README](../README.md)

# Commands

## The sigdek command

Type `npx sigdek` for `sigdek`, unless you [installed the command](install.md).

| Command | What it does |
|---|---|
| `sigdek init` | Add SigDek's hooks to the current repository. `--windows`, `--sh` and `--powershell` choose the form of the Cursor hooks (see [Windows](windows.md)); `--tools` picks Claude Code, Cursor or both; `--offline` pins only this machine's platform. `--remove` takes the hooks out, and `.sigdek/` once no hooks use it. `sigdek init -h` lists every option |
| `sigdek login` / `logout` | Connect or disconnect this machine; sign-in happens in the browser. Logout asks the server that signed it in to revoke the machine's token and says if it couldn't. Agents are still recorded after logout, on the machine only; those records upload if it signs in to the same team again, as long as SigDek already has a record from this machine; login says how many upload and how many stay |
| `sigdek status` | A checklist of what is set up and working, with the next step for anything that isn't. It reads only local files |
| `sigdek log` | Recent records from this machine (`-n` for how many, `--json` for the raw records) |
| `sigdek verify` | Recompute the local hash chain and list any record that was changed, or removed from the middle. It can't see records removed from the end of the log; the hosted service catches that for records it has received |
| `sigdek flush` | Upload now (normally automatic, in the background), without waiting out a back-off |
| `sigdek version` | The version and platform |

## What status shows

`[x]` marks something that works, `[ ]` something missing, with the next step under it, and `[-]` something you may not need, such as Cursor for someone who uses only Claude Code. Under the checklist are the details support asks for: the rules in use, the records on this machine, the last error and the device.

Right after `npx sigdek init` on a Mac, before any agent has run in the project and before signing in:

```
sigdek 0.1.1 (darwin/arm64)

[x] Claude Code records in this project, through its hooks in .claude/settings.json
[x] Cursor records in this project, through its hooks in .cursor/hooks.json
    Cursor on Windows can't run these hooks. If anyone uses Cursor on Windows here, run
    `npx sigdek init --windows` and commit the change.
[ ] SigDek 0.1.1, the recorder this project pins, isn't downloaded yet
    It downloads, and is checked, the first time Claude Code or Cursor runs here.
[ ] This machine isn't connected to a team, so its records stay on it
    Next: run `npx sigdek login`.
[ ] No records on this machine yet
    Next: start Claude Code or Cursor here and give the agent a task.

Rules       12 flag, 0 block (built-in defaults)
Records     0 on this machine
Errors      none
Device      not set up yet (it is created the first time an agent runs here)
```

With `--windows` hooks, the note under Cursor isn't there. With the plugin, the first item says Claude Code records through SigDek's plugin, and names the settings file that turns it on.

Once the machine is signed in:

- the connected item reads `[x] Connected to Acme as alice@acme.example`, with your team and your email;
- an item on uploads follows it. When everything works, it reads `[x] Uploads work: the last one finished just now, and nothing is waiting`. Before the first upload, it says why nothing has uploaded yet, and what to do;
- the Rules line names your team's rules and their version, such as `12 flag, 1 block (your team's rules, version 3)`;
- the Device line adds where it uploads, such as `uploading to https://ingest.sigdek.com`.

## The Claude Code plugin

Installed for you, the plugin records you in every project you open in Claude Code on this machine and changes no repository. It's for Claude Code only. Where you add it depends on where you use Claude Code:

- **In a terminal:** in Claude Code, run `/plugin marketplace add tektonsoftware/sigdek`, then `/plugin install sigdek@sigdek`, and choose **Install for you (user scope)**.
- **In VS Code:** in the Claude Code panel, type `/plugins`. Manage plugins opens. On its Marketplaces tab, enter `tektonsoftware/sigdek`. Then on the Plugins tab, search for **sigdek**, choose **Install**, then **Install for you**.
- **In the desktop app:** Claude Code's docs describe the desktop app's plugin browser (the **+** button, then **Add plugins**) but not adding a marketplace from a GitHub repository there. So add the plugin once in a terminal or in VS Code. On one computer, all three share the plugins you install for yourself.
- **Cloud sessions,** including claude.ai/code, don't load plugins installed on your machine.

Then `/sigdek:connect` signs the machine in, as `npx sigdek login` does: Claude shows a link and a code, and you approve the same code in the browser. As with `login`, only what agents do after that uploads.

- The first time on a machine, the plugin downloads its recorder before it signs in. If Claude says the recorder is still downloading, or couldn't be downloaded, run `/sigdek:connect` again a little later, or run `npx sigdek login`, which doesn't need that download.
- `/sigdek:connect` needs `sh`, so on Windows without Git Bash, run `npx sigdek login`.

To take the plugin out, uninstall it from `/plugin` in Claude Code. [Plugin and project hooks together](how-it-works.md#plugin-and-project-hooks-together) says what happens in a project that also has `sigdek init`, and [the plugin's README](../plugins/claude-code/README.md) what it runs, downloads, writes and sends.
