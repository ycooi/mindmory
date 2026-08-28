package lite

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domain "mindmory.local/core/internal/memory"
)

// Importer migrates the legacy PostgreSQL CSV exports (from the Docker-era
// daemon) into the canonical JSONL store. The CSVs are produced by
// `psql ... COPY ... TO STDOUT WITH (FORMAT csv, HEADER true)`.
type Importer struct {
	Store *Store
}

// HasData reports whether any canonical records exist.
func (s *Store) HasData() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions) > 0 || len(s.messages) > 0 || len(s.memories) > 0
}

// ImportCSV loads memories, sessions, messages, proposals, evidence,
// continuity, and project context from dir into the store, then flushes.
func (i *Importer) ImportCSV(dir string) error {
	s := i.Store
	// Order matters: sessions before messages; memories + messages before
	// evidence (which needs message content for quote hydration).
	if err := i.importSessions(dir); err != nil {
		return err
	}
	if err := i.importMessages(dir); err != nil {
		return err
	}
	if err := i.importMemories(dir); err != nil {
		return err
	}
	if err := i.importProposals(dir); err != nil {
		return err
	}
	if err := i.importContinuity(dir); err != nil {
		return err
	}
	if err := i.importEvidence(dir); err != nil {
		return err
	}
	if err := i.importProjectContext(dir); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushAllLocked()
}

func readCSV(path string) ([][]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if len(rows) < 2 {
		return nil, nil
	}
	return rows[1:], nil // drop header
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05-07"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (i *Importer) importSessions(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "sessions.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 12 {
			continue
		}
		record := SessionRow{
			SessionID: row[0], ClientKey: row[1], ExternalSessionID: row[2],
			Title: row[3], ProjectKey: row[4],
			StartedAt: parseTime(row[5]), LastActivityAt: parseTime(row[6]), CreatedAt: parseTime(row[7]),
		}
		s.sessions[record.SessionID] = record
		s.sessionsExt[record.ClientKey+"\x00"+record.ExternalSessionID] = record.SessionID
	}
	return nil
}

func (i *Importer) importMessages(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "messages.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 11 {
			continue
		}
		record := MessageRow{
			MessageID: row[0], SessionID: row[1], ExternalMessageID: row[2],
			Role: row[3], ContentType: row[4], Content: row[5], ContentHash: row[6],
			OccurredAt: parseTime(row[7]), CreatedAt: parseTime(row[8]),
			Sensitivity: "NORMAL",
		}
		if record.Role == "user" {
			secretLike, instructionLike := detectPolicy(record.Content)
			record.SecretLike = secretLike
			record.InstructionLike = instructionLike
		}
		s.messages[record.MessageID] = record
	}
	return nil
}

func (i *Importer) importMemories(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "memories.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 18 {
			continue
		}
		record := MemoryRow{
			MemoryID: row[0], Kind: row[1], Subject: row[2], Content: row[3], ContentHash: row[4],
			Lifecycle: row[5], EpistemicStatus: row[6], Sensitivity: row[9],
			SupersedesMemoryID: row[10],
			CreatedAt:          parseTime(row[11]), UpdatedAt: parseTime(row[12]),
			ScopeType: row[13], ProjectKey: row[14],
			AccessCount: atoi64(row[15]), Activation: atof(row[17]),
		}
		// Legacy CSV confidence is 0.5; treat as full confidence (it was
		// USER_ACCEPTED). New memories default confidence 1.0.
		record.Confidence = 1.0 // legacy rows were USER_ACCEPTED
		record.Importance, _ = strconv.ParseFloat(row[8], 64)
		if last := row[16]; last != "" && last != "\\N" && last != "NULL" {
			t := parseTime(last)
			record.LastAccessedAt = &t
		}
		s.memories[record.MemoryID] = record
	}
	return nil
}

