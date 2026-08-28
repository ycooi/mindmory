package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadProtectedConfigLoadsOnlyBridgeValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := strings.Join([]string{
		"MINDMORY_OWNER='private owner'",
		"MINDMORY_ENDPOINT=http://127.0.0.1:58080",
		"MINDMORY_MCP_TOKEN='client-token-at-least-24-characters'",
		"MINDMORY_BOUND_SESSION_ID=session-1",
		"MINDMORY_ADMIN_TOKEN=must-not-be-loaded",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := readProtectedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["MINDMORY_MCP_TOKEN"] != "client-token-at-least-24-characters" || values["MINDMORY_BOUND_SESSION_ID"] != "session-1" {
		t.Fatalf("values=%v", values)
	}
	if _, ok := values["MINDMORY_ADMIN_TOKEN"]; ok {
		t.Fatal("admin credential crossed into MCP configuration")
	}
}

func TestReadProtectedConfigRejectsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	if err := os.WriteFile(path, []byte("MINDMORY_ENDPOINT=http://127.0.0.1:58080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedConfig(path); err == nil || !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("err=%v", err)
	}
}

func TestBootstrapStatusContainsNoSecret(t *testing.T) {
	status := bootstrapStatus("/approved/mindmory-config.sh", "/approved/setup.sh", "missing token")
	raw := status["configuration"].(map[string]any)
	if raw["secrets_exposed"] != false || raw["credentials_loaded"] != false {
		t.Fatalf("configuration=%v", raw)
	}
	if strings.Contains(strings.ToLower(formatBootstrapIncident(status)), "paste a token") {
		t.Fatal("bootstrap instructions ask for a token")
	}
}
