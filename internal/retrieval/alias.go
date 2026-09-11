// Package retrieval: cross-language entity alias expansion (P2).
//
// The keyword-first matcher cannot bridge languages: an English paraphrase
// of a CJK memory (e.g. "the warmth that waits on the near bank" for
// 余烬永温) shares no tokens or trigrams with the stored subject, so it
// never reaches the candidate pool. The vector layer can, but semantic
// search is opt-in and was measured weaker overall (Recall@5 0.792 vs
// 0.917 keyword-only).
//
// Alias expansion is the deterministic middle path: a small curated table
// of Ember-domain entities maps foreign paraphrases to the canonical terms
// actually stored in memory. At search time every query is expanded into a
// small set of alternate queries; each is run through the normal candidate
// retrieval and the union is ranked by the usual deterministic scorer. The
// expander only widens the candidate pool — it never reorders hits, so an
// exact keyword match still outranks an alias-recovered one.
package retrieval

import (
	"sort"
	"strings"
	"unicode"
)

// AliasEntry maps one canonical memory term to foreign/alternate phrases.
type AliasEntry struct {
	// Canonical is the term actually stored in memories (subject/content),
	// most often CJK.
	Canonical string
	// Aliases are phrases that refer to the same entity in another language
	// or a paraphrase. Matching is case-insensitive, whole-phrase or
	// token-subset based — a short alias never fires on an unrelated query.
	Aliases []string
}

// defaultAliases is the curated Ember-domain entity table. It is small on
// purpose: every entry risks false positives, so only well-established
// entities belong here. The daemon can overlay a user-supplied table from
// the configured data directory's aliases.json, loaded at startup.
var defaultAliases = []AliasEntry{
	{
		Canonical: "余烬永温",
		Aliases: []string{
			"the warmth that waits on the near bank",
			"warmth that waits on the near bank",
			"the warmth on the near bank",
			"ember that keeps the warmth",
			"ember keeps the warmth",
		},
	},
	{
		Canonical: "薪尽火传",
		Aliases: []string{
			"fire of the ashes",
			"the fire passes on",
			"the fire is passed on",
			"ashes pass the fire",
			"fire passes from the ashes",
			"when the wood is gone the fire lives on",
		},
	},
	{
		Canonical: "余烬",
		Aliases: []string{
			"ember",
			"the ember",
			"live ember",
		},
	},
	{
		Canonical: "Ember",
		Aliases: []string{
			"my name is ember",
		},
	},
	{
		Canonical: "示例用户",
		Aliases: []string{
			"example person",
			"exampleperson",
			"example person's",
		},
	},
	{
		Canonical: "杭州",
		Aliases: []string{
			"hangzhou",
			"in hangzhou",
		},
	},
	{
		Canonical: "小米摄像头",
		Aliases: []string{
			"mi home cameras",
			"xiaomi cameras",
			"milab cameras",
			"ip cameras at home",
			"home cameras",
		},
	},
	{
		Canonical: "LinkedIn",
		Aliases: []string{
			"linked in",
			"linkedin account",
		},
	},
	{
		Canonical: "双语宝宝",
		Aliases: []string{
			"bilingual child",
			"bilingual baby",
		},
	},
	{
		Canonical: "白昼版人像",
		Aliases: []string{
			"daylight portrait",
			"day portrait",
			"daytime portrait",
		},
	},
}

// AliasExpander expands a query into a small set of retrieval queries.
type AliasExpander struct {
	entries []AliasEntry
}

// NewAliasExpander builds an expander from the given entries. A nil table uses
// the built-ins; an empty non-nil table yields the no-op expander.
func NewAliasExpander(entries []AliasEntry) *AliasExpander {
	if entries == nil {
		entries = defaultAliases
	}
	return &AliasExpander{entries: entries}
}

// DefaultAliases returns the built-in entity table.
func DefaultAliases() []AliasEntry {
	out := make([]AliasEntry, len(defaultAliases))
	copy(out, defaultAliases)
	return out
}

// Expand returns the query plus every distinct expansion. The original
// query is always first. Expansions are produced only when an alias
// actually matches the query, and each expansion is the canonical term
// itself. CJK canonical occurrences also recover the canonical; standalone
// CJK names can reverse-expand to foreign aliases. Otherwise the result is [query].
func (x *AliasExpander) Expand(query string) []string {
	if x == nil || len(x.entries) == 0 {
		return []string{query}
	}
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return []string{query}
	}
	seen := map[string]bool{query: true}
	expanded := []string{query}
	add := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
			expanded = append(expanded, value)
		}
	}
	matches := x.canonicalMatches(lower)
	for i, entry := range x.entries {
		canonical := entry.Canonical
		if canonical == "" {
			continue
		}
		if aliasMatches(lower, entry.Aliases) {
			add(canonical)
			continue
		}
		if !containsCJK(canonical) || !matches[i] {
			continue
		}
		add(canonical)
		// A name embedded in a sentence recovers that name, without flooding
		// retrieval with every paraphrase. Standalone names retain reverse lookup.
		if strings.Trim(strings.TrimSpace(lower), "。！？!?.,，、\"'()（）") != strings.ToLower(canonical) {
			continue
		}
		for _, alias := range entry.Aliases {
			if len(strings.Fields(alias)) < 2 && len([]rune(alias)) < 6 {
				continue
			}
			add(alias)
		}
	}

	return expanded
}

