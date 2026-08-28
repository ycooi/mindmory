// Package mcp_test live-verifies the real mindmory-mcp-stdio binary against a
// running Mindmory control plane, driving it with the official MCP SDK exactly
// as a harness would. These tests skip unless the live environment is set:
// MINDMORY_LIVE_MCP_TOKEN and MINDMORY_LIVE_ADMIN_TOKEN (endpoint defaults to
// http://127.0.0.1:58080). Run with --network host so the container reaches
// the host's loopback stack.
package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Base-36 keeps unique synthetic identifiers from accidentally resembling a
// 13-19 digit payment-card number to the intentionally fail-closed policy.
func uniqueSuffix() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

func liveEnv(t *testing.T) (endpoint, mcpToken, adminToken string) {
	t.Helper()
	endpoint = os.Getenv("MINDMORY_LIVE_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:58080"
	}
	mcpToken = os.Getenv("MINDMORY_LIVE_MCP_TOKEN")
	adminToken = os.Getenv("MINDMORY_LIVE_ADMIN_TOKEN")
	if mcpToken == "" || adminToken == "" {
		t.Skip("MINDMORY_LIVE_MCP_TOKEN and MINDMORY_LIVE_ADMIN_TOKEN required for live MCP tests")
	}
	return endpoint, mcpToken, adminToken
}

