// Package retrieval defines deterministic, read-only Stage 5A retrieval
// contracts. Retrieval can filter, rank, truncate, and structure authoritative
// state; it cannot mutate authority.
package retrieval

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mindmory.local/core/internal/artifact/policy"
	"mindmory.local/core/internal/memory"
)

const (
	DefaultSearchLimit = 8
	MaximumSearchLimit = 20
	// IdentityHitsMax bounds how many identity-core memories a single search
	// may return when the caller's limit would otherwise truncate them.
	// Identity memories are the anchors of who the agent is, so matched ones
	// are kept beyond the caller's limit, but the cap keeps an identity-heavy
	// future corpus from unboundedly growing every response.
	IdentityHitsMax   = 20
	MaximumQueryBytes = 1024
)

type SessionScope struct {
	SessionID  string `json:"session_id"`
	ClientKey  string `json:"-"`
	ProjectKey string `json:"project_key,omitempty"`
}

type TurnScope struct {
	Session       SessionScope `json:"session"`
	MessageID     string       `json:"message_id"`
	IsCurrentUser bool         `json:"is_current_user"`
}

type SearchRequest struct {
	SessionID string        `json:"session_id"`
	Query     string        `json:"query"`
	Limit     int           `json:"limit,omitempty"`
	Kinds     []memory.Kind `json:"kinds,omitempty"`
	Mode      SearchMode    `json:"mode,omitempty"`
}

type SearchMode string

const (
	SearchLexical          SearchMode = "LEXICAL"
	SearchSemanticFallback SearchMode = "SEMANTIC_FALLBACK"
	SearchSemantic         SearchMode = "SEMANTIC"
)

func (r SearchRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.Query) == "" || !utf8.ValidString(r.Query) || len([]byte(r.Query)) > MaximumQueryBytes {
		return errors.New("invalid retrieval query")
	}
	if r.Limit < 0 || r.Limit > MaximumSearchLimit {
		return errors.New("invalid retrieval result limit")
	}
	for _, kind := range r.Kinds {
		if kind.Validate() != nil {
			return errors.New("invalid memory kind filter")
		}
	}
	if r.Mode != "" && r.Mode != SearchLexical && r.Mode != SearchSemanticFallback && r.Mode != SearchSemantic {
		return errors.New("invalid retrieval search mode")
	}
	return nil
}

func (r SearchRequest) EffectiveLimit() int {
	if r.Limit == 0 {
		return DefaultSearchLimit
	}
	return r.Limit
}

type MemoryHit struct {
	MemoryID        string    `json:"memory_id"`
	Kind            string    `json:"kind"`
	Subject         string    `json:"subject"`
	Content         string    `json:"content"`
	Scope           string    `json:"scope"`
	ProjectKey      string    `json:"project_key,omitempty"`
	EpistemicStatus string    `json:"epistemic_status"`
	Score           float64   `json:"score"`
	UpdatedAt       time.Time `json:"updated_at"`
	// MatchClass is the retrieval match class (5 exact .. 1 semantic). It is
	// an internal ranking detail, not part of the wire contract.
	MatchClass    int     `json:"-"`
	MatchStrength float64 `json:"-"`
	// Stage 7A heat surface. Importance is the declared grade (also the decay
	// floor); Heat is the stored adaptive anchor; EffectiveHeat is the
	// session-clock-decayed value computed at read time; AccessCount counts
	// per-session uses. Grade is the re-quantized human reading of heat.
	Importance    float64 `json:"importance,omitempty"`
	Heat          float64 `json:"heat,omitempty"`
	EffectiveHeat float64 `json:"effective_heat,omitempty"`
	AccessCount   int64   `json:"access_count,omitempty"`
	RepeatCount   int64   `json:"repeat_count,omitempty"`
	Grade         string  `json:"grade,omitempty"`
}

// IdentityKind reports whether kind belongs to the identity core — the
// memories that never decay from neglect (§A3, Stage 7A). Mirrors the
// reflex identity classifier in the service layer.
func IdentityKind(kind string) bool {
	switch memory.Kind(kind) {
	case memory.KindUserPreference, memory.KindPersonalGoal, memory.KindPersonalConstraint:
		return true
	default:
		return false
	}
}

