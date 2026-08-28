// Package vectorstore implements Mindmory Lite's disposable, persistent
// semantic projection. Canonical memory authority remains outside this package.
package vectorstore

import (
	"context"
	"errors"
)

const (
	FormatVersion        = 1
	HeaderSize           = 64
	InputVersion         = 1
	NormalizationVersion = 1
)

type DType uint8

const (
	DTypeFloat32LE DType = 1
	DTypeFloat16LE DType = 2 // reserved; version 1 rejects it
)

type GenerationSpec struct {
	Name                 string
	ModelName            string
	ModelDigest          string
	Dimensions           int
	DType                DType
	NormalizationVersion int
	InputVersion         int
	FormatVersion        int
}

type Ref struct {
	Generation         string `json:"generation"`
	Ordinal            uint64 `json:"ordinal"`
	SourceType         string `json:"source_type"`
	SourceID           string `json:"source_id"`
	EmbeddingInputHash string `json:"embedding_input_hash"`
	VectorChecksum     string `json:"vector_checksum"`
	CreatedAt          string `json:"created_at"`
}

type Candidate struct {
	Ordinal            uint64
	SourceType         string
	SourceID           string
	EmbeddingInputHash string
	Score              float64
}

type SearchOptions struct {
	Limit          int
	MinimumScore   float64
	AllowedOrdinal func(uint64) bool
}

var ErrCorrupt = errors.New("vector generation corrupt")

type Searcher interface {
	Search(context.Context, []float32, SearchOptions) ([]Candidate, error)
}
