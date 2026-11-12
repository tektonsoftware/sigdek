[Back to the README](../README.md)

# Windows

## Claude Code

On Windows, Claude Code runs hooks through Git Bash; without Git Bash it uses PowerShell, where SigDek records nothing but still doesn't block. So SigDek records Claude Code on Windows only where Git for Windows is installed ([git-scm.com](https://git-scm.com/downloads/win)). To check, run `git --version` in PowerShell. `sigdek status` on Windows says when it can't find Git Bash for Claude Code.

The plugin's `/sigdek:connect` also needs Git Bash on Windows; without it, run `npx sigdek login` in a terminal.

## Cursor

Cursor runs hooks through PowerShell on Windows. Windows PowerShell 5.1 can't run the sh-only commands (it has no `||`), so with them Cursor records nothing there, without blocking. `sigdek init` has three forms of Cursor hook command:

| Form | Written by | Runs on |
|---|---|---|
| sh only | `sigdek init` on macOS and Linux, or `sigdek init --sh` | macOS and Linux. Cursor on Windows records nothing with it, and isn't blocked. `sigdek init` says so when it writes it. |
| sh and PowerShell | `sigdek init --windows`, or `sigdek init` run on Windows | Windows, macOS and Linux |
| PowerShell only | `sigdek init --powershell` | Windows only. The commands need a command named `powershell`, so Cursor on macOS and Linux records nothing with them, even with PowerShell 7, which installs only `pwsh` there. |

`sigdek init` keeps the sh-and-PowerShell form when it runs again anywhere, once a project's Cursor hooks run under PowerShell, so running it again on a Mac doesn't stop Cursor recording on Windows. `sigdek init --sh` goes back to the sh-only commands.

The Cursor plugin's hooks use the sh-only commands, so the plugin records nothing in Cursor on Windows either.

### The sh-and-PowerShell commands

`sigdek init --windows` writes Cursor commands that sh and PowerShell each read as their own program: sh runs `.sigdek/launch.sh` (macOS and Linux), and PowerShell runs `.sigdek/launch.ps1` (Windows PowerShell 5.1 and PowerShell 7), so the one committed `.cursor/hooks.json` works for everyone. `.sigdek/README.md`, which `sigdek init` writes, shows both programs as each shell reads them. Both halves give Cursor a safe answer when the launcher gives none.

CI runs each command inside the PowerShell wrapper Cursor is reported to use on Windows (Cursor's docs say only that it pipes the input into the command), and with the input on stdin instead. **It hasn't been tried in the Cursor app on Windows.**

### The execution policy

The PowerShell commands run launch.ps1 with `-ExecutionPolicy RemoteSigned`, for that one process only. A project set up with SigDek 0.1.0 has `Bypass` there instead. That still works, and `sigdek status` counts it as current; running `sigdek init` again replaces it with the current commands.

launch.ps1 isn't signed, and Windows' default policy on a PC refuses every script. RemoteSigned still refuses a launch.ps1 marked as downloaded from the internet, such as one unpacked from a downloaded ZIP file; a git clone isn't marked, and `Unblock-File .sigdek\launch.ps1` removes the mark.

[Microsoft's docs](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_execution_policies) add that RemoteSigned might not run a script from a network share (a UNC path) on systems that don't tell UNC paths from internet paths, and that its zone check can fail where the Windows desktop shell isn't available, as on Windows Server Core. In those cases the sh-and-PowerShell commands give Cursor its safe answer, the `--powershell` commands give it none, and nothing is recorded; `Bypass`, which 0.1.0 wrote, ran them.

### What sigdek status checks on Windows

`sigdek status` on Windows says:

- when a project's Cursor commands can't run there, and names `--windows`;
- when `.sigdek/launch.ps1` is missing, or marked as downloaded;
- when it can't find Git Bash for Claude Code.

It can't see PowerShell refusing launch.ps1 for other reasons, and says so.

## Where Cursor on Windows records nothing

Whatever the form:

- **Cursor's CLI started from Git Bash.** When Git Bash is anywhere above `cursor-agent` in its process tree, Cursor hands its PowerShell wrapper to bash, every hook fails, and Cursor reports each tool call as "Hook blocked". That's Cursor's own bug, reported on its forum, and it hits any project's hooks. Start `cursor-agent` from PowerShell or Command Prompt instead.
- **Machines whose policies are managed.** An execution policy set by Group Policy (such as AllSigned) overrides the one the command sets, and AppLocker or WDAC can run PowerShell in Constrained Language Mode, which stops launch.ps1. Then nothing is recorded, and `sigdek status` can't tell. The sh-and-PowerShell commands give Cursor its safe answer; the `--powershell` commands give it none.
