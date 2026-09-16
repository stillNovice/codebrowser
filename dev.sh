#!/bin/sh
# dev.sh — rebuild and restart the codebrowse tmux session on every change.
# Usage: ./dev.sh [repo-to-browse]
set -eu
cd "$(dirname "$0")"

SESSION=codebrowse
REPO="${1:-.}"

serve() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -c "$PWD" \
    "env -u OPENAI_API_KEY -u OPENAI_BASE_URL -u CODEBROWSE_MODEL \
     ./codebrowse -repo '$REPO' 2>&1 | tee /tmp/codebrowse.log; \
     echo '--- server exited, press enter to close'; read"
  echo "[dev] restarted tmux session '$SESSION' (tmux attach -t $SESSION)"
}

build() {
  gofmt -w . && go vet ./... && go build -buildvcs=false -o codebrowse .
}

if build; then
  serve
else
  echo "[dev] initial build failed; fix and save"
fi

# poll mtimes every second; restart on any change (no fswatch dependency)
last=""
while :; do
  cur=$(find . -name '*.go' -o -name '*.js' -o -name '*.css' -o -name '*.html' \
        | grep -v '^\./\.git' | xargs stat -f '%N %m' 2>/dev/null | sort | shasum)
  if [ -n "$last" ] && [ "$cur" != "$last" ]; then
    echo "[dev] change detected"
    if build; then serve; else echo "[dev] build failed, keeping old server"; fi
  fi
  last="$cur"
  sleep 1
done
