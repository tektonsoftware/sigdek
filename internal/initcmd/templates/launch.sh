#!/bin/sh
SIGDEK_SAFE= #
[ "${1:-}" = hook ] && [ "${2:-}" = cursor ] && case "${3:-}" in preToolUse|subagentStart|beforeShellExecution|beforeMCPExecution|beforeReadFile) SIGDEK_SAFE='{"permission":"allow"}' ;; beforeSubmitPrompt) SIGDEK_SAFE='{"continue":true}' ;; *) SIGDEK_SAFE='{}' ;; esac #
trap '[ -z "$SIGDEK_SAFE" ] || printf "%s\n" "$SIGDEK_SAFE"; exit 0' EXIT #
sigdek_eol=lf
[ "$sigdek_eol" = lf ] || { sigdek_src=$(tr -d '\r' < "$0" 2>/dev/null) && [ -n "$sigdek_src" ] && exec sh -c "$sigdek_src" "$0" "$@"; exit 0; } #
# SigDek launcher. Runs the recorder version pinned in recorder.lock, downloading it on
# first use and checking its SHA-256 before it ever runs. It never blocks the agent: whatever
# goes wrong, it exits 0, printing only the "carry on" answer Cursor needs (nothing for
# Claude Code). Hooks run it as `sh launch.sh ...`, so it needs no exec bit.
# Source: https://github.com/tektonsoftware/sigdek
#
# The code comes before this header so that a merge conflict in the header (two branches that
# pinned different versions) is already inside the EXIT trap. The lines above end in "#" so
# they still work if a Windows checkout gave this file CRLF line endings (the comment swallows
# the carriage return); a CRLF copy then re-runs without them. The hook commands that run this
# file also never pass on a failure of their own (`; exit 0`, or Cursor's safe answer), for a
# launcher that is missing, unreadable or broken before the trap.
#
# From here on, every exit (even a shell error) goes through the trap above: status 0 and,
# for Cursor, the safe answer. Only a successful exec of the recorder skips it.
case "$0" in */*) DIR=${0%/*} ;; *) DIR=. ;; esac

# The plugins ship this same file. A plugin's copy steps aside in a project that has
# SigDek's own hooks for the same tool (`sigdek init`), so nothing is recorded twice. Only the
# project folder itself counts: Claude Code reads .claude/settings.json from the folder the
# session started in, and Cursor reads .cursor/hooks.json from the workspace root, so an agent
# started in a subdirectory of a SigDek repository runs no repository hooks.
case "$DIR" in
  .sigdek | */.sigdek) ;;
  *)
    if [ "${2:-}" = cursor ]; then P=${CURSOR_PROJECT_DIR:-$PWD} F=.cursor/hooks.json; else P=${CLAUDE_PROJECT_DIR:-$PWD} F=.claude/settings.json; fi
    if [ -f "$P/.sigdek/launch.sh" ] && [ -f "$P/$F" ] && grep -q 'sigdek/launch\.' "$P/$F" 2>/dev/null; then exit 0; fi
    ;;
esac

# A binary you installed or built yourself (developing SigDek, or a team that distributes it).
# Anyone who can set this, or edit the hook files, can switch the recorder off; the team's
# Coverage report then shows this machine as silent.
if [ -n "${SIGDEK_BIN:-}" ] && [ -x "$SIGDEK_BIN" ]; then exec "$SIGDEK_BIN" "$@"; fi

# Fast path, using only shell built-ins: the pinned version is already here (its checksum was
# checked when it was downloaded).
LOCK=$DIR/recorder.lock
VERSION=
if [ -f "$LOCK" ]; then
  while IFS='=' read -r k v; do
    if [ "$k" = version ]; then VERSION=$v; break; fi
  done <"$LOCK"
fi
[ -n "$VERSION" ] || exit 0
CACHE=${CLAUDE_PLUGIN_DATA:-${SIGDEK_CACHE:-${SIGDEK_HOME:-$HOME/.sigdek}/bin}}
for BIN in "$CACHE/$VERSION/sigdek" "$CACHE/$VERSION/sigdek.exe"; do
  if [ -f "$BIN" ] && [ -x "$BIN" ]; then exec "$BIN" "$@"; fi
done

# First use of this version on this machine.
val() { sed -n "s/^$1=//p" "$LOCK" | head -n 1 | tr -d '\r'; }
VERSION=$(val version)
BASE=$(val url)
[ -n "$VERSION" ] || exit 0
case "$(uname -s 2>/dev/null)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  MINGW* | MSYS* | CYGWIN*) OS=windows ;;
  *) exit 0 ;;
esac
case "$(uname -m 2>/dev/null)" in
  x86_64 | amd64) ARCH=amd64 ;;
  arm64 | aarch64) ARCH=arm64 ;;
  *) exit 0 ;;
