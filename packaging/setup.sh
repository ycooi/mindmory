#!/bin/sh
# Mindmory MCP (lite) — first-run initialization sequence.
#
# Generates fresh, per-instance secrets, writes ./mindmory-config.sh (mode 600), starts the
# single-process lite daemon (no Docker, no PostgreSQL), waits until it
# reports ready, and prints local-agent integration choices.
#
# Everything generated here is random and lives only on this machine. No
# telemetry, no cloud, no shared state. Your memories stay in ./var/data/ as
# human-readable JSONL (plus a derived SQLite search index, var/index.db).
#
# Usage:
#   ./setup.sh                       interactive first run
#   ./setup.sh --yes                 non-interactive (auto-answers prompts)
#   ./setup.sh --owner "Alice"       set the owner name shown in the stack
#   ./setup.sh --http-port 58080     port for the daemon (default 58080)
#   ./setup.sh --reset               overwrite an existing config
#   ./setup.sh --skip-start          generate config only; you start the daemon
#   ./setup.sh --agent --complete-mcp agent-safe setup/repair; emits JSON without secrets
#   ./setup.sh --help                show this help

set -eu

# --------------------------------------------------------------------------
# defaults
# --------------------------------------------------------------------------
OWNER=""
HTTP_PORT=58080
YES=0
RESET=0
SKIP_START=0
AGENT=0
COMPLETE_MCP=0

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --owner)         OWNER="${2:-}"; shift 2 ;;
    --http-port)     HTTP_PORT="${2:-}"; shift 2 ;;
    --yes)           YES=1; shift ;;
    --reset)         RESET=1; shift ;;
    --skip-start)    SKIP_START=1; shift ;;
    --agent)         AGENT=1; shift ;;
    --complete-mcp)  COMPLETE_MCP=1; AGENT=1; shift ;;
    --help|-h)       usage ;;
    *) echo "error: unknown option: $1" >&2; usage ;;
  esac
done

say()  {
  if [ "$AGENT" -eq 1 ]; then
    printf 'setup: %s\n' "$*" >&2
  else
    printf '\033[1;32m==>\033[0m %s\n' "$*"
  fi
}
warn() { printf '\033[1;33m!!>\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[1;31m!!>\033[0m %s\n' "$*" >&2; exit 1; }
shell_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

# --------------------------------------------------------------------------
# 0. platform sanity — the tarball ships a binary set per platform
# --------------------------------------------------------------------------
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) fail "unsupported OS: $OS (Mindmory MCP ships macOS and Linux binaries)" ;;
esac
case "$ARCH" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "unsupported architecture: $ARCH" ;;
esac

for bin in mindmoryd-lite mindmory-mcp-stdio mindmoryctl; do
  [ -x "bin/$bin" ] || fail "missing ./bin/$bin — this is a platform-specific package (you appear to be on $os/$arch); download the right tarball"
done

# --------------------------------------------------------------------------
# 1. prerequisites — no Docker needed, just an executable daemon
# --------------------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  GET="curl -fsS"
elif command -v wget >/dev/null 2>&1; then
  GET="wget -qO-"
else
  GET=""
  warn "curl/wget not found — readiness will not be polled; check http://127.0.0.1:$HTTP_PORT/health/ready manually"
fi

case "$HTTP_PORT" in
  ''|*[!0-9]*) fail "--http-port must be a number" ;;
esac

# Agent completion is idempotent: reuse an existing protected configuration
# instead of rotating secrets or overwriting memory identity.
EXISTING_CONFIG=0
if [ "$COMPLETE_MCP" -eq 1 ] && [ -f mindmory-config.sh ]; then
  set -a
  . ./mindmory-config.sh
  set +a
  OWNER="${MINDMORY_OWNER:-mindmory}"
  HTTP_PORT="${MINDMORY_HTTP_PORT:-58080}"
  EXISTING_CONFIG=1
fi

