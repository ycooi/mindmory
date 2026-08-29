# Wiring Mindmory MCP into DeepSeek Harness

Mindmory exposes a stdio MCP server (`bin/mindmory-mcp-stdio`) and a local
Harness lifecycle plugin (`dsh/checkpoint-relay.mjs`). DeepSeek Harness loads
both through per-profile patch files: the MCP bridge provides the memory tools,
while the relay archives exact user prompts and assembled assistant responses.

## 1. Run the initialization sequence

```bash
./setup.sh
```

This generates fresh secrets, writes `mindmory-config.sh`, starts the lite daemon (no
Docker), and waits until `http://127.0.0.1:58080/health/ready` returns
`ready`. At the end it prints the exact patch block for your machine. The
token remains only in the protected local configuration.

## 2. Paste the patch into your profiles

Harness has two profile layers you can patch: `web` (this GUI) and `headless`
(CLI sessions). Paste the block printed by `setup.sh` — or the template
`cordis.patch.example.yml` after replacing `<ABSOLUTE-PATH-TO-BIN>` — into
**both**:

```text
~/.dsh/profiles/web/cordis.patch.yml
~/.dsh/profiles/headless/cordis.patch.yml
```

The printed block contains two entries. Keep both:

- `mcp-mindmory` exposes the `mcp__mindmory__*` tools.
- `mindmory-checkpoint-relay` observes Harness `user/message` and
  `assistant/message` events and checkpoints both roles in event order.

The relay ignores synthetic user injections and empty/tool-call-only assistant
messages. It uses stable Harness message IDs for retry idempotency and forwards
the assembled assistant message, not partial streaming chunks.

Then restart the harness or start a new session. The agent now has these tools:

| Tool | What it does |
| --- | --- |
| `memory_context` | Bounded packet of current project context + active memories. Use at session start. |
| `memory_search` | Search memories (keyword + semantic), with exact evidence on hits. |
| `memory_recall` | Recall one memory with its lifecycle and exact original evidence. |
| `memory_remember` | Propose remembering an explicit statement from the current turn. |
| `memory_correct` | Propose correcting a memory, grounded in an explicit correction in the current turn. |
| `memory_forget` | Propose forgetting a memory, requested explicitly in the current turn. |
| `memory_diff` | What changed since your last session's cursor. |
| `memory_feedback` | Tell the system a memory helped or misled you. |
| `artifact_search` / `artifact_read` | Search/read attached artifact metadata. |
| `proposal_review` | Inspect pending mutation proposals (staged, awaiting review). |
| `ops_recent` | Recent operational journal events. |

### Upgrading a legacy Harness profile

If a profile already contains `@deepseek-ai/dsh-checkpoint-relay`, remove that
entire legacy entry before adding `mindmory-checkpoint-relay`. The old private
relay archives user messages only. Running both relays together can submit the
same user turn under two different idempotency keys.

## 3. Credential handling

The MCP token exists only in `mindmory-config.sh` and is read internally by the
stdio bridge and checkpoint adapter. Neither Harness entry contains an endpoint
or credential. Do not paste the token into a profile or conversation. If
configuration is missing, the bridge exposes only `mindmory_status`, which
reports a safe setup command without exposing the token.

## 4. Non-dsh clients

Any MCP-capable host works. Register `bin/mindmory-mcp-stdio` as a stdio
server using its absolute path:

```text
command=/absolute/path/to/bin/mindmory-mcp-stdio
```

For Claude Desktop, add an `mcpServers` entry pointing `command` at the
absolute path of `bin/mindmory-mcp-stdio`. The lite daemon (`bin/mindmoryd-lite`)
must be running either way — `setup.sh` starts it for you.
