#!/usr/bin/env bash
# REHEARSAL ONLY (branch release-rehearsal, never merged).
# Installs SigDek the way a user would from the rehearsal build, on macOS or Linux (and, for
# the launcher sections, under Git Bash on Windows), with nothing published anywhere.
#
#   ART   the rehearsal-build artifact: dist/<v>/, npm-tgz/<v>/, tap.bundle
#   WORK  an empty scratch folder; results go to $WORK/out
# Optional:
#   SECTIONS     which parts to run (default: npm repo plugin brew upgrade slow)
#   SKIP_SERVERS 1 when the servers already run (Windows starts them from PowerShell)
#   REPO         a repository already set up with `sigdek init` (when SECTIONS has no npm)
#   BREW_UPDATE  full: `brew update` for the upgrade; tap (default): fetch only this tap
#   BREW_CLEANUP 1 (default): uninstall sigdek and untap at the end
#
# Nothing here signs in, and every record stays in a temporary SIGDEK_HOME.
set -u
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

ART=${ART:?set ART to the rehearsal-build artifact}
WORK=${WORK:?set WORK to an empty scratch folder}
V1=${V1:-0.0.99}
V2=${V2:-0.0.100}
PORT=8765
BAD_PORT=8766
REG_PORT=4873
SLOW_PORT=8767
SLOW_BAD_PORT=8768
SLOW_RATE=${SLOW_RATE:-250000} # bytes a second (2 Mbit/s): a 7 MB recorder takes about 28 s
BASE=http://127.0.0.1:$PORT
BAD=http://127.0.0.1:$BAD_PORT
REG=http://127.0.0.1:$REG_PORT/
SLOW=http://127.0.0.1:$SLOW_PORT
SLOW_BAD=http://127.0.0.1:$SLOW_BAD_PORT
SECTIONS=" ${SECTIONS:-npm repo plugin brew upgrade slow} "
OUT=$WORK/out
mkdir -p "$WORK" "$OUT"
WORK=$(cd "$WORK" && pwd)
ART=$(cd "$ART" && pwd)
OUT=$WORK/out
RESULTS=$OUT/results.txt
LOG=$OUT/log.txt
FAILED=0
: >>"$RESULTS"

case "$(uname -s)" in
  Darwin) OSN=darwin ;;
  Linux) OSN=linux ;;
  MINGW* | MSYS* | CYGWIN*) OSN=windows ;;
  *) OSN=unknown ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) ARCHN=amd64 ;;
  arm64 | aarch64) ARCHN=arm64 ;;
esac
EXE=""
[ "$OSN" = windows ] && EXE=.exe
LABEL=${LABEL:-$OSN}

# Isolation: never the real ~/.sigdek, never a real service, never a binary set by hand.
unset SIGDEK_BIN SIGDEK_CACHE CLAUDE_PLUGIN_DATA CLAUDE_PLUGIN_ROOT CURSOR_PLUGIN_ROOT CLAUDE_PROJECT_DIR CURSOR_PROJECT_DIR
export SIGDEK_INGEST_URL=http://127.0.0.1:9 SIGDEK_APP_URL=http://127.0.0.1:9
export SIGDEK_HOME=$WORK/home-default

log() { printf '%s\n' "$*" | tee -a "$LOG"; }
pass() { printf 'PASS | %s | %s | %s\n' "$LABEL" "$1" "$2" | tee -a "$RESULTS" "$LOG"; }
fail() { printf 'FAIL | %s | %s | %s\n' "$LABEL" "$1" "$2" | tee -a "$RESULTS" "$LOG"; FAILED=1; }
note() { printf 'NOTE | %s | %s | %s\n' "$LABEL" "$1" "$2" | tee -a "$RESULTS" "$LOG"; }
has() { case "$SECTIONS" in *" $1 "*) return 0 ;; esac; return 1; }
now_ms() {
  if [ -n "${EPOCHREALTIME:-}" ]; then local t=${EPOCHREALTIME/[.,]/}; echo $((t / 1000)); else perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; fi
}
# A path as the agent tools give it to hooks: native on Windows (forward slashes keep the JSON simple).
npath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf %s "$1"; fi; }
records() { cat "$1"/log/events-*.jsonl 2>/dev/null | wc -l | tr -d ' '; }
fresh_home() { local h=$WORK/homes/$1; rm -rf "$h"; mkdir -p "$h"; printf %s "$h"; }

# Synthetic hook input, with the fields the adapter tests use.
cc_start() { printf '{"session_id":"rehearsal-1","transcript_path":"/tmp/rehearsal.jsonl","cwd":"%s","hook_event_name":"SessionStart","source":"startup","model":"claude-sonnet-4-5"}' "$1"; }
cc_post() { printf '{"session_id":"rehearsal-1","transcript_path":"/tmp/rehearsal.jsonl","cwd":"%s","prompt_id":"prompt-1","permission_mode":"default","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"npm test"},"tool_response":{"stdout":"ok","stderr":""},"tool_use_id":"toolu_r1","duration_ms":12}' "$1"; }
cur_ev() { printf '{"conversation_id":"conv-r1","generation_id":"gen-r1","model":"gpt-5","hook_event_name":"%s","cursor_version":"2.4.0","workspace_roots":["%s"],"user_email":"dev@example.com","transcript_path":null,"tool_name":"Shell","tool_input":{"command":"npm test"},"cwd":"%s"}' "$1" "$2" "$2"; }

# run_hook <label> <home> <input> <hook command, exactly as in the hook file>
# Runs the command through sh -c in the current folder, as the agent tools do.
run_hook() {
  local label=$1 home=$2 input=$3 cmd=$4 before after t0 t1 slug
  slug=$(printf %s "$label" | tr -c 'A-Za-z0-9' '-')
  before=$(records "$home")
  t0=$(now_ms)
  HOOK_OUT=$(printf '%s' "$input" | SIGDEK_HOME="$home" sh -c "$cmd" 2>"$OUT/stderr-$LABEL-$slug.txt")
  HOOK_STATUS=$?
  t1=$(now_ms)
  after=$(records "$home")
  HOOK_NEW=$((after - before))
  HOOK_MS=$((t1 - t0))
  log "  [$label] exit=$HOOK_STATUS new_records=$HOOK_NEW ms=$HOOK_MS stdout=${HOOK_OUT:-<none>} stderr=$(head -c 300 "$OUT/stderr-$LABEL-$slug.txt" | tr '\n' ' ')"
}

