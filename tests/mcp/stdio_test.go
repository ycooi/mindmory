package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRealStdioListsToolsAndBindsMutationAuthority(t *testing.T) {
	var mu sync.Mutex
	var mutation map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && len(r.URL.Path) > 20:
			_, _ = w.Write([]byte(`{"session":{"session_id":"00000000-0000-4000-8000-000000000601","project_key":"Mindmory"},"message_id":"00000000-0000-4000-8000-000000000631","is_current_user":true}`))
		case r.URL.Path == "/v1/context/packet":
			_, _ = w.Write([]byte(`{"session":{"session_id":"00000000-0000-4000-8000-000000000601","project_key":"Mindmory"},"continuity_cursor":"opaque","memories":[],"truncated":false,"returned_chars":0}`))
		case r.URL.Path == "/v1/memory/mutations":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&mutation)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"proposal_id":"p1","outcome":"STAGED","reason_code":"INTENT_UNCERTAIN"}`))
		default:
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer backend.Close()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	distribution := t.TempDir()
	if err := os.Mkdir(filepath.Join(distribution, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(distribution, "bin", "mindmory-mcp-stdio")
	build := exec.Command("go", "build", "-o", binary, "./cmd/mindmory-mcp-stdio")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	configFile := filepath.Join(distribution, "mindmory-config.sh")
	configText := "MINDMORY_ENDPOINT=" + backend.URL + "\n" +
		"MINDMORY_MCP_TOKEN=test-model-facing-token-0123456789\n" +
		"MINDMORY_BOUND_SESSION_ID=00000000-0000-4000-8000-000000000601\n" +
		"MINDMORY_BOUND_MESSAGE_ID=00000000-0000-4000-8000-000000000631\n"
	if err := os.WriteFile(configFile, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	command.Env = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "stage5-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2025-11-25" {
		t.Fatalf("negotiated protocol=%s", got)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	// Mirror the authoritative mcpserver inventory to keep the wire contract
	// honest, including the sanitized read-only startup/configuration status.
	want := []string{"artifact_read", "artifact_search", "memory_context", "memory_correct", "memory_diff", "memory_feedback", "memory_forget", "memory_recall", "memory_remember", "memory_search", "mindmory_status", "ops_recent", "proposal_review"}
	if len(names) != len(want) {
		t.Fatalf("tools=%v (want %d, got %d)", names, len(want), len(names))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools=%v (want %v)", names, want)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_context", Arguments: map[string]any{"query": "MCP"}})
	if err != nil || result.IsError {
		t.Fatalf("context result=%+v err=%v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_forget", Arguments: map[string]any{"target_memory_id": "m1", "evidence_quote": "forget it"}})
	if err != nil || result.IsError {
		t.Fatalf("forget result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if mutation["session_id"] != "00000000-0000-4000-8000-000000000601" || mutation["message_id"] != "00000000-0000-4000-8000-000000000631" {
		t.Fatalf("authority not server injected: %+v", mutation)
	}
}

func TestUnconfiguredStdioStartsRestrictedBootstrapServer(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binary := os.Getenv("MINDMORY_RELEASE_MCP_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "mindmory-mcp-stdio")
		build := exec.Command("go", "build", "-o", binary, "./cmd/mindmory-mcp-stdio")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, output)
		}
	}
	missingConfig := filepath.Join(t.TempDir(), "missing-config.sh")
	command := exec.Command(binary)
	command.Env = []string{"MINDMORY_CONFIG_FILE=" + missingConfig}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "bootstrap-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "mindmory_status" {
		t.Fatalf("bootstrap tools=%v", listed.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory_status"})
	if err != nil || result.IsError {
		t.Fatalf("status=%+v err=%v", result, err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	text := string(raw)
	if !strings.Contains(text, "MCP_CONFIGURATION_REQUIRED") || !strings.Contains(text, "--agent --complete-mcp") {
		t.Fatalf("bootstrap status=%s", text)
	}
	if strings.Contains(strings.ToLower(text), "mcp_token") {
		t.Fatalf("bootstrap leaked credential field: %s", text)
	}
}