esac
EXT=""
[ "$OS" = windows ] && EXT=".exe"
BIN="$CACHE/$VERSION/sigdek$EXT"
if [ -f "$BIN" ] && [ -x "$BIN" ]; then exec "$BIN" "$@"; fi
WANT=$(val "${OS}_${ARCH}")
if [ -z "$WANT" ]; then
  # `sigdek init --offline` pins only the platform it ran on. Say so in the error log, once a
  # day, so `sigdek status` on this machine shows why nothing is recorded.
  if mkdir -p "$CACHE/$VERSION" 2>/dev/null && [ -z "$(find "$CACHE/$VERSION/.no-build" -mmin -1440 2>/dev/null)" ]; then
    : >"$CACHE/$VERSION/.no-build" 2>/dev/null
    (
      umask 077
      L=${SIGDEK_HOME:-$HOME/.sigdek}
      mkdir -p "$L" && printf '{"at":"%s","error":"launcher: .sigdek/recorder.lock pins SigDek %s with no build for %s/%s, so nothing is recorded on this machine; run sigdek init with network access and commit .sigdek/recorder.lock"}\n' \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(printf %s "$VERSION" | tr -cd 'A-Za-z0-9._+-')" "$OS" "$ARCH" >>"$L/errors.log"
    ) 2>/dev/null
  fi
  exit 0
fi
[ -n "$BASE" ] || exit 0
mkdir -p "$CACHE/$VERSION" 2>/dev/null || exit 0

# A download carrying on in the background (see later, below) leaves the network to itself.
MARK="$CACHE/$VERSION/.downloading"
[ -n "$(find "$MARK" -mmin -7 2>/dev/null)" ] && exit 0

# After a failed download, wait 1, 2, 4 ... up to 60 minutes before trying again, so a machine
# that can't reach the release doesn't spend every event on the network. A new checksum in
# recorder.lock retries at once.
STAMP="$CACHE/$VERSION/.download-failed"
FAILS=0
if [ -f "$STAMP" ]; then
  read -r FAILS STAMPED <"$STAMP" 2>/dev/null
  case "$FAILS" in '' | *[!0-9]*) FAILS=1 ;; esac
  if [ "$STAMPED" = "$WANT" ]; then
    WAIT=1 i=1
    while [ "$i" -lt "$FAILS" ] && [ "$WAIT" -lt 60 ]; do WAIT=$((WAIT * 2)) i=$((i + 1)); done
    [ "$WAIT" -gt 60 ] && WAIT=60
    [ -n "$(find "$STAMP" -mmin "-$WAIT" 2>/dev/null)" ] && exit 0
  else
    FAILS=0
  fi
fi
TMP="$BIN.$$.download"
failed() {
  rm -f "$TMP"
  echo "$((FAILS + 1)) $WANT" >"$STAMP" 2>/dev/null
  exit 0
}
sha() { (sha256sum "$1" 2>/dev/null || shasum -a 256 "$1" 2>/dev/null || openssl dgst -sha256 -r "$1" 2>/dev/null) | awk '{print $1}' | head -n 1; }

# A link too slow for the hook's 8 seconds: the download starts again in the background, with up
# to 5 minutes, and makes the same checks; the next event after it lands finds the recorder
# above. It shares no stdin, stdout or stderr with the hook, so the agent doesn't wait for it,
# and ignores the hangup signal, so it outlives the hook. Its failure counts like this one would.
# One at a time: it holds .downloading (launch.ps1 too), which counts as stale after 6 minutes.
later() {
  [ -e "$MARK" ] && [ -n "$(find "$MARK" -mmin +6 2>/dev/null)" ] && rm -f "$MARK"
  (set -C && : >"$MARK") 2>/dev/null || { rm -f "$TMP"; exit 0; }
  (
    trap '' HUP
    if curl -fsSL --connect-timeout 10 --max-time 300 -o "$TMP" "$BASE/$NAME" && [ "$(sha "$TMP")" = "$WANT" ] &&
      chmod +x "$TMP" && mv -f "$TMP" "$BIN"; then
      rm -f "$STAMP"
    else
      rm -f "$TMP"
      echo "$((FAILS + 1)) $WANT" >"$STAMP"
    fi
    rm -f "$MARK"
  ) </dev/null >/dev/null 2>&1 &
  exit 0
}

# The agent waits at most 10 seconds for a hook; the download must finish well inside that.
NAME="sigdek_${VERSION}_${OS}_${ARCH}${EXT}"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 2 --max-time 8 -o "$TMP" "$BASE/$NAME" 2>/dev/null || { [ $? = 28 ] && later; failed; }
elif command -v wget >/dev/null 2>&1; then
  wget -q -T 8 -t 1 -O "$TMP" "$BASE/$NAME" 2>/dev/null || failed
else
  failed
fi
[ "$(sha "$TMP")" = "$WANT" ] || failed
chmod +x "$TMP" 2>/dev/null && mv -f "$TMP" "$BIN" 2>/dev/null || failed
rm -f "$STAMP"
exec "$BIN" "$@"
