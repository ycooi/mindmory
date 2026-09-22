#!/bin/sh
# Shared UserPromptSubmit/Stop adapter for Codex, DeepSeek Harness, and Claude Code.
# Host event JSON is read from stdin. No conversation content or credential is
# printed to stderr. Codex and DeepSeek Harness use the native hook path:
# UserPromptSubmit archives the prompt and returns a bounded relevance packet
# directly as hook context; Stop archives the completed response. Other hosts
# retain checkpoint-only behavior.
set -eu

HOST_NAME="${1:-generic}"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DIST_DIR=$(dirname -- "$SCRIPT_DIR")
CONFIG_FILE="$DIST_DIR/mindmory-config.sh"

if [ ! -f "$CONFIG_FILE" ]; then
  printf '%s\n' 'Mindmory checkpoint skipped: run setup.sh first' >&2
  exit 1
fi

set -a
. "$CONFIG_FILE"
set +a
case "$HOST_NAME" in
  codex|deepseek-harness)
    exec "$DIST_DIR/bin/mindmoryctl" native-hook --host "$HOST_NAME" --max-chars 320 --max-memories 3
    ;;
  *)
    exec "$DIST_DIR/bin/mindmoryctl" checkpoint-hook --host "$HOST_NAME"
    ;;
esac
