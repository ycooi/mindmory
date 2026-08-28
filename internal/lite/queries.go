package lite

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/artifact/policy"
	"mindmory.local/core/internal/auth"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// --- sessions ---

func (s *Store) ResolveSession(ctx context.Context, principal auth.Principal, sessionID string) (SessionRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.sessions[sessionID]
	if !ok || row.ClientKey != principal.Key {
		return SessionRow{}, errNoRows
	}
	return row, nil
}

func (s *Store) ResolveSessionByExternal(ctx context.Context, clientKey, externalID string) (SessionRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sessionsExt[clientKey+"\x00"+externalID]
	if !ok {
		return SessionRow{}, errNoRows
	}
	return s.sessions[id], nil
}

func (s *Store) UpsertSession(ctx context.Context, principal auth.Principal, externalID, title, projectKey string, activity time.Time) (SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := principal.Key + "\x00" + externalID
	if id, ok := s.sessionsExt[key]; ok {
		row := s.sessions[id]
		if conflict(row.Title, title) || conflict(row.ProjectKey, projectKey) {
			return row, errSessionConflict
		}
		if activity.After(row.LastActivityAt) {
			row.LastActivityAt = activity
			s.sessions[id] = row
			return row, s.flushKindLocked("sessions", s.sessionsJSONL())
		}
		return row, nil
	}
	now := time.Now().UTC()
	row := SessionRow{SessionID: newID(), ClientKey: principal.Key, ExternalSessionID: externalID,
		Title: title, ProjectKey: projectKey, StartedAt: now, LastActivityAt: activity, CreatedAt: now,
		Seq: s.nextSessionSeq()}
	s.sessions[row.SessionID] = row
	s.sessionsExt[key] = row.SessionID
	if err := s.flushKindLocked("sessions", s.sessionsJSONL()); err != nil {
		return row, err
	}
	return row, s.persistMeta()
}

func conflict(existing, incoming string) bool {
	if incoming == "" {
		return false
	}
	return existing != "" && existing != incoming
}

// --- messages ---

func (s *Store) InsertMessage(ctx context.Context, sessionID string, m checkpointMessage) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotent by external id within the session.
	for id, row := range s.messages {
		if row.SessionID == sessionID && row.ExternalMessageID == m.ExternalMessageID {
			if row.Role != string(m.Role) || row.ContentType != m.ContentType ||
				row.Content != m.Content || row.ContentHash != m.Hash ||
				row.AssistantID != m.AssistantID || row.AssistantName != m.AssistantName {
				return "", false, errMessageConflict
			}
			return id, true, nil
		}
	}
	secretLike, instructionLike := archive.DetectMessagePolicy(m.Content)
	id := newID()
	s.messageSeq++
	if m.Role == archive.RoleUser {
		s.turnSeq++
	}
	row := MessageRow{
		SchemaVersion: 1, MessageID: id, SessionID: sessionID, ExternalMessageID: m.ExternalMessageID,
		Role: string(m.Role), ContentType: m.ContentType, Content: m.Content, ContentHash: m.Hash,
		ExactContentHash: hashContent(m.Content), MessageSeq: s.messageSeq, TurnSeq: s.turnSeq,
		OccurredAt: m.OccurredAt, CreatedAt: time.Now().UTC(),
		SecretLike: secretLike, InstructionLike: instructionLike, Sensitivity: "NORMAL",
		AssistantID: m.AssistantID, AssistantName: m.AssistantName,
	}
	row, err := s.appendMessageRecordLocked(row)
	if err != nil {
		s.messageSeq--
		if m.Role == archive.RoleUser {
			s.turnSeq--
		}
		return "", false, err
	}
	s.messages[id] = row
	// messages.jsonl is now a compatibility projection. The segmented journal
	// above is authoritative and was fsynced before the in-memory state changed.
	_ = s.flushKindLocked("messages", s.messagesJSONL())
	return id, false, nil
}

func (s *Store) LatestUserMessageID(ctx context.Context, sessionID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best string
	var bestTurn, bestMessage int64
	for id, row := range s.messages {
		if row.SessionID != sessionID || row.Role != "user" {
			continue
		}
		if best == "" || row.TurnSeq > bestTurn || (row.TurnSeq == bestTurn && row.MessageSeq > bestMessage) {
			best, bestTurn, bestMessage = id, row.TurnSeq, row.MessageSeq
		}
	}
	if best == "" {
		return "", errNoRows
	}
	return best, nil
}

