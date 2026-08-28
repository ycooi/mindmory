package lite

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mindmory.local/core/internal/config"
	domain "mindmory.local/core/internal/memory"
)

// newRelevanceFixture builds an isolated store with one session and three
// memories: one matching the query, one non-matching, and one SECRET that
// must never be returned by relevance.
func newRelevanceFixture(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := newTestStore(t)
	principal := testPrincipal()
	if _, err := store.UpsertSession(context.Background(), principal, "ext-relevance", "relevance test", "ember", time.Now().UTC()); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	memories := []MemoryRow{
		{
			MemoryID: "rel-1", Kind: string(domain.KindUserPreference), Subject: "我喜欢喝气泡水",
			Content: "我喜欢喝气泡水而不是可乐", ContentHash: hashContent("我喜欢喝气泡水而不是可乐"),
			Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1.0,
			Importance: 0.6, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.8,
		},
		{
			MemoryID: "rel-2", Kind: string(domain.KindDocumentFact), Subject: "部署流程",
			Content: "部署流程是先备份再发布", ContentHash: hashContent("部署流程是先备份再发布"),
			Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 0.8,
			Importance: 0.5, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.5,
		},
		{
			MemoryID: "rel-3", Kind: string(domain.KindDocumentFact), Subject: "气泡水机密",
			Content: "气泡水秘密配方", ContentHash: hashContent("气泡水秘密配方"),
			Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 0.8,
			Importance: 0.5, Sensitivity: "SECRET", ScopeType: "GLOBAL", Activation: 0.5,
		},
	}
	for _, m := range memories {
		if err := store.insertMemoryFixture(context.Background(), m); err != nil {
			t.Fatalf("insert memory %s: %v", m.MemoryID, err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokens := map[string]config.MCPPrincipalConfig{
		"test-client": {Token: config.MCPToken(strings.Repeat("t", 24)), Capabilities: []config.MCPClientCapability{config.MCPContextRead, config.MCPMemoryPropose}},
	}
	return NewServer(store, "owner", strings.Repeat("k", 32), "admin-token", tokens, log, true), store
}

func postRelevance(t *testing.T, server *Server, sessionID, query string) relevanceResponse {
	t.Helper()
	body, err := json.Marshal(relevanceRequest{SessionID: sessionID, Query: query})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/context/relevance", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	var out relevanceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestRelevanceBasic(t *testing.T) {
	server, store := newRelevanceFixture(t)
	session := firstSessionID(t, store)

	// First call: only the NORMAL matching memory (SECRET excluded).
	out := postRelevance(t, server, session, "气泡水")
	if len(out.Memories) == 0 {
		t.Fatalf("no memories returned for query 气泡水")
	}
	found := map[string]bool{}
	for _, h := range out.Memories {
		found[h.MemoryID] = true
		if h.MemoryID == "rel-3" {
			t.Errorf("SECRET memory leaked into relevance: %s", h.MemoryID)
		}
	}
	if !found["rel-1"] {
		t.Errorf("expected rel-1 in relevance hits, got %v", found)
	}
	// The matching hit must not record access (injection is not use).
	row, err := store.LoadMemoryRow(context.Background(), "rel-1")
	if err != nil {
		t.Fatalf("load rel-1: %v", err)
	}
	if row.AccessCount != 0 {
		t.Errorf("relevance recorded access on rel-1: AccessCount = %d, want 0", row.AccessCount)
	}

	// Second call: rel-1 is now surfaced for the session -> excluded.
	second := postRelevance(t, server, session, "气泡水")
	for _, h := range second.Memories {
		if h.MemoryID == "rel-1" {
			t.Errorf("rel-1 re-injected after being surfaced")
		}
	}

	// An irrelevant query returns nothing.
	empty := postRelevance(t, server, session, "量子力学")
	if len(empty.Memories) != 0 {
		t.Errorf("irrelevant query returned %d memories, want 0", len(empty.Memories))
	}
}

func TestRelevanceBudget(t *testing.T) {
	server, store := newRelevanceFixture(t)
	session := firstSessionID(t, store)
	// Tiny budget forces truncation.
	body, _ := json.Marshal(relevanceRequest{SessionID: session, Query: "气泡水", MaxChars: 4})
	request := httptest.NewRequest(http.MethodPost, "/v1/context/relevance", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var out relevanceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 0 || !out.Truncated {
		t.Errorf("tiny budget: memories = %d, truncated = %v; want 0 and true", len(out.Memories), out.Truncated)
	}
}

func TestRelevanceValidation(t *testing.T) {
	server, _ := newRelevanceFixture(t)
	for _, bad := range []relevanceRequest{
		{SessionID: "", Query: "气泡水"},                                  // no session
		{SessionID: "not-a-uuid", Query: "气泡水"},                        // bad session
		{SessionID: "01a040e1-72c6-71e3-bca7-ce4ceee2f44b", Query: ""}, // no query
	} {
		body, _ := json.Marshal(bad)
		request := httptest.NewRequest(http.MethodPost, "/v1/context/relevance", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("request %+v: status = %d, want 400", bad, recorder.Code)
		}
	}
}

// firstSessionID returns the id of the fixture's single session.
func firstSessionID(t *testing.T, store *Store) string {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	for id := range store.sessions {
		return id
	}
	t.Fatal("no session in store")
	return ""
}
