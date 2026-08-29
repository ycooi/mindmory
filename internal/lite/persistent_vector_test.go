package lite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

type countingEmbedder struct {
	model, digest string
	value         []float32
	calls, inputs int
	fail          bool
}

func (e *countingEmbedder) ModelName() string   { return e.model }
func (e *countingEmbedder) ModelDigest() string { return e.digest }
func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.inputs += len(texts)
	if e.fail {
		return nil, errors.New("offline")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = append([]float32(nil), e.value...)
	}
	return out, nil
}

func persistentFixture(id, subject, content string) MemoryRow {
	return MemoryRow{MemoryID: id, Kind: string(domain.KindProjectDecision), Subject: subject, Content: content, ContentHash: hashContent(content), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1, Importance: .7, Sensitivity: "NORMAL", ScopeType: "GLOBAL", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func TestPersistentVectorsReuseAfterReopen(t *testing.T) {
	t.Setenv("MINDMORY_DERIVED_DIR", "")
	t.Setenv("MINDMORY_VECTOR_DIR", "")
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	row := persistentFixture("persistent-a", "durable semantic beacon", "survives process restart")
	if err = store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	build := &countingEmbedder{model: "fixture", digest: "sha256:model", value: []float32{1, 0, 0}}
	summary, err := store.SyncVectors(context.Background(), build, VectorSyncOptions{})
	if err != nil || summary.Embedded != 1 || build.inputs != 1 {
		t.Fatalf("build=%+v calls=%d inputs=%d err=%v", summary, build.calls, build.inputs, err)
	}
	generation := store.VectorStore.Generation()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.EnableLowRAMExperiment(); err != nil {
		t.Fatal(err)
	}
	if store.VectorStore == nil || store.VectorStore.Generation() != generation {
		t.Fatalf("generation not reused: %+v", store.VectorStatus())
	}
	if status := store.VectorStatus(); status.CurrentActiveMemories != 1 || status.Missing != 0 || status.Stale != 0 || status.Tombstoned != 0 {
		t.Fatalf("low-RAM vector status: %+v", status)
	}
	offline := &countingEmbedder{model: "fixture", digest: "sha256:model", fail: true}
	summary, err = store.SyncVectors(context.Background(), offline, VectorSyncOptions{})
	if err != nil || offline.calls != 0 || summary.AlreadyCurrent != 1 {
		t.Fatalf("reopen sync=%+v calls=%d err=%v", summary, offline.calls, err)
	}
	semantic := true
	query := &countingEmbedder{model: "fixture", digest: "sha256:model", value: []float32{1, 0, 0}}
	server := &Server{Store: store, Aliases: retrieval.NewAliasExpander(nil), Log: testLogger(), SemanticSearch: &semantic, Embedder: query, queryCache: newQueryVectorCache(8)}
	scope := retrieval.SessionScope{SessionID: "session", ProjectKey: ""}
	request := retrieval.SearchRequest{SessionID: "session", Query: "meaning without lexical overlap", Limit: 5, Mode: retrieval.SearchSemantic}
	hits, err := server.searchMemories(context.Background(), scope, request, false)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != row.MemoryID {
		t.Fatalf("semantic hits=%+v err=%v", hits, err)
	}
	_, err = server.searchMemories(context.Background(), scope, request, false)
	if err != nil || query.calls != 1 {
		t.Fatalf("query cache calls=%d err=%v", query.calls, err)
	}
}

func TestAnyModelIdentityChangeRebuildsAllAndFailureKeepsOldGeneration(t *testing.T) {
	store := newTestStore(t)
	for _, row := range []MemoryRow{
		persistentFixture("change-a", "first", "first memory"),
		persistentFixture("change-b", "second", "second memory"),
	} {
		if err := store.insertMemoryFixture(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	first := &countingEmbedder{model: "model-alpha", digest: "revision-1", value: []float32{1, 0, 0}}
	if summary, err := store.SyncVectors(context.Background(), first, VectorSyncOptions{}); err != nil || summary.Embedded != 2 {
		t.Fatalf("first summary=%+v err=%v", summary, err)
	}
	oldGeneration := store.VectorStore.Generation()
	failingChange := &countingEmbedder{model: "model-beta", digest: "revision-2", value: []float32{0, 1, 0}, fail: true}
	if _, err := store.SyncVectors(context.Background(), failingChange, VectorSyncOptions{}); err == nil {
		t.Fatal("changed model failure accepted")
	}
	if store.VectorStore.Generation() != oldGeneration {
		t.Fatal("failed rebuild replaced the old generation")
	}
	changed := &countingEmbedder{model: "model-beta", digest: "revision-2", value: []float32{0, 1, 0}}
	summary, err := store.SyncVectors(context.Background(), changed, VectorSyncOptions{})
	if err != nil || summary.Embedded != 2 || changed.inputs != 2 {
		t.Fatalf("changed summary=%+v inputs=%d err=%v", summary, changed.inputs, err)
	}
	manifest := store.VectorStore.Manifest()
	if manifest.ModelName != "model-beta" || manifest.ModelDigest != "revision-2" || manifest.Generation == oldGeneration || manifest.State != "READY" {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestSemanticFallbackAndLexicalModesAvoidEmbedder(t *testing.T) {
	store := newTestStore(t)
	row := persistentFixture("lexical-a", "strong lexical beacon", "exact searchable content")
	if err := store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	build := &countingEmbedder{model: "fixture", value: []float32{1, 0, 0}}
	if _, err := store.SyncVectors(context.Background(), build, VectorSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	semantic := true
	query := &countingEmbedder{model: "fixture", value: []float32{1, 0, 0}}
	server := &Server{Store: store, Aliases: retrieval.NewAliasExpander(nil), Log: testLogger(), SemanticSearch: &semantic, Embedder: query, queryCache: newQueryVectorCache(8)}
	scope := retrieval.SessionScope{SessionID: "session"}
	for _, mode := range []retrieval.SearchMode{retrieval.SearchLexical, retrieval.SearchSemanticFallback} {
		hits, err := server.searchMemories(context.Background(), scope, retrieval.SearchRequest{SessionID: "session", Query: "strong lexical beacon", Mode: mode}, false)
		if err != nil || len(hits) == 0 {
			t.Fatalf("mode=%s hits=%+v err=%v", mode, hits, err)
		}
	}
	if query.calls != 0 {
		t.Fatalf("lexical paths embedded %d times", query.calls)
	}
}

func TestDisabledEmbeddingProviderLeavesLexicalSearchHealthy(t *testing.T) {
	store := newTestStore(t)
	row := persistentFixture("lexical-disabled", "offline lexical beacon", "search works without an embedding service")
	if err := store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	semantic := true
	server := &Server{Store: store, Aliases: retrieval.NewAliasExpander(nil), Log: testLogger(), SemanticSearch: &semantic, Embedder: nil}
	hits, err := server.searchMemories(context.Background(), retrieval.SessionScope{SessionID: "session"}, retrieval.SearchRequest{SessionID: "session", Query: "offline lexical beacon", Mode: retrieval.SearchSemanticFallback}, false)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != row.MemoryID {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestSecretLikeQueriesAreNotCached(t *testing.T) {
	cache := newQueryVectorCache(2)
	cache.put("g", "password is hunter2", []float32{1})
	if _, ok := cache.get("g", "password is hunter2"); ok {
		t.Fatal("secret-like query cached")
	}
}

func TestQueryVectorCacheConcurrentAccess(t *testing.T) {
	cache := newQueryVectorCache(8)
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			cache.put("generation", "safe repeated query", []float32{1, 2, 3})
			vector, ok := cache.get("generation", "safe repeated query")
			if !ok || len(vector) != 3 {
				t.Errorf("cache miss")
			}
			if len(vector) > 0 {
				vector[0] = 99
			}
		}()
	}
	wait.Wait()
	vector, ok := cache.get("generation", "safe repeated query")
	if !ok || vector[0] != 1 {
		t.Fatalf("cache vector mutated: %v", vector)
	}
}
