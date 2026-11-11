#!/usr/bin/env node
"use strict";
// Runs the SigDek recorder built for this platform. npm installs exactly one of the
// @tektonsoftware/sigdek-* packages, chosen by their "os" and "cpu" fields; this file only finds it.
// Source: https://github.com/tektonsoftware/sigdek
const { spawnSync } = require("node:child_process");

const os = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
const pkg = `@tektonsoftware/sigdek-${os}-${arch}`;

let bin;
try {
  if (!os || !arch) throw new Error("unsupported");
  bin = require.resolve(`${pkg}/bin/sigdek${os === "windows" ? ".exe" : ""}`);
} catch {
  console.error(
    `sigdek: there's no build for ${process.platform} ${process.arch}, or ${pkg} wasn't installed ` +
      "(npm skips optional dependencies with --omit=optional). Builds exist for macOS, Linux and Windows on x64 and arm64.",
  );
  process.exit(1);
}

// Started by npx (npm exec)? Then the recorder prints its next steps as `npx sigdek …`, so they
// work when copied; an installed `sigdek` command prints `sigdek …`.
const npx = process.env.npm_lifecycle_event === "npx" || process.env.npm_command === "exec";
const env = npx ? { ...process.env, SIGDEK_VIA: "npx" } : process.env;
const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit", env });
if (result.error) {
  console.error(`sigdek: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
