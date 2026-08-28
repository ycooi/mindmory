package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mindmory.local/core/internal/debugnode"
)

func Run(version string) int {
	cfg, configPath, setupPath, err := loadMCPConfig()
	if err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		status := bootstrapStatus(configPath, setupPath, err.Error())
		instructions := formatBootstrapIncident(status)
		server := mcp.NewServer(&mcp.Implementation{Name: "mindmory", Version: version}, &mcp.ServerOptions{Instructions: instructions})
		bootstrapRuntime{status: status}.register(server)
		logger.Warn("Mindmory MCP bootstrap mode", "reason_code", "MCP_CONFIGURATION_REQUIRED", "config_file", configPath)
		if runErr := server.Run(context.Background(), &mcp.StdioTransport{}); runErr != nil && !errors.Is(runErr, context.Canceled) {
			logger.Error("MCP stdio stopped", "reason_code", "TRANSPORT_ERROR")
			return 1
		}
		return 0
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "DEBUG" {
		level = slog.LevelDebug
	} else if cfg.LogLevel == "WARN" {
		level = slog.LevelWarn
	} else if cfg.LogLevel == "ERROR" {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	client := Client{Endpoint: cfg.Endpoint, Token: cfg.Token, Observer: debugnode.SlogObserver{Logger: logger}}
	ctx := context.Background()
	instructions := "Use memory_context with mode=reflex at the beginning of each conversation. If Mindmory returns ACTION_REQUIRED, show its warning and commands to the user exactly and do not execute remediation."
	actionRequired := false
	if status, statusErr := client.SystemStatus(ctx); statusErr == nil && (status.State == "ACTION_REQUIRED" || status.State == "BUILDING") && len(status.Incidents) > 0 {
		actionRequired = true
		instructions = formatIncident(status.Incidents[0])
		logger.Warn("Mindmory action required", "code", status.Incidents[0].Code, "incident_id", status.Incidents[0].IncidentID)
	}
	if cfg.BoundMessageID != "" && !actionRequired {
		turn, err := client.Do(ctx, "GET", queryPath("/v1/context/sessions/"+cfg.BoundSessionID, map[string]string{"message_id": cfg.BoundMessageID}), nil)
		if err != nil || turn["is_current_user"] != true {
			// The static profile binding may be stale: every user turn archives a
			// new message, so a fixed message id cannot stay "current" across
			// turns. Do not hard-fail — the Runtime re-resolves the latest
			// current-user message on each mutation call, and read tools never
			// depend on the bound message.
			logger.Warn("MCP bound authority stale; continuing with per-call resolution", "reason_code", "BOUND_CURRENT_USER_REQUIRED")
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mindmory", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	Runtime{Client: client, SessionID: cfg.BoundSessionID, MessageID: cfg.BoundMessageID}.Register(server)
	logger.Info("Mindmory MCP stdio ready", "tool_count", len(knownToolNames))
	if err = server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("MCP stdio stopped", "reason_code", "TRANSPORT_ERROR")
		return 1
	}
	return 0
}

func formatBootstrapIncident(status map[string]any) string {
	incidents, _ := status["incidents"].([]any)
	if len(incidents) == 0 {
		return "Mindmory requires local setup. Call mindmory_status for instructions."
	}
	incident, _ := incidents[0].(map[string]any)
	commands, _ := incident["copy_paste_commands"].([]string)
	command := ""
	if len(commands) > 0 {
		command = commands[0]
	}
	return "MINDMORY_SETUP_REQUIRED. Only mindmory_status is available. Show the user this command and run it only with approval: " + command + ". Never request or display a token."
}
