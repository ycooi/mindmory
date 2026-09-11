package mcp_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Run against an isolated daemon started with the documented synthetic overlay:
// [{"canonical":"青岚计划","aliases":["blue mist project"]}].
func TestStage6ALiveMultilingualAlias(t *testing.T) {
	if os.Getenv("MINDMORY_LIVE_ALIAS_FIXTURE") != "1" {
		t.Skip("isolated alias fixture required")
	}
	endpoint, token, _ := liveEnv(t)
	content := "记住：青岚计划采用独立审核制度。"
	sessionID, messageID := liveCheckpoint(t, endpoint, token, "alias-live-"+uniqueSuffix(), "message", "alias-fixture", content)
	session := connectMCP(t, buildMCPBinary(t), endpoint, token, sessionID, messageID)
	result, isError := callTool(t, session, "memory_remember", map[string]any{"memory_kind": "PROJECT_DECISION", "scope": "PROJECT", "subject": "青岚计划", "evidence_quote": content})
	if isError {
		t.Fatalf("remember: %s", result)
	}
	var remembered struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(result), &remembered); err != nil || remembered.MemoryID == "" {
		t.Fatalf("remember: %s", result)
	}
	for _, query := range []string{"请介绍青岚计划的安排", "please explain blue mist project"} {
		result, isError = callTool(t, session, "memory_search", map[string]any{"query": query, "limit": 5})
		if isError || !strings.Contains(result, remembered.MemoryID) {
			t.Fatalf("%q: %s", query, result)
		}
	}
}
