package lite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	domain "mindmory.local/core/internal/memory"
)

type MutationCommit struct {
	Mutation              domain.MutationKind
	ProposalID            string
	RequestHash           string
	ResolutionReason      string
	ReviewAuthorization   string
	NewMemory             *MemoryRow
	UpdatedMemory         *MemoryRow
	TargetMemoryID        string
	ExpectedTargetVersion int64
	TargetLifecycle       domain.Lifecycle
	Evidence              MessageEvidenceRow
	ContinuityKind        string
	RelatedMemoryID       string
	ProjectKey            string
	Sensitivity           string
	TraceID               string
}

type MutationCommitResult struct {
	MemoryID        string
	Revision        int64
	EventID         string
	ProjectionError error
}

// MemoryMutationEvent is canonical mutation truth. Once this complete record
// is appended and fsynced, the mutation exists; snapshots and indexes are
// rebuildable projections.
type MemoryMutationEvent struct {
	EventVersion          int                 `json:"event_version"`
	EventSeq              int64               `json:"event_seq"`
	EventID               string              `json:"event_id"`
	RequestHash           string              `json:"request_hash"`
	Mutation              domain.MutationKind `json:"mutation"`
	ProposalID            string              `json:"proposal_id"`
	Proposal              *domain.Proposal    `json:"proposal,omitempty"`
	ResolutionReason      string              `json:"resolution_reason"`
	ReviewAuthorization   string              `json:"review_authorization,omitempty"`
	NewMemory             *MemoryRow          `json:"new_memory,omitempty"`
	UpdatedMemory         *MemoryRow          `json:"updated_memory,omitempty"`
	TargetMemoryID        string              `json:"target_memory_id,omitempty"`
	ExpectedTargetVersion int64               `json:"expected_target_version,omitempty"`
	TargetLifecycle       domain.Lifecycle    `json:"target_lifecycle,omitempty"`
	Evidence              MessageEvidenceRow  `json:"evidence"`
	Continuity            ContinuityRow       `json:"continuity"`
	PreviousEventHash     string              `json:"previous_event_hash,omitempty"`
	EventHash             string              `json:"event_hash"`
	KeyID                 string              `json:"key_id"`
	HMAC                  string              `json:"hmac"`
	CreatedAt             time.Time           `json:"created_at"`
}

func (s *Store) CommitMutation(_ context.Context, commit MutationCommit) (MutationCommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if len(s.integrityKey) < 32 {
		return MutationCommitResult{}, fmt.Errorf("mutation integrity key not configured")
	}

	var committedProposal *domain.Proposal
	if commit.ProposalID != "" {
		proposal, ok := s.proposals[commit.ProposalID]
		if !ok || proposal.Status != domain.ProposalPending || proposal.RequestHash != commit.RequestHash {
			return MutationCommitResult{}, fmt.Errorf("proposal state conflict")
		}
		proposalCopy := proposal
		committedProposal = &proposalCopy
	} else if commit.ReviewAuthorization != "ADMIN_RETIRE" || commit.RequestHash == "" {
		return MutationCommitResult{}, fmt.Errorf("proposal required")
	}
	if commit.NewMemory != nil {
		if _, exists := s.memories[commit.NewMemory.MemoryID]; exists {
			return MutationCommitResult{}, fmt.Errorf("new memory already exists")
		}
		if commit.NewMemory.StateVersion <= 0 {
			commit.NewMemory.StateVersion = 1
		}
		commit.NewMemory.SchemaVersion = SchemaVersion
		if commit.NewMemory.CreatedAt.IsZero() {
			commit.NewMemory.CreatedAt = now
		}
		if commit.NewMemory.UpdatedAt.IsZero() {
			commit.NewMemory.UpdatedAt = now
		}
	}
	if commit.UpdatedMemory != nil {
		if commit.TargetMemoryID == "" || commit.UpdatedMemory.MemoryID != commit.TargetMemoryID {
			return MutationCommitResult{}, fmt.Errorf("updated memory mismatch")
		}
	}
	if commit.TargetMemoryID != "" {
		target, exists := s.memories[commit.TargetMemoryID]
		if !exists || target.Lifecycle != string(domain.LifecycleActive) ||
			target.StateVersion != commit.ExpectedTargetVersion {
			return MutationCommitResult{}, fmt.Errorf("target version conflict")
		}
	}

	revision := int64(1)
	if len(s.continuity) > 0 {
		revision = s.continuity[len(s.continuity)-1].RevisionNumber + 1
	}
	if commit.Evidence.CreatedAt.IsZero() {
		commit.Evidence.CreatedAt = now
	}
	s.mutationEventSeq++
	event := MemoryMutationEvent{
		EventVersion: 3, EventSeq: s.mutationEventSeq, EventID: newID(),
		RequestHash: commit.RequestHash, Mutation: commit.Mutation, ProposalID: commit.ProposalID,
		Proposal:         committedProposal,
		ResolutionReason: commit.ResolutionReason, ReviewAuthorization: commit.ReviewAuthorization,
		NewMemory: commit.NewMemory, UpdatedMemory: commit.UpdatedMemory, TargetMemoryID: commit.TargetMemoryID,
		ExpectedTargetVersion: commit.ExpectedTargetVersion, TargetLifecycle: commit.TargetLifecycle,
		Evidence: commit.Evidence, CreatedAt: now,
		Continuity: ContinuityRow{
			RevisionNumber: revision, EventID: newID(), ChangeKind: commit.ContinuityKind,
			TargetKind: "COGNITIVE_MEMORY", TargetID: memoryIDForCommit(commit),
			RelatedTargetID: commit.RelatedMemoryID, ProjectKey: commit.ProjectKey,
			Sensitivity: commit.Sensitivity, TraceID: commit.TraceID,
			SafeMetadataJSON: "{}", CreatedAt: now,
		},
	}
	event.PreviousEventHash = s.lastEventHash
	event.EventHash = mutationEventHash(event)
	event.KeyID = integrityKeyID(s.integrityKey)
	event.HMAC = mutationEventHMAC(s.integrityKey, event.EventHash)
	if err := s.appendMutationEventLocked(event); err != nil {
		s.mutationEventSeq--
		return MutationCommitResult{}, err
	}
	s.lastEventHash = event.EventHash

	s.applyMutationEventLocked(event)
	projectionErr := s.refreshMutationProjectionsLocked(event)
	return MutationCommitResult{
		MemoryID: memoryIDForEvent(event), Revision: revision,
		EventID: event.EventID, ProjectionError: projectionErr,
	}, nil
}

