package lite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func validLiteEnv() map[string]string {
	return map[string]string{"MINDMORY_OWNER": "owner", "MINDMORY_CURSOR_SIGNING_KEY": strings.Repeat("k", 32), "MINDMORY_MCP_CLIENT_TOKENS_JSON": `{"client":{"token":"token-at-least-twenty-four-characters","capabilities":["CONTEXT_READ"]}}`}
}
func loadTestEnv(values map[string]string) (EnvConfig, error) {
	return LoadEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
}

func TestLiteRuntimeConfigDefaultsAndStorageRoot(t *testing.T) {
	values := validLiteEnv()
	values["MINDMORY_ROOT_DIR"] = "/opt/mindmory"
	cfg, err := loadTestEnv(values)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.DataDir != "/opt/mindmory/var/data" || cfg.Storage.DerivedDir != "/opt/mindmory/var/derived" || cfg.Storage.VectorDir != "/opt/mindmory/var/derived/vectors" || cfg.Storage.SnapshotDir != "/opt/mindmory/var/data/snapshots" || cfg.Storage.ExportDir != "/opt/mindmory/var/export" {
		t.Fatalf("storage=%+v", cfg.Storage)
	}
	if cfg.Embedding.Provider != "ollama" || cfg.Embedding.Endpoint != "http://127.0.0.1:11434" || cfg.Embedding.Path != "/api/embed" {
		t.Fatalf("embedding=%+v", cfg.Embedding)
	}
}

func TestEmbeddingProviderDisabled(t *testing.T) {
	values := validLiteEnv()
	values["MINDMORY_EMBED_PROVIDER"] = "disabled"
	values["MINDMORY_SEMANTIC_SEARCH"] = "1"
	cfg, err := loadTestEnv(values)
	if err != nil {
		t.Fatal(err)
	}
	embedder, err := NewConfiguredEmbedder(cfg.Embedding)
	if err != nil || embedder != nil {
		t.Fatalf("embedder=%T err=%v", embedder, err)
	}
	if !cfg.SemanticEnabled {
		t.Fatal("semantic flag not parsed")
	}
}

func TestRemoteEmbeddingRequiresExplicitSecureConfiguration(t *testing.T) {
	base := validLiteEnv()
	base["MINDMORY_EMBED_PROVIDER"] = "openai-compatible"
	base["MINDMORY_EMBED_ENDPOINT"] = "https://embeddings.example.com"
	base["MINDMORY_EMBED_MODEL_DIGEST"] = "sha256:model"
	if _, err := loadTestEnv(base); err == nil || !strings.Contains(err.Error(), "ALLOW_REMOTE") {
		t.Fatalf("missing remote opt-in err=%v", err)
	}
	base["MINDMORY_EMBED_ALLOW_REMOTE"] = "1"
	if _, err := loadTestEnv(base); err == nil || !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("missing key err=%v", err)
	}
	base["MINDMORY_EMBED_API_KEY"] = "secret"
	base["MINDMORY_EMBED_ENDPOINT"] = "http://embeddings.example.com"
	if _, err := loadTestEnv(base); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure remote err=%v", err)
	}
	base["MINDMORY_EMBED_ENDPOINT"] = "https://embeddings.example.com"
	cfg, err := loadTestEnv(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Path != "/v1/embeddings" {
		t.Fatalf("path=%q", cfg.Embedding.Path)
	}
}

func TestOpenAICompatibleEmbedderRequestAndOrdering(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		var request compatibleEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "remote-model" || request.Dimensions != 3 || len(request.Input) != 2 {
			t.Errorf("request=%+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 1, "embedding": []float32{0, 1, 0}}, {"index": 0, "embedding": []float32{1, 0, 0}}}})
	}))
	defer server.Close()
	embedder := &OpenAICompatibleEmbedder{Endpoint: server.URL, Path: "/v1/embeddings", Model: "remote-model", Digest: "sha256:model", APIKey: "key", Dimensions: 3, Client: server.Client()}
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer key" || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("auth=%q vectors=%v", authorization, vectors)
	}
}

func TestStorageRejectsDerivedCanonicalCollision(t *testing.T) {
	values := validLiteEnv()
	values["MINDMORY_DATA_DIR"] = "/tmp/memory"
	values["MINDMORY_DERIVED_DIR"] = "/tmp/memory"
	if _, err := loadTestEnv(values); err == nil {
		t.Fatal("canonical/derived collision accepted")
	}
}

func TestStorageRejectsDerivedNestedInCanonical(t *testing.T) {
	values := validLiteEnv()
	values["MINDMORY_DATA_DIR"] = "/tmp/memory"
	values["MINDMORY_DERIVED_DIR"] = "/tmp/memory/derived"
	if _, err := loadTestEnv(values); err == nil {
		t.Fatal("derived directory nested in canonical data was accepted")
	}
}

func TestRelativeStorageOverridesResolveFromRoot(t *testing.T) {
	values := validLiteEnv()
	values["MINDMORY_ROOT_DIR"] = "/opt/mindmory"
	values["MINDMORY_DATA_DIR"] = "canonical"
	values["MINDMORY_VECTOR_DIR"] = "semantic"
	cfg, err := loadTestEnv(values)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.DataDir != "/opt/mindmory/canonical" || cfg.Storage.VectorDir != "/opt/mindmory/semantic" {
		t.Fatalf("storage=%+v", cfg.Storage)
	}
}

func TestConfiguredStorageLayoutControlsDerivedAndSnapshots(t *testing.T) {
	root := t.TempDir()
	storage := StorageConfig{DataDir: filepath.Join(root, "canonical"), DerivedDir: filepath.Join(root, "derived"), VectorDir: filepath.Join(root, "semantic"), SnapshotDir: filepath.Join(root, "backups")}
	store, err := OpenConfigured(storage, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.derivedDir != storage.DerivedDir || store.vectorDir != storage.VectorDir || store.snapshotDir != storage.SnapshotDir {
		t.Fatalf("store paths: derived=%q vector=%q snapshot=%q", store.derivedDir, store.vectorDir, store.snapshotDir)
	}
	snapshot, err := store.CreateSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(storage.SnapshotDir, snapshot.Path)
	if err != nil || strings.HasPrefix(relative, "..") {
		t.Fatalf("snapshot path=%q root=%q", snapshot.Path, storage.SnapshotDir)
	}
}