# --------------------------------------------------------------------------
# 2. random secrets (per instance, never shared)
# --------------------------------------------------------------------------
if [ "$EXISTING_CONFIG" -eq 0 ]; then
rand_hex() {
  chars="${1:-64}"
  bytes=$(( (chars + 1) / 2 ))
  if command -v openssl >/dev/null 2>&1; then
    h="$(openssl rand -hex "$bytes" 2>/dev/null || true)"
  fi
  if [ -z "${h:-}" ] && [ -r /dev/urandom ]; then
    h="$(od -An -N "$bytes" -tx1 /dev/urandom 2>/dev/null | tr -d ' \n' || true)"
  fi
  if [ -z "${h:-}" ]; then
    h="$( (date +%s%N 2>/dev/null; cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen 2>/dev/null) | cksum | awk '{print $1$1}' || true)"
    while [ "${#h}" -lt "$chars" ]; do h="${h}${h}"; done
  fi
  printf '%s' "$h" | cut -c1-"$chars"
}

CURSOR_KEY="$(rand_hex 64)"            # >= 32 bytes, signs session cursors
ADMIN_TOKEN="$(rand_hex 48)"           # >= 24 bytes, operator (used if MINDMORY_AUTH=token)
CLIENT_TOKEN="$(rand_hex 48)"          # the token your assistant presents

# The daemon defaults to local-trust mode on loopback (MINDMORY_AUTH=token
# restores verification), but a client token is still configured so the same
# env works if the surface is ever exposed beyond 127.0.0.1.
MCP_CLIENT_TOKENS_JSON="{\"local-agent\":{\"token\":\"$CLIENT_TOKEN\",\"capabilities\":[\"CONTEXT_READ\",\"ARCHIVE_CHECKPOINT\",\"MEMORY_PROPOSE\",\"ARTIFACT_SEARCH\",\"ARTIFACT_READ\",\"RESOURCE_READ\",\"OPS_READ\"]}}"

# --------------------------------------------------------------------------
# 3. owner name
# --------------------------------------------------------------------------
if [ -z "$OWNER" ]; then
  default_owner="$(hostname 2>/dev/null || echo mindmory)"
  if [ "$YES" -eq 0 ] && [ -t 0 ]; then
    printf 'Owner name (the single owner of this instance) [%s]: ' "$default_owner"
    read -r OWNER || true
  fi
  OWNER="${OWNER:-$default_owner}"
fi

# --------------------------------------------------------------------------
# 4. write ./mindmory-config.sh (only if allowed)
# --------------------------------------------------------------------------
if [ -f mindmory-config.sh ]; then
  if [ "$RESET" -eq 1 ]; then
    warn "replacing existing ./mindmory-config.sh (fresh secrets)"
  elif [ "$YES" -eq 0 ] && [ -t 0 ]; then
    printf 'mindmory-config.sh already exists — overwrite with fresh secrets? [y/N]: '
    read -r ans || true
    case "$ans" in
      y|Y|yes|YES) warn "replacing existing ./mindmory-config.sh (fresh secrets)" ;;
      *) echo "aborted — nothing changed"; exit 1 ;;
    esac
  else
    fail "mindmory-config.sh already exists (use --reset to overwrite it)"
  fi
fi

umask 077
OWNER_ENV="$(shell_quote "$OWNER")"
cat > mindmory-config.sh <<EOF
# Mindmory MCP (lite) — generated by setup.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ).
# These secrets are random and unique to this machine. Never share this file.

# --- Identity -------------------------------------------------------------
MINDMORY_OWNER=$OWNER_ENV
MINDMORY_CURSOR_SIGNING_KEY=$CURSOR_KEY

# --- Host port and data ---------------------------------------------------
MINDMORY_HTTP_PORT=$HTTP_PORT
MINDMORY_DATA_DIR=var/data

# --- Operator credential (always enforced on administrative routes) --------
MINDMORY_ADMIN_TOKEN=$ADMIN_TOKEN

# --- Daemon client principals ----------------------------------------------
# Single-quoted so the JSON survives `set -a; . ./mindmory-config.sh` sourcing
# and shell sourcing.
MINDMORY_MCP_CLIENT_TOKENS_JSON='$MCP_CLIENT_TOKENS_JSON'
MINDMORY_LOCAL_CLIENT_KEY=local-agent

