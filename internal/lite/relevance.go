// Per-step relevance injection (Channel C of the Stage 6B wiring plan).
//
// Serves a bounded, ranked packet of memories relevant to the CURRENT user
// query so a dsh host plugin can inject it on every pre-step — the automatic
// recall lane that reflex-at-session-start and on-demand tools do not cover.
//
// Governance invariants:
//   - The server ranks; the plugin is a dumb relay. Nothing here mutates
//     memory, so the evidence gate is untouched.
//   - F3: candidates come from the same search path as every other retrieval
//     (ACTIVE + NORMAL only; secret/instruction-like rows are never eligible).
//   - No activation bump: relevance search does NOT record access, so
//     injection cannot warm a memory into being re-injected forever (the
//     "clinginess" loop the ranking model was designed to avoid).
//   - Session de-dup: memories already surfaced this session (reflex packet,
//     explicit packet, or a previous relevance injection) are excluded, and
//     returned hits are marked surfaced for this session.
package lite

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/config"
	"mindmory.local/core/internal/httpapi"
	"mindmory.local/core/internal/retrieval"
)

const (
	relevanceDefaultMaxChars    = 1200
	relevanceMaxCharsCap        = 4000
	relevanceDefaultMaxMemories = 5
	relevanceMaxMemoriesCap     = 12
	// Automatic injection must be materially stricter than an explicit
	// search. A fuzzy match needs at least 70% distinctive-token coverage
	// before it is allowed into model context.
	relevanceStrongTokenCoverage = 0.70
)

// relevanceRequest is the wire input for POST /v1/context/relevance.
type relevanceRequest struct {
	SessionID   string `json:"session_id"`
	Query       string `json:"query"`
	MaxChars    int    `json:"max_chars,omitempty"`
	MaxMemories int    `json:"max_memories,omitempty"`
	StrongOnly  bool   `json:"strong_only,omitempty"`
}

// relevanceResponse is the bounded ranked packet for one pre-step.
type relevanceResponse struct {
	SessionID  string                `json:"session_id"`
	ProjectKey string                `json:"project_key,omitempty"`
	Query      string                `json:"query"`
	Memories   []retrieval.MemoryHit `json:"memories"`
	Truncated  bool                  `json:"truncated"`
}

// MarkSurfaced records memory ids as surfaced in a session so per-step
// relevance injection does not re-inject them. Ephemeral (in-memory).
func (s *Store) MarkSurfaced(sessionID string, memoryIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(memoryIDs) == 0 {
		return
	}
	perSession := s.surfaced[sessionID]
	if perSession == nil {
		perSession = map[string]time.Time{}
		s.surfaced[sessionID] = perSession
	}
	now := time.Now().UTC()
	for _, id := range memoryIDs {
		if id != "" {
			perSession[id] = now
		}
	}
}

// Surfaced returns a copy of the memory ids already surfaced in a session.
func (s *Store) Surfaced(sessionID string) map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]time.Time{}
	for id, at := range s.surfaced[sessionID] {
		out[id] = at
	}
	return out
}

// relevance handles POST /v1/context/relevance: a budgeted, deduplicated,
// no-bump ranked packet for the current user query.
func (s *Server) relevance(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request relevanceRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	query := strings.TrimSpace(request.Query)
	if !isUUID(request.SessionID) || query == "" || !utf8.ValidString(query) || len([]byte(query)) > retrieval.MaximumQueryBytes {
		s.fail(w, apperror.New(apperror.ContextQueryInvalid, false, nil))
		return
	}
	maxChars := request.MaxChars
	if maxChars <= 0 {
		maxChars = relevanceDefaultMaxChars
	} else if maxChars > relevanceMaxCharsCap {
		maxChars = relevanceMaxCharsCap
	}
	maxMemories := request.MaxMemories
	if maxMemories <= 0 {
		maxMemories = relevanceDefaultMaxMemories
	} else if maxMemories > relevanceMaxMemoriesCap {
		maxMemories = relevanceMaxMemoriesCap
	}
	scope, err := s.sessionScope(r.Context(), principal, request.SessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	started := time.Now()
	// recordAccess=false: injection is not use, so it must not warm memories
	// into a self-reinforcing re-injection loop.
	hits, err := s.searchMemories(r.Context(), scope, retrieval.SearchRequest{SessionID: request.SessionID, Query: query, Limit: maxMemories * 2, Mode: retrieval.SearchLexical}, false)
	if err != nil {
		s.ops("RELEVANCE", principal, request.SessionID, "error", apperror.Code(err), "", time.Since(started), map[string]any{"query": query})
		s.fail(w, err)
		return
	}
	surfaced := s.Store.Surfaced(scope.SessionID)
	out := relevanceResponse{SessionID: scope.SessionID, ProjectKey: scope.ProjectKey, Query: query, Memories: []retrieval.MemoryHit{}}
	budget := maxChars
	for _, h := range hits {
		if request.StrongOnly && !strongAutomaticMatch(h, query) {
			continue
		}
		if len(out.Memories) >= maxMemories {
			out.Truncated = true
			break
		}
		if _, seen := surfaced[h.MemoryID]; seen {
			continue
		}
		n := len(h.Subject) + len(h.Content)
		if n > budget {
			out.Truncated = true
			continue
		}
		out.Memories = append(out.Memories, h)
		budget -= n
	}
	ids := make([]string, 0, len(out.Memories))
	for _, h := range out.Memories {
		ids = append(ids, h.MemoryID)
	}
	s.Store.MarkSurfaced(scope.SessionID, ids)
	s.ops("RELEVANCE", principal, request.SessionID, "ok", "", "", time.Since(started), map[string]any{
		"query": query, "memories": len(out.Memories), "truncated": out.Truncated,
	})
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func strongAutomaticMatch(hit retrieval.MemoryHit, query string) bool {
	if hit.MatchClass >= int(MatchContent) {
		return true
	}
	if hit.MatchClass != int(MatchFuzzy) {
		return false
	}
	// The ranked hit's MatchStrength may carry normalized BM25 relevance, and
	// ClassifyMatch may choose trigram similarity before token coverage.
	// Compute distinctive-token coverage directly so a strong lexical match is
	// not rejected merely because it lives in a small index, while one shared
	// token in a long unrelated query still stays out.
	coverage := automaticRelevanceCoverage(query, hit.Subject+" "+hit.Content)
	return coverage >= relevanceStrongTokenCoverage
}

// automaticRelevanceCoverage recognizes a deliberately tiny set of
// high-confidence paraphrases for the no-model injection path. Canonicalizing
// instead of expanding keeps the denominator stable: an unrelated record that
// shares only "funds" still covers one of the query's concepts, while
// "store emergency funds" can match a record phrased as "keep emergency
// money". Candidate
// retrieval must still find a real lexical anchor before this gate runs.
func automaticRelevanceCoverage(query, text string) float64 {
	canonicalize := func(tokens map[string]bool) map[string]bool {
		out := make(map[string]bool, len(tokens))
		for token := range tokens {
			switch token {
			case "keep", "put", "store":
				token = "place"
			case "cash", "money", "fund", "funds":
				token = "funds"
			}
			out[token] = true
		}
		return out
	}
	q := canonicalize(retrievalTokenSet(query))
	if len(q) == 0 {
		return 0
	}
	t := canonicalize(retrievalTokenSet(text))
	matched := 0
	for token := range q {
		if t[token] {
			matched++
		}
	}
	return float64(matched) / float64(len(q))
}