// EffectiveHeat applies the session-clock decay to a stored heat anchor:
//
//	heat_eff = floor + (heat − floor) · e^(−Δs / τ_s),   τ_s = τ₀ × (0.5 + heat)
//
// τ₀ per declared grade (floor): 1.0→90, 0.8→60, 0.6→45, 0.4→30, 0.2→15
// sessions. Decay asymptotically returns heat toward its own importance
// floor — never toward zero — so a critical commitment stays prominent.
// Identity kinds (USER_PREFERENCE / PERSONAL_GOAL / PERSONAL_CONSTRAINT)
// are structurally exempt: they never decay from neglect (Δs forced to 0).
func EffectiveHeat(importance, heat float64, sessionsSince int64, identity bool) float64 {
	floor := importance
	if identity {
		sessionsSince = 0
	}
	if heat <= floor {
		return heat
	}
	var tau0 float64
	switch {
	case floor >= 0.9:
		tau0 = 90
	case floor >= 0.7:
		tau0 = 60
	case floor >= 0.5:
		tau0 = 45
	case floor >= 0.3:
		tau0 = 30
	default:
		tau0 = 15
	}
	tau := tau0 * (0.5 + heat)
	if tau <= 0 {
		return floor
	}
	return floor + (heat-floor)*math.Exp(-float64(sessionsSince)/tau)
}

// HeatGrade re-quantizes an effective-heat float into the human 3-band
// reading (cold / warm / hot), mirroring the declared-grade floor semantics:
// a memory at or above its importance floor is warm by construction.
func HeatGrade(importance, effectiveHeat float64) string {
	switch {
	case effectiveHeat >= 0.7:
		return "hot"
	case effectiveHeat >= 0.4:
		return "warm"
	default:
		return "cold"
	}
}

// AccessKind is the per-use event type written to memory_access_events.
type AccessKind string

const (
	AccessRecall      AccessKind = "RECALL"
	AccessSearchHit   AccessKind = "SEARCH_HIT"
	AccessReflexCore  AccessKind = "REFLEX_CORE"
	AccessReflexImp   AccessKind = "REFLEX_IMPORTANT"
	AccessFeedback    AccessKind = "FEEDBACK"
	AccessMaintenance AccessKind = "MAINTENANCE"
)

// FeedbackRequest is the explicit helped/misled signal. Outcome maps to the
// FEEDBACK access event's outcome column; it is a suggestion only — heat
// change is governed by the maintenance pass.
type FeedbackRequest struct {
	SessionID string `json:"session_id"`
	MemoryID  string `json:"memory_id"`
	Outcome   string `json:"outcome"` // "helped" | "misled"
	Note      string `json:"note,omitempty"`
}

func (r FeedbackRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.MemoryID) == "" {
		return errors.New("invalid feedback request")
	}
	switch strings.TrimSpace(r.Outcome) {
	case "helped", "misled":
	default:
		return errors.New("invalid feedback outcome")
	}
	return nil
}

type MemoryRecall struct {
	MemoryID             string             `json:"memory_id"`
	Kind                 string             `json:"kind"`
	Subject              string             `json:"subject"`
	Content              string             `json:"content,omitempty"`
	Scope                string             `json:"scope"`
	ProjectKey           string             `json:"project_key,omitempty"`
	EpistemicStatus      string             `json:"epistemic_status"`
	Lifecycle            string             `json:"lifecycle"`
	SupersedesMemoryID   string             `json:"supersedes_memory_id,omitempty"`
	SupersededByMemoryID string             `json:"superseded_by_memory_id,omitempty"`
	ContentAvailability  string             `json:"content_availability"`
	Importance           float64            `json:"importance,omitempty"`
	Heat                 float64            `json:"heat,omitempty"`
	EffectiveHeat        float64            `json:"effective_heat,omitempty"`
	AccessCount          int64              `json:"access_count,omitempty"`
	RepeatCount          int64              `json:"repeat_count,omitempty"`
	Grade                string             `json:"grade,omitempty"`
	MessageEvidence      []MessageEvidence  `json:"message_evidence"`
	ArtifactEvidence     []ArtifactEvidence `json:"artifact_evidence"`
}

