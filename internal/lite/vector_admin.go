package lite

import (
	"context"
	"mindmory.local/core/internal/lite/vectorstore"
)

type VectorStatus struct {
	Generation            string `json:"generation,omitempty"`
	State                 string `json:"state"`
	Vectors               int    `json:"vectors"`
	CurrentActiveMemories int    `json:"current_active_memories"`
	Missing               int    `json:"missing"`
	Stale                 int    `json:"stale"`
	Tombstoned            int    `json:"tombstoned"`
	Dimensions            int    `json:"dimensions"`
	DType                 string `json:"dtype,omitempty"`
	ModelName             string `json:"model_name,omitempty"`
	ModelDigest           string `json:"model_digest,omitempty"`
	EmbeddingInputVersion int    `json:"embedding_input_version,omitempty"`
	NormalizationVersion  int    `json:"normalization_version,omitempty"`
}

func (s *Store) VectorStatus() VectorStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := VectorStatus{State: "DEGRADED"}
	for _, row := range s.memories {
		if row.Lifecycle == "ACTIVE" && row.Sensitivity == "NORMAL" && !row.SecretLike && !row.InstructionLike {
			status.CurrentActiveMemories++
		}
	}
	if s.VectorStore == nil {
		status.Missing = status.CurrentActiveMemories
		return status
	}
	manifest := s.VectorStore.Manifest()
	status.Generation = manifest.Generation
	status.State = "ACTIVE"
	status.Vectors = s.VectorStore.Size()
	status.Dimensions = manifest.Dimensions
	status.DType = manifest.DType
	status.ModelName = manifest.ModelName
	status.ModelDigest = manifest.ModelDigest
	status.EmbeddingInputVersion = manifest.EmbeddingInputVersion
	status.NormalizationVersion = manifest.NormalizationVersion
	for id, row := range s.memories {
		if row.Lifecycle != "ACTIVE" || row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike {
			continue
		}
		if !s.VectorStore.Has(id, EmbeddingInputHash(row)) {
			status.Missing++
		}
	}
	for _, ref := range s.VectorStore.Refs() {
		row, ok := s.memories[ref.SourceID]
		if !ok || row.Lifecycle != "ACTIVE" || row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike {
			status.Tombstoned++
		} else if ref.EmbeddingInputHash != EmbeddingInputHash(row) {
			status.Stale++
		}
	}
	return status
}

func (s *Store) VerifyVectors(ctx context.Context, full bool) error {
	s.mu.RLock()
	store := s.VectorStore
	s.mu.RUnlock()
	if store == nil {
		return vectorstore.ErrCorrupt
	}
	return store.Verify(ctx, full)
}
