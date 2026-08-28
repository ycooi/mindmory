package lite

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"unicode"

	"mindmory.local/core/internal/artifact/policy"
)

// tupleHash is the length-prefixed SHA-256 tuple digest used for message
// hashes (matches the archive contract's MessageHash).
func tupleHash(values ...string) string {
	hasher := sha256.New()
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func hashContent(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func quoteHash(quote string) string {
	digest := sha256.Sum256([]byte(quote))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// --- quote binding (mirrors service/memory/evidence.go) ---

type quoteBinding struct {
	Hash       string
	StartByte  int
	EndByte    int
	ReasonCode string
}

const (
	reasonExactEvidenceRequired  = "EXACT_EVIDENCE_QUOTE_REQUIRED"
	reasonEvidenceAmbiguous      = "EVIDENCE_AMBIGUOUS"
	reasonEvidenceRequiresReview = "EVIDENCE_REQUIRES_REVIEW"
)

// bindExactQuote locates the single exact occurrence of quote in content and
// validates the byte range against the content hash.
func bindExactQuote(content, quote string) quoteBinding {
	needle := []byte(quote)
	if len(needle) == 0 {
		return quoteBinding{ReasonCode: reasonExactEvidenceRequired}
	}
	haystack := []byte(content)
	first, count := -1, 0
	for offset := 0; offset <= len(haystack)-len(needle); {
		index := indexBytes(haystack[offset:], needle)
		if index < 0 {
			break
		}
		absolute := offset + index
		if first < 0 {
			first = absolute
		}
		count++
		if count > 1 {
			return quoteBinding{ReasonCode: reasonEvidenceAmbiguous}
		}
		offset = absolute + 1
	}
	if count == 0 {
		return quoteBinding{ReasonCode: reasonExactEvidenceRequired}
	}
	digest := sha256.Sum256(needle)
	result := quoteBinding{Hash: "sha256:" + hex.EncodeToString(digest[:]), StartByte: first, EndByte: first + len(needle)}
	if validateQuote(content, result.StartByte, result.EndByte, result.Hash) != nil {
		return quoteBinding{ReasonCode: reasonExactEvidenceRequired}
	}
	return result
}

func validateQuote(content string, startByte, endByte int, quoteHashValue string) error {
	if startByte < 0 || endByte <= startByte || endByte > len(content) {
		return errQuoteMismatch
	}
	digest := sha256.Sum256([]byte(content)[startByte:endByte])
	want := "sha256:" + hex.EncodeToString(digest[:])
	if quoteHashValue != want {
		return errQuoteMismatch
	}
	return nil
}

var errQuoteMismatch = errMismatch{}

type errMismatch struct{}

func (errMismatch) Error() string { return "quote hash mismatch" }

func indexBytes(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if subtle.ConstantTimeCompare(haystack[i:i+len(needle)], needle) == 1 {
			return i
		}
	}
	return -1
}

// --- sensitivity helpers ---

func trigramSimilarity(left, right string) float64 {
	l := trigrams(strings.ToLower(left))
	r := trigrams(strings.ToLower(right))
	if len(l) == 0 || len(r) == 0 {
		return 0
	}
	union := map[string]bool{}
	for t := range l {
		union[t] = true
	}
	for t := range r {
		union[t] = true
	}
	shared := 0
	for t := range l {
		if r[t] {
			shared++
		}
	}
	if len(union) == 0 {
		return 0
	}
	return float64(shared) / float64(len(union))
}
func trigrams(value string) map[string]bool {
	runes := []rune(value)
	if len(runes) == 0 {
		return map[string]bool{}
	}
	if len(runes) == 1 {
		return map[string]bool{string(runes) + "  ": true, " " + string(runes) + " ": true, "  " + string(runes): true}
	}
	// pg_trgm pads with two leading spaces and one trailing space.
	padded := "  " + value + " "
	out := map[string]bool{}
	pr := []rune(padded)
	for i := 0; i+3 <= len(pr); i++ {
		out[string(pr[i:i+3])] = true
	}
	return out
}
func tokenSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, token := range wordTokens(value) {
		out[token] = true
	}
	return out
}
func wordTokens(value string) []string {
	var tokens []string
	var current []rune
	flush := func() {
		if len(current) >= 2 {
			tokens = append(tokens, string(current))
		}
		current = nil
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			current = append(current, r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
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
func round6(value float64) float64 {
	return float64(int64(value*1e6+0.5)) / 1e6
}

func inheritSensitivity(left, right policy.Sensitivity) policy.Sensitivity {
	return policy.InheritSensitivity(left, right)
}
