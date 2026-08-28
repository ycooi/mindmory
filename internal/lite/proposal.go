package lite

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"sync"
	"time"

	domain "mindmory.local/core/internal/memory"
)

// FindProposalByHash returns an existing proposal for the exact request hash.
func (s *Store) FindProposalByHash(ctx context.Context, requestHash string) (domain.Proposal, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.proposalsHash[requestHash]
	if !ok {
		return domain.Proposal{}, false, nil
	}
	return s.proposals[id], true, nil
}

// StageProposal records a PENDING proposal with its governance reason.
func (s *Store) StageProposal(ctx context.Context, proposal domain.Proposal, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal.Status = domain.ProposalPending
	proposal.ReasonCode = reason
	proposal.CreatedAt = time.Now().UTC()
	s.proposals[proposal.ID] = proposal
	if proposal.RequestHash != "" {
		s.proposalsHash[proposal.RequestHash] = proposal.ID
	}
	return s.flushKindLocked("proposals", s.proposalsJSONL())
}

// ListProposals returns proposals filtered by status ("", PENDING, APPLIED,
// REJECTED), newest first.
func (s *Store) ListProposals(ctx context.Context, status string, limit int) ([]domain.Proposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Proposal
	for _, p := range s.proposals {
		if status != "" && string(p.Status) != status {
			continue
		}
		out = append(out, p)
	}
	// newest first by CreatedAt desc, then id asc for stability.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetProposal returns one proposal by id.
func (s *Store) GetProposal(ctx context.Context, proposalID string) (domain.Proposal, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proposals[proposalID]
	if !ok {
		return domain.Proposal{}, false, nil
	}
	return p, true, nil
}

// RejectProposal marks a proposal REJECTED (never applied).
func (s *Store) RejectProposal(ctx context.Context, proposalID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal, ok := s.proposals[proposalID]
	if !ok {
		return errNoRows
	}
	now := time.Now().UTC()
	proposal.Status = domain.ProposalRejected
	proposal.ReasonCode = reason
	proposal.ResolvedAt = &now
	s.proposals[proposalID] = proposal
	return s.flushKindLocked("proposals", s.proposalsJSONL())
}

// ResolveProposalApplied marks a proposal APPLIED with its memory id.
func (s *Store) ResolveProposalApplied(ctx context.Context, proposalID, memoryID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal, ok := s.proposals[proposalID]
	if !ok {
		return errNoRows
	}
	now := time.Now().UTC()
	proposal.Status = domain.ProposalApplied
	proposal.ReasonCode = reason
	proposal.AppliedMemoryID = memoryID
	proposal.ResolvedAt = &now
	s.proposals[proposalID] = proposal
	return s.flushKindLocked("proposals", s.proposalsJSONL())
}

// --- identity ---

var uuidMu sync.Mutex

// newID emits an RFC 9562 UUIDv7 string (server-owned identifier).
func newID() string {
	return newUUIDv7()
}

func newUUIDv7() string {
	uuidMu.Lock()
	defer uuidMu.Unlock()
	var value [16]byte
	millis := uint64(time.Now().UTC().UnixMilli())
	value[0] = byte(millis >> 40)
	value[1] = byte(millis >> 32)
	value[2] = byte(millis >> 24)
	value[3] = byte(millis >> 16)
	value[4] = byte(millis >> 8)
	value[5] = byte(millis)
	if _, err := rand.Read(value[6:]); err != nil {
		panic("secure UUID randomness unavailable")
	}
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
