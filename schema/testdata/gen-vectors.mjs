// Independent reference: canonical JSON via JSON.stringify with UTF-16-sorted keys (what the TS server uses).
import { createHash } from "node:crypto";
import { writeFileSync } from "node:fs";
const canon = (v) => {
  if (v === null || typeof v !== "object") return JSON.stringify(v);
  if (Array.isArray(v)) return "[" + v.map(canon).join(",") + "]";
  return "{" + Object.keys(v).sort().map((k) => JSON.stringify(k) + ":" + canon(v[k])).join(",") + "}";
};
const base = {
  v: 1, id: "019a2b3c-4d5e-7f60-8a1b-2c3d4e5f6a7b", ts: "2026-10-28T19:28:24.123Z", seq: 1,
  prev: "0".repeat(64), hash: "", device: "dev_01", user: null, tool: "claude-code", tool_version: null,
  session: "abc123", prompt_id: null, repo: { remote: "github.com/acme/api", root_hash: "a".repeat(64) },
  event: "session_start", model: "claude-opus-5-5", redactions: 0,
};
const inputs = [
  { name: "session_start", value: base },
  { name: "action_with_awkward_strings", value: { ...base, seq: 2, event: "action", prompt_id: "p-1",
      action: { kind: "shell", name: "Bash", command: "echo \"héllo 🌍\"\t&& printf '\\\\'    \u0001\u001f </script> & done\n", input_hash: "b".repeat(64) },
      outcome: "ran", duration_ms: 1234, rule: "force-push", permission_mode: "default", redactions: 2 } },
  { name: "key_order_utf16", value: { "b": 1, "a": [true, false, null], "é": "x", "😀": "y", "ﬀ": "z", "A": { "z": 0, "": -5 } } },
  { name: "nested_nulls", value: { ...base, event: "subagent_stop", subagent: null, config: { source: null, path: "~/.claude/settings.json" } } },
];
const vectors = inputs.map(({ name, value }) => {
  const v = structuredClone(value); delete v.hash;
  const c = canon(v);
  return { name, input: value, canonical: c, sha256: createHash("sha256").update(c, "utf8").digest("hex") };
});
writeFileSync("canonical-vectors.json", JSON.stringify(vectors, null, 2) + "\n");
console.log(vectors.map((v) => v.name + " " + v.sha256).join("\n"));
