# Agent integration packages

Mindmory is host-neutral. Choose the directory matching the local agent that
will use it:

| Host | Package | Automatic user-turn checkpoint |
| --- | --- | --- |
| Codex CLI, desktop, or IDE | `codex/` | Yes, with the bundled `UserPromptSubmit` hook |
| Claude Code | `claude-code/` | Yes, with the bundled `UserPromptSubmit` hook |
| Other local MCP clients | `generic/` | Requires a host lifecycle adapter |
| DeepSeek Harness | `../dsh/` | Use its existing harness integration |

Run `../setup.sh --agent --complete-mcp` before configuring a host. Never copy
the contents of `mindmory-config.sh` into an MCP profile or conversation. The
stdio bridge and checkpoint adapter read the protected adjacent file locally.

The MCP registration provides search, recall, context, status, and mutation
tools. The lifecycle hook is also important: it archives the exact current
user prompt, allowing `memory_remember`, `memory_correct`, and `memory_forget`
to prove their evidence came from the current turn.

Installing agents must obtain user approval before changing host configuration
or enabling lifecycle hooks.
