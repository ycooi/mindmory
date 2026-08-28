// Package lite: memory model algorithms (P0 fixes after expert review).
//
// The expert review identified that importance, activation, confidence and
// validity were conflated inside a single "heat" number, that a misled
// memory could rehabilitate itself by time alone, and that single-float
// ranking saturated at 1.0. These functions implement the corrected model:
//
//	Importance  — persistent, explicitly assigned, never the decay floor.
//	Activation  — decays by session toward zero; refreshed only by USE.
//	Confidence  — changed only by evidence/feedback; never recovers by time.
//	Validity    — ACTIVE / SUPERSEDED / FORGOTTEN / DISPUTED (lifecycle +
//	              disputed flag).
//
// Ranking is lexicographic by match class first, then deterministic
// tie-breakers, instead of one saturated float. A display score is computed
// separately for the wire contract.
package lite

import (
	"math"
	"strings"
	"unicode"

	"mindmory.local/core/internal/retrieval"
)

// ActivationTauSessions returns the activation half-life-ish time constant
// in sessions. It is NOT anchored to importance — a memory cools by usage
// absence, not by how important it is. Identity kinds (USER_PREFERENCE /
// PERSONAL_GOAL / PERSONAL_CONSTRAINT) never reach this function: they are
// the anchors of who Ember is and never decay (see ActivationFor).
func ActivationTauSessions(importance float64) float64 {
	switch {
	case importance >= 0.9:
		return 90.0
	case importance >= 0.7:
		return 60.0
	case importance >= 0.5:
		return 45.0
	case importance >= 0.3:
		return 30.0
	default:
		return 15.0
	}
}

// ActivationFor returns the effective activation for a memory. Identity-core
// memories (USER_PREFERENCE / PERSONAL_GOAL / PERSONAL_CONSTRAINT) NEVER
// decay — they are the anchors of identity, kept at full activation every
// session by design (the project owner's explicit decision, overruling the expert's
// 'permanent storage != permanent hotness' suggestion for these kinds).
// All other memories decay by session absence.
func ActivationFor(kind string, activation float64, sessionsSince int64, importance float64) float64 {
	if retrieval.IdentityKind(kind) {
		return activation
	}
	return ActivationEffective(activation, sessionsSince, ActivationTauSessions(importance))
}

// ActivationEffective decays activation by the number of sessions since the
// memory's last meaningful use. There is NO importance floor: activation
// cools toward zero, so an unused memory stops dominating retrieval even if
// it is important. It can be re-warmed only by being used again.
func ActivationEffective(activation float64, sessionsSince int64, tau float64) float64 {
	if activation <= 0 || sessionsSince <= 0 {
		return activation
	}
	if tau <= 0 {
		return 0
	}
	return activation * math.Exp(-float64(sessionsSince)/tau)
}

// MatchClass is the retrieval-match ladder from the expert review. Exact
// subject matches are a protected class that always outranks fuzzy/semantic
// matches, without relying on float band tuning.
type MatchClass int

const (
	MatchNone     MatchClass = 0
	MatchSemantic MatchClass = 1 // vector-only
	MatchFuzzy    MatchClass = 2 // trigram / token overlap
	MatchContent  MatchClass = 3 // exact content phrase
	MatchSubject  MatchClass = 4 // subject phrase/substring
	MatchExact    MatchClass = 5 // exact subject equality
)

// MatchResult captures the class and within-class strength of a query hit.
type MatchResult struct {
	Class    MatchClass
	Strength float64 // 0..1 within-class; ties broken by this
}

// ClassifyMatch deterministically classifies how a memory matches a query,
// mirroring the old band ladder but exposing class + strength separately so
// ranking can be lexicographic instead of saturated.
func ClassifyMatch(row MemoryRow, query string) MatchResult {
	q := lowerTrim(query)
	if q == "" {
		return MatchResult{Class: MatchNone}
	}
	subject := lowerTrim(row.Subject)
	content := lowerTrim(row.Content)
	switch {
	case subject == q:
		return MatchResult{Class: MatchExact, Strength: 1.0}
	case containsCJK(q):
		// CJK subject phrase or content phrase (no word boundaries to lean on).
		if subjectPhraseContains(subject, q) {
			return MatchResult{Class: MatchSubject, Strength: MatchStrength(query, subject)}
		}
		if contentPhraseContains(content, q) {
			return MatchResult{Class: MatchContent, Strength: MatchStrength(query, content)}
		}
		// Mixed-language query ("示例用户 linkedin"): the CJK and ASCII tokens
		// are not contiguous in the subject, but each may match separately.
		// A subject containing both tokens ("Example Person 的中文名是示例用户,
		// LinkedIn 是…") is a genuine subject match, not a fuzzy one.
		if mixed := mixedTokenSubjectMatch(subject, q); mixed {
			return MatchResult{Class: MatchSubject, Strength: MatchStrength(query, subject)}
		}
	case phraseContains(subject, q):
		return MatchResult{Class: MatchSubject, Strength: MatchStrength(query, subject)}
	case phraseContains(content, q):
		return MatchResult{Class: MatchContent, Strength: MatchStrength(query, content)}
	}
	sim := trigramSimilarity(q, subject+" "+content)
	if sim >= 0.3 {
		return MatchResult{Class: MatchFuzzy, Strength: sim}
	}
	if overlapStrength(q, subject+" "+content) > 0 {
		return MatchResult{Class: MatchFuzzy, Strength: 0.4 + 0.4*overlapStrength(q, subject+" "+content)}
	}
	// Semantic candidates are classified by the vector path, not here.
	return MatchResult{Class: MatchNone}
}