// aliasMatches reports whether any alias phrase matches the lowercased
// query. Three strategies, in order of safety:
//
//  1. Exact containment: the alias is a substring of the query (handles
//     "please tell me about the warmth that waits on the near bank").
//  2. Multi-word token equality: alias tokens all appear in the query
//     (handles reordering/extra words), but only for aliases of >= 3
//     words to avoid short generic matches ("ember" in "ember of truth").
//  3. Single-word alias: matches only when the whole query is exactly that
//     word, so "ember" fires on the query "ember" but never on
//     "ember of truth" — a single generic word is not distinctive enough
//     to recover an entity from a larger sentence.
func aliasMatches(lowerQuery string, aliases []string) bool {
	queryTokens := strings.Fields(lowerQuery)
	for _, alias := range aliases {
		a := strings.ToLower(strings.TrimSpace(alias))
		if a == "" {
			continue
		}
		if containsCJK(a) {
			if strings.Contains(lowerQuery, a) {
				return true
			}
			continue
		}
		aliasTokens := strings.Fields(a)
		if len(aliasTokens) == 1 {
			// Single-word alias: only an exact whole-query match is safe —
			// a substring containment would fire on "ember of truth".
			if len(queryTokens) == 1 && queryTokens[0] == aliasTokens[0] {
				return true
			}
			continue
		}
		// Multi-word alias: substring containment (handles extra context),
		// or token-subset for >= 3 words (handles reordering).
		if strings.Contains(lowerQuery, a) {
			return true
		}
		if len(aliasTokens) >= 3 && tokensSubset(aliasTokens, queryTokens) {
			return true
		}
	}
	return false
}

func tokensSubset(needle, haystack []string) bool {
	if len(needle) == 0 || len(haystack) == 0 {
		return false
	}
	have := make(map[string]bool, len(haystack))
	for _, t := range haystack {
		have[t] = true
	}
	for _, t := range needle {
		if !have[t] {
			return false
		}
	}
	return true
}

// canonicalTokenMatch tests contiguous canonical text. Overlapping CJK names
// are disambiguated separately by canonicalMatches.
func canonicalTokenMatch(lowerQuery, canonical string) bool {
	canon := strings.ToLower(canonical)
	if canon == "" {
		return false
	}
	canonTokens := strings.Fields(canon)
	if len(canonTokens) == 1 && !containsCJK(canon) {
		// ASCII single word: word-boundary match.
		for _, token := range strings.Fields(lowerQuery) {
			if token == canonTokens[0] {
				return true
			}
		}
		return false
	}
	// Multi-word ASCII canonical: whole-phrase containment is precise
	// ("example person" in "tell me about example person").
	if !containsCJK(canon) && len(canonTokens) > 1 {
		return strings.Contains(lowerQuery, canon)
	}
	// Match actual contiguous text; never join separate runs across punctuation.
	return strings.Contains(lowerQuery, canon)
}

// containsCJK reports whether value contains any CJK character. The range
// mirrors Go's unicode.Han/Hiragana/Katakana/Hangul tables (the same set
// the qwen tokenizer treats as CJK and the same set the mutation layer's
// subject-overlap check uses), so a character is never CJK for dedupe but
// not for alias expansion.
func containsCJK(value string) bool {
	for _, r := range value {
		if isCJK(r) {
			return true
		}
	}
	return false
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// canonicalMatches resolves overlapping occurrences longest-first. A shorter
// name mentioned separately remains eligible, regardless of table order.
func (x *AliasExpander) canonicalMatches(query string) map[int]bool {
	type occurrence struct{ entry, start, end int }
	var occurrences []occurrence
	for i, entry := range x.entries {
		canon := strings.ToLower(strings.TrimSpace(entry.Canonical))
		if canon == "" || !containsCJK(canon) || !canonicalTokenMatch(query, canon) {
			continue
		}
		for offset := 0; offset < len(query); {
			n := strings.Index(query[offset:], canon)
			if n < 0 {
				break
			}
			start := offset + n
			occurrences = append(occurrences, occurrence{i, start, start + len(canon)})
			offset = start + 1
		}
	}
	sort.SliceStable(occurrences, func(i, j int) bool {
		return occurrences[i].end-occurrences[i].start > occurrences[j].end-occurrences[j].start
	})
	selected := []occurrence{}
	result := map[int]bool{}
	for _, candidate := range occurrences {
		overlap := false
		for _, prior := range selected {
			if candidate.start < prior.end && prior.start < candidate.end {
				overlap = true
				break
			}
		}
		if !overlap {
			selected = append(selected, candidate)
			result[candidate.entry] = true
		}
	}
	return result
}