type MessageEvidence struct {
	Type         string `json:"type"`
	Relation     string `json:"relation"`
	MessageID    string `json:"message_id"`
	Quote        string `json:"quote,omitempty"`
	OccurredAt   string `json:"occurred_at"`
	QuoteHash    string `json:"quote_hash,omitempty"`
	Availability string `json:"availability"`
}

type ArtifactEvidence struct {
	Type         string          `json:"type"`
	Relation     string          `json:"relation"`
	FragmentID   string          `json:"fragment_id"`
	ArtifactID   string          `json:"artifact_id"`
	Excerpt      string          `json:"excerpt,omitempty"`
	Availability string          `json:"availability"`
	Locator      json.RawMessage `json:"locator"`
}

const (
	DefaultArtifactLimit = 8
	MaximumArtifactLimit = 20
	DefaultArtifactChars = 16000
	MaximumArtifactChars = 64000
	MaximumFragments     = 32
	DefaultContextChars  = 12000
	MaximumContextChars  = 32000
)

type ArtifactSearchRequest struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
}

func (r ArtifactSearchRequest) EffectiveLimit() int {
	if r.Limit == 0 {
		return DefaultArtifactLimit
	}
	return r.Limit
}
func (r ArtifactSearchRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.Query) == "" || !utf8.ValidString(r.Query) || len([]byte(r.Query)) > MaximumQueryBytes || r.Limit < 0 || r.Limit > MaximumArtifactLimit {
		return errors.New("invalid artifact search")
	}
	return nil
}

type ArtifactHit struct {
	ArtifactID       string  `json:"artifact_id"`
	Title            string  `json:"title"`
	OriginalFilename string  `json:"original_filename"`
	Purpose          string  `json:"purpose"`
	ArtifactKind     string  `json:"artifact_kind"`
	ArtifactRole     string  `json:"artifact_role"`
	ProjectKey       string  `json:"project_key,omitempty"`
	StorageState     string  `json:"storage_state"`
	ProcessingStatus string  `json:"processing_status"`
	Recoverability   string  `json:"recoverability"`
	SizeBytes        int64   `json:"size_bytes"`
	Score            float64 `json:"score,omitempty"`
}

type ArtifactReadRequest struct {
	SessionID  string `json:"session_id"`
	ArtifactID string `json:"artifact_id"`
	MaxChars   int    `json:"max_chars,omitempty"`
}

func (r ArtifactReadRequest) EffectiveMaxChars() int {
	if r.MaxChars == 0 {
		return DefaultArtifactChars
	}
	return r.MaxChars
}
func (r ArtifactReadRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.ArtifactID) == "" || r.MaxChars < 0 || r.MaxChars > MaximumArtifactChars {
		return errors.New("invalid artifact read")
	}
	return nil
}

type ArtifactRepresentation struct {
	RepresentationID string `json:"representation_id"`
	Type             string `json:"type"`
}
type ReadFragment struct {
	FragmentID string          `json:"fragment_id"`
	Locator    json.RawMessage `json:"locator"`
	Content    string          `json:"content"`
}
type ArtifactRead struct {
	Artifact       ArtifactHit             `json:"artifact"`
	Representation *ArtifactRepresentation `json:"representation,omitempty"`
	Fragments      []ReadFragment          `json:"fragments"`
	Availability   string                  `json:"availability"`
	Readable       bool                    `json:"readable"`
	Truncated      bool                    `json:"truncated"`
	ReturnedChars  int                     `json:"returned_chars"`
}

