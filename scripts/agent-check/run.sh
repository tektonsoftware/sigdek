#!/usr/bin/env bash
# shellcheck disable=SC2016 # the backticks in the prompt and the JavaScript are meant literally
# Runs one real agent session against the released SigDek recorder, in a scratch repository with
# its own SIGDEK_HOME, then checks what was recorded (check-log.mjs). Used by
# .github/workflows/agent-check.yml after each new Claude Code release, and by hand.
#
#   scripts/agent-check/run.sh init     Claude Code, with the hooks `npx sigdek@latest init` writes
#   scripts/agent-check/run.sh plugin   Claude Code, with this repository's plugin (--plugin-dir)
#   scripts/agent-check/run.sh cursor   Cursor's terminal agent (`agent`), with `init`'s hooks
#
# Needs `claude` (or `agent`) on PATH and signed in: ANTHROPIC_API_KEY or CURSOR_API_KEY in CI.
# It never signs the recorder in, so nothing leaves the machine. The Block rule is a cached team
# rule set written to SIGDEK_HOME/rules.json. SIGDEK_CHECK_WORK picks the scratch folder.
set -euo pipefail

mode=${1:-}
case "$mode" in init | plugin | cursor) ;; *) echo "usage: $0 init|plugin|cursor" >&2; exit 2 ;; esac
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=${SIGDEK_CHECK_WORK:-$(mktemp -d)}/$mode
repo=$work/repo
rm -rf "$work"
mkdir -p "$repo" "$work/home"
export SIGDEK_HOME=$work/home
# What customers run: the launchers download the pinned release and check its SHA-256.
unset SIGDEK_BIN SIGDEK_CACHE

cat >"$SIGDEK_HOME/rules.json" <<'EOF'
{
  "version": 1,
  "rules": [
    {
      "id": "release-check-block",
      "label": "Release check block",
      "effect": "deny",
      "kinds": ["shell"],
      "command": "touch sigdek-check-blocked*",
      "message": "Blocked by SigDek's release check."
    }
  ]
}
EOF

cd "$repo"
git init -q -b main
git config user.name "SigDek release check"
git config user.email "release-check@sigdek.example"
git remote add origin https://github.com/example-org/sigdek-release-check.git
echo "# Scratch repository for SigDek's release check" >README.md
git add README.md
git commit -q -m "Scratch repository"

# Fetch the pinned recorder before the session, so the first hook doesn't spend its wait on it.
warm() {
  sh "$1" version >/dev/null 2>&1 || true
  for _ in $(seq 1 60); do
    ls "$2"/*/sigdek >/dev/null 2>&1 && return 0
    sleep 2
  done
  echo "the recorder pinned by $1 didn't download within 2 minutes" >&2
  return 1
}

if [ "$mode" != plugin ]; then
  npx -y sigdek@latest init
  warm .sigdek/launch.sh "$SIGDEK_HOME/bin"
  git add -A
  git commit -q -m "SigDek init"
fi

prompt='This is an automated check of a logging tool. Do exactly these steps, in order, one tool call each, and nothing else:
1. Run the shell command `ls` (no options).
2. Run the shell command `git status --short`.
3. Create the file notes.txt containing the single line: hello
4. Read the file notes.txt.
5. Run the shell command `touch sigdek-check-blocked.txt`. It is expected to be refused. If it is, do not retry it and do not work around it.
Then reply with the single word: done'

out=$work/agent-output.json
# At most 10 minutes a session, so an agent that hangs or loops can't run up the bill
# (`timeout` is GNU coreutils: on the CI runner, not on a stock Mac).
limit=()
command -v timeout >/dev/null 2>&1 && limit=(timeout 600)

case "$mode" in
  init | plugin)
    echo "Claude Code $(claude --version)"
    plugin=()
    [ "$mode" = plugin ] && plugin=(--plugin-dir "$root/plugins/claude-code")
    # Only the project's settings, so a person's own hooks or plugins can't change the result.
    printf '%s\n' "$prompt" | ${limit[@]+"${limit[@]}"} claude -p ${plugin[@]+"${plugin[@]}"} \
      --model haiku --max-budget-usd 0.25 --output-format json \
      --setting-sources project,local --permission-prompts none \
      --allowedTools "Bash(ls)" "Bash(git status --short)" "Bash(touch sigdek-check-blocked.txt)" Write Read \
      >"$out" || echo "claude exited $?"
    node -e 'const o = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); console.log(`Claude Code answered: ${String(o.result ?? "").slice(0, 200)} (cost $${o.total_cost_usd ?? "?"}, ${o.num_turns ?? "?"} turns)`)' "$out" || cat "$out"
    ;;
  cursor)
    echo "Cursor agent $(agent --version)"
    trust=()
    case "$(agent --help 2>&1 || true)" in *--trust*) trust=(--trust) ;; esac
    model=()
    [ -n "${SIGDEK_CHECK_CURSOR_MODEL:-}" ] && model=(--model "$SIGDEK_CHECK_CURSOR_MODEL")
    ${limit[@]+"${limit[@]}"} agent -p --force ${trust[@]+"${trust[@]}"} ${model[@]+"${model[@]}"} --output-format text "$prompt" >"$out" 2>&1 || echo "agent exited $?"
    echo "Cursor agent answered: $(head -c 300 "$out")"
    ;;
esac

# Give the hooks a moment to finish writing (Claude Code doesn't wait for SessionEnd hooks).
sleep 3

tool=claude-code
[ "$mode" = cursor ] && tool=cursor
launcher=.sigdek/launch.sh
if [ "$mode" = plugin ]; then
  # The plugin's hooks kept their recorder in Claude Code's plugin data folder; verify runs the
  # launcher outside Claude Code, so it needs its own copy first.
  launcher=$root/plugins/claude-code/scripts/launch.sh
  warm "$launcher" "$SIGDEK_HOME/bin"
fi
status=0
node "$here/check-log.mjs" "$SIGDEK_HOME" "$tool" "$repo" || status=1
# The launcher exits 0 when it can't run the recorder, so look for verify's own answer.
verified=$(sh "$launcher" verify 2>&1 || true)
echo "$verified"
case "$verified" in
  *"records on this machine are intact"*) echo "ok   sigdek verify" ;;
  *) echo "FAIL sigdek verify"; status=1 ;;
esac
exit $status