func (s *Store) LoadMessageEvidence(ctx context.Context, sessionID, messageID string) (archive.MessageEvidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.messages[messageID]
	if !ok || row.SessionID != sessionID {
		return archive.MessageEvidence{}, errNoRows
	}
	return archive.MessageEvidence{
		MessageID: row.MessageID, SessionID: row.SessionID, Role: archive.Role(row.Role),
		Content: row.Content, ContentHash: row.ContentHash, Retrieved: false,
		SecretLike: row.SecretLike, InstructionLike: row.InstructionLike,
		Sensitivity: policySensitivity(row.Sensitivity),
	}, nil
}

// --- memories ---

func (s *Store) insertMemoryFixture(ctx context.Context, m MemoryRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	if m.StateVersion <= 0 {
		m.StateVersion = 1
	}
	m.SchemaVersion = SchemaVersion
	s.memories[m.MemoryID] = m
	if err := s.flushKindLocked("memories", s.memoriesJSONL()); err != nil {
		return err
	}
	if s.Index != nil {
		if memoryRowPolicyAllowed(m) && m.Lifecycle == "ACTIVE" {
			return s.Index.Upsert(m)
		}
		return s.Index.Remove(m.MemoryID)
	}
	return nil
}

// FlushAccessBumps persists pending access bumps to the canonical file in
// one atomic rewrite. Safe from any non-hot path; a no-op when nothing is
// pending. The index needs no refresh — it never stored these counters.
func (s *Store) FlushAccessBumps() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushAccessBumpsLocked()
}

func (s *Store) flushAccessBumpsLocked() error {
	if len(s.accessBumps) == 0 {
		return nil
	}
	for memoryID, bumps := range s.accessBumps {
		row, ok := s.memories[memoryID]
		if !ok {
			continue
		}
		row.AccessCount += bumps
		s.memories[memoryID] = row
	}
	s.accessBumps = map[string]int64{}
	return s.flushKindLocked("memories", s.memoriesJSONL())
}

// promoteImportance bumps a declared importance grade up one step in the
// 5-grade set (0.2/0.4/0.6/0.8/1.0), capping at 1.0. A memory the user has
// reaffirmed is more important, not less.
func promoteImportance(importance float64) float64 {
	switch {
	case importance >= 1.0:
		return 1.0
	case importance >= 0.8:
		return 1.0
	case importance >= 0.6:
		return 0.8
	case importance >= 0.4:
		return 0.6
	case importance >= 0.2:
		return 0.4
	default:
		return 0.4
	}
}

func (s *Store) LoadMemoryRow(ctx context.Context, memoryID string) (MemoryRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.memories[memoryID]
	if !ok {
		return MemoryRow{}, errNoRows
	}
	return row, nil
}

