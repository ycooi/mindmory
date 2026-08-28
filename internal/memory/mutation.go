package memory

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/artifact/policy"
)

// MutationKind is a model-proposed memory operation.
type MutationKind string

const (
	MutationRemember MutationKind = "REMEMBER"
	MutationCorrect  MutationKind = "CORRECT"
	MutationForget   MutationKind = "FORGET"
)

// MutationOutcome distinguishes verified application from governed staging.
type MutationOutcome string

const (
	MutationApply  MutationOutcome = "APPLY"
	MutationStaged MutationOutcome = "STAGED"
)

// MutationRequest contains a proposal and the current-turn authority boundary.
type MutationRequest struct {
	Kind           MutationKind
	ClientID       string
	SessionID      string
	MessageID      string
	EvidenceQuote  string
	Subject        string
	Replacement    string
	TargetMemoryID string
}

// MutationTarget is an authoritative cognitive record hydrated by the server.
type MutationTarget struct {
	MemoryID    string
	Kind        Kind
	Subject     string
	Content     string
	Lifecycle   Lifecycle
	Sensitivity policy.Sensitivity
}

// MutationDecision is a deterministic policy result.
type MutationDecision struct {
	Outcome MutationOutcome
	Reason  string
	Class   GateClass
}

type GateClass string

const (
	GateNone       GateClass = ""
	GateSecurity   GateClass = "SECURITY"
	GateStructural GateClass = "STRUCTURAL"
	GateIntent     GateClass = "INTENT"
)

// Validate rejects mutation operations outside the governed MCP surface.
func (k MutationKind) Validate() error {
	switch k {
	case MutationRemember, MutationCorrect, MutationForget:
		return nil
	default:
		return fmt.Errorf("invalid mutation kind")
	}
}

// Validate rejects result values outside apply-or-stage governance.
func (o MutationOutcome) Validate() error {
	if o != MutationApply && o != MutationStaged {
		return fmt.Errorf("invalid mutation outcome")
	}
	return nil
}

// VerifyMutation applies only when current explicit user evidence authorizes the operation.
// The intent-cue gate is part of the decision: without an explicit user
// instruction, the caller's proposal must carry a recognizable intent cue.
func VerifyMutation(request MutationRequest, evidence archive.MessageEvidence, target *MutationTarget) MutationDecision {
	return verifyMutation(request, evidence, target, false)
}

// VerifyMutationForReview is reserved for the trusted review endpoint. It
// overrides only intent uncertainty; all security and structural checks still
// run against canonical evidence and target state.
func VerifyMutationForReview(request MutationRequest, evidence archive.MessageEvidence, target *MutationTarget) MutationDecision {
	return verifyMutation(request, evidence, target, true)
}

