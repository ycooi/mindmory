# Codex integration

This package supports local Codex clients: ChatGPT desktop, Codex CLI, and the
Codex IDE extension. The recommended integration uses lifecycle hooks only:
it retrieves bounded context before inference and archives completed turns
without registering model-visible tools. MCP remains an optional compatibility
path, not an extension of the native session lifecycle.

## Agent installation

1. From the extracted Mindmory distribution root, run:

   ```bash
   ./setup.sh --agent --complete-native
   ```

2. Obtain user approval to change Codex configuration and enable the hook.

3. Merge `hooks.json.example` into the matching Codex `hooks.json`, replacing
   `/ABSOLUTE/PATH`. Do not overwrite unrelated hooks.

4. If Mindmory was previously registered as an MCP server, disable it to
   remove its per-session schema cost:

   ```toml
   [mcp_servers.mindmory]
   command = "/ABSOLUTE/PATH/bin/mindmory-mcp-stdio"
   enabled = false
   ```

5. Restart Codex and use `/hooks` to review and trust the exact hook
   definition. Installation succeeds when a synthetic `UserPromptSubmit`
   event produces either an empty JSON object (no relevant memory) or one
   bounded `additionalContext` value.

## Expected behavior

- Codex sees no Mindmory tool schema. Unused sessions therefore pay zero
  Mindmory tokens.
- `UserPromptSubmit` archives the exact prompt under the Codex session and
  project, requests strict relevance without warming the result, and injects
  at most three memories within a 320-character packet, with an independent
  80-estimated-token ceiling for CJK and mixed-language safety.
- Weak fuzzy matches are rejected. An unrelated prompt emits `{}` and adds no
  model context.
- `Stop` archives `last_assistant_message` asynchronously with Codex identity.
  It does not block or continue the agent.
- If checkpointing fails, the hook reports a generic error without printing
  the prompt or credential. Use `mindmoryctl --check-config` and the daemon
  readiness endpoint for diagnostics.

## Optional MCP compatibility

The compact MCP bridge can be enabled for status, explicit search, and recall,
but it is bound to a separate shared continuity session. Do not enable it beside
the native hook and assume remember/correct/forget operations have authority
over the current native Codex turn: those mutations safely stage unless the
exact user message was checkpointed into the bridge's bound session.

Use native mode by itself for normal Codex operation. If a workflow requires
evidence-backed MCP mutations, use the MCP compatibility lifecycle documented
under `../generic/` so both checkpointing and mutation use the same bound
session. The compact one-tool profile is not required for automatic native
retrieval, archival, or passive learning.

The hook affects local Codex clients only. Hosted ChatGPT web does not load a
machine's local Codex configuration.
