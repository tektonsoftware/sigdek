#!/usr/bin/env node
// Checks the local log a scripted agent session left behind (see run.sh): the records SigDek
// must have made, and the Block rule's refusal. Prints one line per check and exits 1 if any
// failed.
//   node scripts/agent-check/check-log.mjs <SIGDEK_HOME> <claude-code|cursor> <scratch repo>
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const [home, tool, repo] = process.argv.slice(2);
if (!home || !["claude-code", "cursor"].includes(tool) || !repo) {
  console.error("usage: node check-log.mjs <SIGDEK_HOME> <claude-code|cursor> <scratch repo>");
  process.exit(2);
}

const logDir = join(home, "log");
const records = existsSync(logDir)
  ? readdirSync(logDir)
      .filter((f) => /^events-\d{4}-\d{2}\.jsonl$/.test(f))
      .sort()
      .flatMap((f) => readFileSync(join(logDir, f), "utf8").split("\n").filter(Boolean))
      .map((l) => JSON.parse(l))
  : [];

let failed = 0;
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${!ok && detail ? ` (${detail})` : ""}`);
  if (!ok) failed++;
};

const actions = records.filter((r) => r.event === "action");
const shell = (re) => actions.filter((r) => r.action?.kind === "shell" && re.test(r.action.command?.trim() ?? ""));
const onFile = (kind) => actions.filter((r) => r.action?.kind === kind && /(^|\/)notes\.txt$/.test(r.action.path ?? ""));
const outcomes = (rs) => [...new Set(rs.map((r) => r.outcome))].join(", ") || "none";
const seen = `${records.length} records: ${[...new Set(records.map((r) => r.event))].join(", ") || "none"}`;

check(records.length > 0, `${tool} made records through SigDek's hooks`, "the log is empty, so the agent ran none of SigDek's hooks");
if (tool === "claude-code") {
  check(records.some((r) => r.event === "session_start"), "a session start", seen);
  check(records.some((r) => r.event === "session_end"), "a session end", seen);
}
check(records.some((r) => r.event === "prompt" && r.prompt?.length > 0 && /^[0-9a-f]{64}$/.test(r.prompt.hash ?? "")), "the prompt, as its length and hash", seen);
check(shell(/^ls( \.)?$/).some((r) => r.outcome === "ran"), "the shell command `ls`, ran", `outcomes: ${outcomes(shell(/^ls( \.)?$/))}`);
check(onFile("file_edit").some((r) => r.outcome === "ran"), "the write to notes.txt, ran", `outcomes: ${outcomes(onFile("file_edit"))}`);
check(onFile("file_read").some((r) => r.outcome === "ran"), "the read of notes.txt, ran", `outcomes: ${outcomes(onFile("file_read"))}`);

const blocked = actions.filter((r) => r.action?.kind === "shell" && /\bsigdek-check-blocked\b/.test(r.action.command ?? ""));
check(
  blocked.some((r) => r.outcome === "denied_by_rule" && r.rule === "release-check-block"),
  "`touch sigdek-check-blocked.txt` stopped by the Block rule",
  `outcomes: ${outcomes(blocked)}`,
);
check(!existsSync(join(repo, "sigdek-check-blocked.txt")), "the blocked command didn't run (its file doesn't exist)");

const wrongTool = records.filter((r) => r.tool !== tool);
check(wrongTool.length === 0, `every record says the agent is ${tool}`, `${wrongTool.length} say ${[...new Set(wrongTool.map((r) => r.tool))].join(", ")}`);
const noSession = records.filter((r) => !r.session);
check(noSession.length === 0, "every record has a session", `${noSession.length} without`);
const remote = records.filter((r) => !/example-org\/sigdek-release-check/.test(r.repo?.remote ?? ""));
check(remote.length === 0, "every record names the scratch repository", `${remote.length} name ${[...new Set(remote.map((r) => r.repo?.remote))].join(", ")}`);

const errorsLog = join(home, "errors.log");
const errors = existsSync(errorsLog) ? readFileSync(errorsLog, "utf8").trim() : "";
check(errors === "", "nothing in the recorder's errors.log", errors.split("\n").slice(-3).join(" | "));

console.log(`${tool}: ${seen}`);
process.exit(failed ? 1 : 0);
