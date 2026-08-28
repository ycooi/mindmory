package vectorstore

import (
	"container/heap"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

type scoredHeap []Candidate

func (h scoredHeap) Len() int           { return len(h) }
func (h scoredHeap) Less(i, j int) bool { return h[i].Score < h[j].Score }
func (h scoredHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *scoredHeap) Push(x any)        { *h = append(*h, x.(Candidate)) }
func (h *scoredHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

func (s *Store) Search(ctx context.Context, query []float32, options SearchOptions) ([]Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, fmt.Errorf("vector store closed")
	}
	if options.Limit <= 0 || len(s.refs) == 0 {
		return nil, nil
	}
	encoded, err := normalizeEncode(query, int(s.header.Dimensions))
	if err != nil {
		return nil, err
	}
	q := make([]float32, len(query))
	for i := range q {
		q[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
	}
	top := &scoredHeap{}
	heap.Init(top)
	for i, ref := range s.refs {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if options.AllowedOrdinal != nil && !options.AllowedOrdinal(uint64(i)) {
			continue
		}
		record, err := s.readRecordLocked(uint64(i))
		if err != nil {
			return nil, err
		}
		var score float64
		for d := range q {
			score += float64(q[d]) * float64(math.Float32frombits(binary.LittleEndian.Uint32(record[d*4:])))
		}
		if score < options.MinimumScore {
			continue
		}
		candidate := Candidate{Ordinal: uint64(i), SourceType: ref.SourceType, SourceID: ref.SourceID, EmbeddingInputHash: ref.EmbeddingInputHash, Score: score}
		if top.Len() < options.Limit {
			heap.Push(top, candidate)
		} else if score > (*top)[0].Score {
			heap.Pop(top)
			heap.Push(top, candidate)
		}
	}
	out := make([]Candidate, top.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(top).(Candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

func (s *Store) readRecordLocked(ordinal uint64) ([]byte, error) {
	offset, err := VectorOffset(s.header, ordinal)
	if err != nil {
		return nil, err
	}
	size := int(s.header.RecordSize)
	if s.mapped != nil && len(s.mapped.bytes()) > 0 {
		b := s.mapped.bytes()
		end := int(offset) + size
		if int(offset) < 0 || end > len(b) {
			return nil, fmt.Errorf("%w: record bounds", ErrCorrupt)
		}
		return b[int(offset):end], nil
	}
	b := make([]byte, size)
	_, err = s.file.ReadAt(b, offset)
	return b, err
}
