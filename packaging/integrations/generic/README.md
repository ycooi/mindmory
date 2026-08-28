# Generic local MCP integration

Use this package for an agent host that supports local stdio MCP servers but
does not have a dedicated Mindmory adapter.

1. Run `./setup.sh --agent --complete-mcp` from the distribution root.
2. With user approval, register the returned absolute `mcp_command` as a stdio
   server named `mindmory`. Do not add credential environment variables.
3. Restart the MCP connection and call `mindmory_status`.
4. Instruct the agent to call `memory_context` at conversation start and to
   show any `ACTION_REQUIRED` incident to the user verbatim.

This enables status, context, search, and recall. Full evidence-backed mutation
support requires the host to invoke this distribution's
`integrations/checkpoint-hook.sh generic` for every `UserPromptSubmit`-style
event, passing JSON on stdin with these fields:

```json
{
  "session_id": "host-session-id",
  "turn_id": "host-turn-id",
  "prompt": "the exact current user message",
  "cwd": "/optional/project/path"
}
```

The adapter is idempotent for the same host, session, turn, and prompt. It
writes nothing on success and never returns credentials. Do not simulate this
lifecycle by asking the model to paraphrase the user message; evidence must be
the exact host-provided prompt.
