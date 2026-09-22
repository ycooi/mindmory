# Generic local MCP integration

Use this package for an agent host that supports local stdio MCP servers but
does not have a dedicated Mindmory adapter.

1. Run `./setup.sh --agent --complete-mcp` from the distribution root.
2. With user approval, register the returned absolute `mcp_command` as a stdio
   server named `mindmory`. Do not add credential environment variables.
3. Restart the MCP connection and call the `mindmory` gateway with
   `action=mindmory_status`.
4. Instruct the agent to use memory actions only when prior state may matter,
   keep `limit` / `max_chars` small, and show any `ACTION_REQUIRED` incident
   to the user verbatim.

This enables status, context, search, and recall. Evidence-backed
remember/correct/forget operations additionally require the exact current user
message to be present in the same bound continuity session used by the MCP
bridge. Full conversation capture and that mutation authority require the host
to invoke this distribution's
`integrations/checkpoint-hook.sh generic` for both user-submit and
response-stop events. A user event passes:

```json
{
  "session_id": "host-session-id",
  "turn_id": "host-turn-id",
  "prompt": "the exact current user message",
  "cwd": "/optional/project/path"
}
```

The completed assistant event passes:

```json
{
  "session_id": "host-session-id",
  "turn_id": "host-turn-id",
  "hook_event_name": "Stop",
  "last_assistant_message": "the exact completed assistant response",
  "cwd": "/optional/project/path"
}
```

The compatibility adapter intentionally archives into the shared, unscoped
`mindmory-continuity` session created by setup; `cwd` is accepted as host input
but is not used as a project scope. This exact session binding is what lets the
server validate mutation evidence. The adapter is idempotent for the same host,
session, turn, role, and content. It writes nothing on success and never
returns credentials. Do not simulate this lifecycle by asking the model to
paraphrase either side; archive exact host-provided messages.
