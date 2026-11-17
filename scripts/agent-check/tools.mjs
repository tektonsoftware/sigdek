#!/usr/bin/env node
// Checks that every Claude Code tool that takes a shell command is one SigDek's hook treats as a
// command, so a team's Block rule on that command stops it whichever tool the agent picks
// (BB-RULE-1: Monitor and PowerShell once ran commands that no rule saw).
//
// It reads Claude Code's own tool list, input schemas included, by running `claude -p` against
// a stand-in for the Messages API on 127.0.0.1 that records the request and answers "ok", so it
// needs no API key and costs nothing. Every tool whose input has a string `command`, MCP tools
// aside, is then sent to the hook as a PreToolUse with a command the Block rule in
// SIGDEK_HOME/rules.json refuses (see run.sh); the hook must refuse it and record it as a shell
// action. It also checks that Monitor still names the WebSocket it watches as ws.url, the field
// the hook reads so URL rules see it (QA3-REC-1). Prints one line per check and exits 1 if any
// failed.
//
//   node scripts/agent-check/tools.mjs <SIGDEK_HOME> <scratch repo> <hook command and its arguments>
//   e.g. node tools.mjs "$SIGDEK_HOME" "$repo" sh "$repo/.sigdek/launch.sh" hook claude-code
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [home, repo, ...hook] = process.argv.slice(2);
if (!home || !repo || hook.length === 0) {
  console.error("usage: node tools.mjs <SIGDEK_HOME> <scratch repo> <hook command...>");
  process.exit(2);
}

let failed = 0;
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${!ok && detail ? ` (${detail})` : ""}`);
  if (!ok) failed++;
};

// The stand-in Messages API: records each request's tools, answers every message with "ok".
const tools = new Map();
const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    let j = {};
    try {
      j = JSON.parse(body);
    } catch {}
    for (const t of j.tools ?? []) if (t?.name) tools.set(t.name, t);
    if (!req.url.startsWith("/v1/messages") || req.url.includes("count_tokens")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(req.url.includes("count_tokens") ? '{"input_tokens":1}' : "{}");
      return;
    }
    const msg = {
      id: "msg_agent_check", type: "message", role: "assistant", model: j.model ?? "stand-in",
      content: [{ type: "text", text: "ok" }], stop_reason: "end_turn", stop_sequence: null,
      usage: { input_tokens: 1, output_tokens: 1 },
    };
    if (!j.stream) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(msg));
      return;
    }
    res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
    const send = (type, data) => res.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...data })}\n\n`);
    send("message_start", { message: { ...msg, content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } } });
    send("content_block_start", { index: 0, content_block: { type: "text", text: "" } });
    send("content_block_delta", { index: 0, delta: { type: "text_delta", text: "ok" } });
    send("content_block_stop", { index: 0 });
    send("message_delta", { delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } });
    send("message_stop", {});
    res.end();
  });
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));

// Claude Code in an empty folder with its own config folder, so no person's settings, plugins
// or MCP servers change the list. Tool search off, so deferred tools (Monitor among them) are
// sent with their schemas; the PowerShell tool on, as Windows can have it.
const work = mkdtempSync(join(tmpdir(), "sigdek-tools-"));
const env = {
  PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: process.env.TMPDIR ?? tmpdir(),
  ANTHROPIC_BASE_URL: `http://127.0.0.1:${server.address().port}`,
  ANTHROPIC_API_KEY: "agent-check-stand-in", // not a key: the stand-in accepts anything
  CLAUDE_CONFIG_DIR: join(work, "config"),
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
  ENABLE_TOOL_SEARCH: "false",
  CLAUDE_CODE_USE_POWERSHELL_TOOL: "1",
};
const session = await new Promise((done) => {
  const p = spawn("claude", ["-p", "Reply with the single word: ok", "--setting-sources", "project,local", "--strict-mcp-config", "--output-format", "json"],
    { cwd: work, env, stdio: ["ignore", "pipe", "pipe"] });
  let out = "";
  p.stdout.on("data", (c) => (out += c));
  p.stderr.on("data", (c) => (out += c));
  const timer = setTimeout(() => p.kill(), 120_000);
  p.on("error", (e) => (out += String(e)));
  p.on("close", (code) => {
    clearTimeout(timer);
    done({ code, out });
  });
});
server.close();
rmSync(work, { recursive: true, force: true });