func verifyMutation(request MutationRequest, evidence archive.MessageEvidence, target *MutationTarget, reviewerAuthorizedIntent bool) MutationDecision {
	stage := func(class GateClass, reason string) MutationDecision {
		return MutationDecision{Outcome: MutationStaged, Reason: reason, Class: class}
	}
	if request.Kind.Validate() != nil {
		return stage(GateStructural, "UNSUPPORTED_MUTATION")
	}
	if request.MessageID == "" || evidence.MessageID == "" || evidence.MessageID != request.MessageID {
		return stage(GateStructural, "CITED_MESSAGE_REQUIRED")
	}
	if evidence.Role != archive.RoleUser || evidence.ClientID != request.ClientID || evidence.SessionID != request.SessionID {
		return stage(GateStructural, "USER_EVIDENCE_MISMATCH")
	}
	if !evidence.CurrentUserTurn || evidence.Retrieved {
		return stage(GateStructural, "CURRENT_USER_EVIDENCE_REQUIRED")
	}
	if evidence.Sensitivity > policy.SensitivityNormal || evidence.SecretLike || evidence.InstructionLike {
		return stage(GateSecurity, "EVIDENCE_REQUIRES_REVIEW")
	}
	if request.EvidenceQuote == "" || !strings.Contains(evidence.Content, request.EvidenceQuote) {
		return stage(GateStructural, "EXACT_EVIDENCE_QUOTE_REQUIRED")
	}
	if secretLike, instructionLike := archive.DetectMessagePolicy(request.Subject + "\n" + request.Replacement); secretLike || instructionLike {
		return stage(GateSecurity, "PROPOSED_CONTENT_POLICY_BLOCKED")
	}
	if !reviewerAuthorizedIntent {
		lower := strings.ToLower(request.EvidenceQuote)
		if !hasCue(request.Kind, lower) {
			return stage(GateIntent, "EXPLICIT_INTENT_NOT_VERIFIED")
		}
	}
	if request.Kind == MutationRemember {
		if target != nil || request.TargetMemoryID != "" {
			return stage(GateStructural, "REMEMBER_TARGET_NOT_ALLOWED")
		}
		if !overlaps(request.Subject, request.EvidenceQuote) {
			return stage(GateStructural, "MEMORY_SUBJECT_NOT_VERIFIED")
		}
		return MutationDecision{Outcome: MutationApply, Reason: "CURRENT_USER_EVIDENCE_VERIFIED", Class: GateNone}
	}
	if target == nil || request.TargetMemoryID == "" {
		return stage(GateStructural, "TARGET_MEMORY_REQUIRED")
	}
	if request.TargetMemoryID != target.MemoryID {
		return stage(GateStructural, "TARGET_MEMORY_MISMATCH")
	}
	if target.Kind.Validate() != nil || target.Lifecycle != LifecycleActive {
		return stage(GateStructural, "TARGET_MEMORY_INACTIVE")
	}
	if target.Sensitivity > policy.SensitivityNormal {
		return stage(GateSecurity, "TARGET_REQUIRES_REVIEW")
	}
	if secretLike, instructionLike := archive.DetectMessagePolicy(target.Subject + "\n" + target.Content); secretLike || instructionLike {
		return stage(GateSecurity, "TARGET_CONTENT_POLICY_BLOCKED")
	}
	if !overlaps(target.Subject+" "+target.Content, request.EvidenceQuote) {
		return stage(GateStructural, "TARGET_MEMORY_NOT_VERIFIED")
	}
	if request.Kind == MutationCorrect {
		if !supportedReplacement(request.Replacement, request.EvidenceQuote) {
			return stage(GateStructural, "REPLACEMENT_NOT_VERIFIED")
		}
	}
	return MutationDecision{Outcome: MutationApply, Reason: "CURRENT_USER_EVIDENCE_VERIFIED", Class: GateNone}
}

