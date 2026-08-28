package mcpserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mindmory.local/core/internal/config"
)

const configFileName = "mindmory-config.sh"

type bootstrapRuntime struct {
	status map[string]any
}

func (r bootstrapRuntime) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mindmory_status",
		Description: "Report why Mindmory is in restricted bootstrap mode and show safe setup instructions. This tool never returns credentials.",
	}, r.statusTool)
}

func (r bootstrapRuntime) statusTool(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
	return success(r.status)
}

func bootstrapStatus(configPath, setupPath, reason string) map[string]any {
	command := shellQuote(setupPath) + " --agent --complete-mcp"
	incident := map[string]any{
		"code":                  "MCP_CONFIGURATION_REQUIRED",
		"incident_id":           "bootstrap-mcp-configuration",
		"severity":              "ACTION_REQUIRED",
		"affected_capabilities": []string{"MEMORY_CONTEXT", "MEMORY_SEARCH", "MEMORY_MUTATION"},
		"data_safety":           "No memory data was modified. No credential was sent to the agent or conversation.",
		"operator_message":      "Mindmory MCP is installed but its local pairing configuration is missing or incomplete.",
		"copy_paste_commands":   []string{command},
		"agent_instruction":     "Show this warning and command to the user. Run it only with the user's approval, then restart the MCP connection. Never ask the user to paste a token into chat.",
	}
	return map[string]any{
		"state":      "ACTION_REQUIRED",
		"mcp":        "BOOTSTRAP_RESTRICTED",
		"incidents":  []any{incident},
		"statistics": map[string]any{},
		"configuration": map[string]any{
			"config_file":        configPath,
			"setup_program":      setupPath,
			"credentials_loaded": false,
			"secrets_exposed":    false,
		},
		"diagnostic": reason,
	}
}

// loadMCPConfig overlays process environment values over a protected local
// config file. Environment always wins, preserving explicit host settings.
func loadMCPConfig() (config.MCPServerConfig, string, string, error) {
	configPath, setupPath := distributionPaths()
	values := map[string]string{}
	if explicit := strings.TrimSpace(os.Getenv("MINDMORY_CONFIG_FILE")); explicit != "" {
		configPath = explicit
	}
	if parsed, err := readProtectedConfig(configPath); err == nil {
		values = parsed
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.MCPServerConfig{}, configPath, setupPath, err
	}
	lookup := func(key string) (string, bool) {
		if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
		value, ok := values[key]
		return value, ok
	}
	cfg, err := config.LoadMCPServer(lookup)
	return cfg, configPath, setupPath, err
}

func distributionPaths() (configPath, setupPath string) {
	executable, err := os.Executable()
	if err != nil {
		return configFileName, "./setup.sh"
	}
	root := filepath.Dir(filepath.Dir(executable))
	return filepath.Join(root, configFileName), filepath.Join(root, "setup.sh")
}

func readProtectedConfig(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("Mindmory config must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("Mindmory config permissions are %04o; require 0600", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	allowed := map[string]bool{
		"MINDMORY_ENDPOINT": true, "MINDMORY_MCP_TOKEN": true,
		"MINDMORY_BOUND_SESSION_ID": true, "MINDMORY_BOUND_MESSAGE_ID": true,
		"MINDMORY_MCP_LOG_LEVEL": true,
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !allowed[key] {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
