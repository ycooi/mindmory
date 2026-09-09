package lite

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeBM25PreservesLargeScoreOrdering(t *testing.T) {
	strong := normalizeBM25(-30)
	weak := normalizeBM25(-20)
	if strong <= weak || strong >= 1 || weak <= 0 {
		t.Fatalf("strong=%f weak=%f", strong, weak)
	}
}

func TestMixedAlphaNumeric(t *testing.T) {
	for value, want := range map[string]bool{
		"nova000022": true, "PR123": true, "123": false, "policy": false,
		"8th": false, "21st": false,
	} {
		if got := mixedAlphaNumeric(value); got != want {
			t.Errorf("mixedAlphaNumeric(%q)=%t want %t", value, got, want)
		}
	}
}

func TestBM25CandidatesPrioritizeRareIdentifierAcrossInflection(t *testing.T) {
	index, err := OpenMemoryIndex(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	rows, queries := generateScaleScenario(240, 50)
	if err := index.RebuildFrom(rows); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		if query.expectNone {
			continue
		}
		candidates, err := index.SearchRankedCandidates(query.question, "project-a", nil, 10)
		if err != nil {
			t.Fatalf("%s search: %v", query.id, err)
		}
		if len(candidates) == 0 || candidates[0].MemoryID != query.expectedID {
			t.Fatalf("%s first=%+v want=%s", query.id, candidates, query.expectedID)
		}
		if !candidates[0].BM25 || candidates[0].Relevance <= 0 {
			t.Fatalf("%s did not carry BM25 relevance: %+v", query.id, candidates[0])
		}
	}
}

func TestBM25HardNegativeDoesNotTriggerCandidates(t *testing.T) {
	index, err := OpenMemoryIndex(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	rows, _ := generateScaleScenario(240, 50)
	if err := index.RebuildFrom(rows); err != nil {
		t.Fatal(err)
	}
	candidates, err := index.SearchRankedCandidates("unrelated quasar propulsion absence000741", "project-a", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("hard negative returned candidates: %+v", candidates)
	}
}

func TestBM25ProjectionTracksUpsertAndRemove(t *testing.T) {
	index, err := OpenMemoryIndex(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	row := MemoryRow{MemoryID: "bm25-lifecycle", Kind: "PROJECT_DECISION", Subject: "rare aurora sentinel",
		Content: "shipping checks completed", ContentHash: hashContent("shipping checks completed"), Lifecycle: "ACTIVE",
		Sensitivity: "NORMAL", ScopeType: "GLOBAL"}
	if err := index.Upsert(row); err != nil {
		t.Fatal(err)
	}
	ids, err := index.SearchCandidates("aurora shipped after checking", "", nil, 10)
	if err != nil || len(ids) != 1 || ids[0] != row.MemoryID {
		t.Fatalf("upsert search ids=%v err=%v", ids, err)
	}
	if err := index.Remove(row.MemoryID); err != nil {
		t.Fatal(err)
	}
	ids, err = index.SearchCandidates("aurora shipped after checking", "", nil, 10)
	if err != nil || len(ids) != 0 {
		t.Fatalf("removed memory remained indexed: ids=%v err=%v", ids, err)
	}
}

func TestSearchRemainsResponsiveDuringIndexRebuild(t *testing.T) {
	index, err := OpenMemoryIndex(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	rows, _ := generateScaleScenario(10_000, 50)
	if err := index.RebuildFrom(rows); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- index.RebuildFrom(rows) }()
	// Give the writer enough time to enter its transaction. WAL readers must
	// still see the previous committed projection on another pooled connection.
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	ids, err := index.SearchCandidates("artifact nova000001 cobalt protocol", "project-a", nil, 10)
	latency := time.Since(started)
	if err != nil || len(ids) == 0 || ids[0] != "scale-000001" {
		t.Fatalf("concurrent search ids=%v err=%v", ids, err)
	}
	if latency > 500*time.Millisecond {
		t.Fatalf("search blocked behind index rebuild for %s", latency)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("foreground search during rebuild completed in %s", latency)
}
