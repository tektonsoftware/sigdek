[Back to the README](../README.md)

# Cursor

## Recording Cursor

Cursor is recorded through SigDek's Cursor hooks: those `sigdek init` writes in a repository's `.cursor/hooks.json`, or those of SigDek's Cursor plugin. To set up a project, follow [I use Cursor](setup.md#i-use-cursor).

Cursor reads `.cursor/hooks.json` from the workspace root, so open the project's top folder in Cursor, not a folder inside it.

## Cursor also runs Claude Code's hooks

Cursor also runs Claude Code's hooks by default (its Third-Party Imports setting loads `.claude/settings.json`, `.claude/settings.local.json` and `~/.claude/settings.json`), so in a repository set up with `sigdek init` each Cursor action reaches SigDek's Claude Code hook as well as its Cursor hook. SigDek's Claude Code hook stands aside when Cursor runs it, so each Cursor action is recorded once, by the Cursor hook; Cursor's actions are recorded only through SigDek's Cursor hooks (`sigdek init` or the Cursor plugin).

## The Cursor plugin

SigDek's Cursor plugin isn't listed on the Cursor Marketplace yet (Cursor reviews each plugin by hand). Until it is, set up each repository with `sigdek init`, or, on Cursor's Teams and Enterprise plans, add it to a team marketplace with **Import from Repo** and `https://github.com/tektonsoftware/sigdek`, which lists the plugin in `.cursor-plugin/marketplace.json`.

The plugin's hooks use the sh-only commands, so the plugin records nothing in Cursor on Windows (it blocks nothing either). [Its README](../plugins/cursor/README.md) says what it runs, downloads, writes and sends.

## MCP tools

Cursor's general `preToolUse` and `postToolUse` hooks, which SigDek uses, don't name the MCP server, so for Cursor an MCP pattern like `github/merge_*` matches on the tool part. See [Rules](rules.md#mcp-tools-in-cursor).

## Cursor on Windows

See [Windows](windows.md). In short: set up the project with `sigdek init --windows`, or run `sigdek init` on Windows. Those hooks haven't been tried in the Cursor app on Windows yet.
