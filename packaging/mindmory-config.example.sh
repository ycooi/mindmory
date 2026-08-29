# Mindmory MCP (lite) — environment template.
# Copy to mindmory-config.sh and replace every value with independently generated secrets
# of at least 24 characters (32+ for the cursor key). Never reuse a token.
# `./setup.sh` generates all of this for you — this template is for
# configuring by hand.

# --- Identity -------------------------------------------------------------
# Any name; identifies the single owner of this instance.
MINDMORY_OWNER=your-owner-name
# At least 32 random bytes. Signs session cursors and canonical mutation history.
MINDMORY_CURSOR_SIGNING_KEY=replace-with-at-least-32-random-bytes

# --- Host port and data ---------------------------------------------------
MINDMORY_HTTP_PORT=58080
# Base directory for relative paths, followed by independently configurable
# canonical, derived, vector, snapshot, and exchange locations.
MINDMORY_ROOT_DIR=.
MINDMORY_DATA_DIR=var/data
MINDMORY_DERIVED_DIR=var/derived
MINDMORY_VECTOR_DIR=var/derived/vectors
MINDMORY_SNAPSHOT_DIR=var/data/snapshots
MINDMORY_EXPORT_DIR=var/export
# Bind address. Keep 127.0.0.1 (loopback) for a single-user machine.
MINDMORY_ADDRESS=127.0.0.1:58080
# local = trust loopback (single-user default). "token" enforces Bearer
# verification (recommended if the surface is ever exposed beyond loopback).
MINDMORY_AUTH=local
# Experimental: release archive-sized Go maps after SQLite projection startup.
# Reduces steady-state RAM; startup still temporarily materializes JSONL.
MINDMORY_LOW_RAM_EXPERIMENT=0

# --- Daemon credentials -----------------------------------------------------
# Operator token (always enforced on administrative routes). >= 24 chars.
MINDMORY_ADMIN_ENDPOINT=http://127.0.0.1:58080
MINDMORY_ADMIN_TOKEN=replace-with-a-random-admin-token
# Client principals — the token your assistant presents. Token >= 24 chars.
# Keep the value single-quoted: the outer quotes are stripped by both shell
# sourcing (`set -a; . ./mindmory-config.sh`) and service-manager loading.
MINDMORY_MCP_CLIENT_TOKENS_JSON='{"local-agent":{"token":"replace-with-a-random-client-token","capabilities":["CONTEXT_READ","ARCHIVE_CHECKPOINT","MEMORY_PROPOSE","ARTIFACT_SEARCH","ARTIFACT_READ","RESOURCE_READ","OPS_READ"]}}'
# Required in local mode; must name one configured client principal above.
MINDMORY_LOCAL_CLIENT_KEY=local-agent

# --- Semantic search (optional) ---------------------------------------------
# 0 = built-in lexical (SQLite FTS) search only. 1 = also blend local vector
# matches; requires Ollama running at http://127.0.0.1:11434 and a one-time
# backfill: MINDMORY_EMBED_MODEL=... mindmoryd-lite --embed
MINDMORY_SEMANTIC_SEARCH=0
MINDMORY_EMBED_MODEL=qwen3-embedding:0.6b

# --- MCP stdio server (runs on the host, launched by your assistant) --------
# The MCP token must equal the client token inside MINDMORY_MCP_CLIENT_TOKENS_JSON.
MINDMORY_ENDPOINT=http://127.0.0.1:58080
MINDMORY_MCP_TOKEN=replace-with-the-same-client-token-as-in-mcp-client-tokens-json
# REQUIRED — the continuity session the stdio server scopes calls to.
# `./setup.sh` creates the initial session and fills this in automatically.
MINDMORY_BOUND_SESSION_ID=replace-with-the-session-id-from-setup
# Optional: bind the stdio server to one archived turn. When omitted, the
# server re-resolves the latest current-user turn per mutation call.
# MINDMORY_BOUND_MESSAGE_ID=
MINDMORY_MCP_LOG_LEVEL=INFO
