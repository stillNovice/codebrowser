#!/bin/sh
# serve.sh — (re)build codebrowse and (re)start it for a repo.
# Always serves the latest code; kills any instance already running
# for the same repo. Prints the actual URL on stdout.
#
# Usage: serve.sh [-r repo] [-o file] [-p port] [-s session] [--attach] [--chat]
set -eu
cd "$(dirname "$0")"

REPO="."; OPEN=""; PORT=4004; SESSION=""; ATTACH=0; CHAT=0; MODE=""; NOCHAT=0
while [ $# -gt 0 ]; do
  case "$1" in
    -r|--repo)    REPO="$2"; shift 2;;
    -o|--open)    OPEN="$2"; shift 2;;
    -p|--port)    PORT="$2"; shift 2;;
    -s|--session) SESSION="$2"; shift 2;;
    --attach)     ATTACH=1; shift;;
    --mode)       MODE="$2"; shift 2;;
    --no-chat)    NOCHAT=1; shift;;
    --chat)       CHAT=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

REPO=$(cd "$REPO" && pwd)   # canonicalize

echo "[serve] building latest binary…" >&2
gofmt -w . >/dev/null 2>&1 || true
go build -buildvcs=false -ldflags "-X main.BuildVersion=$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o codebrowse . || { echo "[serve] build FAILED" >&2; exit 1; }

# Kill any codebrowse already serving this repo (stale binaries lie).
pkill -f "codebrowse -repo $REPO" 2>/dev/null && echo "[serve] killed stale instance for $REPO" >&2 || true
# Also kill by legacy/binary-path variants of the same repo.
pkill -f "codebrowse .*-repo $REPO" 2>/dev/null || true
sleep 0.5

ARGS="-repo '$REPO' -port $PORT"
[ -n "$OPEN" ]    && ARGS="$ARGS -open '$OPEN'"
[ -n "$SESSION" ] && ARGS="$ARGS -session '$SESSION'"
[ "$ATTACH" = 1 ] && ARGS="$ARGS -attach"
[ -n "$MODE" ] && ARGS="$ARGS -mode $MODE"
# Both modes can host the pi session in the browser (chat + attach),
# unless explicitly overridden with --no-chat. Build mode uses the same
# attached session so users can work without switching back to terminal pi.
if { [ "$MODE" = "review" ] || [ "$MODE" = "build" ]; } && [ "$NOCHAT" != 1 ]; then
  ARGS="$ARGS -chat -attach"
fi
[ "$CHAT" = 1 ]   && ARGS="$ARGS -chat"

LOG=/tmp/codebrowse-$(echo "$REPO" | shasum | cut -c1-8).log
# env -u: keep OPENAI_* from hijacking pi's model catalog
eval "nohup env -u OPENAI_API_KEY -u OPENAI_BASE_URL -u CODEBROWSE_MODEL \
  ./codebrowse $ARGS > '$LOG' 2>&1 &"

# Wait for the serving line and report the real URL (port may probe upward).
for i in 1 2 3 4 5 6 7 8 9 10; do
  URL=$(grep -o 'http://[^ ]*' "$LOG" 2>/dev/null | tail -1) && [ -n "$URL" ] && break
  sleep 0.5
done
echo "[serve] repo: $REPO" >&2
echo "[serve] log:  $LOG" >&2
echo "${URL:-http://127.0.0.1:$PORT}"
