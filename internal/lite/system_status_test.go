package lite

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mindmory.local/core/internal/lite/vectorstore"
)

func statusFixture(t *testing.T, model, digest string, dimensions int) *Store {
	t.Helper()
	root := t.TempDir()
	store, err := OpenConfigured(StorageConfig{DataDir: root + "/data", DerivedDir: root + "/derived", VectorDir: root + "/vectors", SnapshotDir: root + "/snapshots"}, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := vectorstore.Create(store.vectorDir, vectorstore.GenerationSpec{ModelName: model, ModelDigest: digest, Dimensions: dimensions})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.VectorStore = vectors
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestIncidentBoundAdminRebuildResolvesModelMismatch(t *testing.T) {
	store := newTestStore(t)
	if err := store.insertMemoryFixture(t.Context(), persistentFixture("incident-memory", "incident", "model change fixture")); err != nil {
		t.Fatal(err)
	}
	oldEmbedder := &countingEmbedder{model: "model-alpha", digest: "revision-1", value: []float32{1, 0, 0}}
	if _, err := store.SyncVectors(t.Context(), oldEmbedder, VectorSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	newEmbedder := &countingEmbedder{model: "model-beta", digest: "revision-2", value: []float32{0, 1, 0}}
	semantic := true
	server := &Server{Store: store, AdminToken: "admin", Embedder: newEmbedder, Log: testLogger(), SemanticSearch: &semantic}
	status := server.InitializeStatus(StorageConfig{DataDir: store.Dir(), DerivedDir: store.derivedDir, VectorDir: store.vectorDir}, EmbeddingConfig{Provider: "openai-compatible", Model: "model-beta", ModelDigest: "revision-2", Dimensions: 3}, true)
	if status.State != SystemActionRequired || len(status.Incidents) != 1 || server.semanticEnabled() {
		t.Fatalf("status=%+v", status)
	}
	payload, _ := json.Marshal(vectorRebuildRequest{IncidentID: status.Incidents[0].IncidentID, Confirm: true})
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/vectors/rebuild", bytes.NewReader(payload))
	request.Header.Set("X-Admin-Token", "admin")
	recorder := httptest.NewRecorder()
	server.adminVectorRebuild(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	resolved := server.SystemStatus()
	if resolved.State != SystemReady || len(resolved.Incidents) != 0 || store.VectorStore.Manifest().ModelName != "model-beta" || !server.semanticEnabled() {
		t.Fatalf("resolved=%+v manifest=%+v", resolved, store.VectorStore.Manifest())
	}
}

func TestSystemStatusExposesSanitizedConfigAndLiveCounts(t *testing.T) {
	store := newTestStore(t)
	store.memories["active"] = persistentFixture("active", "active", "safe content")
	store.memories["inactive"] = MemoryRow{MemoryID: "inactive", Lifecycle: "RETIRED", SecretLike: true, InstructionLike: true}
	storage := StorageConfig{RootDir: "/srv/mindmory", DataDir: "/srv/mindmory/canonical", DerivedDir: "/srv/mindmory/derived", VectorDir: "/srv/mindmory/derived/vectors", SnapshotDir: "/srv/mindmory/snapshots", ExportDir: "/srv/mindmory/export"}
	embedding := EmbeddingConfig{Provider: "openai-compatible", Endpoint: "https://embeddings.example.com", Path: "/v1/embeddings", Model: "model-any", ModelDigest: "revision-1", APIKey: "must-never-be-visible", Dimensions: 3, Timeout: time.Minute, AllowRemote: true}
	semantic := false
	server := &Server{Store: store, SemanticSearch: &semantic}
	server.InitializeStatus(storage, embedding, false)
	status := server.SystemStatus()
	if status.SoftwareVersion == "" || status.Configuration.Storage.CanonicalDir != storage.DataDir || status.Configuration.Embedding.Model != embedding.Model || status.Statistics.Memories != 2 || status.Statistics.ActiveMemories != 1 || status.Statistics.SecretLikeMemories != 1 {
		t.Fatalf("status=%+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(embedding.APIKey)) || bytes.Contains(raw, []byte("cursor")) || bytes.Contains(raw, []byte("token")) {
		t.Fatalf("secret-bearing status: %s", raw)
	}
}

func TestSystemStatusEndpointIsGETOnly(t *testing.T) {
	store := newTestStore(t)
	semantic := false
	server := &Server{Store: store, AdminToken: "admin", Log: testLogger(), SemanticSearch: &semantic}
	server.InitializeStatus(StorageConfig{DataDir: store.Dir()}, EmbeddingConfig{Provider: "disabled"}, false)
	request := httptest.NewRequest(http.MethodPost, "/v1/system/status", nil)
	request.Header.Set("X-Admin-Token", "admin")
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestStartupDetectsAnyDeclaredModelIdentityChange(t *testing.T) {
	store := statusFixture(t, "model-alpha", "revision-1", 3)
	configured := EmbeddingConfig{Provider: "openai-compatible", Model: "model-beta", ModelDigest: "revision-2", Dimensions: 3}
	status := assessSystemStatus(store, configured, true, time.Unix(1, 0).UTC())
	if status.State != SystemActionRequired || status.Embeddings != "MODEL_MISMATCH" || status.MCP != "QUARANTINED" {
		t.Fatalf("status=%+v", status)
	}
	if len(status.Incidents) != 1 || status.Incidents[0].Code != "EMBEDDING_MODEL_MISMATCH" {
		t.Fatalf("incidents=%+v", status.Incidents)
	}
	incident := status.Incidents[0]
	if !strings.Contains(incident.OperatorMessage, "model-alpha") || !strings.Contains(incident.OperatorMessage, "model-beta") || !strings.Contains(strings.Join(incident.CopyPasteCommands, "\n"), incident.IncidentID) {
		t.Fatalf("incident=%+v", incident)
	}
}

func TestStartupDetectsRevisionChangeWithSameModelAndDimensions(t *testing.T) {
	store := statusFixture(t, "model-any", "revision-1", 3)
	configured := EmbeddingConfig{Provider: "openai-compatible", Model: "model-any", ModelDigest: "revision-2", Dimensions: 3}
	status := assessSystemStatus(store, configured, true, time.Now())
	if status.State != SystemActionRequired || status.Incidents[0].Code != "EMBEDDING_MODEL_MISMATCH" {
		t.Fatalf("status=%+v", status)
	}
}

func TestStartupAcceptsMatchingIdentityAndDisabledProvider(t *testing.T) {
	store := statusFixture(t, "model-any", "revision-1", 3)
	matching := assessSystemStatus(store, EmbeddingConfig{Provider: "openai-compatible", Model: "model-any", ModelDigest: "revision-1", Dimensions: 3}, true, time.Now())
	if matching.State != SystemReady || len(matching.Incidents) != 0 {
		t.Fatalf("matching=%+v", matching)
	}
	disabled := assessSystemStatus(store, EmbeddingConfig{Provider: "disabled"}, false, time.Now())
	if disabled.State != SystemReady || disabled.Embeddings != "DISABLED" || len(disabled.Incidents) != 0 {
		t.Fatalf("disabled=%+v", disabled)
	}
}
