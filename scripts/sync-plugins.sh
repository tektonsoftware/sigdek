#!/usr/bin/env bash
# Copy the one launcher into each plugin and, given a version, pin it in their lock files.
#   scripts/sync-plugins.sh                 copy launchers only (CI checks nothing drifted)
#   scripts/sync-plugins.sh 0.3.0 dist/checksums.txt   pin a release
set -euo pipefail
cd "$(dirname "$0")/.."
for p in claude-code cursor; do
  install -m 0755 internal/initcmd/templates/launch.sh "plugins/$p/scripts/launch.sh"
done
[[ $# -eq 0 ]] && exit 0

version=$1 sums=$2
lock="# Pinned SigDek recorder for this plugin. Updated by the release workflow.
version=$version
url=https://github.com/tektonsoftware/sigdek/releases/download/v$version"
for t in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64; do
  ext=""; [[ $t == windows_* ]] && ext=".exe"
  sum=$(awk -v n="sigdek_${version}_${t}${ext}" '$2==n {print $1}' "$sums")
  [[ -n "$sum" ]] || { echo "no checksum for $t" >&2; exit 1; }
  lock+=$'\n'"$t=$sum"
done
for p in claude-code cursor; do
  printf '%s\n' "$lock" > "plugins/$p/scripts/recorder.lock"
done
for f in plugins/claude-code/.claude-plugin/plugin.json plugins/cursor/.cursor-plugin/plugin.json; do
  sed -i.bak -E "s/\"version\": \"[^\"]+\"/\"version\": \"$version\"/" "$f" && rm -f "$f.bak"
done