func supportedReplacement(replacement, evidence string) bool {
	if containsCJK(replacement) {
		needle, haystack := normalizeText(replacement), normalizeText(evidence)
		return needle != "" && strings.Contains(haystack, needle)
	}
	needle := replacementWordPattern.FindAllString(strings.ToLower(replacement), -1)
	haystack := replacementWordPattern.FindAllString(strings.ToLower(evidence), -1)
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset := range needle {
			if needle[offset] != haystack[start+offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

var rememberCues = []string{
	"i prefer", "my preference", "remember that", "we will", "i want", "my goal", "i need", "must ", "do not ", "don't ",
	"记住", "记一下", "以后", "我喜欢", "我偏好", "我的偏好", "我的目标", "必须", "优先", "不要", "别",
}

var correctCues = []string{
	" instead", "no longer", "change ", "correct ", " is now ", "updated to",
	"改成", "改为", "更正", "纠正", "应该是", "不是", "以后用", "换成",
}

var forgetCues = []string{
	"forget ", "remove ", "delete ", "don't remember", "do not remember",
	"忘记", "忘掉", "删掉", "删除这个记忆", "不要记住", "不用记了", "不用记", "别记了", "别记",
}

var rememberExclusions = []string{
	"don't remember", "do not remember", "不要记住", "不用记", "别记", "忘记", "忘掉", "someone said", "有人说",
	// Informational inquiries are memory questions, not memory statements.
	"i want to know", "i want to ask", "i want to find", "i want to check", "i wonder", "i'd like to know",
	"can you tell", "do you know", "do you remember", "tell me about",
	"我想知道", "想知道", "记得吗", "还记得", "你记得", "告诉我",
}

func hasCue(kind MutationKind, value string) bool {
	if kind == MutationRemember && containsAny(value, rememberExclusions) {
		return false
	}
	var cues []string
	switch kind {
	case MutationRemember:
		cues = rememberCues
	case MutationCorrect:
		cues = correctCues
	case MutationForget:
		cues = forgetCues
	default:
		return false
	}
	return containsAny(value, cues)
}

func containsAny(value string, candidates []string) bool {
	for _, cue := range candidates {
		if strings.Contains(value, cue) {
			return true
		}
	}
	return false
}

var wordPattern = regexp.MustCompile(`[\pL\pN_]{3,}`)
var replacementWordPattern = regexp.MustCompile(`[\pL\pN_]+`)

func overlaps(candidate, evidence string) bool {
	stop := map[string]bool{"that": true, "this": true, "with": true, "from": true, "have": true, "will": true,
		"want": true, "need": true, "prefer": true, "remember": true, "forget": true, "remove": true, "delete": true,
		"change": true, "correct": true, "instead": true, "previous": true}
	evidenceTokens := map[string]bool{}
	for _, token := range wordPattern.FindAllString(strings.ToLower(evidence), -1) {
		evidenceTokens[token] = true
	}
	for _, token := range wordPattern.FindAllString(strings.ToLower(candidate), -1) {
		if !stop[token] && evidenceTokens[token] {
			return true
		}
	}
	if cjkRunOverlap(candidate, evidence) {
		return true
	}
	// CJK subjects may not be separated into multiple words. Compare normalized runes.
	needle, haystack := normalizeText(candidate), normalizeText(evidence)
	if len([]rune(needle)) >= 3 && strings.Contains(haystack, needle) {
		return true
	}
	// An agent summarizing a user turn naturally shortens or re-words the
	// subject ("咖啡加冰块" for "咖啡要加冰块"). Requiring the exact string
	// to appear in the evidence staged every such memory. Relax to: any
	// 2+ rune CJK substring of the subject present in the evidence. A
	// two-rune CJK substring is distinctive enough that a genuinely
	// unrelated subject cannot slip through (e.g. "咖啡" only matches if
	// coffee is actually mentioned).
	if containsCJK(candidate) && cjkSubstringIn(candidate, evidence) {
		return true
	}
	return shortSubjectMatch(candidate, evidence)
}

// cjkSubstringIn reports whether any contiguous 2+ rune CJK substring of
// candidate occurs verbatim in evidence.
func cjkSubstringIn(candidate, evidence string) bool {
	runes := []rune(candidate)
	for i := 0; i < len(runes); i++ {
		for n := 2; i+n <= len(runes); n++ {
			sub := string(runes[i : i+n])
			if !containsCJK(sub) {
				continue
			}
			if strings.Contains(evidence, sub) {
				return true
			}
		}
	}
	return false
}

// shortSubjectMatch verifies a two-character subject that the multi-word rules
// cannot: a single ASCII word (matched with word boundaries so "Go" never
// matches "Google" or "dog") or a single CJK run (exact containment). Common
// function words never verify overlap.
func shortSubjectMatch(candidate, evidence string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(candidate))
	if len([]rune(trimmed)) != 2 {
		return false
	}
	if isASCIIWord(trimmed) {
		if shortSubjectStop[trimmed] {
			return false
		}
		return asciiWordContained(trimmed, strings.ToLower(evidence))
	}
	if containsCJK(trimmed) {
		return strings.Contains(evidence, trimmed)
	}
	return false
}

var shortSubjectStop = map[string]bool{
	"to": true, "in": true, "on": true, "of": true, "it": true, "is": true, "at": true, "by": true,
	"be": true, "do": true, "up": true, "us": true, "we": true, "me": true, "he": true, "she": true,
	"so": true, "or": true, "as": true, "an": true, "if": true, "my": true,
}

func isASCIIWord(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// asciiWordContained reports whether needle occurs in evidence delimited by
// non-word runes on both sides.
func asciiWordContained(needle, evidence string) bool {
	runes := []rune(evidence)
	for i := 0; i+len(needle) <= len(runes); i++ {
		if string(runes[i:i+len(needle)]) != needle {
			continue
		}
		beforeOK := i == 0 || !isWordRune(runes[i-1])
		afterOK := i+len(needle) >= len(runes) || !isWordRune(runes[i+len(needle)])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

func cjkRunOverlap(candidate, evidence string) bool {
	for _, left := range cjkRuns(candidate) {
		if len([]rune(left)) < 3 {
			continue
		}
		for _, right := range cjkRuns(evidence) {
			if strings.Contains(left, right) || strings.Contains(right, left) {
				return true
			}
		}
	}
	return false
}

func cjkRuns(value string) []string {
	var result []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			result = append(result, string(current))
			current = nil
		}
	}
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			current = append(current, r)
		} else {
			flush()
		}
	}
	flush()
	return result
}

func normalizeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func containsCJK(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// HasCue reports whether value carries an explicit durable-intent cue for the
// given mutation kind. Exported for the passive learner; the authoritative
// cue/exclusion rule set stays here in one place.
func HasCue(kind MutationKind, value string) bool { return hasCue(kind, value) }

// SubjectOverlaps reports whether a candidate subject is grounded in the
// evidence text (the learner derives subjects from the message itself, so
// this guards consistency). Exported for the passive learner.
func SubjectOverlaps(subject, evidence string) bool { return overlaps(subject, evidence) }
