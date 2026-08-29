# Claude Code integration

This package registers Mindmory as a local stdio MCP server, checkpoints each
submitted user prompt, and archives Claude Code's completed assistant response
at the end of the turn.

## Agent installation

1. From the extracted Mindmory distribution root, run:

   ```bash
   ./setup.sh --agent --complete-mcp
   ```

2. Obtain user approval to change Claude Code configuration and enable the
   hook.

3. Register the absolute `mcp_command` returned by setup for the current user:

   ```bash
   claude mcp add --transport stdio --scope user mindmory -- /ABSOLUTE/PATH/bin/mindmory-mcp-stdio
   ```

   For a project-scoped installation, copy `mcp.json.example` to `.mcp.json`,
   replace `/ABSOLUTE/PATH`, and preserve any existing MCP entries.

4. Merge `settings.json.example` into `~/.claude/settings.json` or the
   project's `.claude/settings.json`, replacing `/ABSOLUTE/PATH`. Do not
   overwrite unrelated settings or hooks.

   Upgrades from Mindmory `v0.1.1` must add the new `Stop` block as well as
   retaining `UserPromptSubmit`; replacing binaries alone cannot modify the
   user's Claude Code settings.

5. Restart Claude Code. Use `/mcp` or `claude mcp list`, then call
   `mindmory_status`. Installation succeeds only when its state is `READY`.

## Expected behavior

The `UserPromptSubmit` hook sends the user's prompt and the `Stop` hook sends
`last_assistant_message` with role `assistant`. Both operate through the
loopback daemon and print neither conversation content nor credentials. Read
tools work with MCP registration alone; evidence-backed mutations require the
user-prompt checkpoint lifecycle.

This package targets Claude Code. Claude's hosted API MCP connector cannot
launch this local stdio process without a separately operated remote gateway.
