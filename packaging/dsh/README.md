# Native Mindmory for DeepSeek Harness

DeepSeek Harness can use Mindmory without registering an MCP server. The
bundled Cordis plugin attaches to the Harness lifecycle and keeps model context
small:

- `agent/pre-step` archives the exact direct prompt, asks Mindmory for strict
  relevance, and appends only the bounded plain-text result as a sourced recall
  message. Harness therefore records exactly what the model receives.
- `assistant/message` archives the assembled reply. Partial streaming chunks,
  tool-only messages, and synthetic user injections are ignored.
- An unrelated prompt adds zero model-visible bytes. A relevant prompt receives
  at most three memories, 320 characters, and approximately 80 tokens.
- No Mindmory tool schema, IDs, scores, transport JSON, endpoint, or credential
  enters the prompt or Harness profile.

## Install

Run `./setup.sh`, then add the block from `dsh/cordis.patch.example.yml` to
both Harness profile layers:

```text
~/.dsh/profiles/web/cordis.patch.yml
~/.dsh/profiles/headless/cordis.patch.yml
```

Replace `<ABSOLUTE-PATH-TO-DIST>` with the extracted Mindmory directory and
restart Harness. Remove or disable older `mcp-mindmory`, `mindmory-reflex`,
`mindmory-relevance`, and `mindmory-checkpoint-relay` rows so a prompt is not
retrieved or archived twice.

The plugin invokes `integrations/checkpoint-hook.sh deepseek-harness`. That
adapter reads the protected `mindmory-config.sh` beside the distribution; the
Harness profile contains no token. Child processes receive a scrubbed
environment without credential-like variable names.

## Failure behavior

Mindmory retrieval is advisory. If the local daemon or adapter is unavailable,
the Harness turn proceeds unchanged and a local warning is logged. Downstream
Harness policy remains authoritative: a rejected pre-step receives no injected
context. Assistant checkpoint failures likewise do not add text to the model
conversation.

## Optional MCP compatibility

Register `bin/mindmory-mcp-stdio` only when the agent genuinely needs status or
broad exploratory search and recall operations. The compact MCP profile exposes
one gateway, but it reintroduces a fixed schema cost in every Harness session.
It is bound to a shared compatibility session, not the native Harness session;
therefore enabling it beside the native relay does not give
remember/correct/forget operations current-turn authority. Use the generic
bound-session checkpoint lifecycle instead if evidence-backed MCP mutations are
required. Native retrieval, archival, and passive learning do not require MCP.