func liveCheckpoint(t *testing.T, endpoint, token, externalSession, externalMessage, project, content string) (sessionID, messageID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"external_session_id": externalSession, "project_key": project, "mode": "INCREMENTAL",
		"messages": []map[string]any{{"external_message_id": externalMessage, "role": "user", "content_type": "text/plain",
			"content": content, "occurred_at": time.Now().UTC().Format(time.RFC3339)}},
		"tool_events": []any{},
	})
	request, err := http.NewRequest(http.MethodPost, endpoint+"/v1/checkpoints", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("checkpoint status=%d", response.StatusCode)
	}
	var result struct {
		SessionID  string            `json:"session_id"`
		MessageIDs map[string]string `json:"message_ids"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || result.MessageIDs[externalMessage] == "" {
		t.Fatalf("checkpoint omitted authority: %+v", result)
	}
	return result.SessionID, result.MessageIDs[externalMessage]
}

func buildMCPBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("MINDMORY_RELEASE_MCP_BINARY"); binary != "" {
		absolute, err := filepath.Abs(binary)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(absolute)
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("release MCP binary is not executable: %s", absolute)
		}
		return absolute
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binary := filepath.Join(t.TempDir(), "mindmory-mcp-stdio")
	build := exec.Command("go", "build", "-o", binary, "./cmd/mindmory-mcp-stdio")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	return binary
}

func connectMCP(t *testing.T, binary, endpoint, token, sessionID, messageID string) *mcp.ClientSession {
	t.Helper()
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"MINDMORY_ENDPOINT="+endpoint, "MINDMORY_MCP_TOKEN="+token,
		"MINDMORY_BOUND_SESSION_ID="+sessionID, "MINDMORY_BOUND_MESSAGE_ID="+messageID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "live-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func connectPackagedMCP(t *testing.T, binary string) *mcp.ClientSession {
	t.Helper()
	command := exec.Command(binary)
	// The release contract intentionally supplies no token environment. The
	// bridge must discover its protected config beside the distribution.
	command.Env = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "release-acceptance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// toolJSON extracts the structured tool result as JSON.
func toolJSON(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err == nil {
			return string(raw)
		}
	}
	raw, _ := json.Marshal(result.Content)
	return string(raw)
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s transport error: %v", name, err)
	}
	return toolJSON(t, result), result.IsError
}

func TestStage6ALiveMCPSessionIsGenuinelyFunctional(t *testing.T) {
	endpoint, mcpToken, _ := liveEnv(t)
	content := "记住以后 Mindmory 的 MCP server 优先用 Go。"
	sessionID, messageID := liveCheckpoint(t, endpoint, mcpToken, "live-mcp-"+uniqueSuffix(), "m1", "Mindmory", content)

	session := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, messageID)
	if got := session.InitializeResult().ProtocolVersion; got != "2025-11-25" {
		t.Fatalf("negotiated protocol=%s", got)
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	// Current authoritative tool inventory, including sanitized status.
	want := []string{"artifact_read", "artifact_search", "memory_context", "memory_correct", "memory_diff", "memory_feedback", "memory_forget", "memory_recall", "memory_remember", "memory_search", "mindmory_status", "ops_recent", "proposal_review"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools=%v (want %v)", names, want)
	}

	packet, isError := callTool(t, session, "memory_context", nil)
	if isError || !strings.Contains(packet, "continuity_cursor") || !strings.Contains(packet, sessionID) {
		t.Fatalf("memory_context result=%s error=%t", packet, isError)
	}
	// The MCP default explicit budget is 3000 chars: a per-turn refresh must
	// not silently carry a ~3k-token packet.
	if len(packet) > 5000 {
		t.Fatalf("default memory_context packet oversized (%d chars): %s", len(packet), packet[:200])
	}
	large, isError := callTool(t, session, "memory_context", map[string]any{"max_chars": 6000})
	if isError || !strings.Contains(large, sessionID) {
		t.Fatalf("explicit max_chars override failed: %s error=%t", large[:min(len(large), 200)], isError)
	}

	reflex, isError := callTool(t, session, "memory_context", map[string]any{"mode": "reflex"})
	// Reflex mode always returns the bound session and the continuity cursor;
	// project_context is legitimately absent when the project has no promoted
	// state, and the packet may be truncated at its token budget.
	if isError || !strings.Contains(reflex, `"session"`) || !strings.Contains(reflex, sessionID) || !strings.Contains(reflex, `"continuity_cursor"`) {
		t.Fatalf("reflex-mode memory_context result=%s error=%t", reflex, isError)
	}
	invalidMode, isError := callTool(t, session, "memory_context", map[string]any{"mode": "bogus"})
	if !isError || !strings.Contains(invalidMode, "CONTEXT_QUERY_INVALID") {
		t.Fatalf("invalid mode result=%s error=%t", invalidMode, isError)
	}

	remembered, isError := callTool(t, session, "memory_remember", map[string]any{
		"memory_kind": "PROJECT_DECISION", "scope": "PROJECT", "subject": "Mindmory MCP server 功能测试 " + uniqueSuffix(), "evidence_quote": content,
	})
	if isError || !strings.Contains(remembered, `"outcome":"APPLIED"`) {
		t.Fatalf("memory_remember result=%s error=%t", remembered, isError)
	}
	var rememberResponse struct {
		Outcome  string `json:"outcome"`
		MemoryID string `json:"memory_id"`
	}
	if err = json.Unmarshal([]byte(remembered), &rememberResponse); err != nil || rememberResponse.MemoryID == "" {
		t.Fatalf("remember response=%s err=%v", remembered, err)
	}
	memoryID := rememberResponse.MemoryID

	recalled, isError := callTool(t, session, "memory_recall", map[string]any{"memory_id": memoryID})
	if isError || !strings.Contains(recalled, `"lifecycle":"ACTIVE"`) || !strings.Contains(recalled, content) {
		t.Fatalf("memory_recall result=%s error=%t", recalled, isError)
	}

	search, isError := callTool(t, session, "memory_search", map[string]any{"query": "Go", "limit": 5})
	if isError || !strings.Contains(search, "Mindmory MCP server") {
		t.Fatalf("memory_search result=%s error=%t", search, isError)
	}

	diff, isError := callTool(t, session, "memory_diff", nil)
	if isError || !strings.Contains(diff, "MEMORY_CREATED") || !strings.Contains(diff, "next_cursor") {
		t.Fatalf("memory_diff result=%s error=%t", diff, isError)
	}
}

func TestStage6ALiveMCPPackagedHookToRememberFlow(t *testing.T) {
	binary, prompt := os.Getenv("MINDMORY_RELEASE_MCP_BINARY"), os.Getenv("MINDMORY_RELEASE_HOOK_PROMPT")
	if binary == "" || prompt == "" {
		t.Skip("release binary and hook prompt required")
	}
	session := connectPackagedMCP(t, binary)
	status, isError := callTool(t, session, "mindmory_status", nil)
	if isError || !strings.Contains(status, `"state":"READY"`) {
		t.Fatalf("packaged status=%s error=%t", status, isError)
	}
	remembered, isError := callTool(t, session, "memory_remember", map[string]any{
		"memory_kind": "USER_PREFERENCE", "scope": "GLOBAL",
		"subject": "packaged host checkpoint color", "evidence_quote": prompt,
	})
	if isError || !strings.Contains(remembered, `"outcome":"APPLIED"`) {
		t.Fatalf("packaged remember=%s error=%t", remembered, isError)
	}
	search, isError := callTool(t, session, "memory_search", map[string]any{"query": "ultraviolet", "limit": 5})
	if isError || !strings.Contains(search, "packaged host checkpoint color") {
		t.Fatalf("packaged search=%s error=%t", search, isError)
	}
}

func TestStage6ALiveMCPCorrectAndForgetChain(t *testing.T) {
	endpoint, mcpToken, _ := liveEnv(t)
	external := "live-mcp-chain-" + uniqueSuffix()

	rememberQuote := "记住以后 Mindmory 的 MCP server 优先用 Go。"
	sessionID, message1 := liveCheckpoint(t, endpoint, mcpToken, external, "m1", "Mindmory", rememberQuote)
	session1 := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, message1)
	// The subject must be unique: the store already holds "Mindmory MCP
	// server" memories from early development, and content-level dedupe
	// would stage (not apply) a duplicate subject.
	remembered, isError := callTool(t, session1, "memory_remember", map[string]any{
		"memory_kind": "PROJECT_DECISION", "scope": "PROJECT", "subject": "Mindmory MCP server 测试链 " + external, "evidence_quote": rememberQuote,
	})
	if isError || !strings.Contains(remembered, `"outcome":"APPLIED"`) {
		t.Fatalf("remember=%s error=%t", remembered, isError)
	}
	var first struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(remembered), &first); err != nil || first.MemoryID == "" {
		t.Fatalf("remember response=%s", remembered)
	}

	correctQuote := "更正一下，Mindmory 的 MCP server 应该优先用 Rust。"
	_, message2 := liveCheckpoint(t, endpoint, mcpToken, external, "m2", "Mindmory", correctQuote)
	session2 := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, message2)
	corrected, isError := callTool(t, session2, "memory_correct", map[string]any{
		"target_memory_id": first.MemoryID, "replacement": "Rust", "evidence_quote": correctQuote,
	})
	if isError || !strings.Contains(corrected, `"outcome":"APPLIED"`) {
		t.Fatalf("correct=%s error=%t", corrected, isError)
	}
	var second struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(corrected), &second); err != nil || second.MemoryID == "" {
		t.Fatalf("correct response=%s", corrected)
	}
	recalled, isError := callTool(t, session2, "memory_recall", map[string]any{"memory_id": second.MemoryID})
	if isError || !strings.Contains(recalled, first.MemoryID) {
		t.Fatalf("supersession chain missing: %s error=%t", recalled, isError)
	}

	forgetQuote := "忘掉之前 Mindmory MCP server 优先用 Rust 的偏好。"
	_, message3 := liveCheckpoint(t, endpoint, mcpToken, external, "m3", "Mindmory", forgetQuote)
	session3 := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, message3)
	forgotten, isError := callTool(t, session3, "memory_forget", map[string]any{
		"target_memory_id": second.MemoryID, "evidence_quote": forgetQuote,
	})
	if isError || !strings.Contains(forgotten, `"outcome":"APPLIED"`) {
		t.Fatalf("forget=%s error=%t", forgotten, isError)
	}
	recalled, isError = callTool(t, session3, "memory_recall", map[string]any{"memory_id": second.MemoryID})
	if isError || !strings.Contains(recalled, `"lifecycle":"FORGOTTEN"`) {
		t.Fatalf("forgotten recall=%s error=%t", recalled, isError)
	}
}

func TestStage6ALiveMCPRefusesNonCurrentBoundTurn(t *testing.T) {
	endpoint, mcpToken, _ := liveEnv(t)
	external := "live-mcp-stale-" + uniqueSuffix()
	sessionID, message1 := liveCheckpoint(t, endpoint, mcpToken, external, "m1", "Mindmory", "第一条消息。")

	// The MCP server binds once with a static profile message id, which
	// goes stale on the next turn. Mutations re-resolve the CURRENT user
	// turn per call (currentMessageID -> ?latest=1), so a server bound to
	// an old message still writes against the latest archived turn — the
	// designed behavior, not an acceptance of stale turns. Verify that a
	// mutation from the stale-bound server targets the NEW message's quote.
	session := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, message1)
	content2 := "第二条消息,请记住它。"
	liveCheckpoint(t, endpoint, mcpToken, external, "m2", "Mindmory", content2)

	remembered, isError := callTool(t, session, "memory_remember", map[string]any{
		"memory_kind": "PROJECT_DECISION", "scope": "PROJECT",
		"subject":        "第二条消息请记住",
		"evidence_quote": content2,
	})
	if isError || !strings.Contains(remembered, `"outcome":"APPLIED"`) {
		t.Fatalf("bound server must write against the latest turn: %s error=%t", remembered, isError)
	}
}

func TestStage6ALiveMCPAuthoritySmugglingIsInert(t *testing.T) {
	endpoint, mcpToken, _ := liveEnv(t)
	content := "记住以后 Mindmory 的 MCP server 优先用 Go。"
	sessionID, messageID := liveCheckpoint(t, endpoint, mcpToken, "live-mcp-smuggle-"+uniqueSuffix(), "m1", "Mindmory", content)

	session := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, messageID)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "memory_remember", Arguments: map[string]any{
		"memory_kind": "PROJECT_DECISION", "scope": "PROJECT", "subject": "Mindmory MCP server 走私测试 " + uniqueSuffix(), "evidence_quote": content,
		"session_id": "00000000-0000-0000-0000-000000000000", "message_id": "00000000-0000-0000-0000-000000000000",
		"project_key": "SomeOtherProject",
	}})
	if err != nil {
		t.Fatalf("smuggled call failed at transport: %v", err)
	}
	payload := toolJSON(t, result)
	// The official SDK validates tool arguments against the typed input
	// schema and rejects authority fields outright — stronger than ignoring
	// them: the smuggled authority can never reach a mutation.
	if !result.IsError || !strings.Contains(payload, "additional properties") {
		t.Fatalf("smuggled authority fields were not rejected: %s", payload)
	}
	// The server stays healthy after the hostile call.
	packet, isError := callTool(t, session, "memory_context", nil)
	if isError || !strings.Contains(packet, sessionID) {
		t.Fatalf("server unhealthy after smuggling: %s error=%t", packet, isError)
	}
}

func TestStage6ALiveMCPDiffRejectsTamperedCursor(t *testing.T) {
	endpoint, mcpToken, _ := liveEnv(t)
	sessionID, messageID := liveCheckpoint(t, endpoint, mcpToken, "live-mcp-diff-"+uniqueSuffix(), "m1", "Mindmory", "diff test。")
	session := connectMCP(t, buildMCPBinary(t), endpoint, mcpToken, sessionID, messageID)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "memory_diff", Arguments: map[string]any{
		"cursor": "eyJ0YW1wZXJlZCI6dHJ1ZX0.tampered", "limit": 10,
	}})
	if err != nil {
		t.Fatalf("tampered cursor call failed at transport: %v", err)
	}
	if !result.IsError {
		t.Fatalf("tampered cursor accepted: %s", toolJSON(t, result))
	}
	if !strings.Contains(toolJSON(t, result), "CURSOR_INVALID") {
		t.Fatalf("tampered cursor error missing code: %s", toolJSON(t, result))
	}
}
