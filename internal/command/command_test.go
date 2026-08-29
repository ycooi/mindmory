package command

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointHookArchivesHostPromptWithoutWritingResponse(t *testing.T) {
	var got hookCheckpointRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkpoints" || r.Header.Get("Authorization") != "Bearer client-token-at-least-24-characters" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"session_id":"s1"}`)
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	input := bytes.NewBufferString(`{"session_id":"chat-1","turn_id":"turn-2","prompt":"remember this","cwd":"/project"}`)
	if code := runCheckpointHook([]string{"--host", "codex"}, input); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if got.ExternalSessionID != "mindmory-continuity" || got.ProjectKey != "/project" || len(got.Messages) != 1 {
		t.Fatalf("unexpected checkpoint: %#v", got)
	}
	message := got.Messages[0]
	if message.Role != "user" || message.Content != "remember this" || !strings.HasPrefix(message.ExternalMessageID, "codex-") {
		t.Fatalf("unexpected message: %#v", message)
	}
}

func TestCheckpointHookArchivesAssistantStopWithIdentity(t *testing.T) {
	var received []hookCheckpointRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got hookCheckpointRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		received = append(received, got)
		_, _ = io.WriteString(w, `{"session_id":"s1"}`)
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	transcript := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(transcript, []byte("completed turn"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"session_id": "chat-1", "hook_event_name": "Stop", "transcript_path": transcript,
		"last_assistant_message": "I completed the requested refactor.", "cwd": "/project",
	})
	if code := runCheckpointHook([]string{"--host", "claude-code"}, bytes.NewReader(input)); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if code := runCheckpointHook([]string{"--host", "claude-code"}, bytes.NewReader(input)); code != 0 {
		t.Fatalf("retry code=%d", code)
	}
	if len(received) != 2 || len(received[0].Messages) != 1 {
		t.Fatalf("unexpected checkpoints: %#v", received)
	}
	message := received[0].Messages[0]
	if message.Role != "assistant" || message.Content != "I completed the requested refactor." ||
		message.AssistantID != "claude-code" || message.AssistantName != "Claude Code" ||
		!strings.HasPrefix(message.ExternalMessageID, "claude-code-assistant-") {
		t.Fatalf("unexpected assistant message: %#v", message)
	}
	if retryID := received[1].Messages[0].ExternalMessageID; retryID != message.ExternalMessageID {
		t.Fatalf("same transcript position was not idempotent: %s != %s", retryID, message.ExternalMessageID)
	}
	if err := os.WriteFile(transcript, []byte("completed turn\nnext completed turn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCheckpointHook([]string{"--host", "claude-code"}, bytes.NewReader(input)); code != 0 {
		t.Fatalf("next turn code=%d", code)
	}
	if nextID := received[2].Messages[0].ExternalMessageID; nextID == message.ExternalMessageID {
		t.Fatalf("identical assistant text in a later transcript position collapsed: %s", nextID)
	}
}

func TestCheckpointHookRejectsEmptyPrompt(t *testing.T) {
	if code := runCheckpointHook(nil, bytes.NewBufferString(`{"session_id":"chat-1","prompt":""}`)); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestLiteOperatorCommandsMapToLiveAdminRoutes(t *testing.T) {
	tests := []struct {
		args         []string
		method, path string
	}{
		{[]string{"ops"}, http.MethodGet, "/v1/admin/ops"},
		{[]string{"proposals"}, http.MethodGet, "/v1/admin/proposals"},
		{[]string{"snapshot"}, http.MethodPost, "/v1/admin/snapshot"},
		{[]string{"learner", "extract"}, http.MethodPost, "/v1/admin/learner/extract"},
		{[]string{"proposal", "approve", "p1"}, http.MethodPost, "/v1/admin/proposals/p1/approve"},
		{[]string{"proposal", "reject", "p1"}, http.MethodPost, "/v1/admin/proposals/p1/reject"},
		{[]string{"memory", "retire", "m1"}, http.MethodPost, "/v1/admin/memories/m1/retire"},
	}
	for _, test := range tests {
		method, path, ok := adminOperation(test.args)
		if !ok || method != test.method || path != test.path {
			t.Fatalf("%v => %s %s %v", test.args, method, path, ok)
		}
	}
}

func TestLiteVectorStatusUsesAdminCredential(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/system/status" || r.Header.Get("X-Admin-Token") != "admin-token-at-least-24-characters" {
			t.Errorf("path=%q token=%q", r.URL.Path, r.Header.Get("X-Admin-Token"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "READY"})
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ADMIN_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_ADMIN_TOKEN", "admin-token-at-least-24-characters")
	if code := Run("mindmoryctl", "test", []string{"vectors", "status"}); code != 0 || !called {
		t.Fatalf("code=%d called=%t", code, called)
	}
}

func TestLiteAdminCommandUsesAdminHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/ops" || r.Header.Get("X-Admin-Token") != "admin-token-at-least-24-characters" {
			t.Errorf("path=%q token=%q", r.URL.Path, r.Header.Get("X-Admin-Token"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ADMIN_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_ADMIN_TOKEN", "admin-token-at-least-24-characters")
	if code := Run("mindmoryctl", "test", []string{"ops"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
}