// lowerTrim lowercases and trims (runes-aware trim not needed for scoring).
func containsCJK(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func lowerTrim(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func phraseContains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	// Word-boundary phrase match: needle as a contiguous run inside haystack.
	return strings.Contains(haystack, needle)
}

func subjectPhraseContains(subject, needle string) bool { return strings.Contains(subject, needle) }
func contentPhraseContains(content, needle string) bool { return strings.Contains(content, needle) }

// MatchStrength measures how much of the QUERY is evidenced in the matched
// field — the fraction of query tokens found intact in the field. It is
// field-length independent: a long-title memory with the full query present
// is not penalized for being long. The secondary coverage bonus (query char
// share of field) only breaks ties between equal token-hit fractions.
func MatchStrength(query, field string) float64 {
	q := tokenSet(query)
	if len(q) == 0 {
		return 0
	}
	fieldLower := strings.ToLower(field)
	hit := 0
	for token := range q {
		// whole-token presence in the field (not just substring of a longer
		// word) — tokenSet already produced word-boundary tokens from query,
		// and the field is scanned for each as a token.
		if fieldTokenPresent(fieldLower, token) {
			hit++
		}
	}
	ratio := float64(hit) / float64(len(q))
	if ratio == 0 {
		return 0
	}
	// Secondary tie-break: query char share of field, capped at 0.1.
	// Deliberately NOT applied when the query is a single CJK token: a
	// long subject ("我的中文名是余烬 我是中国人 我是双语宝宝") that
	// contains the query is just as relevant as a short one ("我是双语宝宝"),
	// and penalizing the longer field for its length buried identity
	// memories under short project notes. With one token the ratio is
	// already 1.0, so coverage adds nothing but length bias.
	coverage := 0.0
	if len(q) > 1 || !containsCJK(query) {
		coverage = float64(len(strings.ToLower(strings.TrimSpace(query)))) / float64(max(1, len(fieldLower)))
		if coverage > 0.1 {
			coverage = 0.1
		}
	}
	return round6(ratio*0.9 + coverage*0.1)
}

// fieldTokenPresent reports whether needle occurs in field delimited by
// non-word runes on both sides (word-boundary match).
func fieldTokenPresent(field, needle string) bool {
	runes := []rune(field)
	needleRunes := []rune(needle)
	if len(needleRunes) == 0 {
		return false
	}
	for i := 0; i+len(needleRunes) <= len(runes); i++ {
		if string(runes[i:i+len(needleRunes)]) != needle {
			continue
		}
		beforeOK := i == 0 || !isWordRune(runes[i-1])
		afterOK := i+len(needleRunes) >= len(runes) || !isWordRune(runes[i+len(needleRunes)])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isWordRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func overlapStrength(query, text string) float64 {
	q := tokenSet(query)
	if len(q) == 0 {
		return 0
	}
	t := tokenSet(text)
	matched := 0
	for token := range q {
		if t[token] {
			matched++
		}
	}
	if matched == 0 {
		for _, run := range cjkRuns(query) {
			if len([]rune(run)) >= 2 && strings.Contains(text, run) {
				return 0.6
			}
		}
		return 0
	}
	return float64(matched) / float64(len(q))
}

// confidenceFactor down-weights retrieval for low-confidence/disputed
// memories. It is a multiplier, never zero (the record remains retrievable).
func confidenceFactor(confidence float64, disputed bool) float64 {
	if disputed {
		return 0.25
	}
	switch {
	case confidence >= 0.9:
		return 1.0
	case confidence >= 0.7:
		return 0.85
	case confidence >= 0.5:
		return 0.6
	case confidence >= 0.3:
		return 0.4
	default:
		return 0.25
	}
}

// DisplayScore computes a 0..1 presentation score from the match result and
// model state. It is for the wire contract only — internal ordering uses
// the lexicographic key (see rankMemories).
func DisplayScore(m MatchResult, activation float64, confidence float64, disputed bool, project bool) float64 {
	if m.Class == MatchNone {
		return 0
	}
	base := float64(m.Class) / 5.0 // 0.2..1.0 by class
	// Nudge by strength within class (keeps presentation monotone with rank).
	base += 0.1 * m.Strength
	if project {
		base += 0.05
	}
	score := base * confidenceFactor(confidence, disputed) * activationFactor(activation)
	if score > 1.0 {
		score = 1.0
	}
	return round6(score)
}

// activationFactor maps activation into a 0..1 retrieval factor. Activation
// is in [0,1] by construction; keep it as a direct multiplier (a fully
// cooled memory still retrievable, just not promoted).
func activationFactor(activation float64) float64 {
	if activation <= 0 {
		return 0.1
	}
	if activation > 1 {
		return 1
	}
	return activation
}

// RankKey is the lexicographic ordering key from the expert review. Exact
// subject matches are a protected class; everything else is compared field
// by field, never collapsed into one saturated float.
type RankKey struct {
	ScopeEligible int        // 1 = eligible
	Class         MatchClass // 5 exact .. 1 semantic
	// IdentityCoreGlobal: 2 = identity-kind + GLOBAL ("who I am"),
	// 1 = identity-kind + PROJECT (project-scoped preference/note),
	// 0 = not identity. GLOBAL identity memories outrank project-scoped
	// ones at the same class: a project note that merely contains the
	// query term must not beat the actual identity statement.
	IdentityCoreGlobal int
	Strength           float64 // within-class strength (0..1)
	Confidence         float64 // confidence factor
	Validity           int     // ACTIVE=2, SUPERSEDED/DISPUTED=1, FORGOTTEN=0
	Activation         float64 // session-decayed activation
	Importance         float64
	UpdatedAt          int64 // unix nanos, desc
	MemoryID           string
}

// RankKeyFor builds the key for a memory row against a query.
func RankKeyFor(row MemoryRow, match MatchResult, sessionsSince int64) RankKey {
	eff := ActivationFor(row.Kind, row.Activation, sessionsSince, row.Importance)
	validity := 2
	switch row.Lifecycle {
	case "ACTIVE":
		if row.Disputed {
			validity = 1
		} else {
			validity = 2
		}
	case "SUPERSEDED":
		validity = 1
	case "FORGOTTEN":
		validity = 0
	default:
		validity = 0
	}
	identityGlobal := 0
	if retrieval.IdentityKind(row.Kind) {
		if row.ScopeType == "GLOBAL" {
			identityGlobal = 2
		} else {
			identityGlobal = 1
		}
	}
	return RankKey{
		ScopeEligible: 1,
		Class:         match.Class,
		// GLOBAL identity memories (the anchors of who Ember is, never
		// decaying) rank ahead of project-scoped ones at the same class,
		// even when the project note has slightly higher raw strength: a
		// note that merely contains the query term must not beat the
		// identity statement itself. Static property — cannot go stale or
		// favor overused memories, so no recency-bias "clinginess".
		IdentityCoreGlobal: identityGlobal,
		Strength:           match.Strength,
		Confidence:         confidenceFactor(row.Confidence, row.Disputed),
		Validity:           validity,
		Activation:         eff,
		Importance:         row.Importance,
		UpdatedAt:          row.UpdatedAt.UnixNano(),
		MemoryID:           row.MemoryID,
	}
}

// lessKey reports whether key a ranks before key b (lexicographic, desc on
// numeric fields except memory_id which is the final asc tiebreak).
func lessKey(a, b RankKey) bool {
	if a.ScopeEligible != b.ScopeEligible {
		return a.ScopeEligible > b.ScopeEligible
	}
	if a.Class != b.Class {
		return a.Class > b.Class
	}
	if a.IdentityCoreGlobal != b.IdentityCoreGlobal {
		return a.IdentityCoreGlobal > b.IdentityCoreGlobal
	}
	if a.Strength != b.Strength {
		return a.Strength > b.Strength
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if a.Validity != b.Validity {
		return a.Validity > b.Validity
	}
	if a.Activation != b.Activation {
		return a.Activation > b.Activation
	}
	if a.Importance != b.Importance {
		return a.Importance > b.Importance
	}
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.MemoryID < b.MemoryID
}

// mixedTokenSubjectMatch reports whether every whitespace-separated token
// of a mixed CJK+ASCII query occurs somewhere in the subject. Used for
// queries like "示例用户 linkedin" where the tokens are not contiguous in
// the stored subject but are all genuinely present.
func mixedTokenSubjectMatch(subject, query string) bool {
	fields := strings.Fields(query)
	if len(fields) < 2 {
		return false
	}
	for _, token := range fields {
		if !strings.Contains(subject, strings.ToLower(token)) {
			return false
		}
	}
	return true
}
