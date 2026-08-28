#!/bin/sh
# Shared UserPromptSubmit adapter for Codex and Claude Code.
# Host event JSON is read from stdin. No prompt or credential is printed.
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
exec "$DIST_DIR/bin/mindmoryctl" checkpoint-hook --host "$HOST_NAME"
