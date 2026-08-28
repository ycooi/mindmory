package memory

import (
	"errors"
	"time"
)

type EvidenceSourceKind string

const (
	EvidenceMessage          EvidenceSourceKind = "MESSAGE"
	EvidenceArtifactFragment EvidenceSourceKind = "ARTIFACT_FRAGMENT"
	EvidenceCognitiveMemory  EvidenceSourceKind = "COGNITIVE_MEMORY"
)

type RevisionEvidence struct {
	Kind         EvidenceSourceKind
	SourceID     string
	Relation     string
	Quote        *MessageQuote
	CitationHash string
}

type CoreRevisionRequest struct {
	BlockKey string
	Content  string
	Evidence []RevisionEvidence
}

type ProjectRevisionRequest struct {
	ProjectKey    string
	Objective     string
	CurrentState  string
	Decisions     []string
	OpenQuestions []string
	NextActions   []string
	Evidence      []RevisionEvidence
}

type EpisodeRevisionRequest struct {
	EpisodeID string
	Title     string
	Summary   string
	StartAt   time.Time
	EndAt     time.Time
	Evidence  []RevisionEvidence
}

type RevisionResult struct {
	RevisionID         string `json:"revision_id"`
	LogicalID          string `json:"logical_id"`
	Revision           int    `json:"revision"`
	ContinuityRevision int64  `json:"continuity_revision"`
	Sensitivity        string `json:"sensitivity"`
}

func (e RevisionEvidence) Validate() error {
	if e.SourceID == "" || (e.Relation != "SUPPORTS" && e.Relation != "CONTRADICTS" && e.Relation != "DERIVED_FROM") {
		return errors.New("invalid revision evidence")
	}
	switch e.Kind {
	case EvidenceMessage:
		if e.Quote == nil || e.CitationHash != "" {
			return errors.New("message revision evidence requires quote")
		}
	case EvidenceArtifactFragment:
		if e.CitationHash == "" || e.Quote != nil {
			return errors.New("artifact revision evidence requires citation hash")
		}
	case EvidenceCognitiveMemory:
		if e.Quote != nil || e.CitationHash != "" {
			return errors.New("memory revision evidence uses identity")
		}
	default:
		return errors.New("invalid revision evidence source")
	}
	return nil
}
