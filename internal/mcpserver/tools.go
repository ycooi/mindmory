package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Runtime struct {
	Client               Client
	SessionID, MessageID string
}

// ContextInput carries the packet request. Mode selects which compiler runs:
// reflex is the bounded session-start wake-up packet; explicit is the
// query-driven refresh. Query is a retrieval focus, not a command.
type ContextInput struct {
	Query    string `json:"query,omitempty" jsonschema:"Optional retrieval focus. Empty = most-recent memories; non-empty = query-scored. Leave empty for a session-start refresh; pass a focus to steer an explicit packet."`
	Mode     string `json:"mode,omitempty" jsonschema:"Packet mode: 'reflex' = bounded session-start packet (identity core, important decisions, recent delta); 'explicit' = query-driven refresh (default)."`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"Character budget for the returned packet. Default 3000; raise only when more context is genuinely needed, lower to keep tokens small."`
}

// defaultContextChars is the MCP default for explicit packets: a deliberate
// refresh should be bounded (a full 12k-char packet is ~3k tokens per call);
// raise it explicitly only when more context is genuinely needed.
const defaultContextChars = 3000

type SearchInput struct {
	Query string `json:"query" jsonschema:"Search terms; returns matching memories AND artifact metadata. Keyword and semantic (vector) matches are blended — exact keyword hits rank first."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results, 1 to 20 (default 8)."`
}
type RecallInput struct {
	MemoryID string `json:"memory_id" jsonschema:"The memory_id from a previous search, packet, or diff result."`
}
type RememberInput struct {
	MemoryKind    string `json:"memory_kind" jsonschema:"Kind of memory: USER_PREFERENCE, PERSONAL_GOAL, PERSONAL_CONSTRAINT, PROJECT_DECISION, DOCUMENT_FACT, LESSON, CORRECTION, ENTITY_RELATION, or DATASET_FACT."`
	Scope         string `json:"scope" jsonschema:"Visibility: GLOBAL (applies everywhere) or PROJECT (this project only)."`
	Subject       string `json:"subject" jsonschema:"Short title for the memory; MUST be derived from and overlap the evidence_quote."`
	EvidenceQuote string `json:"evidence_quote" jsonschema:"EXACT substring quoted from the current user message — the memory's evidence. Governance requires it verbatim from the latest user turn; do not paraphrase."`
}
type CorrectInput struct {
	TargetMemoryID string `json:"target_memory_id" jsonschema:"memory_id of the memory to correct (from search/recall)."`
	Replacement    string `json:"replacement" jsonschema:"New content; the corrected memory keeps the old subject and supersedes the target."`
	EvidenceQuote  string `json:"evidence_quote" jsonschema:"EXACT substring from the current user message that states the correction; verbatim, not paraphrased."`
}
type ForgetInput struct {
	TargetMemoryID string `json:"target_memory_id" jsonschema:"memory_id of the memory to forget (from search/recall)."`
	EvidenceQuote  string `json:"evidence_quote" jsonschema:"EXACT substring from the current user message requesting deletion (e.g. includes 忘记/forget); verbatim, not paraphrased."`
}
type ArtifactInput struct {
	Query string `json:"query" jsonschema:"Search terms over artifact titles, filenames, and purpose."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results, 1 to 20 (default 8)."`
}
type ArtifactReadInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"artifact_id from artifact_search."`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"Character budget for the read (default 4000)."`
}
type DiffInput struct {
	Cursor string `json:"cursor,omitempty" jsonschema:"Opaque continuity cursor from a previous memory_context or memory_diff result. Empty = most recent changes."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum changes, 1 to 100 (default 20)."`
}
type FeedbackInput struct {
	MemoryID string `json:"memory_id" jsonschema:"memory_id of the memory being rated."`
	Outcome  string `json:"outcome" jsonschema:"Whether the memory helped or misled you: 'helped' warms it, 'misled' cools it."`
	Note     string `json:"note,omitempty" jsonschema:"Optional note on why; recorded with the signal."`
}
type ProposalReviewInput struct {
	Status string `json:"status,omitempty" jsonschema:"Proposal status filter: PENDING (default), APPLIED, REJECTED."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum proposals to return, 1 to 100 (default 50)."`
}
type OpsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Maximum journal events to return, 1 to 500 (default 50)."`
}

// knownToolNames is the authoritative tool inventory (mirrors Register).
// Order groups the menu by intent: reads first, then mutations, then
// artifacts, then the operational journal.
var knownToolNames = []string{
	"mindmory_status", "memory_context", "memory_search", "memory_recall", "memory_diff",
	"memory_remember", "memory_correct", "memory_forget", "memory_feedback",
	"artifact_search", "artifact_read", "ops_recent", "proposal_review",
}

