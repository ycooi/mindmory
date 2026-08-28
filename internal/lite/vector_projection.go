package lite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// EmbeddingInput is a deliberately versioned semantic surface.
func EmbeddingInput(row MemoryRow) string {
	return strings.TrimSpace(row.Subject) + "\n" + strings.TrimSpace(row.Content)
}

func EmbeddingInputHash(row MemoryRow) string {
	sum := sha256.Sum256([]byte("embedding-input-v1\x00" + EmbeddingInput(row)))
	return fmt.Sprintf("sha256:%x", sum[:])
}

// loadVectorProjection loads disposable vectors. Any malformed or stale row
// is ignored: semantic search may lose recall until a rebuild, but canonical
// memory authority and readiness never depend on derived data.
func (s *Store) loadVectorProjection() error {
	lines, err := readJSONL(s.path("vectors"))
	if err != nil {
		return nil
	}
	loaded := make(map[string]VectorProjectionRow, len(lines))
	for _, line := range lines {
		var row VectorProjectionRow
		if json.Unmarshal(line, &row) != nil || row.MemoryID == "" || row.ContentHash == "" ||
			row.Model == "" || row.Dimensions <= 0 || row.Dimensions != len(row.Vector) {
			continue
		}
		memory, ok := s.memories[row.MemoryID]
		if !ok || memory.ContentHash != row.ContentHash {
			continue
		}
		loaded[row.MemoryID] = row
	}
	s.vectorRows = loaded
	return nil
}

func (s *Store) flushVectorProjectionLocked() error {
	ids := make([]string, 0, len(s.vectorRows))
	for id := range s.vectorRows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var lines []byte
	for _, id := range ids {
		line, err := json.Marshal(s.vectorRows[id])
		if err != nil {
			return err
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	return s.flushKindLocked("vectors", lines)
}

func (s *Store) vectorForMemory(memoryID, contentHash string) ([]float32, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.vectorRows[memoryID]
	if !ok || row.ContentHash != contentHash || len(row.Vector) == 0 {
		return nil, false
	}
	return append([]float32(nil), row.Vector...), true
}