const all = [...tools.values()];
check(all.length > 0, "Claude Code sent its tool list", `claude exited ${session.code}: ${session.out.slice(0, 300)}`);
if (all.length === 0) process.exit(1);
const deferred = all.filter((t) => t.defer_loading || /placeholder/i.test(t.name));
check(deferred.length === 0, "every tool came with its input schema", `deferred: ${deferred.map((t) => t.name).join(", ")}; ENABLE_TOOL_SEARCH=false may have stopped working`);

const takesCommand = (t) => {
  const c = t.input_schema?.properties?.command;
  const type = c?.type;
  return !t.name.startsWith("mcp__") && (type === "string" || (Array.isArray(type) && type.includes("string")));
};
const commandTools = all.filter(takesCommand).map((t) => t.name).sort();
console.log(`Claude Code's tools: ${all.map((t) => t.name + (takesCommand(t) ? " [command]" : "")).sort().join(", ")}`);
check(commandTools.includes("Bash"), "the list includes Bash, with its command", "a list without it isn't Claude Code's");

// Without a command, Monitor watches a WebSocket given as ws: {url, protocols?}. The hook reads
// ws.url (webSocketURL in internal/adapter/claudecode.go) so URL rules judge that address; if the
// address moves, such a call gets past them unseen.
const monitor = all.find((t) => t.name === "Monitor");
if (monitor) {
  const ws = monitor.input_schema?.properties?.ws;
  check(
    ws?.type === "object" && ws?.properties?.url?.type === "string",
    "Claude Code's Monitor tool names its WebSocket as ws.url, where the hook reads it",
    `ws is now ${ws ? JSON.stringify({ ...ws, description: undefined }).slice(0, 300) : "gone"}; ` +
      "update the Monitor case in internal/adapter/claudecode.go and release the recorder",
  );
} else {
  console.log("--   Claude Code sent no Monitor tool, so its WebSocket input wasn't checked");
}

const records = () => {
  const dir = join(home, "log");
  return existsSync(dir)
    ? readdirSync(dir).filter((f) => /^events-\d{4}-\d{2}\.jsonl$/.test(f)).sort()
        .flatMap((f) => readFileSync(join(dir, f), "utf8").split("\n").filter(Boolean)).map((l) => JSON.parse(l))
    : [];
};
for (const name of commandTools) {
  const command = `touch sigdek-check-blocked-${name.toLowerCase()}.txt`;
  const payload = JSON.stringify({
    session_id: "agent-check-tools", transcript_path: "", cwd: repo, permission_mode: "default",
    hook_event_name: "PreToolUse", tool_name: name, tool_input: { command, description: "agent check" },
  });
  const r = spawnSync(hook[0], hook.slice(1), { cwd: repo, env: { ...process.env, SIGDEK_HOME: home }, input: payload, encoding: "utf8", timeout: 60_000 });
  let decision = "";
  try {
    decision = JSON.parse(r.stdout).hookSpecificOutput?.permissionDecision ?? "";
  } catch {}
  const rec = records().find((x) => x.event === "action" && x.action?.name === name && x.action?.command === command);
  check(
    r.status === 0 && decision === "deny" && rec?.outcome === "denied_by_rule" && rec?.action?.kind === "shell",
    `Claude Code's ${name} tool takes a command, and the Block rule stops it`,
    `exit ${r.status}, answer ${JSON.stringify(r.stdout?.trim() ?? "")}, record ${rec ? `${rec.outcome} ${rec.action.kind}` : "none"}; ` +
      "map the tool in internal/adapter/claudecode.go and release the recorder",
  );
}
process.exit(failed ? 1 : 0);
