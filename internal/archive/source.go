package archive

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ExperienceSourceType identifies the trusted upstream capture mode for a session.
type ExperienceSourceType string

const (
	ExperienceSourceGenericCheckpoint ExperienceSourceType = "GENERIC_CHECKPOINT"
	ExperienceSourceHarnessNativeLog  ExperienceSourceType = "HARNESS_NATIVE_LOG"
	ExperienceSourceImport            ExperienceSourceType = "IMPORT"
)

const GenericCheckpointSourceName = "mindmory-checkpoint"

var (
	canonicalSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sourceName      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// ExperienceSource is server/integration-derived session provenance. It is not a public request.
type ExperienceSource struct {
	Type      ExperienceSourceType
	Name      string
	SessionID string
}

// GenericCheckpointSource assigns the direct checkpoint path's fixed provenance.
func GenericCheckpointSource(externalSessionID string) ExperienceSource {
	return ExperienceSource{Type: ExperienceSourceGenericCheckpoint, Name: GenericCheckpointSourceName, SessionID: externalSessionID}
}

// Validate rejects unknown capture modes and malformed source identity.
func (s ExperienceSource) Validate() error {
	if s.Type.Validate() != nil || !sourceName.MatchString(s.Name) || !validSourceID(s.SessionID) {
		return errors.New("invalid experience source")
	}
	return nil
}

// Validate rejects source modes outside the frozen cross-harness vocabulary.
func (s ExperienceSourceType) Validate() error {
	switch s {
	case ExperienceSourceGenericCheckpoint, ExperienceSourceHarnessNativeLog, ExperienceSourceImport:
		return nil
	default:
		return errors.New("invalid experience source type")
	}
}

// SourceEvent identifies an upstream native event. The empty value is correct for generic
// checkpoints, which must not fabricate native-log identity.
type SourceEvent struct {
	ID       string
	Sequence *int64
	Hash     string
}

// Validate requires a complete, canonical identity whenever upstream event provenance exists.
func (s SourceEvent) Validate() error {
	if s.ID == "" && s.Sequence == nil && s.Hash == "" {
		return nil
	}
	if !validSourceID(s.ID) || !IsCanonicalSHA256(s.Hash) || (s.Sequence != nil && *s.Sequence < 0) {
		return errors.New("invalid source event provenance")
	}
	return nil
}

// IsCanonicalSHA256 validates Mindmory's one canonical SHA-256 wire/storage format.
func IsCanonicalSHA256(value string) bool { return canonicalSHA256.MatchString(value) }

func validSourceID(value string) bool {
	return len(strings.TrimSpace(value)) > 0 && len(value) <= 512 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