# --- MCP stdio server (runs on the host, launched by your assistant) --------
# MINDMORY_MCP_TOKEN must equal the token inside MINDMORY_MCP_CLIENT_TOKENS_JSON.
MINDMORY_ENDPOINT=http://127.0.0.1:$HTTP_PORT
MINDMORY_MCP_TOKEN=$CLIENT_TOKEN
MINDMORY_MCP_LOG_LEVEL=INFO
EOF
chmod 600 mindmory-config.sh
say "mindmory-config.sh written (chmod 600) with fresh secrets for owner '$OWNER'"
else
  say "reusing existing mindmory-config.sh; credentials were not rotated or printed"
fi

if [ "$SKIP_START" -eq 1 ]; then
  say "skipped starting the daemon (--skip-start). Start it yourself with:"
  echo "    set -a; . ./mindmory-config.sh; set +a; nohup ./bin/mindmoryd-lite >>var/mindmoryd-lite.log 2>&1 &"
  echo "    curl -s http://127.0.0.1:$HTTP_PORT/health/ready"
  exit 0
fi

# --------------------------------------------------------------------------
# 5. start the lite daemon and wait for readiness
# --------------------------------------------------------------------------
mkdir -p var/data
say "starting the Mindmory lite daemon (no Docker) on 127.0.0.1:$HTTP_PORT"
set -a
. ./mindmory-config.sh
set +a
if [ -f var/mindmoryd-lite.pid ] && kill -0 "$(cat var/mindmoryd-lite.pid)" 2>/dev/null; then
  warn "daemon already running (pid $(cat var/mindmoryd-lite.pid)); not starting a second one"
else
  if command -v setsid >/dev/null 2>&1; then
    setsid ./bin/mindmoryd-lite >>var/mindmoryd-lite.log 2>&1 &
  else
    nohup ./bin/mindmoryd-lite >>var/mindmoryd-lite.log 2>&1 &
  fi
  echo "$!" > var/mindmoryd-lite.pid
fi
DAEMON_PID="$(cat var/mindmoryd-lite.pid)"

ready=0
if [ -n "$GET" ]; then
  say "waiting for readiness at http://127.0.0.1:$HTTP_PORT/health/ready"
  i=0
  while [ "$i" -lt 30 ]; do
    if $GET "http://127.0.0.1:$HTTP_PORT/health/ready" 2>/dev/null | grep -q ready; then
      say "daemon is ready (pid $DAEMON_PID)"
      ready=1
      break
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ "$i" -ge 30 ]; then
    warn "still not ready after 30s — inspect the log: tail -n 50 var/mindmoryd-lite.log"
  fi
fi

# --------------------------------------------------------------------------
# 5b. create the initial continuity session. mindmory-mcp-stdio requires a
#     bound session id (MINDMORY_BOUND_SESSION_ID); this archives one
#     neutral setup message so the session exists, then records the
#     authoritative id in ./mindmory-config.sh for the dsh patch below. Idempotent —
#     re-running setup.sh reuses the same external id.
# --------------------------------------------------------------------------
BOUND_SESSION_ID="${MINDMORY_BOUND_SESSION_ID:-}"
if [ -z "$BOUND_SESSION_ID" ] && [ "$ready" -eq 1 ] && command -v curl >/dev/null 2>&1; then
  say "creating the initial continuity session"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  resp="$(curl -fsS -X POST "http://127.0.0.1:$HTTP_PORT/v1/checkpoints" \
    -H 'Content-Type: application/json' \
    -d "{\"external_session_id\":\"mindmory-continuity\",\"mode\":\"INCREMENTAL\",\"messages\":[{\"external_message_id\":\"setup-0001\",\"role\":\"user\",\"content_type\":\"text/plain\",\"content\":\"Mindmory MCP first-run initialization\",\"occurred_at\":\"$now\"}],\"tool_events\":[]}" 2>/dev/null || true)"
  BOUND_SESSION_ID="$(printf '%s' "$resp" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  if [ -n "$BOUND_SESSION_ID" ]; then
    printf '\n# Bound continuity session (created by setup.sh; required by mindmory-mcp-stdio).\nMINDMORY_BOUND_SESSION_ID=%s\n' "$BOUND_SESSION_ID" >> mindmory-config.sh
    export MINDMORY_BOUND_SESSION_ID="$BOUND_SESSION_ID"
    say "bound session created: $BOUND_SESSION_ID"
  else
    warn "could not create the initial session — set MINDMORY_BOUND_SESSION_ID manually (see dsh/README.md)"
  fi
