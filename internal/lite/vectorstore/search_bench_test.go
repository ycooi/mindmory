package vectorstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"
)

func benchmarkVectors(count, dimensions int) map[string][]float32 {
	out := make(map[string][]float32, count)
	for i := 0; i < count; i++ {
		vector := make([]float32, dimensions)
		vector[i%dimensions] = 1
		vector[(i*17+3)%dimensions] = .25
		out[fmt.Sprintf("memory-%06d", i)] = vector
	}
	return out
}

func BenchmarkPersistentFlatSearch1Kx512(b *testing.B) {
	vectors := benchmarkVectors(1000, 512)
	store, err := Create(b.TempDir(), GenerationSpec{ModelName: "benchmark", Dimensions: 512})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	for id, vector := range vectors {
		if _, err := store.Append("MEMORY", id, "hash:"+id, vector); err != nil {
			b.Fatal(err)
		}
	}
	query := vectors["memory-000321"]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Search(context.Background(), query, SearchOptions{Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLegacyRAMSortAll1Kx512(b *testing.B) {
	vectors := benchmarkVectors(1000, 512)
	query := vectors["memory-000321"]
	type scored struct {
		id    string
		score float64
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		results := make([]scored, 0, len(vectors))
		for id, vector := range vectors {
			var dot float64
			for i, value := range query {
				dot += float64(value) * float64(vector[i])
			}
			results = append(results, scored{id, dot})
		}
		sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
		_ = math.Float64bits(results[0].score)
	}
}