type ContextRequest struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query,omitempty"`
	MaxChars  int    `json:"max_chars,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// Reflex and explicit packet modes. Explicit is the general query-driven
// context path (the historical /v1/context/packet contract). Reflex is the
// Stage 6 Channel B session-start packet: bounded, token-budgeted, compiled
// outside model reasoning through the dedicated reflex route.
const (
	ReflexMode   = "reflex"
	ExplicitMode = "explicit"
)

func (r ContextRequest) EffectiveMode() string {
	if r.Mode == ReflexMode {
		return ReflexMode
	}
	return ExplicitMode
}

// Reflex packet section ceilings, per Stage 6 Channel B. Ceilings are caps,
// not quotas: empty sections are not padded. The section sum is 2200; the
// remaining 300 of the 2500-token hard maximum is an explicit reserve for
// JSON framing, headers, memory-id/hash/timestamp tokens, and tokenizer
// estimation error — the heuristic estimator (EstimatedTokens) undercounts
// punctuation and metadata, so the reserve is not spendable.
const (
	ReflexCoreBudget        = 400
	ReflexProjectBudget     = 600
	ReflexDeltaBudget       = 500
	ReflexImportantBudget   = 400
	ReflexLoopsBudget       = 300
	ReflexHardMaxTokens     = 2500
	ReflexCoreMaxItems      = 8
	ReflexImportantMaxItems = 8
)

// EstimatedTokens approximates model tokens for reflex budgeting. CJK and
// Hangul runes cost about one token each; other text averages four characters
// per token. A non-empty string costs at least one token.
func EstimatedTokens(value string) int {
	cjk, other := 0, 0
	for _, r := range value {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		default:
			other++
		}
	}
	if cjk == 0 && other == 0 {
		return 0
	}
	estimate := cjk + (other+3)/4
	if estimate < 1 {
		return 1
	}
	return estimate
}

func (r ContextRequest) EffectiveMaxChars() int {
	if r.MaxChars == 0 {
		return DefaultContextChars
	}
	return r.MaxChars
}
func (r ContextRequest) Validate() error {
	switch r.Mode {
	case "", ReflexMode, ExplicitMode:
	default:
		return errors.New("invalid context mode")
	}
	if strings.TrimSpace(r.SessionID) == "" || !utf8.ValidString(r.Query) || len([]byte(r.Query)) > MaximumQueryBytes || r.MaxChars < 0 || r.MaxChars > MaximumContextChars {
		return errors.New("invalid context request")
	}
	return nil
}

type ProjectContext struct {
	Revision      int             `json:"revision"`
	Objective     string          `json:"objective"`
	CurrentState  string          `json:"current_state"`
	Decisions     json.RawMessage `json:"decisions"`
	OpenQuestions json.RawMessage `json:"open_questions"`
	NextActions   json.RawMessage `json:"next_actions"`
}
type ContextPacket struct {
	Session          SessionScope       `json:"session"`
	ContinuityCursor string             `json:"continuity_cursor"`
	ProjectContext   *ProjectContext    `json:"project_context,omitempty"`
	Memories         []MemoryHit        `json:"memories"`
	Core             []MemoryHit        `json:"core,omitempty"`
	Important        []MemoryHit        `json:"important,omitempty"`
	OpenLoops        []string           `json:"open_loops,omitempty"`
	Delta            []ContinuityChange `json:"delta,omitempty"`
	Truncated        bool               `json:"truncated"`
	ReturnedChars    int                `json:"returned_chars"`
}

type DiffRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func (r DiffRequest) EffectiveLimit() int {
	if r.Limit == 0 {
		return 20
	}
	return r.Limit
}
func (r DiffRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || r.Limit < 0 || r.Limit > 100 {
		return errors.New("invalid diff request")
	}
	return nil
}

type ContinuityChange struct {
	ChangeKind      string    `json:"change_kind"`
	TargetKind      string    `json:"target_kind"`
	TargetID        string    `json:"target_id"`
	RelatedTargetID string    `json:"related_target_id,omitempty"`
	ProjectKey      string    `json:"project_key,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
type DiffResult struct {
	Changes    []ContinuityChange `json:"changes"`
	NextCursor string             `json:"next_cursor"`
}

// MemoryEligible applies authority before ranking. historical permits explicit
// known-ID lifecycle recall but never relaxes project or sensitivity policy.
func MemoryEligible(session SessionScope, scope memory.ScopeType, projectKey string, lifecycle memory.Lifecycle, sensitivity policy.Sensitivity, historical bool) bool {
	if sensitivity != policy.SensitivityNormal {
		return false
	}
	if !historical && lifecycle != memory.LifecycleActive {
		return false
	}
	if scope == memory.ScopeGlobal {
		return true
	}
	return scope == memory.ScopeProject && session.ProjectKey != "" && projectKey == session.ProjectKey
}