# check_bin <channel> <path to sigdek> : `sigdek version` and one Claude Code hook.
check_bin() {
  local ch=$1 bin=$2 v=${3:-$V1} out st home
  out=$("$bin" version 2>&1)
  st=$?
  log "  [$ch] $bin version -> exit=$st: $out"
  if [ $st -eq 0 ] && [ "$out" = "sigdek $v ($OSN/$ARCHN)" ]; then pass "$ch: sigdek version" "$out"; else fail "$ch: sigdek version" "exit=$st: $out"; fi
  home=$(fresh_home "$ch-hook")
  out=$(cc_start "$(npath "$WORK")" | SIGDEK_HOME="$home" "$bin" hook claude-code 2>&1)
  st=$?
  local n
  n=$(records "$home")
  log "  [$ch] hook claude-code SessionStart -> exit=$st records=$n out=${out:-<none>}"
  if [ $st -eq 0 ] && [ "$n" = 1 ] && [ -z "$out" ]; then
    pass "$ch: hook claude-code (SessionStart)" "exit 0, 1 record in $(ls "$home"/log)"
  else
    fail "$ch: hook claude-code (SessionStart)" "exit=$st records=$n out=$out"
  fi
}

PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  if [ "${TAPPED:-0}" = 1 ] && [ "${BREW_CLEANUP:-1}" = 1 ]; then
    brew uninstall --force sigdek >>"$LOG" 2>&1
    brew untrust --formula tektonsoftware/sigdek/sigdek >>"$LOG" 2>&1
    brew untap tektonsoftware/sigdek >>"$LOG" 2>&1 && log "brew: untapped tektonsoftware/sigdek"
    # `brew test` and `brew audit` turn on Homebrew's developer mode; leave it as it was.
    [ "${BREW_DEV_WAS:-}" = off ] && brew developer off >>"$LOG" 2>&1
    rm -f "$(brew --cache)"/downloads/*--sigdek_* "$(brew --cache)"/sigdek--* 2>/dev/null
  fi
}
trap cleanup EXIT

log "== Release rehearsal on $LABEL ($OSN/$ARCHN), $(date -u +%Y-%m-%dT%H:%M:%SZ)"
log "ART=$ART WORK=$WORK sections:$SECTIONS"

# ---------------------------------------------------------------------------------------------
# The files a GitHub release would serve, under the same paths: <base>/v<version>/<name>.
# A second server serves the same names with this platform's binary changed by one byte.
if [ "${SKIP_SERVERS:-0}" != 1 ]; then
  rm -rf "$WORK/srv" "$WORK/srv-bad"
  for v in "$V1" "$V2"; do
    mkdir -p "$WORK/srv/v$v" "$WORK/srv-bad/v$v"
    cp "$ART/dist/$v"/sigdek_* "$ART/dist/$v/checksums.txt" "$WORK/srv/v$v/"
    cp "$ART/dist/$v"/sigdek_* "$ART/dist/$v/checksums.txt" "$WORK/srv-bad/v$v/"
    printf 'x' >>"$WORK/srv-bad/v$v/sigdek_${v}_${OSN}_${ARCHN}${EXE}"
  done
  PY=$(command -v python3 || command -v python)
  "$PY" -m http.server "$PORT" --bind 127.0.0.1 --directory "$WORK/srv" >"$OUT/http.log" 2>&1 &
  PIDS+=($!)
  "$PY" -m http.server "$BAD_PORT" --bind 127.0.0.1 --directory "$WORK/srv-bad" >"$OUT/http-bad.log" 2>&1 &
  PIDS+=($!)
  # The same two, at SLOW_RATE: a link too slow for the hook's 8-second download.
  "$PY" "$SCRIPT_DIR/throttle.py" "$SLOW_PORT" "$WORK/srv" "$SLOW_RATE" "$OUT/http-slow.log" >"$OUT/http-slow-err.log" 2>&1 &
  PIDS+=($!)
  "$PY" "$SCRIPT_DIR/throttle.py" "$SLOW_BAD_PORT" "$WORK/srv-bad" "$SLOW_RATE" "$OUT/http-slow-bad.log" >"$OUT/http-slow-bad-err.log" 2>&1 &
  PIDS+=($!)
  echo "${PIDS[*]}" >"$WORK/server-pids"
  for i in $(seq 1 50); do curl -fsS "$BASE/v$V1/checksums.txt" >/dev/null 2>&1 && break; sleep 0.2; done
  curl -fsS "$BASE/v$V1/checksums.txt" >/dev/null && log "release server up at $BASE (and tampered copy at $BAD)" || fail "setup" "release server didn't start"
fi

# ---------------------------------------------------------------------------------------------
# The tap and bucket repository: what main looks like after release.yml's pin commit, with
# the download base rewritten to the local server. Named sigdek.git, not homebrew-*.
rm -rf "$WORK/sigdek.git" "$WORK/tap-v1" "$WORK/tap-v2"
git clone -q --bare "$ART/tap.bundle" "$WORK/sigdek.git"
git -C "$WORK/sigdek.git" update-ref refs/heads/main "refs/tags/local-$V1"
git clone -q --branch "local-$V1" "$WORK/sigdek.git" "$WORK/tap-v1" 2>/dev/null
git clone -q --branch "local-$V2" "$WORK/sigdek.git" "$WORK/tap-v2" 2>/dev/null
log "tap repository at $WORK/sigdek.git, main at local-$V1: $(git -C "$WORK/sigdek.git" log --oneline -1 main)"

# ---------------------------------------------------------------------------------------------
if has npm; then
  log "== npm, through a local Verdaccio with all seven packages"
  mkdir -p "$WORK/verdaccio/storage"
  cat >"$WORK/verdaccio/config.yaml" <<EOF
storage: $WORK/verdaccio/storage
auth:
  htpasswd:
    file: $WORK/verdaccio/htpasswd
    max_users: 10
uplinks:
  npmjs:
    url: https://registry.npmjs.org/
packages:
  'sigdek':
    access: \$all
    publish: \$authenticated
  '@tektonsoftware/*':
    access: \$all
    publish: \$authenticated
  '**':
    access: \$all
    proxy: npmjs
log: { type: stdout, format: pretty, level: warn }
EOF
  # Verdaccio itself comes from the real registry, with its own cache.
  (cd "$WORK/verdaccio" && env -u NPM_CONFIG_USERCONFIG npm_config_registry=https://registry.npmjs.org/ npm_config_cache="$WORK/tool-cache" \
    npx --yes verdaccio@6.11.1 --config "$WORK/verdaccio/config.yaml" --listen "127.0.0.1:$REG_PORT" >"$OUT/verdaccio.log" 2>&1) &
  PIDS+=($!)
  echo "${PIDS[*]}" >"$WORK/server-pids"
  for i in $(seq 1 150); do curl -fsS "${REG}-/ping" >/dev/null 2>&1 && break; sleep 1; done
  # npx runs Verdaccio as a child process; stop that one too.
  for p in $(lsof -tiTCP:$REG_PORT -sTCP:LISTEN 2>/dev/null); do PIDS+=("$p"); done
  for p in $(pgrep -f "verdaccio@6.11.1 --config $WORK/" 2>/dev/null); do PIDS+=("$p"); done
  echo "${PIDS[*]}" >"$WORK/server-pids"
  tok=$(curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"name":"rehearsal","password":"rehearsal-pass-1"}' \
    "${REG}-/user/org.couchdb.user:rehearsal" | sed -n 's/.*"token": *"\([^"]*\)".*/\1/p')
  [ -n "$tok" ] || fail "npm: setup" "Verdaccio didn't start or gave no token (see verdaccio.log)"
  printf 'registry=%s\n//127.0.0.1:%s/:_authToken=%s\n' "$REG" "$REG_PORT" "$tok" >"$WORK/npmrc"
  # Everything npm does from here uses the local registry, a fresh cache and a scratch global prefix.
  export NPM_CONFIG_USERCONFIG=$WORK/npmrc npm_config_cache=$WORK/npm-cache npm_config_prefix=$WORK/npm-global
  export npm_config_registry=$REG npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
  log "node $(node --version), npm $(npm --version)"

  publish() { # release.yml's order: the six platform packages, then sigdek
    local v=$1 f ok=1
    for f in "$ART/npm-tgz/$v"/tektonsoftware-sigdek-*.tgz "$ART/npm-tgz/$v/sigdek-$v.tgz"; do
      if npm publish "$f" --access public >>"$LOG" 2>&1; then log "  published $(basename "$f")"; else ok=0; log "  publish FAILED: $f"; fi
    done
    [ $ok = 1 ] && pass "npm: publish $v (7 packages) to Verdaccio" "$(ls "$ART/npm-tgz/$v" | tr '\n' ' ')" || fail "npm: publish $v to Verdaccio" "see log"
  }
  publish "$V1"

  mkdir -p "$WORK/empty" && cd "$WORK/empty" || exit 1
  t0=$(now_ms)
  out=$(npx --yes sigdek version 2>&1)
  st=$?
  log "  npx --yes sigdek version -> exit=$st ($(($(now_ms) - t0)) ms): $out"
  if [ $st -eq 0 ] && [ "$out" = "sigdek $V1 ($OSN/$ARCHN)" ]; then pass "npm: npx sigdek version" "$out"; else fail "npm: npx sigdek version" "exit=$st: $out"; fi
  # Which platform package npm chose, and whether the binary kept its exec bit.
  pk=$(find "$WORK/npm-cache/_npx" -path "*@tektonsoftware/sigdek-*/bin/sigdek*" 2>/dev/null | head -n 5)
  log "  npx installed: $pk"
  [ -n "$pk" ] && log "  $(ls -l $pk)"
  npkgs=$(ls -d "$WORK"/npm-cache/_npx/*/node_modules/@tektonsoftware/* 2>/dev/null | wc -l | tr -d ' ')
  log "  platform packages installed by npx: $npkgs (expect 1)"

  # npx sigdek init, in a scratch git repository
  REPO=$WORK/repo
  rm -rf "$REPO" && mkdir -p "$REPO" && cd "$REPO" && git init -q && git config user.email dev@example.com && git config user.name "Dev Example"
  out=$(npx --yes sigdek init 2>&1)
  st=$?
  log "  npx --yes sigdek init -> exit=$st"
  printf '%s\n' "$out" | sed 's/^/    /' | tee -a "$LOG" >/dev/null
  lock=$(cat "$REPO/.sigdek/recorder.lock" 2>/dev/null)
  log "  .sigdek/recorder.lock:"; printf '%s\n' "$lock" | sed 's/^/    /' >>"$LOG"
  want_ok=1
  for t in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64; do
    e=""; case $t in windows_*) e=.exe ;; esac
    s=$(awk -v n="sigdek_${V1}_${t}${e}" '$2==n {print $1}' "$ART/dist/$V1/checksums.txt")
    printf '%s\n' "$lock" | grep -qx "$t=$s" || { want_ok=0; log "  lock is missing $t=$s"; }
  done
  if [ $st -eq 0 ] && printf '%s\n' "$lock" | grep -qx "version=$V1" && printf '%s\n' "$lock" | grep -qx "url=$BASE/v$V1" && [ $want_ok = 1 ]; then
    pass "npm: npx sigdek init" "exit 0; recorder.lock pins $V1, url=$BASE/v$V1 and all six checksums from checksums.txt"
  else
    fail "npm: npx sigdek init" "exit=$st; lock: $(printf '%s' "$lock" | tr '\n' ' ')"
  fi
  ls -la "$REPO/.sigdek" >>"$LOG" 2>&1
  git -C "$REPO" add -A && git -C "$REPO" commit -qm "Record agent activity with SigDek" && log "  committed .claude .cursor .sigdek in the scratch repository"

  # npm install -g sigdek
  out=$(npm install -g sigdek 2>&1)
  st=$?
  log "  npm install -g sigdek -> exit=$st: $(printf '%s' "$out" | tr '\n' ' ')"
  GBIN=$WORK/npm-global/bin/sigdek
  [ "$OSN" = windows ] && GBIN=$WORK/npm-global/sigdek.cmd
  if [ $st -eq 0 ]; then check_bin "npm -g" "$GBIN"; else fail "npm: npm install -g sigdek" "exit=$st"; fi
  # npx in a folder where npm -g has sigdek: does it run the global one?
  cd "$WORK/empty" && log "  npx sigdek version with sigdek installed globally: $(npx --yes sigdek version 2>&1)"
fi

# ---------------------------------------------------------------------------------------------
if has repo; then
  log "== Repository install: the .sigdek/launch.sh that sigdek init wrote"
  REPO=${REPO:-$WORK/repo}
  if [ ! -f "$REPO/.sigdek/launch.sh" ]; then
    fail "repo: setup" "no $REPO/.sigdek/launch.sh"
  else
    cd "$REPO" || exit 1
    P=$(npath "$REPO")
    H=$(fresh_home repo-cc)
    export CLAUDE_PROJECT_DIR=$P
    run_hook "repo claude-code first use" "$H" "$(cc_start "$P")" 'sh "$CLAUDE_PROJECT_DIR/.sigdek/launch.sh" hook claude-code; exit 0'
    first_ms=$HOOK_MS
    if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ] && [ -z "$HOOK_OUT" ] && [ -f "$H/bin/$V1/sigdek$EXE" ]; then
      pass "repo: Claude Code hook, first use" "exit 0, downloaded to bin/$V1/sigdek$EXE, checked, ran; 1 record; ${first_ms} ms"
    else
      fail "repo: Claude Code hook, first use" "exit=$HOOK_STATUS records=$HOOK_NEW out=$HOOK_OUT cached=$(ls "$H/bin/$V1" 2>&1 | tr '\n' ' ')"
    fi
    run_hook "repo claude-code cached" "$H" "$(cc_post "$P")" 'sh "$CLAUDE_PROJECT_DIR/.sigdek/launch.sh" hook claude-code; exit 0'
    if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ]; then pass "repo: Claude Code hook, cached" "exit 0, 1 record (PostToolUse Bash); ${HOOK_MS} ms"; else fail "repo: Claude Code hook, cached" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi
    if [ "$OSN" = windows ]; then
      W=$(cygpath -w "$REPO")
      CLAUDE_PROJECT_DIR=$W run_hook "repo claude-code backslash path" "$H" "$(cc_post "$P")" 'sh "$CLAUDE_PROJECT_DIR/.sigdek/launch.sh" hook claude-code; exit 0'
      if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ]; then pass "repo: Claude Code hook, CLAUDE_PROJECT_DIR=$W" "exit 0, 1 record"; else fail "repo: Claude Code hook, CLAUDE_PROJECT_DIR=$W" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi
    fi
    unset CLAUDE_PROJECT_DIR
    out=$(SIGDEK_HOME=$H "$H/bin/$V1/sigdek$EXE" verify 2>&1)
    st=$?
    log "  sigdek verify -> exit=$st: $out"
    [ $st -eq 0 ] && pass "repo: sigdek verify on those records" "$(printf '%s' "$out" | head -n 1)" || fail "repo: sigdek verify" "exit=$st: $out"

    H=$(fresh_home repo-cursor)
    run_hook "repo cursor preToolUse first use" "$H" "$(cur_ev preToolUse "$P")" "sh .sigdek/launch.sh hook cursor preToolUse | grep '^{' || echo '{\"permission\":\"allow\"}'"
    if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{"permission":"allow"}' ] && [ -f "$H/bin/$V1/sigdek$EXE" ]; then
      pass "repo: Cursor hook preToolUse, first use" "exit 0, answer $HOOK_OUT from the recorder; downloaded; ${HOOK_MS} ms"
    else
      fail "repo: Cursor hook preToolUse, first use" "exit=$HOOK_STATUS out=$HOOK_OUT"
    fi
    run_hook "repo cursor sessionStart" "$H" "$(cur_ev sessionStart "$P")" "sh .sigdek/launch.sh hook cursor sessionStart | grep '^{' || echo '{}'"
    if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{}' ] && [ $HOOK_NEW -eq 1 ]; then pass "repo: Cursor hook sessionStart" "exit 0, answer {}, 1 record"; else fail "repo: Cursor hook sessionStart" "exit=$HOOK_STATUS out=$HOOK_OUT records=$HOOK_NEW"; fi
    run_hook "repo cursor postToolUse" "$H" "$(cur_ev postToolUse "$P")" "sh .sigdek/launch.sh hook cursor postToolUse | grep '^{' || echo '{}'"
    if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{}' ] && [ $HOOK_NEW -eq 1 ]; then pass "repo: Cursor hook postToolUse" "exit 0, answer {}, 1 record"; else fail "repo: Cursor hook postToolUse" "exit=$HOOK_STATUS out=$HOOK_OUT records=$HOOK_NEW"; fi
    head -c 2000 "$H"/log/events-*.jsonl >"$OUT/sample-record-$LABEL.jsonl" 2>/dev/null
    out=$(cd "$REPO" && SIGDEK_HOME=$H "$H/bin/$V1/sigdek$EXE" status 2>&1)
    log "  sigdek status in the repository:"; printf '%s\n' "$out" | sed 's/^/    /' >>"$LOG"
  fi
fi

# ---------------------------------------------------------------------------------------------
if has plugin; then
  log "== Plugin launchers, first use, from the pinned plugins (main after the pin commit)"
  PLAIN=$WORK/plain
  rm -rf "$PLAIN" && mkdir -p "$PLAIN" && git -C "$PLAIN" init -q
  cd "$PLAIN" || exit 1
  P=$(npath "$PLAIN")
  log "  plugins/claude-code/scripts/recorder.lock at local-$V1:"; sed 's/^/    /' "$WORK/tap-v1/plugins/claude-code/scripts/recorder.lock" >>"$LOG"
  export CLAUDE_PROJECT_DIR=$P CURSOR_PROJECT_DIR=$P
  export CLAUDE_PLUGIN_ROOT=$(npath "$WORK/tap-v1/plugins/claude-code")
  CC='sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" hook claude-code; exit 0'
  # Cache in the plugin's data folder (CLAUDE_PLUGIN_DATA), as Claude Code gives a plugin.
  H=$(fresh_home plugin-cc)
  export CLAUDE_PLUGIN_DATA=$(npath "$WORK/plugin-data/claude-code")
  rm -rf "$WORK/plugin-data"
  run_hook "plugin claude-code first use" "$H" "$(cc_start "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ] && [ -f "$WORK/plugin-data/claude-code/$V1/sigdek$EXE" ]; then
    pass "plugin: Claude Code launcher, first use" "exit 0; downloaded to CLAUDE_PLUGIN_DATA/$V1/sigdek$EXE, SHA-256 checked; 1 record; ${HOOK_MS} ms"
  else
    fail "plugin: Claude Code launcher, first use" "exit=$HOOK_STATUS records=$HOOK_NEW data=$(ls -R "$WORK/plugin-data" 2>&1 | tr '\n' ' ')"
  fi
  run_hook "plugin claude-code cached" "$H" "$(cc_post "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ]; then pass "plugin: Claude Code launcher, cached" "exit 0, 1 record; ${HOOK_MS} ms"; else fail "plugin: Claude Code launcher, cached" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi
  unset CLAUDE_PLUGIN_DATA
  # Without CLAUDE_PLUGIN_DATA: the cache is SIGDEK_HOME/bin.
  H=$(fresh_home plugin-cc-nodata)
  run_hook "plugin claude-code first use, no plugin data" "$H" "$(cc_start "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ] && [ -f "$H/bin/$V1/sigdek$EXE" ]; then pass "plugin: Claude Code launcher, first use, cache in SIGDEK_HOME/bin" "exit 0, 1 record"; else fail "plugin: Claude Code launcher, first use without CLAUDE_PLUGIN_DATA" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi
  if [ "$OSN" = windows ]; then
    H=$(fresh_home plugin-cc-backslash)
    CLAUDE_PLUGIN_ROOT=$(cygpath -w "$WORK/tap-v1/plugins/claude-code") CLAUDE_PROJECT_DIR=$(cygpath -w "$PLAIN") \
      run_hook "plugin claude-code backslash paths" "$H" "$(cc_start "$P")" "$CC"
    if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ]; then pass "plugin: Claude Code launcher with backslash CLAUDE_PLUGIN_ROOT" "exit 0, 1 record"; else fail "plugin: Claude Code launcher with backslash CLAUDE_PLUGIN_ROOT" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi
  fi

  export CURSOR_PLUGIN_ROOT=$(npath "$WORK/tap-v1/plugins/cursor")
  H=$(fresh_home plugin-cursor)
  run_hook "plugin cursor first use" "$H" "$(cur_ev sessionStart "$P")" "sh \"\${CURSOR_PLUGIN_ROOT}/scripts/launch.sh\" hook cursor sessionStart | grep '^{' || echo '{}'"
  if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{}' ] && [ $HOOK_NEW -eq 1 ] && [ -f "$H/bin/$V1/sigdek$EXE" ]; then
    pass "plugin: Cursor launcher, first use" "exit 0, answer {}, downloaded and checked, 1 record; ${HOOK_MS} ms"
  else
    fail "plugin: Cursor launcher, first use" "exit=$HOOK_STATUS out=$HOOK_OUT records=$HOOK_NEW"
  fi
  run_hook "plugin cursor preToolUse" "$H" "$(cur_ev preToolUse "$P")" "sh \"\${CURSOR_PLUGIN_ROOT}/scripts/launch.sh\" hook cursor preToolUse | grep '^{' || echo '{\"permission\":\"allow\"}'"
  if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{"permission":"allow"}' ]; then pass "plugin: Cursor launcher preToolUse" "exit 0, answer $HOOK_OUT"; else fail "plugin: Cursor launcher preToolUse" "exit=$HOOK_STATUS out=$HOOK_OUT"; fi

  log "== Tampered download: same names, this platform's binary changed by one byte"
  rm -rf "$WORK/tampered" && mkdir -p "$WORK/tampered"
  cp -R "$WORK/tap-v1/plugins/claude-code" "$WORK/tap-v1/plugins/cursor" "$WORK/tampered/"
  for p in claude-code cursor; do
    sed "s#^url=$BASE#url=$BAD#" "$WORK/tap-v1/plugins/$p/scripts/recorder.lock" >"$WORK/tampered/$p/scripts/recorder.lock"
  done
  H=$(fresh_home tamper-cc)
  CLAUDE_PLUGIN_ROOT=$(npath "$WORK/tampered/claude-code") run_hook "tampered claude-code" "$H" "$(cc_start "$P")" "$CC"
  served=$(grep -c "GET /v$V1/sigdek_${V1}_${OSN}_${ARCHN}${EXE} HTTP/1.[01]\" 200" "$OUT/http-bad.log" 2>/dev/null)
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 0 ] && [ -z "$HOOK_OUT" ] && [ ! -e "$H/bin/$V1/sigdek$EXE" ] && [ -f "$H/bin/$V1/.download-failed" ]; then
    pass "plugin: tampered binary refused (Claude Code)" "server sent it (${served:-?} x 200); exit 0, nothing run, nothing cached, 0 records; .download-failed: $(cat "$H/bin/$V1/.download-failed" | cut -c1-20)..."
  else
    fail "plugin: tampered binary refused (Claude Code)" "exit=$HOOK_STATUS records=$HOOK_NEW out=$HOOK_OUT cache=$(ls -a "$H/bin/$V1" 2>&1 | tr '\n' ' ')"
  fi
  H=$(fresh_home tamper-cursor)
  CURSOR_PLUGIN_ROOT=$(npath "$WORK/tampered/cursor") run_hook "tampered cursor" "$H" "$(cur_ev preToolUse "$P")" "sh \"\${CURSOR_PLUGIN_ROOT}/scripts/launch.sh\" hook cursor preToolUse | grep '^{' || echo '{\"permission\":\"allow\"}'"
  if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{"permission":"allow"}' ] && [ $HOOK_NEW -eq 0 ] && [ ! -e "$H/bin/$V1/sigdek$EXE" ]; then
    pass "plugin: tampered binary refused (Cursor)" "exit 0, safe answer $HOOK_OUT, nothing cached, 0 records"
  else
    fail "plugin: tampered binary refused (Cursor)" "exit=$HOOK_STATUS out=$HOOK_OUT records=$HOOK_NEW"
  fi

  if [ -f "${REPO:-$WORK/repo}/.sigdek/launch.sh" ]; then
    log "== Plugin in a repository set up with sigdek init: the plugin stands aside"
    R=${REPO:-$WORK/repo}
    H=$(fresh_home plugin-aside)
    (cd "$R" && CLAUDE_PROJECT_DIR=$(npath "$R") CLAUDE_PLUGIN_DATA=$(npath "$WORK/plugin-data/claude-code") run_hook "plugin in an init repository" "$H" "$(cc_start "$(npath "$R")")" "$CC"
      if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 0 ]; then pass "plugin: stands aside in a sigdek init repository" "exit 0, 0 records (the repository's hook records)"; else fail "plugin: stands aside in a sigdek init repository" "exit=$HOOK_STATUS records=$HOOK_NEW"; fi)
  fi
  unset CLAUDE_PROJECT_DIR CURSOR_PROJECT_DIR CLAUDE_PLUGIN_ROOT CURSOR_PLUGIN_ROOT
fi

# ---------------------------------------------------------------------------------------------
if has brew; then
  if [ -z "${BREW:-}" ]; then
    BREW=$(command -v brew || ls /home/linuxbrew/.linuxbrew/bin/brew /opt/homebrew/bin/brew /usr/local/bin/brew 2>/dev/null | head -n 1)
  fi
  if [ -z "$BREW" ]; then
    fail "brew: setup" "Homebrew isn't installed here"
  else
    log "== Homebrew ($("$BREW" --version | head -n 1))"
    eval "$("$BREW" shellenv)"
    export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ANALYTICS=1 HOMEBREW_NO_ENV_HINTS=1 HOMEBREW_NO_COLOR=1
    brew untap tektonsoftware/sigdek >/dev/null 2>&1
    case "$(brew developer 2>&1 | head -n 1)" in *disabled*) BREW_DEV_WAS=off ;; *) BREW_DEV_WAS=on ;; esac
    out=$(brew tap tektonsoftware/sigdek "$WORK/sigdek.git" 2>&1)
    st=$?
    log "  brew tap tektonsoftware/sigdek $WORK/sigdek.git -> exit=$st: $(printf '%s' "$out" | tr '\n' ' ')"
    if [ $st -eq 0 ]; then
      TAPPED=1
      pass "brew: tap from a repository not named homebrew-*" "brew tap tektonsoftware/sigdek <path>/sigdek.git: $(printf '%s' "$out" | tail -n 1)"
      # The install command exactly as README.md's Homebrew row documents it.
      DOC=${DOC_README:-$SCRIPT_DIR/../README.md}
      doc_cmd=$(grep '^| macOS or Linux, Homebrew' "$DOC" | grep -o '`brew install [^`]*`' | head -n 1 | tr -d '`')
      log "  README ($DOC) documents: $doc_cmd"
      [ -n "$doc_cmd" ] || { fail "brew: README's install command" "not found in $DOC"; doc_cmd="brew install sigdek"; }
      out=$($doc_cmd 2>&1)
      st=$?
      log "  $doc_cmd -> exit=$st"; printf '%s\n' "$out" | sed 's/^/    /' >>"$LOG"
      if [ $st -eq 0 ]; then
        pass "brew: $doc_cmd (as README documents)" "$(printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g' | grep -E 'Trusted|Cellar/sigdek' | tr '\n' ' ')"
      else
        fail "brew: $doc_cmd (as README documents)" "exit=$st: $(printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g; s/%0A/ /g' | grep -iE 'error' | head -n 2 | tr '\n' ' ')"
        # The fully qualified name trusts this one formula and installs it.
        out=$(brew install tektonsoftware/sigdek/sigdek 2>&1)
        st=$?
        log "  brew install tektonsoftware/sigdek/sigdek -> exit=$st"; printf '%s\n' "$out" | sed 's/^/    /' >>"$LOG"
        if [ $st -eq 0 ]; then
          TRUSTED=1
          pass "brew: brew install tektonsoftware/sigdek/sigdek" "$(printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g' | grep -E 'Trusted|Cellar/sigdek' | tr '\n' ' ')"
        else
          fail "brew: brew install tektonsoftware/sigdek/sigdek" "exit=$st: $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
        fi
      fi
      if [ $st -eq 0 ]; then
        check_bin "brew" "$(brew --prefix)/bin/sigdek"
        out=$(brew test sigdek 2>&1); st=$?
        log "  brew test sigdek -> exit=$st: $(printf '%s' "$out" | tr '\n' ' ')"
        [ $st -eq 0 ] && pass "brew: brew test sigdek (the formula's test block)" "exit 0" || fail "brew: brew test sigdek" "exit=$st"
      fi
      brew info sigdek >>"$OUT/brew-info-$LABEL.txt" 2>&1
      # audit and style are developer commands that install tools; only where asked (the Linux runner).
      if [ "${BREW_AUDIT:-0}" = 1 ]; then
        brew audit --strict --tap tektonsoftware/sigdek >"$OUT/brew-audit-$LABEL.txt" 2>&1
        log "  brew audit --strict --tap -> exit=$?: $(tail -c 2500 "$OUT/brew-audit-$LABEL.txt" | tr '\n' ' ')"
        brew audit --new --formula tektonsoftware/sigdek/sigdek >"$OUT/brew-audit-new-$LABEL.txt" 2>&1
        log "  brew audit --new -> exit=$?: $(tail -c 2500 "$OUT/brew-audit-new-$LABEL.txt" | tr '\n' ' ')"
        brew style --formula tektonsoftware/sigdek/sigdek >"$OUT/brew-style-$LABEL.txt" 2>&1
        log "  brew style -> exit=$?: $(tail -c 2500 "$OUT/brew-style-$LABEL.txt" | tr '\n' ' ')"
      fi
    else
      fail "brew: tap" "exit=$st: $out"
    fi
  fi
fi

# ---------------------------------------------------------------------------------------------
if has upgrade; then
  log "== A second release ($V2): what each channel's user sees"
  if has npm; then
    publish "$V2"
    cd "$WORK/empty" || exit 1
    out=$(npx --yes sigdek version 2>&1)
    log "  npx --yes sigdek version after $V2 is published (npx cache from $V1, sigdek -g still $V1): $out"
    note "upgrade: npx sigdek, $V1 in the npx cache and installed with -g" "$out"
    out=$(cd "$WORK/empty2" 2>/dev/null || mkdir -p "$WORK/empty2" && cd "$WORK/empty2" && npx --yes sigdek@latest version 2>&1)
    log "  npx --yes sigdek@latest version: $out"
    note "upgrade: npx sigdek@latest" "$out"
    o2=$(npm outdated -g 2>&1)
    log "  npm outdated -g: $(printf '%s' "$o2" | tr '\n' ' ')"
    out=$(npm update -g sigdek 2>&1); st=$?
    log "  npm update -g sigdek -> exit=$st: $(printf '%s' "$out" | tr '\n' ' ')"
    out=$("$GBIN" version 2>&1)
    if [ "$out" = "sigdek $V2 ($OSN/$ARCHN)" ]; then pass "upgrade: npm update -g sigdek" "$out"; else fail "upgrade: npm update -g sigdek" "$out"; fi
    # npx, with no global install (a teammate who only ever ran npx sigdek)
    npm uninstall -g sigdek >>"$LOG" 2>&1
    cd "$WORK/empty" && out=$(npx --yes sigdek version 2>&1)
    log "  npx --yes sigdek version with no global install, npx cache from $V1: $out"
    note "upgrade: npx sigdek, $V1 in the npx cache only" "$out"
  fi
  if [ "${TAPPED:-0}" = 1 ]; then
    git -C "$WORK/sigdek.git" update-ref refs/heads/main "refs/tags/local-$V2"
    if [ "${BREW_UPDATE:-tap}" = full ]; then
      out=$(env -u HOMEBREW_NO_AUTO_UPDATE brew update 2>&1); st=$?
      log "  brew update -> exit=$st: $(printf '%s' "$out" | tail -n 5 | tr '\n' ' ')"
    else
      out=$(git -C "$(brew --repository tektonsoftware/sigdek)" pull -q --ff-only 2>&1); st=$?
      log "  (this Mac) tap-only update, git pull in $(brew --repository tektonsoftware/sigdek) -> exit=$st $out"
    fi
    log "  brew outdated: $(brew outdated 2>&1 | grep -i sigdek)"
    out=$(brew upgrade sigdek 2>&1); st=$?
    log "  brew upgrade sigdek -> exit=$st"; printf '%s\n' "$out" | sed 's/^/    /' >>"$LOG"
    out=$("$(brew --prefix)/bin/sigdek" version 2>&1)
    if [ "$out" = "sigdek $V2 ($OSN/$ARCHN)" ]; then pass "upgrade: brew upgrade sigdek" "$out"; else fail "upgrade: brew upgrade sigdek" "exit=$st, now: $out"; fi
  fi
  if has plugin; then
    # A plugin update (Claude Code fetches the new plugin version) pins the new recorder.
    H=$(fresh_home plugin-upgrade)
    rm -rf "$WORK/plugin-data-up"
    (cd "$WORK/plain" && CLAUDE_PROJECT_DIR=$(npath "$WORK/plain") CLAUDE_PLUGIN_ROOT=$(npath "$WORK/tap-v1/plugins/claude-code") CLAUDE_PLUGIN_DATA=$(npath "$WORK/plugin-data-up") \
      run_hook "plugin at $V1" "$H" "$(cc_start "$(npath "$WORK/plain")")" 'sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" hook claude-code; exit 0'
     CLAUDE_PROJECT_DIR=$(npath "$WORK/plain") CLAUDE_PLUGIN_ROOT=$(npath "$WORK/tap-v2/plugins/claude-code") CLAUDE_PLUGIN_DATA=$(npath "$WORK/plugin-data-up") \
      run_hook "plugin at $V2" "$H" "$(cc_start "$(npath "$WORK/plain")")" 'sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" hook claude-code; exit 0'
     if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ] && [ -f "$WORK/plugin-data-up/$V2/sigdek$EXE" ]; then
       pass "upgrade: plugin pinned to $V2" "downloaded $V2 next to $V1 ($(ls "$WORK/plugin-data-up" | tr '\n' ' ')), recorded"
     else
       fail "upgrade: plugin pinned to $V2" "records=$HOOK_NEW data=$(ls "$WORK/plugin-data-up" 2>&1 | tr '\n' ' ')"
     fi)
  fi
fi

# ---------------------------------------------------------------------------------------------
if has slow; then
  NAME=sigdek_${V1}_${OSN}_${ARCHN}${EXE}
  SIZE=$(wc -c <"$WORK/srv/v$V1/$NAME" | tr -d ' ')
  log "== Slow link: the plugin launchers' first download at $SLOW_RATE bytes a second ($NAME, $SIZE bytes: about $((SIZE / SLOW_RATE)) s; the hook allows 8 s)"
  for i in $(seq 1 50); do curl -fsS -o /dev/null "$SLOW/v$V1/checksums.txt" 2>/dev/null && break; sleep 0.2; done
  SP=$WORK/slow-plugins SD=$WORK/slow-data
  rm -rf "$SP" "$SD" "$WORK/slow-plain" && mkdir -p "$SP" "$WORK/slow-plain" && git -C "$WORK/slow-plain" init -q
  cp -R "$WORK/tap-v1/plugins/claude-code" "$WORK/tap-v1/plugins/cursor" "$SP/"
  for p in claude-code cursor; do sed "s#^url=$BASE#url=$SLOW#" "$WORK/tap-v1/plugins/$p/scripts/recorder.lock" >"$SP/$p/scripts/recorder.lock"; done
  mkdir -p "$SP/bad" && cp -R "$WORK/tap-v1/plugins/claude-code" "$SP/bad/"
  sed "s#^url=$BASE#url=$SLOW_BAD#" "$WORK/tap-v1/plugins/claude-code/scripts/recorder.lock" >"$SP/bad/claude-code/scripts/recorder.lock"
  grep -q "^later()" "$SP/claude-code/scripts/launch.sh" && log "  the pinned plugin's launch.sh has the background download" || fail "slow: setup" "the pinned plugins' launch.sh has no background download"
  cd "$WORK/slow-plain" || exit 1
  P=$(npath "$WORK/slow-plain")
  export CLAUDE_PROJECT_DIR=$P CURSOR_PROJECT_DIR=$P
  CC='sh "${CLAUDE_PLUGIN_ROOT}/scripts/launch.sh" hook claude-code; exit 0'
  gets() { grep -c "GET /v$V1/$NAME HTTP" "$OUT/$1" 2>/dev/null; }
  landed() { [ -f "$1/sigdek$EXE" ] && [ ! -e "$1/.downloading" ]; }
  settled() { [ ! -e "$1/.downloading" ] && [ -z "$(ls "$1" 2>/dev/null | grep '\.download$')" ]; }
  base=$(gets http-slow.log) # on Windows, launch.ps1's slow checks used the same server

  # Claude Code, cache in CLAUDE_PLUGIN_DATA.
  CCD=$SD/claude-code/$V1 HCC=$(fresh_home slow-cc)
  CLAUDE_PLUGIN_ROOT=$(npath "$SP/claude-code") CLAUDE_PLUGIN_DATA=$(npath "$SD/claude-code") run_hook "slow claude-code first use" "$HCC" "$(cc_start "$P")" "$CC"
  T_CC=$(now_ms)
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 0 ] && [ -z "$HOOK_OUT" ] && [ $HOOK_MS -lt 9500 ] && [ ! -e "$CCD/sigdek$EXE" ] && [ -e "$CCD/.downloading" ]; then
    pass "slow: Claude Code launcher, first use" "exit 0 in ${HOOK_MS} ms, nothing printed, 0 records; download handed to the background"
  else
    fail "slow: Claude Code launcher, first use" "exit=$HOOK_STATUS ms=$HOOK_MS records=$HOOK_NEW out=$HOOK_OUT cache=$(ls -a "$CCD" 2>&1 | tr '\n' ' ')"
  fi
  CLAUDE_PLUGIN_ROOT=$(npath "$SP/claude-code") CLAUDE_PLUGIN_DATA=$(npath "$SD/claude-code") run_hook "slow claude-code during the download" "$HCC" "$(cc_post "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 0 ] && [ -z "$HOOK_OUT" ] && [ $HOOK_MS -lt 2000 ] && [ ! -e "$CCD/sigdek$EXE" ]; then
    pass "slow: Claude Code launcher, an event during the download" "exit 0 in ${HOOK_MS} ms, 0 records, no second download"
  else
    fail "slow: Claude Code launcher, an event during the download" "exit=$HOOK_STATUS ms=$HOOK_MS records=$HOOK_NEW out=$HOOK_OUT"
  fi

  # Cursor, through its `| grep` hook command, cache in SIGDEK_HOME/bin.
  HCU=$(fresh_home slow-cursor)
  CURSOR_PLUGIN_ROOT=$(npath "$SP/cursor") run_hook "slow cursor first use" "$HCU" "$(cur_ev preToolUse "$P")" "sh \"\${CURSOR_PLUGIN_ROOT}/scripts/launch.sh\" hook cursor preToolUse | grep '^{' || echo '{\"permission\":\"allow\"}'"
  T_CU=$(now_ms)
  if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{"permission":"allow"}' ] && [ $HOOK_NEW -eq 0 ] && [ $HOOK_MS -lt 9500 ]; then
    pass "slow: Cursor launcher, first use" "exit 0 in ${HOOK_MS} ms, answer $HOOK_OUT through grep, 0 records"
  else
    fail "slow: Cursor launcher, first use" "exit=$HOOK_STATUS ms=$HOOK_MS out=$HOOK_OUT records=$HOOK_NEW"
  fi

  # Tampered, on the slow server: the background download is refused too.
  HBAD=$(fresh_home slow-bad)
  CLAUDE_PLUGIN_ROOT=$(npath "$SP/bad/claude-code") run_hook "slow tampered claude-code" "$HBAD" "$(cc_start "$P")" "$CC"
  BAD_MS=$HOOK_MS BAD_ST=$HOOK_STATUS BAD_OUT=$HOOK_OUT

  # Wait for all three (up to 5 minutes, the background limit).
  CC_LAND= CU_LAND=
  for i in $(seq 1 600); do
    [ -z "$CC_LAND" ] && landed "$CCD" && CC_LAND=$(($(now_ms) - T_CC))
    [ -z "$CU_LAND" ] && landed "$HCU/bin/$V1" && CU_LAND=$(($(now_ms) - T_CU))
    [ -n "$CC_LAND" ] && [ -n "$CU_LAND" ] && settled "$HBAD/bin/$V1" && break
    sleep 0.5
  done
  log "  landed: Claude Code ${CC_LAND:-never} ms, Cursor ${CU_LAND:-never} ms after their first hooks returned"
  ls -la "$CCD" "$HCU/bin/$V1" "$HBAD/bin/$V1" >>"$LOG" 2>&1
  n=$(($(gets http-slow.log) - base))
  if [ -n "$CC_LAND" ] && [ -n "$CU_LAND" ] && [ ! -e "$CCD/.download-failed" ] && [ "$n" = 4 ]; then
    pass "slow: the background downloads land" "Claude Code ${CC_LAND} ms and Cursor ${CU_LAND} ms after the first hook returned; $n requests (each hook's and one in the background); no .download-failed"
  else
    fail "slow: the background downloads land" "cc=${CC_LAND:-never} cursor=${CU_LAND:-never} requests=$n cache=$(ls -a "$CCD" 2>&1 | tr '\n' ' ')"
  fi
  CLAUDE_PLUGIN_ROOT=$(npath "$SP/claude-code") CLAUDE_PLUGIN_DATA=$(npath "$SD/claude-code") run_hook "slow claude-code next event" "$HCC" "$(cc_post "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ $HOOK_NEW -eq 1 ] && [ -z "$HOOK_OUT" ]; then pass "slow: Claude Code, the next event records" "exit 0, 1 record; ${HOOK_MS} ms"; else fail "slow: Claude Code, the next event records" "exit=$HOOK_STATUS records=$HOOK_NEW out=$HOOK_OUT"; fi
  CURSOR_PLUGIN_ROOT=$(npath "$SP/cursor") run_hook "slow cursor next event" "$HCU" "$(cur_ev sessionStart "$P")" "sh \"\${CURSOR_PLUGIN_ROOT}/scripts/launch.sh\" hook cursor sessionStart | grep '^{' || echo '{}'"
  if [ $HOOK_STATUS -eq 0 ] && [ "$HOOK_OUT" = '{}' ] && [ $HOOK_NEW -eq 1 ]; then pass "slow: Cursor, the next event records" "exit 0, answer {}, 1 record; ${HOOK_MS} ms"; else fail "slow: Cursor, the next event records" "exit=$HOOK_STATUS out=$HOOK_OUT records=$HOOK_NEW"; fi
  out=$(SIGDEK_HOME=$HCC "$CCD/sigdek$EXE" verify 2>&1) && pass "slow: sigdek verify on those records" "$(printf '%s' "$out" | head -n 1)" || fail "slow: sigdek verify" "$out"

  stamp=$(cat "$HBAD/bin/$V1/.download-failed" 2>/dev/null)
  want=$(awk -v n="$NAME" '$2==n {print $1}' "$ART/dist/$V1/checksums.txt")
  nb=$(gets http-slow-bad.log)
  if [ "$BAD_ST" -eq 0 ] && [ -z "$BAD_OUT" ] && [ "$BAD_MS" -lt 9500 ] && [ ! -e "$HBAD/bin/$V1/sigdek$EXE" ] && [ "$stamp" = "1 $want" ] && [ "$(records "$HBAD")" = 0 ] && settled "$HBAD/bin/$V1"; then
    pass "slow: tampered binary refused by the background download" "hook exit 0 in ${BAD_MS} ms; $nb requests; nothing cached, no temporary file, 0 records; .download-failed: 1 ${want:0:12}..."
  else
    fail "slow: tampered binary refused by the background download" "exit=$BAD_ST ms=$BAD_MS out=$BAD_OUT stamp=$stamp requests=$nb cache=$(ls -a "$HBAD/bin/$V1" 2>&1 | tr '\n' ' ')"
  fi
  CLAUDE_PLUGIN_ROOT=$(npath "$SP/bad/claude-code") run_hook "slow tampered, next event" "$HBAD" "$(cc_post "$P")" "$CC"
  if [ $HOOK_STATUS -eq 0 ] && [ "$(gets http-slow-bad.log)" = "$nb" ]; then pass "slow: after the refused download, the next event backs off" "exit 0 in ${HOOK_MS} ms, no request"; else fail "slow: back-off after the refused download" "exit=$HOOK_STATUS requests=$(gets http-slow-bad.log)"; fi
  grep -E "went away|sent all" "$OUT/http-slow.log" "$OUT/http-slow-bad.log" | sed 's/^/  /' >>"$LOG"
  unset CLAUDE_PROJECT_DIR CURSOR_PROJECT_DIR
fi

log "== Done on $LABEL"
grep -q "^FAIL | $LABEL " "$RESULTS" && FAILED=1
grep -E "^(PASS|FAIL|NOTE) \| $LABEL " "$RESULTS" | sort | uniq -c | awk '{print $2}' | sort | uniq -c | tee -a "$LOG"
exit $FAILED
