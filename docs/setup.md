[Back to the README](../README.md)

# Setting up SigDek

There are three ways to start. Using Cursor? Pick [I use Cursor](#i-use-cursor). Otherwise pick [Our team's project](#our-teams-project) if you can change the project and want everyone who works in it recorded, or [Just me, in Claude Code](#just-me-in-claude-code) to record only yourself.

## Where commands go

Every command below says where it goes:

- **In Claude Code's chat:** type it where you talk to Claude Code, then press Return (Enter on Windows).
- **In a terminal:** on macOS, press Command and Space, type Terminal, and press Return. On Windows, open Start, type PowerShell, and choose Windows PowerShell. On Linux, open Terminal from your apps. In VS Code or Cursor, open your project's folder, then choose Terminal, then New Terminal: it opens in the project's folder already. Elsewhere, go to the folder first: type `cd` and a space, drag the folder onto the window, and press Return.

## Do you have Node.js?

The `npx` commands need it. In a terminal, run `node -v`. You should see a version such as `v24.6.0`; v18 or later is fine. If it says `command not found: node` (macOS), `node: command not found` (Linux) or `The term 'node' is not recognized` (Windows), install Node.js from [nodejs.org](https://nodejs.org/en/download): under "Or get a prebuilt Node.js", choose the macOS Installer (.pkg) or the Windows Installer (.msi), not the commands above it, then open a new terminal. Or [install the `sigdek` command](install.md) (the install script and Scoop need no administrator), and type `sigdek` wherever these steps say `npx sigdek`.

The first time you run `npx sigdek`, npx asks before it downloads SigDek:

```
Need to install the following packages:
sigdek@0.1.1
Ok to proceed? (y)
```

Press Return (Enter on Windows) to say yes. The version is the latest one on npm when you run it. npm may also suggest updating npm; you can ignore that. On Windows, if PowerShell says running scripts is disabled, type `npx.cmd` instead of `npx`.

## Just me, in Claude Code

The Claude Code plugin records you in every project you open in Claude Code on this machine. It's for Claude Code only.

1. **Add the recorder.** This step depends on where you use Claude Code.

   **In a terminal.** Start Claude Code: type `claude` and press Return. Then, in Claude Code's chat:

   ```
   /plugin marketplace add tektonsoftware/sigdek
   ```

   You should see `Successfully added marketplace: sigdek`. Then, in Claude Code's chat:

   ```
   /plugin install sigdek@sigdek
   ```

   Claude Code shows what the plugin adds. Choose **Install for you (user scope)**. The summary starts with `✓ Installed sigdek.` and ends with `Plugin is now active.`, or with `Run /reload-plugins to apply.`, and in that case Claude Code reloads for you. (These two outputs are as Claude Code's plugin docs give them, not captured from a chat here. `claude plugin marketplace add tektonsoftware/sigdek`, the same step typed in a terminal, printed `✔ Successfully added marketplace: sigdek (declared in user settings)`.)

   **In VS Code.** In the Claude Code panel, type `/plugins`. Manage plugins opens. On its Marketplaces tab, enter `tektonsoftware/sigdek`. Then on the Plugins tab, search for **sigdek**, choose **Install**, then **Install for you**.

   **In the desktop app.** Claude Code's docs describe the desktop app's plugin browser (the **+** button, then **Add plugins**) but not adding a marketplace from a GitHub repository there. So do this step once in Claude Code in a terminal or in VS Code. After that the plugin works in the desktop app too: on one computer, Claude Code in a terminal, in VS Code and in the desktop app share the plugins you install for yourself. Cloud sessions, including claude.ai/code, don't load plugins installed on your machine.

2. **Connect your machine.** In Claude Code's chat:

   ```
   /sigdek:connect
   ```

   Claude starts SigDek's sign-in and shows you a link and a code, and your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If the page shows the same code, choose **Connect machine**. Claude then passes on what SigDek prints, such as `Signed in as alice@acme.example to Acme. This machine's records now upload in the background.` Claude's own words vary; SigDek's line doesn't.

   The first time on a machine, the plugin downloads its recorder before it signs in, which can take several seconds. If the recorder is still downloading, or couldn't be downloaded, Claude says so and you can run `/sigdek:connect` again a little later. If Claude Code doesn't know `/sigdek:connect`, or says to use a terminal, run `npx sigdek login` in a terminal instead, as in [Our team's project](#our-teams-project), step 2. `/sigdek:connect` needs `sh`, so on Windows without Git Bash, use `npx sigdek login`.

3. **Watch Activity.** Use Claude Code as usual. When the agent finishes its turn, what it did uploads and shows on the Activity page of your team's dashboard at [app.sigdek.com](https://app.sigdek.com). To check this machine, see [Did it work?](#did-it-work)

On Windows, SigDek records Claude Code only where Git for Windows is installed ([git-scm.com](https://git-scm.com/downloads/win)). To check, run `git --version` in PowerShell; if it isn't recognized, install Git for Windows.

## Our team's project

Set it up once in the project, and everyone who works in it is recorded in Claude Code and Cursor, with nothing to install but one sign-in each. This changes the project for everyone who works in it: check with whoever looks after it first.

1. **Add the recorder.** In a terminal, in your project's folder (the folder you open in VS Code or Cursor for this project; it has a `.git` folder in it, which is hidden):

   ```
   npx sigdek init
   ```

   You should see, on macOS or Linux:

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

   If anyone uses Cursor on Windows in this project, run `npx sigdek init --windows` now, before you commit. See [Cursor on Windows](#cursor-on-windows) below. Run on Windows, `npx sigdek init` writes those hooks without the flag, and prints a line that says Cursor records on all three systems instead of the note.

   Then commit the files it added, in the same terminal:

   ```
   git add .claude/settings.json .cursor/hooks.json .sigdek
   git commit -m "Record agent activity with SigDek"
   ```

   You should see a line like ` 7 files changed, 601 insertions(+)` (the numbers can differ). If SigDek warned that git ignores a file, run the `git add -f` command it printed, then commit again. Then share the change with your team. Most teams share changes with a pull request; ask if you're not sure.

2. **Connect your machine.** In a terminal, in any folder:

   ```
   npx sigdek login
   ```

   You should see, with your own code:

   ```
   Your code: ABCD-EFGH
   Approve it at: https://app.sigdek.com/device?user_code=ABCDEFGH

   Waiting for approval…
   ```

   Your browser opens SigDek's "Connect this machine?" page (sign in first if it asks). If it doesn't open, open the link yourself. If the page shows the same code, choose **Connect machine**. The terminal then says:

   ```
   Signed in as alice@acme.example to Acme. This machine's records now upload in the background.
   ```

   If the machine recorded anything before you connected it, one more line says what happens to those records. Nothing to do.

   If you have SigDek's Claude Code plugin, `/sigdek:connect` in Claude Code's chat does the same, in this project too.

3. **Invite your team.** In the dashboard, an owner or admin opens People and chooses **Invite people**. Each teammate accepts, gets the change from step 1, and runs `npx sigdek login` once on their own machine. From then on, Claude Code and Cursor record for them whenever they start in the project's top folder.

Then **watch Activity**: when an agent finishes its turn, what it did uploads and shows on the dashboard's Activity page.

### Cursor on Windows

Cursor on Windows runs hooks through PowerShell, and Windows PowerShell 5.1 can't run the sh-only hooks that plain `npx sigdek init` writes on macOS and Linux, so with those Cursor records nothing on Windows (it isn't blocked). Claude Code isn't affected either way.

- `npx sigdek init --windows` writes Cursor hooks that run under sh and under PowerShell, so Cursor records on Windows, macOS and Linux. `npx sigdek init` run on Windows writes them too.
- On macOS and Linux, plain `npx sigdek init` writes the sh-only hooks, and says so, unless the project's Cursor hooks already run under PowerShell; then it keeps hooks that run under both.
- A project set up without them can be fixed later: run `npx sigdek init --windows` in its top folder and commit the change. On Windows, `npx sigdek status` says when a project needs it.
- These hooks haven't been tried in the Cursor app on Windows yet. CI runs them the way Cursor is reported to run hooks on Windows.

[Windows](windows.md) has the details, including the PowerShell-only form (`--powershell`) and where Cursor on Windows records nothing whatever the form.

## I use Cursor

SigDek's Cursor plugin isn't on Cursor's Marketplace yet. Until it is, Cursor users set up the project itself: follow [Our team's project](#our-teams-project), in Cursor. In Cursor, open your project's folder (File, then Open Folder), then choose Terminal, then New Terminal; it opens in the project's folder. Open the project's folder, not a folder inside it: Cursor reads SigDek's setup from the folder it opened. This records Claude Code in the project too, for everyone who works in it.

If anyone uses Cursor on Windows in the project, set it up with `npx sigdek init --windows`; see [Cursor on Windows](#cursor-on-windows). [Cursor](cursor.md) has more on how SigDek and Cursor fit together, and on adding the plugin to a team marketplace.

## Did it work?

In a terminal, in your project's folder:

```
npx sigdek status
```

Status answers as a checklist, without going online: it reads only files on this machine. Each item that's missing has the next step under it. When everything is set up, it looks like this:

```
sigdek 0.1.1 (darwin/arm64)

[x] Claude Code records in this project, through its hooks in .claude/settings.json
[x] Cursor records in this project, through its hooks in .cursor/hooks.json
    Cursor on Windows can't run these hooks. If anyone uses Cursor on Windows here, run
    `npx sigdek init --windows` and commit the change.
[x] SigDek 0.1.1, the recorder this project pins, is downloaded and matches its checksum
[x] Connected to Acme as alice@acme.example
[x] Uploads work: the last one finished just now, and nothing is waiting

Rules       12 flag, 0 block (built-in defaults)
Records     0 on this machine
Errors      none
Device      dev_example, uploading to https://ingest.sigdek.com
```

That's from a Mac, in a project whose Cursor hooks run only on macOS and Linux; with `--windows` hooks the note under Cursor isn't there. Under the checklist are the details support asks for: the rules in use, the records on this machine, the last error and the device. (The two connected lines and the device line came from a device file made by hand, not from a real sign-in, so the device id is invented. Signed in, they show your team, your email and your device.)

`[ ]` marks something missing, with the next step under it. For example, before the machine is connected:

```
[ ] This machine isn't connected to a team, so its records stay on it
    Next: run `npx sigdek login`.
```

and before the first agent runs in the project:

```
[ ] SigDek 0.1.1, the recorder this project pins, isn't downloaded yet
    It downloads, and is checked, the first time Claude Code or Cursor runs here.
```

`[-]` marks something you may not need, such as Cursor for someone who uses only Claude Code. With the plugin, the first line says Claude Code records through SigDek's plugin, and names the settings file that turns it on.

## If something goes wrong

| What you see | What to do |
|---|---|
| `command not found: npx` (macOS), `npx: command not found` (Linux) or `The term 'npx' is not recognized` (Windows) | Node.js isn't installed, or the terminal was opened before it was. Install it from [nodejs.org](https://nodejs.org/en/download), then open a new terminal. Or [install the `sigdek` command](install.md) and drop the `npx`. |
| `command not found: sigdek` (macOS), `sigdek: command not found` (Linux) or `The term 'sigdek' is not recognized` (Windows) | The `sigdek` command isn't installed, so type `npx sigdek …` instead, for example `npx sigdek login`. Or [install the command](install.md). |
| `running scripts is disabled on this system` (PowerShell) | Windows is blocking `npx`'s PowerShell script. Type `npx.cmd` instead of `npx`, for example `npx.cmd sigdek login`. Or allow such scripts for your account, once, with `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`; if it asks whether to change the execution policy, answer `Y` and press Enter. |
| `command not found: claude` (macOS), `claude: command not found` (Linux) or `The term 'claude' is not recognized` (Windows) | Claude Code isn't installed for the terminal. If you use it in VS Code or the desktop app, follow the steps for those in [Just me, in Claude Code](#just-me-in-claude-code) instead. Or install it for the terminal with [Claude Code's setup guide](https://code.claude.com/docs/en/setup). |
| `/plugin isn't available in this environment` | You're in Claude Code's desktop app or its VS Code panel (or a cloud session), where the `/plugin` commands don't run. Use the steps for VS Code or the desktop app in [Just me, in Claude Code](#just-me-in-claude-code). |
| `Unknown command: /plugin` in Claude Code | Your Claude Code is too old for plugins. In a terminal, run `claude update`, then start Claude Code again. In the desktop app or VS Code, update the app or the extension. |
| `no such file or directory: /plugin` (macOS), `/plugin: No such file or directory` (Linux) or `The term '/plugin' is not recognized` (Windows) | The `/plugin` commands go in Claude Code's chat, not in a terminal. Type `claude` and press Return to start Claude Code, then type them there. |
| ``run `npx sigdek init` inside a git repository`` (``run `sigdek init` inside a git repository`` with the command installed) | The terminal isn't in your project's folder. Open it from VS Code or Cursor with the project open, or go to the folder with `cd`. The project setup needs a project kept in git. |
| `The following paths are ignored by one of your .gitignore files` | The project tells git to leave out some of SigDek's files, so teammates wouldn't get them. `sigdek init` warned about this and printed a `git add -f` command: check with whoever looks after the project, then run that command and commit. |
| The browser didn't open when connecting your machine | Copy the link after `Approve it at:` into your browser, sign in, check the code and choose **Connect machine**. The terminal finishes on its own. |
| `/sigdek:connect` says SigDek's recorder is still downloading, or couldn't be downloaded | The plugin downloads its recorder the first time it runs on a machine. Run `/sigdek:connect` again a little later, or run `npx sigdek login` in a terminal, which doesn't need that download. |
| Nothing shows in Activity | Records upload when the agent finishes its turn, so wait until its reply is done. Then run `npx sigdek status` in the project's folder: each line marked `[ ]` says what's missing and the next step. If it says this machine isn't connected to a team, connect it again. With the project setup, start Claude Code or Cursor in the project's folder. |
| Cursor on Windows records nothing | Run `npx sigdek status` in the project's folder. If it says Cursor on Windows can't run this project's Cursor hooks, run `npx sigdek init --windows` there and commit the change. See [Cursor on Windows](#cursor-on-windows) and [Windows](windows.md). |
