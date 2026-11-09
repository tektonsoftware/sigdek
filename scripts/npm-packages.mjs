#!/usr/bin/env node
// Builds the npm packages for a release from the binaries in dist/:
//   sigdek                      the launcher (npm/sigdek), depending on all of:
//   @tektonsoftware/sigdek-<os>-<arch>   one binary each, installed only on the matching platform
// Usage: node scripts/npm-packages.mjs <version> <dist dir> <out dir>
import { chmodSync, copyFileSync, cpSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [version, dist, out] = process.argv.slice(2);
if (!version || !dist || !out) {
  console.error("usage: node scripts/npm-packages.mjs <version> <dist dir> <out dir>");
  process.exit(2);
}
const common = {
  version,
  license: "Apache-2.0",
  homepage: "https://sigdek.com",
  repository: { type: "git", url: "git+https://github.com/tektonsoftware/sigdek.git" },
  author: "Tekton Software <hello@sigdek.com>",
};
const targets = [
  ["darwin", "amd64", "darwin", "x64"],
  ["darwin", "arm64", "darwin", "arm64"],
  ["linux", "amd64", "linux", "x64"],
  ["linux", "arm64", "linux", "arm64"],
  ["windows", "amd64", "win32", "x64"],
  ["windows", "arm64", "win32", "arm64"],
];

const optional = {};
for (const [os, arch, npmOs, npmCpu] of targets) {
  const name = `@tektonsoftware/sigdek-${os}-${arch}`;
  const dir = join(out, `sigdek-${os}-${arch}`);
  const ext = os === "windows" ? ".exe" : "";
  mkdirSync(join(dir, "bin"), { recursive: true });
  const bin = join(dir, "bin", `sigdek${ext}`);
  copyFileSync(join(dist, `sigdek_${version}_${os}_${arch}${ext}`), bin);
  chmodSync(bin, 0o755);
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({ name, description: `The SigDek recorder for ${os} ${arch}.`, ...common, os: [npmOs], cpu: [npmCpu], files: ["bin/"], preferUnplugged: true }, null, 2) + "\n",
  );
  writeFileSync(join(dir, "README.md"), `The SigDek recorder binary for ${os} ${arch}. Install \`sigdek\` instead; it picks the right one.\n`);
  optional[name] = version;
}

const main = join(out, "sigdek");
cpSync("npm/sigdek", main, { recursive: true });
chmodSync(join(main, "bin", "sigdek.js"), 0o755);
writeFileSync(
  join(main, "package.json"),
  JSON.stringify(
    {
      name: "sigdek",
      description: "Records what AI coding agents do on your machine, for your team's audit evidence.",
      ...common,
      keywords: ["claude-code", "cursor", "ai-agents", "audit", "soc2", "compliance"],
      bin: { sigdek: "bin/sigdek.js" },
      files: ["bin/"],
      engines: { node: ">=18" },
      optionalDependencies: optional,
    },
    null,
    2,
  ) + "\n",
);
console.log(`npm packages for ${version} written to ${out}`);
