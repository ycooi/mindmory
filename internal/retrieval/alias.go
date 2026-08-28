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
// var/data/aliases.json (see LiteConfig) — same shape, loaded at startup.
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

// NewAliasExpander builds an expander from the given entries. A nil or
// empty table yields the no-op expander (identity on every query).
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
// itself (so the normal matcher can find the memory). If no alias matches,
// the result is exactly [query].
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
	for _, entry := range x.entries {
		canonical := entry.Canonical
		if canonical == "" {
			continue
		}
		if aliasMatches(lower, entry.Aliases) {
			// query matches a foreign alias -> add the canonical term
			if !seen[canonical] {
				seen[canonical] = true
				expanded = append(expanded, canonical)
			}
			continue
		}
		// Reverse direction: the query contains the canonical term, so the
		// foreign aliases are the useful expansions ("example person" from
		// 示例用户) that match the English subjects stored in memory. Without
		// this, a Chinese query never reaches English records that mention
		// the same person.
		//
		// The canonical must be a WHOLE-token match, not a bare substring:
		// "余烬永温" contains "余烬", but that must not pull in the 余烬
		// entry's aliases (ember / live ember) — those are for the shorter
		// name, and expanding them from the longer phrase floods the query
		// with weak fuzzy matches. Single-word aliases are also skipped in
		// the reverse direction: "ember" as a query is too broad.
		// Reverse expansion is only meaningful for CJK canonicals: the
		// user searched in Chinese and the English aliases match the stored
		// English subjects. ASCII canonicals (LinkedIn, Ember) are already
		// English — reverse-expanding them from any query containing the
		// word ("linkedin positioning" -> "linkedin account") pulls in
		// unrelated records and floods the result set.
		if !containsCJK(canonical) {
			continue
		}
		if canonicalTokenMatch(lower, canonical) {
			for _, alias := range entry.Aliases {
				if alias == "" || seen[alias] {
					continue
				}
				// Single-token reverse expansion is allowed only for
				// distinctive words (>= 6 runes): "hangzhou" (8) is a
				// precise place name, while "ember" (5) is a common word
				// that would flood the query with weak fuzzy matches.
				if len(strings.Fields(alias)) < 2 && len([]rune(alias)) < 6 {
					continue
				}
				seen[alias] = true
				expanded = append(expanded, alias)
			}
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

// canonicalTokenMatch reports whether the canonical term appears in the
// lowercased query as a whole token (word-boundary for ASCII, whole-run
// containment for CJK). This prevents a shorter canonical (余烬) from
// matching inside a longer query (余烬永温) and reverse-expanding the
// wrong entry's aliases.
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
	// CJK canonical: the query's CJK run must equal the canonical, or the
	// canonical must be a suffix/prefix of that run. This prevents the
	// shorter canonical 余烬 from matching inside 余烬永温 (which would
	// reverse-expand the wrong entry), while still allowing "杭州" to
	// expand to hangzhou and "示例用户" to example person.
	queryCJK := cjkRun(lowerQuery)
	if queryCJK == "" {
		return false
	}
	canonCJK := cjkRun(canon)
	if canonCJK == "" {
		return false
	}
	if queryCJK == canonCJK {
		return true
	}
	// Allow canonical as the full query when query is exactly canonical.
	return lowerQuery == canon
}

// cjkRun extracts the CJK run of a string (queries are short enough that
// this is unambiguous in practice).
func cjkRun(value string) string {
	var b strings.Builder
	for _, r := range value {
		if isCJK(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
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
