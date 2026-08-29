# Agent integration packages

Mindmory is host-neutral. Choose the directory matching the local agent that
will use it:

| Host | Package | Automatic conversation checkpoint |
| --- | --- | --- |
| Codex CLI, desktop, or IDE | `codex/` | User prompt plus completed assistant response |
| Claude Code | `claude-code/` | User prompt plus completed assistant response |
| Other local MCP clients | `generic/` | Requires a host lifecycle adapter |
| DeepSeek Harness | `../dsh/` | User prompt plus assembled assistant response |

Run `../setup.sh --agent --complete-mcp` before configuring a host. Never copy
the contents of `mindmory-config.sh` into an MCP profile or conversation. The
stdio bridge and checkpoint adapter read the protected adjacent file locally.

The MCP registration provides search, recall, context, status, and mutation
tools. The lifecycle hooks archive both sides of each completed turn. The exact
current user prompt lets `memory_remember`, `memory_correct`, and
`memory_forget` prove user authority; the completed assistant response keeps
the conversation archive and continuity record complete.

Installing agents must obtain user approval before changing host configuration
or enabling lifecycle hooks.