fi
if [ -z "$BOUND_SESSION_ID" ]; then
  BOUND_SESSION_ID="<SESSION-ID-FROM-SETUP>"
fi

if [ "$AGENT" -eq 1 ]; then
  state="ACTION_REQUIRED"
  [ "$ready" -eq 1 ] && [ "$BOUND_SESSION_ID" != "<SESSION-ID-FROM-SETUP>" ] && state="READY"
  config_path="$PWD/mindmory-config.sh"
  command_path="$PWD/bin/mindmory-mcp-stdio"
  credentials_created=true
  [ "$EXISTING_CONFIG" -eq 1 ] && credentials_created=false
  json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
  printf '{"state":"%s","mcp_command":"%s","config_file":"%s","credentials_created":%s,"credentials_rotated":false,"secrets_exposed":false,"restart_mcp":true}\n' \
    "$state" "$(json_escape "$command_path")" "$(json_escape "$config_path")" "$credentials_created"
  exit 0
fi

# --------------------------------------------------------------------------
# 6. next steps — local agent integrations
# --------------------------------------------------------------------------
cat <<EOF

============================================================
 Mindmory MCP (lite) is up — your memories live on this machine only
============================================================
  endpoint : http://127.0.0.1:$HTTP_PORT
  owner    : $OWNER
  pid      : $DAEMON_PID  (stop with: kill $DAEMON_PID)
  data     : $PWD/var/data/  (JSONL — human-readable, diffable, back this up)
  search   : $PWD/var/index.db  (derived SQLite index, rebuildable from JSONL)

Choose the agent integration
----------------------------
Codex:       integrations/codex/README.md
Claude Code: integrations/claude-code/README.md
Other MCP:   integrations/generic/README.md
DeepSeek:    dsh/README.md

DeepSeek Harness quick configuration
------------------------------------
Append this block to BOTH:
    ~/.dsh/profiles/web/cordis.patch.yml
    ~/.dsh/profiles/headless/cordis.patch.yml
then restart the harness (or start a new session):

- insert:
    - id: mcp-mindmory
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        serverName: mindmory
        transport: stdio
        command: $PWD/bin/mindmory-mcp-stdio
        # No token is placed in the agent profile. The bridge securely reads
        # $PWD/mindmory-config.sh beside the distribution.

The tools then appear as mcp__mindmory__* (memory_context, memory_remember,
memory_search, ...). See dsh/README.md in this package for optional extras
(checkpoint-relay, mindmory-reflex) and non-dsh clients.

Make it permanent (optional, Linux)
-----------------------------------
The daemon above was started detached from this shell. To run it across
reboots, install the sample unit and enable it:

    mkdir -p ~/.config/systemd/user
    cat > ~/.config/systemd/user/mindmory-lite.service <<UNIT
    [Unit]
    Description=Mindmory lite — JSONL memory daemon
    After=network-online.target
    Wants=network-online.target

    [Service]
    Type=simple
    WorkingDirectory=$PWD
    EnvironmentFile=$PWD/mindmory-config.sh
    ExecStart=$PWD/bin/mindmoryd-lite
    Restart=on-failure
    RestartSec=3
    KillSignal=SIGINT
    TimeoutStopSec=15

    [Install]
    WantedBy=default.target
    UNIT
    systemctl --user daemon-reload
    systemctl --user enable --now mindmory-lite.service
    kill $DAEMON_PID   # hand over to systemd

Security notes
--------------
- The client token exists only in ./mindmory-config.sh. The MCP bridge reads
  it internally; never paste it into an agent conversation or profile.
- ./mindmory-config.sh holds every secret for this instance — keep it safe; the daemon
  reads it only at startup.
- The daemon listens on 127.0.0.1 only; nothing leaves your machine. Set
  MINDMORY_AUTH=token in ./mindmory-config.sh to enforce bearer verification instead of
  local-trust mode.
EOF
