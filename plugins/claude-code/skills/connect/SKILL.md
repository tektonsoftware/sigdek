---
name: connect
description: Connect this machine to your team's SigDek log, so the agent activity SigDek records here uploads to your team. Signs in through the browser. Run it once on each machine.
disable-model-invocation: true
allowed-tools: Bash(CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" login) Read(//tmp/claude-*/**) Read(//private/tmp/claude-*/**)
---

Connect this machine to the person's SigDek team with SigDek's own sign-in command. Follow these steps exactly. Don't run any other command, and don't change any file.

1. Run this command with the Bash tool, in the background (`run_in_background: true`), exactly as written:

   ```
   CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" login
   ```

2. Read the command's output file until it has a line that starts with `Your code:` and a line that starts with `Approve it at:`, or the command ends. The first time on a machine, SigDek downloads its recorder before it signs in, which can take several seconds, so read the file again until one of those happens.

3. Tell the person, with the link and the code exactly as printed:
   - Their browser should open SigDek's "Connect this machine?" page. If it doesn't, they can open the link.
   - If the page shows the same code, they choose **Connect machine**. SigDek may ask them to sign in first.

   Then end your turn and wait. Don't run anything else while they approve.

4. When the command finishes, tell the person what it printed after "Waiting for approval…", such as "Signed in as … to …".
   - If it ended without printing a code, tell them what it printed instead, word for word: lines that start with `SigDek:` say whether SigDek's recorder is still downloading, or couldn't be downloaded and when it tries again. Then say they can run `/sigdek:connect` again after that, or run `npx sigdek login` in a terminal instead, which doesn't need this download. If it printed nothing at all, say SigDek's recorder isn't ready yet, and give the same two choices.
   - If `sh` wasn't found (Windows without Git Bash), say to run `npx sigdek login` in a terminal instead.
   - If it failed in any other way, quote the error and suggest running `/sigdek:connect` again.