func memoryIDForCommit(commit MutationCommit) string {
	if commit.NewMemory != nil {
		return commit.NewMemory.MemoryID
	}
	if commit.UpdatedMemory != nil {
		return commit.UpdatedMemory.MemoryID
	}
	return commit.TargetMemoryID
}

func memoryIDForEvent(event MemoryMutationEvent) string {
	if event.NewMemory != nil {
		return event.NewMemory.MemoryID
	}
	if event.UpdatedMemory != nil {
		return event.UpdatedMemory.MemoryID
	}
	return event.TargetMemoryID
}

func (s *Store) appendMutationEventLocked(event MemoryMutationEvent) error {
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := s.path("memory_events")
	if err := ensureAppendFile(path, s.dir, 0o640); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err = file.Write(line); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	_ = closeErr // file.Sync above is the authoritative commit point.
	return nil
}

func (s *Store) applyMutationEventLocked(event MemoryMutationEvent) {
	if event.Proposal != nil {
		if _, exists := s.proposals[event.Proposal.ID]; !exists {
			s.proposals[event.Proposal.ID] = *event.Proposal
			if event.Proposal.RequestHash != "" {
				s.proposalsHash[event.Proposal.RequestHash] = event.Proposal.ID
			}
		}
	}
	if event.NewMemory != nil {
		s.memories[event.NewMemory.MemoryID] = *event.NewMemory
	}
	if event.UpdatedMemory != nil {
		s.memories[event.UpdatedMemory.MemoryID] = *event.UpdatedMemory
	}
	if event.TargetMemoryID != "" && event.TargetLifecycle != "" {
		if row, ok := s.memories[event.TargetMemoryID]; ok {
			row.Lifecycle = string(event.TargetLifecycle)
			row.StateVersion = event.ExpectedTargetVersion + 1
			row.UpdatedAt = event.CreatedAt
			s.memories[row.MemoryID] = row
		}
	}
	evidenceID := event.Evidence.MemoryID
	if evidenceID != "" {
		duplicate := false
		for _, existing := range s.evidence[evidenceID] {
			if existing.MessageID == event.Evidence.MessageID &&
				existing.QuoteHash == event.Evidence.QuoteHash &&
				existing.Relation == event.Evidence.Relation {
				duplicate = true
				break
			}
		}
		if !duplicate {
			row := event.Evidence
			if message, ok := s.messages[row.MessageID]; ok {
				row.MessageContent = message.Content
				row.OccurredAt = message.OccurredAt
			}
			s.evidence[evidenceID] = append(s.evidence[evidenceID], row)
		}
	}
	if proposal, ok := s.proposals[event.ProposalID]; ok {
		resolved := event.CreatedAt
		proposal.Status = domain.ProposalApplied
		proposal.ReasonCode = event.ResolutionReason
		proposal.AppliedMemoryID = memoryIDForEvent(event)
		proposal.ResolvedAt = &resolved
		s.proposals[event.ProposalID] = proposal
	}
	for _, row := range s.continuity {
		if row.EventID == event.Continuity.EventID {
			return
		}
	}
	s.continuity = append(s.continuity, event.Continuity)
}

