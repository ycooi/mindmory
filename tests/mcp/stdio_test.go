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
	"mindmory.local/core/internal/retrieval"
)

func TestRealStdioListsToolsAndBindsMutationAuthority(t *testing.T) {
	var mu sync.Mutex
	var mutation map[string]any
	var contextRequest map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && len(r.URL.Path) > 20:
			_, _ = w.Write([]byte(`{"session":{"session_id":"00000000-0000-4000-8000-000000000601","project_key":"Mindmory"},"message_id":"00000000-0000-4000-8000-000000000631","is_current_user":true}`))
		case r.URL.Path == "/v1/context/packet":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&contextRequest)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"session":{"session_id":"00000000-0000-4000-8000-000000000601","project_key":"Mindmory"},"continuity_cursor":"opaque","memories":[],"truncated":false,"returned_chars":0}`))
		case r.URL.Path == "/v1/context/search":
			_, _ = w.Write([]byte(`{"results":[{"memory_id":"memory-0001","subject":"Deployment policy","content":"Use a reviewed release checklist before deployment.","score":0.91,"scope":"PROJECT"},{"memory_id":"memory-0002","subject":"Rollback rule","content":"Keep the previous verified artifact available for rollback.","score":0.84,"scope":"PROJECT"},{"memory_id":"memory-0003","subject":"Testing preference","content":"Run broad regression coverage before reporting a material fix.","score":0.78,"scope":"GLOBAL"},{"memory_id":"memory-0004","subject":"Publication boundary","content":"Do not publish until the user explicitly requests publication.","score":0.72,"scope":"GLOBAL"}]}`))
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
	if releaseBinary := os.Getenv("MINDMORY_RELEASE_MCP_BINARY"); releaseBinary != "" {
		contents, err := os.ReadFile(releaseBinary)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, contents, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		build := exec.Command("go", "build", "-o", binary, "./cmd/mindmory-mcp-stdio")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, output)
		}
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
	if len(names) != 1 || names[0] != "mindmory" {
		t.Fatalf("compact tools=%v", names)
	}
	schema, err := json.Marshal(listed.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema) > 1500 {
		t.Fatalf("compact tool schema is %d bytes; budget is 1500", len(schema))
	}
	fixedMetadataBytes := len(schema) + len(session.InitializeResult().Instructions)
	if fixedMetadataBytes > 1024 {
		t.Fatalf("compact fixed MCP metadata is %d bytes; budget is 1024", fixedMetadataBytes)
	}
	fixedMetadataTokens := retrieval.EstimatedTokens(string(schema) + session.InitializeResult().Instructions)
	if fixedMetadataTokens > 256 {
		t.Fatalf("compact fixed MCP metadata is %d estimated tokens; budget is 256", fixedMetadataTokens)
	}
	if strings.Contains(strings.ToLower(session.InitializeResult().Instructions), "beginning of each conversation") {
		t.Fatalf("MCP instructions still request an eager context call: %q", session.InitializeResult().Instructions)
	}
	help, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory", Arguments: map[string]any{"action": "help", "args": map[string]any{"action": "memory_search"}}})
	if err != nil || help.IsError {
		t.Fatalf("help result=%+v err=%v", help, err)
	}
	helpJSON, _ := json.Marshal(help.StructuredContent)
	if !strings.Contains(string(helpJSON), "query") || !strings.Contains(string(helpJSON), "limit") {
		t.Fatalf("memory_search help=%s", helpJSON)
	}
	search, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory", Arguments: map[string]any{"action": "memory_search", "args": map[string]any{"query": "release policy", "limit": 4}}})
	if err != nil || search.IsError {
		t.Fatalf("search result=%+v err=%v", search, err)
	}
	structured, _ := json.Marshal(search.StructuredContent)
	content, _ := json.Marshal(search.Content)
	wire, _ := json.Marshal(search)
	gatewayRequest := `{"action":"memory_search","args":{"query":"release policy","limit":4}}`
	directQuery := "release policy"
	directText := "Deployment policy: Use a reviewed release checklist before deployment.\n" +
		"Rollback rule: Keep the previous verified artifact available for rollback.\n" +
		"Testing preference: Run broad regression coverage before reporting a material fix.\n" +
		"Publication boundary: Do not publish until the user explicitly requests publication."
	if tokens := retrieval.EstimatedTokens(string(structured)); tokens > 300 {
		t.Fatalf("four-hit structured search result is %d estimated tokens; budget is 300", tokens)
	}
	t.Logf("four-hit search bytes/tokens: structured=%d/%d content=%d/%d full_wire=%d/%d", len(structured), retrieval.EstimatedTokens(string(structured)), len(content), retrieval.EstimatedTokens(string(content)), len(wire), retrieval.EstimatedTokens(string(wire)))
	t.Logf("search request/result comparison tokens: gateway_request=%d direct_query=%d direct_text=%d", retrieval.EstimatedTokens(gatewayRequest), retrieval.EstimatedTokens(directQuery), retrieval.EstimatedTokens(directText))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory", Arguments: map[string]any{"action": "memory_context", "args": map[string]any{"query": "MCP"}}})
	if err != nil || result.IsError {
		t.Fatalf("context result=%+v err=%v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory", Arguments: map[string]any{"action": "memory_forget", "args": map[string]any{"target_memory_id": "m1", "evidence_quote": "forget it"}}})
	if err != nil || result.IsError {
		t.Fatalf("forget result=%+v err=%v", result, err)
	}
	mu.Lock()
	mutationSession, mutationMessage := mutation["session_id"], mutation["message_id"]
	contextMaxChars := contextRequest["max_chars"]
	mu.Unlock()
	if mutationSession != "00000000-0000-4000-8000-000000000601" || mutationMessage != "00000000-0000-4000-8000-000000000631" {
		t.Fatalf("authority not server injected: %+v", mutation)
	}
	if contextMaxChars != float64(1200) {
		t.Fatalf("compact context request=%+v; want max_chars=1200", contextRequest)
	}
	smuggled, smuggleErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "mindmory", Arguments: map[string]any{"action": "memory_forget", "args": map[string]any{"target_memory_id": "m1", "evidence_quote": "forget it", "session_id": "attacker"}}})
	if smuggleErr == nil && (smuggled == nil || !smuggled.IsError) {
		t.Fatalf("compact gateway accepted an unknown authority field: %+v", smuggled)
	}

	// The legacy thirteen-tool contract remains available only when a host
	// explicitly opts into its higher per-session schema cost.
	fullCommand := exec.Command(binary)
	fullCommand.Env = []string{"MINDMORY_MCP_PROFILE=full"}
	fullSession, err := client.Connect(ctx, &mcp.CommandTransport{Command: fullCommand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fullSession.Close()
	fullList, err := fullSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fullNames := make([]string, 0, len(fullList.Tools))
	for _, tool := range fullList.Tools {
		fullNames = append(fullNames, tool.Name)
	}
	sort.Strings(fullNames)
	wantFull := []string{"artifact_read", "artifact_search", "memory_context", "memory_correct", "memory_diff", "memory_feedback", "memory_forget", "memory_recall", "memory_remember", "memory_search", "mindmory_status", "ops_recent", "proposal_review"}
	if strings.Join(fullNames, ",") != strings.Join(wantFull, ",") {
		t.Fatalf("full tools=%v (want %v)", fullNames, wantFull)
	}
	fullSchema, err := json.Marshal(fullList.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(fullSchema) < len(schema)*4 {
		t.Fatalf("compact schema reduction too small: compact=%d full=%d", len(schema), len(fullSchema))
	}
	t.Logf("MCP metadata: compact_schema=%dB compact_fixed=%dB/%d estimated tokens full_schema=%dB schema_reduction=%.1f%%", len(schema), fixedMetadataBytes, fixedMetadataTokens, len(fullSchema), 100*(1-float64(len(schema))/float64(len(fullSchema))))
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

func TestReleaseHookTemplatesCaptureUserAndAssistantRoles(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, relative := range []string{
		"packaging/integrations/codex/hooks.json.example",
		"packaging/integrations/claude-code/settings.json.example",
	} {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
		for _, event := range []string{"UserPromptSubmit", "Stop"} {
			groups := document.Hooks[event]
			if len(groups) != 1 || len(groups[0].Hooks) != 1 || !strings.Contains(groups[0].Hooks[0].Command, "checkpoint-hook.sh") {
				t.Fatalf("%s missing %s checkpoint command: %+v", relative, event, document.Hooks)
			}
		}
	}
}