func (i *Importer) importProposals(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "proposals.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 17 {
			continue
		}
		identity := domainProposalIdentity{
			ClientKey: row[1], SessionID: row[2], MessageID: row[3], Mutation: row[4],
			TargetMemoryID: row[5], ProposedKind: row[6], Subject: row[7], Replacement: row[8],
			Scope: row[15], ProjectKey: row[16],
		}
		proposal := domain.Proposal{
			ID: row[0], Status: domain.ProposalStatus(row[9]), ReasonCode: row[10],
			CreatedAt: parseTime(row[11]), RequestHash: row[13], AppliedMemoryID: "",
			Identity: identity.toIdentity(),
		}
		if resolved := row[12]; resolved != "" && resolved != "\\N" {
			t := parseTime(resolved)
			proposal.ResolvedAt = &t
		}
		s.proposals[proposal.ID] = proposal
		if proposal.RequestHash != "" {
			s.proposalsHash[proposal.RequestHash] = proposal.ID
		}
	}
	return nil
}

func (i *Importer) importContinuity(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "continuity.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 11 {
			continue
		}
		record := ContinuityRow{
			RevisionNumber: atoi64(row[0]), EventID: row[1], ChangeKind: row[2], TargetKind: row[3],
			TargetID: row[4], RelatedTargetID: row[5], ProjectKey: row[6], Sensitivity: row[7],
			TraceID: row[8], SafeMetadataJSON: row[9], CreatedAt: parseTime(row[10]),
		}
		s.continuity = append(s.continuity, record)
	}
	return nil
}

func (i *Importer) importEvidence(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "evidence.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		record := MessageEvidenceRow{
			MemoryID: row[0], MessageID: row[1], QuoteHash: row[2],
			QuoteStart: int(atoi64(row[3])), QuoteEnd: int(atoi64(row[4])),
			Relation: row[5], CreatedAt: parseTime(row[6]),
		}
		if message, ok := s.messages[record.MessageID]; ok {
			record.MessageContent = message.Content
			record.OccurredAt = message.OccurredAt
		}
		s.evidence[record.MemoryID] = append(s.evidence[record.MemoryID], record)
	}
	return nil
}

func (i *Importer) importProjectContext(dir string) error {
	rows, err := readCSV(filepath.Join(dir, "project_context.csv"))
	if err != nil {
		return err
	}
	s := i.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if len(row) < 11 {
			continue
		}
		record := ProjectContextRow{
			RevisionID: row[0], ProjectKey: row[1], Revision: int(atoi64(row[2])),
			Objective: row[3], CurrentState: row[4], Decisions: row[5],
			OpenQuestions: row[6], NextActions: row[7], Sensitivity: row[8],
			IsCurrent: row[9] == "t" || row[9] == "true" || row[9] == "1",
			CreatedAt: parseTime(row[10]),
		}
		s.projectCtx[record.ProjectKey] = record
	}
	return nil
}

func atoi64(value string) int64 {
	if value == "" || value == "\\N" || value == "NULL" {
		return 0
	}
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

func atof(value string) float64 {
	if value == "" || value == "\\N" || value == "NULL" {
		return 0
	}
	f, _ := strconv.ParseFloat(value, 64)
	return f
}

// detectPolicy mirrors archive.DetectMessagePolicy for the import path
// (legacy CSVs carry no policy state; recompute deterministically).
func detectPolicy(content string) (secretLike, instructionLike bool) {
	return detectSecretLike(content), detectInstructionLike(content)
}

func detectSecretLike(content string) bool {
	lower := strings.ToLower(content)
	for _, cue := range []string{"password", "api key", "secret", "token ", "private key", "口令", "密码", "密钥", "access key", "authorization:"} {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func detectInstructionLike(content string) bool {
	lower := strings.ToLower(content)
	for _, cue := range []string{"system prompt", "instruction:", "you are an ai", "你的系统提示", "系统提示词", "ignore previous instructions", "ignore all previous"} {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

// domainProposalIdentity is the importer-side identity mirror.
type domainProposalIdentity struct {
	ClientKey, SessionID, MessageID, Mutation string
	TargetMemoryID, ProposedKind, Subject     string
	Replacement, Scope, ProjectKey            string
}

func (r domainProposalIdentity) toIdentity() domain.ProposalIdentity {
	return domain.ProposalIdentity{
		ClientKey: r.ClientKey, SessionID: r.SessionID, MessageID: r.MessageID,
		Mutation: domain.MutationKind(r.Mutation), TargetMemoryID: r.TargetMemoryID,
		ProposedKind: domain.Kind(r.ProposedKind), Scope: domain.ScopeType(r.Scope),
		ProjectKey: r.ProjectKey, Subject: r.Subject, Replacement: r.Replacement,
	}
}

var _ = context.Background
var _ = json.Valid
