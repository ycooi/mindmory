# Agent integration packages

Mindmory is host-neutral. Choose the directory matching the local agent that
will use it:

| Host | Package | Primary access path |
| --- | --- | --- |
| Codex CLI, desktop, or IDE | `codex/` | Native relevance injection plus two-sided checkpoint; no MCP required |
| Claude Code | `claude-code/` | User prompt plus completed assistant response |
| Other local MCP clients | `generic/` | Requires a host lifecycle adapter |
| DeepSeek Harness | `../dsh/` | Native strict relevance plus two-sided checkpoint; no MCP required |

Run `../setup.sh --agent --complete-native` for Codex or `--complete-mcp` for
an MCP host. Never copy
the contents of `mindmory-config.sh` into an MCP profile or conversation. The
stdio bridge and checkpoint adapter read the protected adjacent file locally.

Codex's hook and the DeepSeek Harness native relay archive the current prompt,
retrieve a strict bounded packet, and inject only memory text. Their completed-
response path keeps the archive two-sided. MCP registration remains available
when explicit search or recall tools are required by another host.

Native and MCP compatibility modes have different lifecycle authority. Native
hooks checkpoint real per-host sessions and rely on native retrieval plus the
passive learner. The static MCP bridge is bound to the shared continuity
session created by setup; evidence-backed mutations apply only when the host
uses the matching generic or Claude Code checkpoint adapter. Enabling the MCP
bridge beside a native hook does not transfer current-turn mutation authority
between those sessions.

Installing agents must obtain user approval before changing host configuration
or enabling lifecycle hooks.
