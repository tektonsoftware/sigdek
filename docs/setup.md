[Back to the README](../README.md)

# Setting up SigDek

Needs Node.js 18 or later. Or [install the `sigdek` command](install.md) and type `sigdek` for `npx sigdek`.

## 1. Set it up in the project's top folder

```
npx sigdek init
```

You should see this on macOS or Linux. For Cursor on Windows, run `npx sigdek init --windows` instead ([Windows](windows.md) says why):

```
SigDek is set up in this repository. Commit these files:
  .claude/settings.json
  .cursor/hooks.json
  .sigdek/launch.sh
  .sigdek/launch.ps1
  .sigdek/recorder.lock
  .sigdek/README.md
  .sigdek/.gitattributes

Note: Cursor on Windows can't run these Cursor hooks. If anyone uses Cursor on Windows in this
project, run `npx sigdek init --windows` before you commit: its hooks work there too.

Next: run `npx sigdek login` to connect this machine to your team. Until then, records stay on this machine.
```

## 2. Start a new session

Start a new Claude Code or Cursor session in the project's top folder, and work as usual. A session that was already open may not have picked up the new hooks.

## 3. Connect the machine

```
npx sigdek login
```

Approve the code in your browser. You should see `Signed in as alice@acme.example to Acme. This machine's records now upload in the background.`

If you aren't in a team yet, signing in asks you to name a new one, which starts a 14-day trial with no card. To join a team that already uses SigDek, accept its invitation first.

**No account?** Skip `login`. Records stay on your machine: `npx sigdek log` shows them and `npx sigdek verify` checks the chain.

## Did it work?

`npx sigdek status` prints a checklist. After the agent's first few actions, before signing in, the checklist reads:

```
[x] Claude Code records in this project, through its hooks in .claude/settings.json
[x] Cursor records in this project, through its hooks in .cursor/hooks.json
    Cursor on Windows can't run these hooks. If anyone uses Cursor on Windows here, run
    `npx sigdek init --windows` and commit the change.
[x] SigDek 0.1.1, the recorder this project pins, is downloaded and matches its checksum
[ ] This machine isn't connected to a team, so its records stay on it
    Next: run `npx sigdek login`.
```

`[ ]` marks something missing, with the next step under it. Once signed in, what an agent did shows on the Activity page at [app.sigdek.com](https://app.sigdek.com) when it finishes its turn. [Commands](commands.md#what-status-shows) has the rest of the output, and every `sigdek` command.

## A team

Commit the files `init` lists, so everyone who pulls the project is set up. In the dashboard, an owner or admin opens People and chooses **Invite people**. Each person accepts, then runs `npx sigdek login`.

## Or the Claude Code plugin

It changes no repository, and records you in every project you open in Claude Code on this machine. In Claude Code in a terminal (for VS Code and the desktop app, see [The Claude Code plugin](commands.md#the-claude-code-plugin)):

```
/plugin marketplace add tektonsoftware/sigdek
/plugin install sigdek@sigdek
/sigdek:connect
```

## Take it out

Run `npx sigdek init --remove` and commit the change. Uninstall the plugin from `/plugin` in Claude Code.

## If something goes wrong

| What you see | What to do |
|---|---|
| `running scripts is disabled on this system` (PowerShell) | Type `npx.cmd` for `npx`. |
| `/plugin isn't available in this environment` | The `/plugin` commands are for Claude Code in a terminal. See [The Claude Code plugin](commands.md#the-claude-code-plugin). |
| `/sigdek:connect` says the recorder is still downloading | Run it again a little later, or run `npx sigdek login`. |
| Nothing is recorded, or nothing shows in Activity | Start a new session in the project's top folder, not a subfolder. Records upload when the agent finishes its turn. `npx sigdek status` says what's missing. |
| Cursor on Windows records nothing | Run `npx sigdek init --windows` and commit the change. [Windows](windows.md) |
| Claude Code on Windows records nothing | It needs Git for Windows. [Windows](windows.md) |
