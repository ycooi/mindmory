package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mindmory.local/core/internal/retrieval"
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
	if got.ExternalSessionID != "mindmory-continuity" || got.ProjectKey != "" || len(got.Messages) != 1 {
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

func TestCheckpointHookPreservesHostTimestampAndDeepSeekIdentity(t *testing.T) {
	var request hookCheckpointRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"session_id":"s1"}`)
	}))
	defer backend.Close()
	t.Setenv("MINDMORY_ENDPOINT", backend.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "test-checkpoint-token-0123456789")
	input := strings.NewReader(`{"session_id":"dsh-1","turn_id":"assistant-1","hook_event_name":"Stop","last_assistant_message":"Exact Harness reply","occurred_at":"2026-08-29T01:02:03.456Z"}`)
	if code := runCheckpointHook([]string{"--host", "deepseek-harness"}, input); code != 0 {
		t.Fatalf("checkpoint hook exit=%d", code)
	}
	message := request.Messages[0]
	if message.Role != "assistant" || message.AssistantID != "deepseek-harness" || message.AssistantName != "DeepSeek Harness" {
		t.Fatalf("assistant identity=%+v", message)
	}
	want := time.Date(2026, 8, 29, 1, 2, 3, 456000000, time.UTC)
	if !message.OccurredAt.Equal(want) {
		t.Fatalf("occurred_at=%s want %s", message.OccurredAt, want)
	}
}

func TestCheckpointHookRejectsEmptyPrompt(t *testing.T) {
	if code := runCheckpointHook(nil, bytes.NewBufferString(`{"session_id":"chat-1","prompt":""}`)); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestNativeHookArchivesAndInjectsOnlyBoundedPlainText(t *testing.T) {
	const internalSessionID = "00000000-0000-4000-8000-000000000123"
	var checkpoint hookCheckpointRequest
	var relevance hookRelevanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkpoints":
			if err := json.NewDecoder(r.Body).Decode(&checkpoint); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session_id":"`+internalSessionID+`"}`)
		case "/v1/context/relevance":
			if err := json.NewDecoder(r.Body).Decode(&relevance); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"memories":[{"memory_id":"private-id","subject":"Rollback rule","content":"Keep the previous verified artifact available.","score":0.99}]}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	input := strings.NewReader(`{"session_id":"thread-1","turn_id":"turn-1","hook_event_name":"UserPromptSubmit","prompt":"what is the rollback rule?","cwd":"/project-a"}`)
	var output bytes.Buffer
	if code := runNativeHook([]string{"--host", "codex", "--max-chars", "120", "--max-memories", "2"}, input, &output); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.HasPrefix(checkpoint.ExternalSessionID, "codex:thread-1:") || checkpoint.ProjectKey != "/project-a" || checkpoint.Messages[0].Role != "user" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	if relevance.SessionID != internalSessionID || relevance.Query != "what is the rollback rule?" || relevance.MaxChars != 120 || relevance.MaxMemories != 2 || !relevance.StrongOnly {
		t.Fatalf("relevance=%+v", relevance)
	}
	var got codexHookOutput
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput == nil || got.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("hook output=%s", output.String())
	}
	contextText := got.HookSpecificOutput.AdditionalContext
	if !strings.Contains(contextText, "Rollback rule") || strings.Contains(contextText, "private-id") || strings.Contains(contextText, "0.99") {
		t.Fatalf("unexpected model context: %q", contextText)
	}
	if count := len([]rune(contextText)); count > 120 {
		t.Fatalf("context runes=%d exceeds budget: %q", count, contextText)
	}
	if tokens := retrieval.EstimatedTokens(contextText); tokens > 30 {
		t.Fatalf("context estimated tokens=%d exceeds budget: %q", tokens, contextText)
	}
}

