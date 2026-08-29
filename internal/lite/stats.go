package lite

import domain "mindmory.local/core/internal/memory"

// StoreStatistics contains counts only. It never includes memory, message,
// evidence, proposal, or project-context content.
type StoreStatistics struct {
	Sessions            int          `json:"sessions"`
	Messages            int          `json:"messages"`
	Memories            int          `json:"memories"`
	ActiveMemories      int          `json:"active_memories"`
	InactiveMemories    int          `json:"inactive_memories"`
	SecretLikeMemories  int          `json:"secret_like_memories"`
	InstructionMemories int          `json:"instruction_like_memories"`
	EvidenceLinks       int          `json:"evidence_links"`
	Proposals           int          `json:"proposals"`
	PendingProposals    int          `json:"pending_proposals"`
	AppliedProposals    int          `json:"applied_proposals"`
	RejectedProposals   int          `json:"rejected_proposals"`
	ContinuityRevisions int          `json:"continuity_revisions"`
	ProjectContexts     int          `json:"project_contexts"`
	MutationEvents      int64        `json:"mutation_events"`
	ArchivedUserTurns   int64        `json:"archived_user_turns"`
	Vectors             VectorStatus `json:"vectors"`
}

func (s *Store) ReadOnlyStatistics() StoreStatistics {
	s.mu.RLock()
	statistics := StoreStatistics{
		Sessions: len(s.sessions), Messages: len(s.messages), Memories: len(s.memories),
		Proposals: len(s.proposals), ContinuityRevisions: len(s.continuity), ProjectContexts: len(s.projectCtx),
		MutationEvents: s.mutationEventSeq, ArchivedUserTurns: s.turnSeq,
	}
	if s.lowRAM && s.Index != nil {
		if counts, err := s.Index.Counts(); err == nil {
			statistics.Messages = counts.Messages
			statistics.Memories = counts.Memories
			statistics.ActiveMemories = counts.ActiveMemories
			statistics.InactiveMemories = counts.InactiveMemories
			statistics.SecretLikeMemories = counts.SecretLikeMemories
			statistics.InstructionMemories = counts.InstructionMemories
			statistics.EvidenceLinks = counts.EvidenceLinks
		}
	}
	for _, memory := range s.memories {
		if memory.Lifecycle == "ACTIVE" {
			statistics.ActiveMemories++
		} else {
			statistics.InactiveMemories++
		}
		if memory.SecretLike {
			statistics.SecretLikeMemories++
		}
		if memory.InstructionLike {
			statistics.InstructionMemories++
		}
	}
	for _, evidence := range s.evidence {
		statistics.EvidenceLinks += len(evidence)
	}
	for _, proposal := range s.proposals {
		switch proposal.Status {
		case domain.ProposalPending:
			statistics.PendingProposals++
		case domain.ProposalApplied:
			statistics.AppliedProposals++
		case domain.ProposalRejected:
			statistics.RejectedProposals++
		}
	}
	s.mu.RUnlock()
	statistics.Vectors = s.VectorStatus()
	return statistics
}