func (s *Store) refreshMutationProjectionsLocked(event MemoryMutationEvent) error {
	if err := s.flushKindLocked("memories", s.memoriesJSONL()); err != nil {
		return err
	}
	if err := s.flushKindLocked("proposals", s.proposalsJSONL()); err != nil {
		return err
	}
	if err := s.flushEvidenceLocked(); err != nil {
		return err
	}
	if err := s.flushKindLocked("continuity", s.continuityJSONL()); err != nil {
		return err
	}
	if s.Index != nil {
		if event.NewMemory != nil {
			if err := s.Index.Upsert(*event.NewMemory); err != nil {
				return err
			}
		}
		if event.UpdatedMemory != nil {
			if err := s.Index.Upsert(*event.UpdatedMemory); err != nil {
				return err
			}
		}
		if event.TargetMemoryID != "" {
			target := s.memories[event.TargetMemoryID]
			if target.MemoryID != "" {
				if err := s.Index.Upsert(target); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Store) loadMutationEvents() error {
	if raw, err := os.ReadFile(s.path("memory_events")); err == nil {
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			return fmt.Errorf("memory_events.jsonl: incomplete final record")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	lines, err := readJSONL(s.path("memory_events"))
	if err != nil {
		return err
	}
	var previous int64
	var previousHash string
	anchorReached := s.integrityAnchorHash == ""
	for _, line := range lines {
		var event MemoryMutationEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("memory_events.jsonl: %w", err)
		}
		if (event.EventVersion != 2 && event.EventVersion != 3) || event.EventSeq != previous+1 ||
			event.PreviousEventHash != previousHash || mutationEventHash(event) != event.EventHash {
			return fmt.Errorf("memory_events.jsonl: invalid event sequence")
		}
		if !anchorReached {
			if event.EventHash == s.integrityAnchorHash {
				anchorReached = true
			}
		} else if len(s.integrityKey) > 0 {
			want := mutationEventHMAC(s.integrityKey, event.EventHash)
			if !hmac.Equal([]byte(want), []byte(event.HMAC)) || event.KeyID != integrityKeyID(s.integrityKey) {
				return fmt.Errorf("memory_events.jsonl: signature invalid at sequence %d", event.EventSeq)
			}
		}
		previous = event.EventSeq
		previousHash = event.EventHash
		s.mutationEventSeq = event.EventSeq
		s.lastEventHash = event.EventHash
		s.applyMutationEventLocked(event)
	}
	if !anchorReached {
		return fmt.Errorf("memory_events.jsonl: key rotation anchor not found")
	}
	if len(lines) > 0 {
		if err := s.flushKindLocked("memories", s.memoriesJSONL()); err != nil {
			return err
		}
		if err := s.flushKindLocked("proposals", s.proposalsJSONL()); err != nil {
			return err
		}
		if err := s.flushEvidenceLocked(); err != nil {
			return err
		}
		if err := s.flushKindLocked("continuity", s.continuityJSONL()); err != nil {
			return err
		}
	}
	return nil
}

func mutationEventHash(event MemoryMutationEvent) string {
	event.EventHash = ""
	event.KeyID = ""
	event.HMAC = ""
	raw, _ := json.Marshal(event)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mutationEventHMAC(key []byte, eventHash string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(eventHash))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func integrityKeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// SetIntegrityKey verifies every canonical mutation event before allowing any
// new mutation. Hash chaining detects removal/reordering; HMAC detects forged
// replacement chains.
func (s *Store) SetIntegrityKey(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(key) < 32 {
		return fmt.Errorf("integrity key must be at least 32 bytes")
	}
	s.integrityKey = append([]byte(nil), key...)
	s.keyRotationSeq = 0
	s.lastRotationHash = ""
	s.integrityAnchorHash = ""
	if err := s.loadKeyRotations(); err != nil {
		return err
	}
	if raw, err := os.ReadFile(s.path("memory_events")); err == nil {
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			return fmt.Errorf("memory event journal has incomplete final record")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	lines, err := readJSONL(s.path("memory_events"))
	if err != nil {
		return err
	}
	var previousHash string
	var previousSeq int64
	anchorReached := s.integrityAnchorHash == ""
	for _, line := range lines {
		var event MemoryMutationEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("memory event decode: %w", err)
		}
		if (event.EventVersion != 2 && event.EventVersion != 3) || event.EventSeq != previousSeq+1 ||
			event.PreviousEventHash != previousHash || mutationEventHash(event) != event.EventHash {
			return fmt.Errorf("memory event hash chain invalid at sequence %d", event.EventSeq)
		}
		if !anchorReached {
			if event.EventHash == s.integrityAnchorHash {
				anchorReached = true
			}
		} else {
			want := mutationEventHMAC(key, event.EventHash)
			if !hmac.Equal([]byte(want), []byte(event.HMAC)) || event.KeyID != integrityKeyID(key) {
				return fmt.Errorf("memory event signature invalid at sequence %d", event.EventSeq)
			}
		}
		previousSeq = event.EventSeq
		previousHash = event.EventHash
	}
	if !anchorReached {
		return fmt.Errorf("memory event key rotation anchor not found")
	}
	s.lastEventHash = previousHash
	return nil
}
