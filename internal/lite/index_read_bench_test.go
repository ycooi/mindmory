package lite

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkSQLiteSearchAndRetrieve10K measures the intended production read
// path: FTS narrows candidates and SQLite returns complete records. Canonical
// JSONL and the Store's in-memory maps are not involved in the timed loop.
func BenchmarkSQLiteSearchAndRetrieve10K(b *testing.B) {
	index, err := OpenMemoryIndex(filepath.Join(b.TempDir(), "index.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = index.Close() })
	rows := make([]MemoryRow, 10_000)
	for i := range rows {
		content := fmt.Sprintf("project preference record %05d", i)
		if i == 7_777 {
			content += " durable sqlite retrieval sentinel"
		}
		rows[i] = MemoryRow{MemoryID: fmt.Sprintf("memory-%05d", i), Kind: "USER_PREFERENCE",
			Subject: content, Content: content, ContentHash: hashContent(content), Lifecycle: "ACTIVE",
			EpistemicStatus: "USER_ACCEPTED", Confidence: 1, Importance: .6, Sensitivity: "NORMAL",
			ScopeType: "GLOBAL", Activation: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	}
	if err := index.RebuildFrom(rows); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ids, err := index.SearchCandidates("durable sqlite retrieval sentinel", "", nil, 10)
		if err != nil || len(ids) != 1 {
			b.Fatalf("search returned %d ids: %v", len(ids), err)
		}
		if _, err := index.LoadMemories(ids); err != nil {
			b.Fatal(err)
		}
	}
}
