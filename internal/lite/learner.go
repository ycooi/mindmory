// Passive learner — Phase A of automatic passive learning, ported from the
// retired PostgreSQL daemon (internal/service/memory/learner.go) to the
// JSONL-backed lite control plane.
//
// Scans archived user turns for explicit durable-intent cues and proposes
// memories through the SAME governed mutation path a model would use
// (evidence binding, cue verification, subject overlap, proposal
// idempotency). No model is in the extraction loop: the deterministic cue +
// overlap gates decide what gets proposed, and the control plane decides
// APPLIED vs STAGED — a current-turn cue applies immediately; an older
// cue-bearing message stages for review (CURRENT_USER_EVIDENCE_REQUIRED).
package lite

import (
	"context"
	"sort"
	"strings"
	"time"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/auth"
	domain "mindmory.local/core/internal/memory"
)

// LearnerOutcome records one message's extraction result.
type LearnerOutcome struct {
	MemoryID   string `json:"memory_id,omitempty"`
	ProposalID string `json:"proposal_id,omitempty"`
	Outcome    string `json:"outcome"` // APPLIED | STAGED | SKIPPED | FAILED
	Reason     string `json:"reason,omitempty"`
}

// LearnerSummary aggregates one extract run.
type LearnerSummary struct {
	Scanned  int              `json:"scanned"`
	Applied  int              `json:"applied"`
	Staged   int              `json:"staged"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
	Outcomes []LearnerOutcome `json:"outcomes,omitempty"`
}

// learnerCandidate is one eligible archived user turn for extraction.
type learnerCandidate struct {
	sessionID  string
	messageID  string
	content    string
	occurredAt time.Time
}

// eligibleLearnerMessages returns up to limit archived user turns that may
// feed extraction: role=user, NORMAL sensitivity, not secret/instruction-
// like, and not already cited by a memory evidence row. Newest first.
func (s *Store) eligibleLearnerMessages(limit int) []learnerCandidate {
	if s.lowRAM && s.Index != nil {
		rows, err := s.Index.LearnerMessages(limit)
		if err == nil {
			return rows
		}
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cited := map[string]bool{}
	for _, rows := range s.evidence {
		for _, row := range rows {
			cited[row.MessageID] = true
		}
	}
	var candidates []learnerCandidate
	for id, row := range s.messages {
		if row.Role != "user" || row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike || cited[id] {
			continue
		}
		candidates = append(candidates, learnerCandidate{
			sessionID: row.SessionID, messageID: id, content: row.Content, occurredAt: row.OccurredAt,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].occurredAt.Equal(candidates[j].occurredAt) {
			return candidates[i].occurredAt.After(candidates[j].occurredAt)
		}
		return candidates[i].messageID > candidates[j].messageID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// LearnerPrincipal returns the MCP principal the learner runs as: the local
// trust key in single-user mode, otherwise the first configured client key.
func (s *Server) LearnerPrincipal() auth.Principal {
	return auth.Principal{Key: s.LearnerKey, Type: auth.PrincipalMCP}
}

// LearnerExtract scans up to maxMessages eligible archived user turns and
// proposes REMEMBER memories for those carrying explicit durable-intent cues.
// principal must be the MCP principal owning the archived sessions (evidence
// hydration requires a client_key match).
func (s *Server) LearnerExtract(ctx context.Context, principal auth.Principal, maxMessages int) (LearnerSummary, error) {
	if principal.Type != auth.PrincipalMCP || principal.Key == "" {
		return LearnerSummary{}, apperror.New(apperror.MemoryProposalInvalid, false, nil)
	}
	if maxMessages <= 0 || maxMessages > 500 {
		maxMessages = 50
	}
	started := time.Now()
	candidates := s.Store.eligibleLearnerMessages(maxMessages)
	summary := LearnerSummary{Scanned: len(candidates)}
	for _, c := range candidates {
		outcome, err := s.learnerPropose(ctx, principal, c)
		if err != nil {
			summary.Failed++
			summary.Outcomes = append(summary.Outcomes, LearnerOutcome{Outcome: "FAILED", Reason: err.Error()})
			continue
		}
		summary.Outcomes = append(summary.Outcomes, outcome)
		switch outcome.Outcome {
		case "APPLIED":
			summary.Applied++
		case "STAGED":
			summary.Staged++
		default:
			summary.Skipped++
		}
	}
	s.ops("LEARNER", principal, "", "ok", "", "", time.Since(started), map[string]any{
		"scanned": summary.Scanned, "applied": summary.Applied, "staged": summary.Staged,
		"skipped": summary.Skipped, "failed": summary.Failed,
	})
	return summary, nil
}

// learnerPropose runs the cue gate and the governed mutation for one message.
func (s *Server) learnerPropose(ctx context.Context, principal auth.Principal, c learnerCandidate) (LearnerOutcome, error) {
	content := strings.TrimSpace(c.content)
	if content == "" || !domain.HasCue(domain.MutationRemember, content) {
		return LearnerOutcome{Outcome: "SKIPPED", Reason: "NO_CUE"}, nil
	}
	subject := deriveSubject(content)
	if !domain.SubjectOverlaps(subject, content) {
		return LearnerOutcome{Outcome: "SKIPPED", Reason: "SUBJECT_NOT_GROUNDED"}, nil
	}
	request := mutationRequest{
		SessionID: c.sessionID, MessageID: c.messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindUserPreference, Scope: domain.ScopeGlobal,
		Subject: subject, EvidenceQuote: content,
	}
	result, err := s.applyMutationAllowOld(ctx, principal, request)
	if err != nil {
		return LearnerOutcome{}, err
	}
	return LearnerOutcome{
		MemoryID: result.MemoryID, ProposalID: result.ProposalID,
		Outcome: result.Outcome, Reason: result.ReasonCode,
	}, nil
}

// deriveSubject turns the message into a compact memory subject: strip a
// leading intent cue and truncate to a readable length. The result is always
// a substring of the evidence, so subject-overlap verification passes.
// Mirrors internal/service/memory/learner.go — kept here so the lite daemon
// has no dependency on the retired PostgreSQL service package.
func deriveSubject(content string) string {
	text := strings.TrimSpace(content)
	lower := strings.ToLower(text)
	for _, prefix := range []string{
		"remember that ", "remember ", "i want ", "i prefer ", "my goal is ", "my preference is ",
		"记住 ", "记一下 ", "别忘了", "以后 ", "我的目标是", "我偏好", "我喜欢", "必须记住", "务必记住", "优先",
	} {
		if strings.HasPrefix(lower, prefix) {
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}
	if runes := []rune(text); len(runes) > 48 {
		text = string(runes[:48])
	}
	return text
}
