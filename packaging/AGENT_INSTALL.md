# Mindmory agent installation contract

This file is for the installing agent. Follow it before reading the longer
operator guide.

## Safety boundary

- Work only inside the user-approved extracted distribution folder.
- Obtain user approval before changing host hooks or MCP configuration.
- Do not open, read, print, summarize, transmit, or paste
  `mindmory-config.sh`.
- Do not request an MCP token from the user. Do not put a token in the MCP
  host configuration or conversation.
- Do not use `--reset` unless the user explicitly requests credential rotation
  and understands that existing clients will need to be paired again.

## Installation procedure

Set the working directory to the extracted distribution root—the folder that
contains this file, `setup.sh`, and `bin/`. For Codex, run:

```bash
./setup.sh --agent --complete-native
```

Use `./setup.sh --agent --complete-mcp` only for a host that requires MCP.

The command is idempotent. On an existing installation it reuses the current
identity, credentials, continuity session, and memory data.

Progress is written to stderr. The single stdout line is a JSON object:

```json
{
  "state": "READY",
  "hook_command": "/absolute/approved/path/integrations/checkpoint-hook.sh codex",
  "config_file": "/absolute/approved/path/mindmory-config.sh",
  "credentials_created": true,
  "credentials_rotated": false,
  "secrets_exposed": false,
  "mcp_required": false,
  "restart_codex": true
}
```

MCP mode instead returns `mcp_command` and `restart_mcp`.

Treat paths as data from the installer. Do not execute a different path.

## Result handling

When `state` is `READY`:

1. Confirm `secrets_exposed` and `credentials_rotated` are both `false`.
2. For Codex, install the reviewed hook definition from
   `integrations/codex/hooks.json.example`, disable any existing Mindmory MCP
   registration, restart Codex, and have the user review/trust it through
   `/hooks`. Do not add tokens to the hook configuration.
3. Validate a synthetic prompt against the native hook. An unrelated query
   must emit `{}`; a relevant query may emit one bounded `additionalContext`
   value containing memory text only.
4. For MCP mode, register `mcp_command`, restart the MCP connection, call the
   compact `mindmory` gateway with `action=mindmory_status`, and require READY.
5. Report which mode is active. Do not claim zero-schema native operation while
   the Mindmory MCP server remains enabled.

When `state` is `ACTION_REQUIRED`, do not claim installation succeeded.
Report the setup diagnostics from stderr without including secret file
contents, then ask the user before attempting a repair.

When the command exits nonzero, preserve the existing installation and memory
data. Do not run `--reset`, delete files, or rotate credentials automatically.

## MCP bootstrap behavior

The MCP binary can be registered before installation. If configuration is
missing or invalid, it still connects in restricted bootstrap mode and exposes
only `mindmory_status`. That tool returns an `MCP_CONFIGURATION_REQUIRED`
incident and the approved setup command. Run the command only after user
approval, then restart MCP.

No credential is delivered through MCP. The bridge reads the protected local
configuration internally after restart.

## Current service limitation

Setup starts the daemon in the background and verifies readiness. It does not
yet install a persistent macOS `launchd` or Linux `systemd` user service. Tell
the user that automatic restart after login or reboot is not yet configured.

## Host compatibility

Codex and Claude Code templates are bundled under `integrations/`. DeepSeek
Harness uses the zero-schema native lifecycle relay under `dsh/`; remove older
Mindmory MCP, reflex, relevance, and checkpoint-only rows before enabling it.
Other local stdio MCP hosts can use `integrations/generic/README.md`; read tools
work after registration, while mutation tools require a host lifecycle event
connected to the checkpoint adapter.