func (r Runtime) Register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{Name: "mindmory_status", Description: "Read Mindmory's sanitized runtime configuration, canonical/derived folder layout, embedding model/settings, live record and vector counts, startup state, and incidents. This tool is read-only and never returns credentials or memory contents. When action is required, report the operator warning and commands exactly; never execute remediation as the agent."}, r.mindmoryStatus)
	// ── Read: what Mindmory knows ──────────────────────────────────────
	mcp.AddTool(server, &mcp.Tool{Name: "memory_context", Description: "Return a bounded packet of the current project's context and active memories. Use at session start (mode=reflex) or when prior decisions, preferences, constraints, or current state may matter (mode=explicit, optionally with a query focus). Returns a continuity_cursor for memory_diff."}, r.memoryContext)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_search", Description: "Search memories (and artifact metadata) for the current project and global scope. Keyword and semantic matches are blended; exact keyword hits rank first. Returns bounded snippets with scores and memory_id handles for memory_recall."}, r.contextSearch)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_recall", Description: "Recall one known memory by memory_id with its lifecycle, supersession chain, and exact quoted evidence from the archived message. Use after a search or packet returns a memory_id."}, r.memoryRecall)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_diff", Description: "Return changes since an opaque continuity cursor (from memory_context or a prior memory_diff): memory created/corrected/forgotten events. Empty cursor = most recent changes. Returns a next_cursor to continue."}, r.memoryDiff)

	// ── Write: governed mutations (evidence must quote the current turn) ─
	mcp.AddTool(server, &mcp.Tool{Name: "memory_remember", Description: "Propose remembering a preference, decision, goal, or constraint. GOVERNANCE: evidence_quote MUST be an exact substring of the current user message and subject must derive from it. The server alone detects durable intent; uncertain intent is staged for review."}, r.memoryRemember)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_correct", Description: "Propose correcting a memory. The server verifies exact current-user evidence, target state, replacement grounding, and correction intent."}, r.memoryCorrect)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_forget", Description: "Propose forgetting a memory. The server verifies exact current-user evidence, target state, and deletion intent."}, r.memoryForget)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_feedback", Description: "Signal whether a surfaced memory helped or misled you — warms or cools its heat so future packets rank it accordingly. A suggestion, not an edit."}, r.memoryFeedback)

	// ── Artifacts ─────────────────────────────────────────────────────
	mcp.AddTool(server, &mcp.Tool{Name: "artifact_search", Description: "Search artifact metadata (titles, filenames, purpose) visible to the project. Returns artifact_id handles for artifact_read."}, r.artifactSearch)
	mcp.AddTool(server, &mcp.Tool{Name: "artifact_read", Description: "Read the best bounded textual representation of an artifact by artifact_id; never returns raw binary, always a bounded excerpt."}, r.artifactRead)

	// ── The nerves: what Mindmory has done ────────────────────────────
	mcp.AddTool(server, &mcp.Tool{Name: "ops_recent", Description: "Return recent operational journal events — Mindmory's nerves: checkpoints, mutations (with governance reasons), searches, recalls, index rebuilds, embeds, and errors. Use to see what has been done over time or audit recent activity."}, r.opsRecent)
	mcp.AddTool(server, &mcp.Tool{Name: "proposal_review", Description: "List pending (or applied/rejected) memory proposals — memories that governance staged awaiting review because the evidence quote lacked an intent cue or exact match. Use to see what is queued; approval/rejection happens via the admin API, not here (read-only)."}, r.proposalReview)
}
func (r Runtime) mindmoryStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	status, err := r.Client.SystemStatus(ctx)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return nil, nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, nil, err
	}
	return success(output)
}
func (r Runtime) proposalReview(ctx context.Context, _ *mcp.CallToolRequest, in ProposalReviewInput) (*mcp.CallToolResult, any, error) {
	status := in.Status
	if status == "" {
		status = "PENDING"
	}
	v, e := r.Client.Do(ctx, http.MethodGet, queryPath("/v1/admin/proposals", map[string]string{"status": status, "limit": fmt.Sprint(in.Limit)}), nil)
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func success(v map[string]any) (*mcp.CallToolResult, any, error) { return nil, v, nil }
func (r Runtime) memoryContext(ctx context.Context, _ *mcp.CallToolRequest, in ContextInput) (*mcp.CallToolResult, any, error) {
	maxChars := in.MaxChars
	if maxChars == 0 {
		maxChars = defaultContextChars
	}
	v, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/packet", map[string]any{"session_id": r.SessionID, "query": in.Query, "mode": in.Mode, "max_chars": maxChars})
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func (r Runtime) contextSearch(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
	mem, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/search", map[string]any{"session_id": r.SessionID, "query": in.Query, "limit": in.Limit})
	if e != nil {
		return nil, nil, e
	}
	art, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/artifacts/search", map[string]any{"session_id": r.SessionID, "query": in.Query, "limit": in.Limit})
	if e != nil {
		return nil, nil, e
	}
	hits := make([]any, 0)
	if values, ok := mem["results"].([]any); ok {
		for _, raw := range values {
			if item, ok := raw.(map[string]any); ok {
				hits = append(hits, map[string]any{"type": "MEMORY", "id": item["memory_id"], "title": item["subject"], "snippet": snippetText(item["content"]), "score": item["score"], "project_scope": scopeLabel(item["scope"])})
			}
		}
	}
	if values, ok := art["results"].([]any); ok {
		for _, raw := range values {
			if item, ok := raw.(map[string]any); ok {
				snippet := item["purpose"]
				if snippet == "" {
					snippet = item["original_filename"]
				}
				hits = append(hits, map[string]any{"type": "ARTIFACT", "id": item["artifact_id"], "title": item["title"], "snippet": snippet, "score": item["score"], "project_scope": scopeLabel(item["project_key"]), "availability": item["storage_state"]})
			}
		}
	}
	return success(map[string]any{"hits": hits})
}
func scopeLabel(value any) string {
	if value == "GLOBAL" || value == nil || value == "" {
		return "GLOBAL"
	}
	return "CURRENT_PROJECT"
}

// snippetText bounds a search-hit excerpt so lookups carry context, not full
// content. 120 runes is enough to identify a memory without duplicating it.
func snippetText(value any) string {
	text, _ := value.(string)
	runes := []rune(text)
	if len(runes) <= 120 {
		return text
	}
	return string(runes[:120]) + "…"
}
func (r Runtime) memoryRecall(ctx context.Context, _ *mcp.CallToolRequest, in RecallInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodGet, queryPath("/v1/context/memories/"+in.MemoryID, map[string]string{"session_id": r.SessionID}), nil)
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}

// currentMessageID resolves the session's latest archived current-user message.
// Direct harness integration binds the MCP server once with a static profile
// message id that goes stale on the next turn, so mutations must re-resolve
// per call (the daemon answers ?latest=1 with the latest user turn).
func (r Runtime) currentMessageID(ctx context.Context) (string, error) {
	v, e := r.Client.Do(ctx, http.MethodGet, queryPath("/v1/context/sessions/"+r.SessionID, map[string]string{"latest": "1"}), nil)
	if e != nil {
		return "", e
	}
	id, _ := v["message_id"].(string)
	if id == "" {
		return "", errors.New("no current user message bound")
	}
	return id, nil
}
func (r Runtime) mutate(ctx context.Context, mutation string, v map[string]any) (*mcp.CallToolResult, any, error) {
	messageID, e := r.currentMessageID(ctx)
	if e != nil {
		return nil, nil, e
	}
	v["session_id"] = r.SessionID
	v["message_id"] = messageID
	v["mutation"] = mutation
	out, e := r.Client.Do(ctx, http.MethodPost, "/v1/memory/mutations", v)
	if e != nil {
		return nil, nil, e
	}
	return success(out)
}
func (r Runtime) memoryRemember(ctx context.Context, _ *mcp.CallToolRequest, in RememberInput) (*mcp.CallToolResult, any, error) {
	return r.mutate(ctx, "REMEMBER", map[string]any{"memory_kind": in.MemoryKind, "scope": in.Scope, "subject": in.Subject, "evidence_quote": in.EvidenceQuote})
}
func (r Runtime) memoryCorrect(ctx context.Context, _ *mcp.CallToolRequest, in CorrectInput) (*mcp.CallToolResult, any, error) {
	return r.mutate(ctx, "CORRECT", map[string]any{"target_memory_id": in.TargetMemoryID, "replacement": in.Replacement, "evidence_quote": in.EvidenceQuote})
}
func (r Runtime) memoryForget(ctx context.Context, _ *mcp.CallToolRequest, in ForgetInput) (*mcp.CallToolResult, any, error) {
	return r.mutate(ctx, "FORGET", map[string]any{"target_memory_id": in.TargetMemoryID, "evidence_quote": in.EvidenceQuote})
}
func (r Runtime) artifactSearch(ctx context.Context, _ *mcp.CallToolRequest, in ArtifactInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/artifacts/search", map[string]any{"session_id": r.SessionID, "query": in.Query, "limit": in.Limit})
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func (r Runtime) artifactRead(ctx context.Context, _ *mcp.CallToolRequest, in ArtifactReadInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodGet, queryPath("/v1/context/artifacts/"+in.ArtifactID+"/read", map[string]string{"session_id": r.SessionID, "max_chars": fmt.Sprint(in.MaxChars)}), nil)
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func (r Runtime) memoryDiff(ctx context.Context, _ *mcp.CallToolRequest, in DiffInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/diff", map[string]any{"session_id": r.SessionID, "cursor": in.Cursor, "limit": in.Limit})
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func (r Runtime) opsRecent(ctx context.Context, _ *mcp.CallToolRequest, in OpsInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodGet, queryPath("/v1/admin/ops", map[string]string{"limit": fmt.Sprint(in.Limit)}), nil)
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
func (r Runtime) memoryFeedback(ctx context.Context, _ *mcp.CallToolRequest, in FeedbackInput) (*mcp.CallToolResult, any, error) {
	v, e := r.Client.Do(ctx, http.MethodPost, "/v1/context/feedback", map[string]any{"session_id": r.SessionID, "memory_id": in.MemoryID, "outcome": in.Outcome, "note": in.Note})
	if e != nil {
		return nil, nil, e
	}
	return success(v)
}