func (s *Store) EligibleMemories(ctx context.Context, scope retrieval.SessionScope, kinds []domain.Kind, limit int) ([]MemoryRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wantKinds := make(map[domain.Kind]bool, len(kinds))
	for _, kind := range kinds {
		wantKinds[kind] = true
	}
	var out []MemoryRow
	for _, row := range s.memories {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !memoryRowEligible(scope, row, false) {
			continue
		}
		if len(wantKinds) > 0 && !wantKinds[domain.Kind(row.Kind)] {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// memoryRowEligible is the final canonical authority check for every
// model-facing memory read. Derived indexes may narrow candidates, but they
// can never grant eligibility.
func memoryRowEligible(scope retrieval.SessionScope, row MemoryRow, historical bool) bool {
	if !memoryRowPolicyAllowed(row) {
		return false
	}
	return retrieval.MemoryEligible(
		scope,
		domain.ScopeType(row.ScopeType),
		row.ProjectKey,
		domain.Lifecycle(row.Lifecycle),
		policySensitivity(row.Sensitivity),
		historical,
	)
}

func memoryRowPolicyAllowed(row MemoryRow) bool {
	if row.ContentHash == "" || hashContent(row.Content) != row.ContentHash {
		return false
	}
	secretLike, instructionLike := archive.DetectMessagePolicy(row.Subject + "\n" + row.Content)
	if row.SecretLike || row.InstructionLike || secretLike || instructionLike {
		return false
	}
	return row.Sensitivity == "NORMAL"
}

// --- evidence links ---

func (s *Store) InsertMessageEvidence(ctx context.Context, memoryID, messageID, quoteHash string, start, end int, relation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := MessageEvidenceRow{
		MemoryID: memoryID, MessageID: messageID, QuoteHash: quoteHash,
		QuoteStart: start, QuoteEnd: end, Relation: relation, CreatedAt: time.Now().UTC(),
	}
	if msg, ok := s.messages[messageID]; ok {
		row.MessageContent = msg.Content
		row.OccurredAt = msg.OccurredAt
	}
	s.evidence[memoryID] = append(s.evidence[memoryID], row)
	return s.flushEvidenceLocked()
}

// flushEvidenceLocked persists the evidence links. They live inside the
// messages file's sibling memory file? No — evidence is its own canonical
// concept; we persist it in the memories file as an embedded field is
// avoided; instead evidence rows are stored in a dedicated file so the
// memories.jsonl stays clean. We keep a separate evidence.jsonl.
func (s *Store) flushEvidenceLocked() error {
	var lines []byte
	ids := make([]string, 0, len(s.evidence))
	for id := range s.evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, row := range s.evidence[id] {
			line, err := marshalJSON(row)
			if err != nil {
				return err
			}
			lines = append(lines, line...)
			lines = append(lines, '\n')
		}
	}
	return s.flushKindLocked("evidence", lines)
}

func (s *Store) MessageEvidenceFor(ctx context.Context, memoryID string) ([]MessageEvidenceRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := append([]MessageEvidenceRow(nil), s.evidence[memoryID]...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows, nil
}

// --- continuity ---

func (s *Store) ContinuityHead(ctx context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.continuity) == 0 {
		return 0, nil
	}
	return s.continuity[len(s.continuity)-1].RevisionNumber, nil
}

func (s *Store) AppendContinuity(ctx context.Context, changeKind, targetKind, targetID, relatedID, projectKey, sensitivity, traceID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var revision int64
	if len(s.continuity) > 0 {
		revision = s.continuity[len(s.continuity)-1].RevisionNumber
	}
	revision++
	row := ContinuityRow{
		RevisionNumber: revision, EventID: newID(), ChangeKind: changeKind, TargetKind: targetKind,
		TargetID: targetID, RelatedTargetID: relatedID, ProjectKey: projectKey, Sensitivity: sensitivity,
		TraceID: traceID, SafeMetadataJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	s.continuity = append(s.continuity, row)
	return revision, s.flushKindLocked("continuity", s.continuityJSONL())
}

func (s *Store) ContinuityChanges(ctx context.Context, after int64, projectKey string, limit int, recent bool) ([]retrieval.ContinuityChange, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []retrieval.ContinuityChange
	highest := after
	if recent {
		for i := len(s.continuity) - 1; i >= 0 && len(out) < limit; i-- {
			row := s.continuity[i]
			if row.ProjectKey != "" && (projectKey == "" || row.ProjectKey != projectKey) {
				continue
			}
			out = append(out, row.change())
			if row.RevisionNumber > highest {
				highest = row.RevisionNumber
			}
		}
	} else {
		for _, row := range s.continuity {
			if row.RevisionNumber <= after {
				continue
			}
			if row.ProjectKey != "" && (projectKey == "" || row.ProjectKey != projectKey) {
				continue
			}
			out = append(out, row.change())
			if row.RevisionNumber > highest {
				highest = row.RevisionNumber
			}
			if len(out) >= limit {
				break
			}
		}
	}
	return out, highest, nil
}

func (r ContinuityRow) change() retrieval.ContinuityChange {
	return retrieval.ContinuityChange{
		ChangeKind: r.ChangeKind, TargetKind: r.TargetKind, TargetID: r.TargetID,
		RelatedTargetID: r.RelatedTargetID, ProjectKey: r.ProjectKey, CreatedAt: r.CreatedAt,
	}
}

// SessionsStartedAfter counts sessions (all projects) whose started_at is
// after the given time — the session-clock decay basis of §A3.
func (s *Store) SessionsStartedAfter(ctx context.Context, after time.Time) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int64
	for _, row := range s.sessions {
		if row.StartedAt.After(after) {
			count++
		}
	}
	return count
}

// SessionSeq returns the monotonic ordinal of the session (P0-6 decay clock).
func (s *Store) SessionSeq(ctx context.Context, sessionID string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionSeqLocked(sessionID)
}

// sessionSeqLocked reads the session ordinal; caller must hold at least RLock.
func (s *Store) sessionSeqLocked(sessionID string) (int64, bool) {
	row, ok := s.sessions[sessionID]
	if !ok {
		return 0, false
	}
	return row.Seq, true
}

// --- project context ---

func (s *Store) CurrentProjectContext(ctx context.Context, projectKey string) (*ProjectContextRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.projectCtx[projectKey]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

// --- feedback / heat ---

// RecordAccess counts a retrieval/use event. P0 fix (expert review):
// mere retrieval (search hit, candidate surfacing) must NOT re-warm a
// memory — that creates a self-reinforcing popularity loop. Only genuine
// use (recall by id) refreshes activation. Feedback changes confidence,
// which never recovers by time.
func (s *Store) RecordAccess(ctx context.Context, memoryID, sessionID string, kind retrieval.AccessKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.memories[memoryID]
	if !ok {
		return errNoRows
	}
	now := time.Now().UTC()
	row.LastAccessedAt = &now
	switch kind {
	case retrieval.AccessRecall, retrieval.AccessReflexCore, retrieval.AccessReflexImp:
		// Genuine use: refresh activation to full and record the use seq.
		row.Activation = 1.0
		if seq, ok := s.sessionSeqLocked(sessionID); ok {
			row.LastUsedSeq = seq
		}
	case retrieval.AccessFeedback:
		// handled by ApplyFeedback (needs outcome); leave activation alone.
	default:
		// search hit / candidate: diagnostic count only, no re-warm.
	}
	s.memories[memoryID] = row
	// Access bumps are batched: a search returning N hits must not rewrite
	// the whole 1MB+ memories file N times, nor re-write the SQLite index
	// (which does not even store these counters). The bump is applied to
	// AccessCount at flush time (FlushAccessBumps) so in-memory counts and
	// the canonical file stay in sync exactly once per batch. A crash loses
	// only diagnostic counts, never authored memory content.
	s.accessBumps[memoryID]++
	return nil
}

// ApplyFeedback applies a helped/misled signal. helped: confidence +0.05
// (cap 1.0) and activation re-warmed (it was genuinely useful). misled:
// confidence -0.25 (floor 0) and the memory is marked DISPUTED. Confidence
// NEVER recovers with time — only an explicit correction (new memory
// superseding this one) resets the record.
func (s *Store) ApplyFeedback(ctx context.Context, memoryID, sessionID string, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.memories[memoryID]
	if !ok {
		return errNoRows
	}
	now := time.Now().UTC()
	row.AccessCount++
	row.LastAccessedAt = &now
	if outcome == "helped" {
		row.Confidence += 0.05
		if row.Confidence > 1.0 {
			row.Confidence = 1.0
		}
		row.Activation = 1.0
		row.Disputed = false // a confirmed-useful memory is no longer disputed
		if seq, ok := s.sessionSeqLocked(sessionID); ok {
			row.LastUsedSeq = seq
		}
	} else {
		row.Confidence -= 0.25
		if row.Confidence < 0 {
			row.Confidence = 0
		}
		row.Disputed = true
	}
	s.memories[memoryID] = row
	// Access bumps are batched: a search returning N hits must not rewrite
	// the whole 1MB+ memories file N times, nor re-write the SQLite index
	// (which does not even store these counters). The bump is applied to
	// AccessCount at flush time (FlushAccessBumps) so in-memory counts and
	// the canonical file stay in sync exactly once per batch. A crash loses
	// only diagnostic counts, never authored memory content.
	s.accessBumps[memoryID]++
	return nil
}

func policySensitivity(value string) policy.Sensitivity {
	switch value {
	case "SENSITIVE":
		return policy.SensitivitySensitive
	case "SECRET":
		return policy.SensitivitySecret
	case "RESTRICTED":
		return policy.SensitivityRestricted
	default:
		return policy.SensitivityNormal
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var (
	errNoRows          = errors.New("no rows")
	errSessionConflict = errors.New("session metadata conflict")
	errMessageConflict = errors.New("external message id conflict")
)

var _ = strings.TrimSpace
