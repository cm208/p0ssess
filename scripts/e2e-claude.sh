#!/bin/sh
# Live end-to-end check of the plugin in a real Claude Code session, in a scratch config, so nothing in
# ~/.claude or ~/.claude.json changes. It spends a few cents of OpenRouter credit.
#
#   scripts/e2e-claude.sh DIR
#
# DIR is a scratch directory. The first run stops and asks you to log in once inside the scratch config
# (CLAUDE_CONFIG_DIR=DIR/claude claude, then /login). Needs Go, ffmpeg and an OpenRouter key in
# ~/.config/p0ssess/secrets/openrouter_key or $P0SSESS_OPENROUTER_KEY_FILE; podman for the agent path.
set -eu
dir=$(mkdir -p "${1:?usage: e2e-claude.sh DIR}" && cd "$1" && pwd)
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
export CLAUDE_CONFIG_DIR="$dir/claude" P0SSESS_HOME="$dir/p0ssess"
mkdir -p "$CLAUDE_CONFIG_DIR" "$P0SSESS_HOME"

if [ ! -f "$CLAUDE_CONFIG_DIR/.credentials.json" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "Log in once in the scratch config, then run this again:"
  echo "  CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR claude    (then /login, then /exit)"
  exit 2
fi

proj=$dir/proj
if [ ! -d "$proj/.git" ]; then
  mkdir -p "$proj/media"
  printf 'def median(xs):\n    s = sorted(xs)\n    n = len(s)\n    return s[n // 2] if n %% 2 else (s[n // 2 - 1] + s[n // 2]) / 2\n' > "$proj/stats.py"
  git -C "$proj" init -q && git -C "$proj" add . && git -C "$proj" -c user.name=e2e -c user.email=e2e@example.com commit -qm init
  ffmpeg -loglevel error -f lavfi -i "color=c=red:s=640x360:d=5" -f lavfi -i "color=c=blue:s=640x360:d=5" \
    -f lavfi -i "sine=frequency=440:duration=10" -filter_complex "[0:v][1:v]concat=n=2:v=1[v]" -map "[v]" -map 2:a \
    -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$proj/media/demo.mp4"
  echo "media/" >> "$proj/.git/info/exclude"
fi

"$repo/bin/p0ssess" setup allow
"$repo/bin/p0ssess" setup statusline

run() {
  name=$1
  shift
  (cd "$proj" && claude -p --plugin-dir "$repo" --output-format stream-json --verbose "$@") > "$dir/$name.jsonl" 2> "$dir/$name.err" || true
}

check() {
  if grep -q "$2" "$dir/$1.jsonl"; then echo "pass  $1: $3"; else echo "FAIL  $1: $3 (see $dir/$1.jsonl)"; fi
}

run hint "Reply with the word ok."
run relay "use deepseek flash to explain what median() in stats.py does with an empty list"
run video "What happens in media/demo.mp4?"
run approve --allowedTools mcp__plugin_p0ssess_p0ssess__approvals_add \
  "Auto-approve media jobs up to 5 cents each for today. Save it without asking me again."
run video2 "Describe media/demo.mp4 again, briefly."
run me "/p0ssess:p0ssess-me"

check hint '"type":"system"' "session started with the plugin"
check relay '"subagent_type":"p0ssess:p0ssess"' "named model went through the relay agent"
check video '"subagent_type":"p0ssess:p0ssess"' "video went to the p0ssess agent"
check video 'mcp__plugin_p0ssess_p0ssess__direct' "the agent used the direct path"
check approve 'auto-approve for media' "plain-language approval became a structured rule"
check video2 '"subagent_type":"p0ssess:p0ssess"' "the second media job went to the agent too"
check me 'month \$' "/p0ssess-me printed the summary"
echo "status line now:"
printf '{"session_id":"e2e","cwd":"%s","model":{"id":"claude-sonnet-5-5"}}' "$proj" | NO_COLOR=1 "$P0SSESS_HOME/bin/p0ssess" statusline
