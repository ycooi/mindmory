# Claude Code integration

This package registers Mindmory as a local stdio MCP server and checkpoints
each submitted user prompt before Claude Code processes it.

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

5. Restart Claude Code. Use `/mcp` or `claude mcp list`, then call
   `mindmory_status`. Installation succeeds only when its state is `READY`.

## Expected behavior

The `UserPromptSubmit` hook receives event JSON on stdin and sends only the
user's prompt and local session metadata to the loopback Mindmory daemon. It
does not print prompts or credentials. Read tools work with MCP registration
alone; evidence-backed mutations require this checkpoint lifecycle.

This package targets Claude Code. Claude's hosted API MCP connector cannot
launch this local stdio process without a separately operated remote gateway.