func TestNativeHookUsesGenericEnvelopeForDeepSeekHarness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkpoints":
			_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
		case "/v1/context/relevance":
			_, _ = io.WriteString(w, `{"memories":[{"memory_id":"hidden","subject":"Harness policy","content":"Keep native recall bounded."}]}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	input := strings.NewReader(`{"session_id":"harness-1","turn_id":"user-1","hook_event_name":"UserPromptSubmit","prompt":"What is the Harness policy?","cwd":"/project"}`)
	var output bytes.Buffer
	if code := runNativeHook([]string{"--host", "deepseek-harness"}, input, &output); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got nativeHookOutput
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.AdditionalContext, "Harness policy") || strings.Contains(output.String(), "hidden") {
		t.Fatalf("generic output=%q", output.String())
	}
	if strings.Contains(output.String(), "hookSpecificOutput") || strings.Contains(output.String(), "hookEventName") {
		t.Fatalf("Codex envelope leaked into Harness output: %q", output.String())
	}
}

func TestNativeSessionIdentitySeparatesProjectMoves(t *testing.T) {
	event := hookInput{SessionID: "thread-1", CWD: "/project-a"}
	first := nativeExternalSessionID("codex", event)
	event.CWD = "/project-b"
	second := nativeExternalSessionID("codex", event)
	if first == second || !strings.HasPrefix(first, "codex:thread-1:") || !strings.HasPrefix(second, "codex:thread-1:") {
		t.Fatalf("first=%q second=%q", first, second)
	}
	if got := nativeExternalSessionID("codex", hookInput{SessionID: "thread-1"}); got != "codex:thread-1" {
		t.Fatalf("empty project identity=%q", got)
	}
}

func TestNativeContextBoundsCJKByEnglishEquivalentTokenBudget(t *testing.T) {
	memories := []hookMemory{{
		Subject: strings.Repeat("长期偏好", 30),
		Content: strings.Repeat("运行广泛回归测试后再确认修复", 30),
	}}
	contextText := formatNativeContext(memories, 320)
	if contextText == "" {
		t.Fatal("bounded CJK context was unexpectedly empty")
	}
	if runes := len([]rune(contextText)); runes > 320 {
		t.Fatalf("runes=%d", runes)
	}
	if tokens := retrieval.EstimatedTokens(contextText); tokens > 80 {
		t.Fatalf("estimated tokens=%d context=%q", tokens, contextText)
	}
}

func TestNativeHookAddsNoContextForEmptyRelevance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/checkpoints" {
			_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
			return
		}
		_, _ = io.WriteString(w, `{"memories":[]}`)
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	var output bytes.Buffer
	input := strings.NewReader(`{"session_id":"thread-2","turn_id":"turn-1","hook_event_name":"UserPromptSubmit","prompt":"unrelated query","cwd":"/project"}`)
	if code := runNativeHook(nil, input, &output); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.TrimSpace(output.String()) != "{}" {
		t.Fatalf("empty lookup injected output: %q", output.String())
	}
}

func TestNativeHookStopArchivesWithoutRetrieval(t *testing.T) {
	relevanceCalls := 0
	var checkpoint hookCheckpointRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/context/relevance" {
			relevanceCalls++
		}
		_ = json.NewDecoder(r.Body).Decode(&checkpoint)
		_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	var output bytes.Buffer
	input := strings.NewReader(`{"session_id":"thread-3","turn_id":"turn-1","hook_event_name":"Stop","last_assistant_message":"Done.","cwd":"/project"}`)
	if code := runNativeHook(nil, input, &output); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if relevanceCalls != 0 || checkpoint.Messages[0].Role != "assistant" || checkpoint.Messages[0].AssistantName != "Codex" {
		t.Fatalf("relevance=%d checkpoint=%+v", relevanceCalls, checkpoint)
	}
	if strings.TrimSpace(output.String()) != "{}" {
		t.Fatalf("stop output=%q", output.String())
	}
}

func TestNativeHookRetrievalFailureFailsOpenAfterCheckpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/checkpoints" {
			_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"internal"}`)
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	var output bytes.Buffer
	input := strings.NewReader(`{"session_id":"thread-4","turn_id":"turn-1","hook_event_name":"UserPromptSubmit","prompt":"query","cwd":"/project"}`)
	if code := runNativeHook(nil, input, &output); code != 0 || strings.TrimSpace(output.String()) != "{}" {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestNativeHookHundredSyntheticTurnsStayWithinBudget(t *testing.T) {
	var checkpointCalls, relevanceCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkpoints":
			checkpointCalls++
			_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
		case "/v1/context/relevance":
			relevanceCalls++
			_, _ = io.WriteString(w, `{"memories":[{"memory_id":"hidden","subject":"测试偏好","content":"运行广泛回归测试后再确认修复。"}]}`)
		}
	}))
	defer server.Close()
	t.Setenv("MINDMORY_ENDPOINT", server.URL)
	t.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	for i := 0; i < 100; i++ {
		input := strings.NewReader(fmt.Sprintf(`{"session_id":"thread-load","turn_id":"turn-%d","hook_event_name":"UserPromptSubmit","prompt":"test %d","cwd":"/project"}`, i, i))
		var output bytes.Buffer
		if code := runNativeHook([]string{"--max-chars", "96"}, input, &output); code != 0 {
			t.Fatalf("turn %d code=%d", i, code)
		}
		var got codexHookOutput
		if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.HookSpecificOutput == nil {
			t.Fatalf("turn %d output=%q err=%v", i, output.String(), err)
		}
		if n := len([]rune(got.HookSpecificOutput.AdditionalContext)); n > 96 {
			t.Fatalf("turn %d context runes=%d", i, n)
		}
		if strings.Contains(output.String(), "hidden") {
			t.Fatalf("turn %d leaked metadata: %s", i, output.String())
		}
	}
	if checkpointCalls != 100 || relevanceCalls != 100 {
		t.Fatalf("checkpoint=%d relevance=%d", checkpointCalls, relevanceCalls)
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
