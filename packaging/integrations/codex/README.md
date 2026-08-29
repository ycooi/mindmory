# Codex integration

This package supports local Codex clients: ChatGPT desktop, Codex CLI, and the
Codex IDE extension. It registers Mindmory as a local stdio MCP server,
checkpoints each submitted user prompt, and archives Codex's completed response
when the turn stops.

## Agent installation

1. From the extracted Mindmory distribution root, run:

   ```bash
   ./setup.sh --agent --complete-mcp
   ```

2. Obtain user approval to change Codex configuration and enable the hook.

3. Register the absolute `mcp_command` returned by setup:

   ```bash
   codex mcp add mindmory -- /ABSOLUTE/PATH/bin/mindmory-mcp-stdio
   ```

   Alternatively, copy `config.toml.example` into the appropriate global or
   trusted-project Codex `config.toml`, replacing `/ABSOLUTE/PATH`.

4. Merge `hooks.json.example` into the matching Codex `hooks.json`, replacing
   `/ABSOLUTE/PATH`. Do not overwrite unrelated hooks.

   Upgrades from Mindmory `v0.1.1` must add the new `Stop` block as well as
   retaining `UserPromptSubmit`; replacing binaries alone cannot modify the
   user's Codex hook configuration.

5. Restart the Codex client. Use `/mcp` or `codex mcp list`, then call
   `mindmory_status`. Installation succeeds only when its state is `READY`.

## Expected behavior

- Codex receives Mindmory's MCP instructions and should call
  `memory_context` near the start of a conversation.
- The `UserPromptSubmit` hook sends the exact prompt directly to the local
  daemon. It emits no successful output into model context.
- The `Stop` hook sends `last_assistant_message` as an assistant-role archive
  record with Codex identity. It does not block or continue the agent.
- If checkpointing fails, the hook reports a generic error without printing
  the prompt or credential. `mindmory_status` remains the authoritative
  diagnostic surface.

The hook affects local Codex clients only. Hosted ChatGPT web does not load a
machine's local Codex configuration.
