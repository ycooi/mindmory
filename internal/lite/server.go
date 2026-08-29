package lite

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/config"
	"mindmory.local/core/internal/httpapi"
	"mindmory.local/core/internal/identity"
	"mindmory.local/core/internal/lite/vectorstore"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// duplicateSubjectThreshold is the trigram-similarity cutoff above which a
// proposed REMEMBER subject is treated as a duplicate of an existing ACTIVE
// memory and staged for review. Near-identical subjects ("compliance rules"
// vs "compliance rules (war-room)") score well above it; genuinely distinct
// subjects score far below.
const duplicateSubjectThreshold = 0.75

// Server is the single-process Mindmory control plane.
type Server struct {
	mutationMu      sync.Mutex
	vectorRebuildMu sync.Mutex
	rateMu          sync.Mutex
	rateWindows     map[string]rateWindow
	Store           *Store
	Auth            *auth.MCPAuthenticator
	Cursor          []byte
	Owner           string
	AdminToken      string
	IntegrityError  error
	// TrustLocal skips model-facing Bearer checks on an explicit loopback
	// bind. Administrative routes still require the admin token.
	TrustLocal bool
	// LocalClientKey is the client key used for TrustLocal (loopback)
	// authentication. It is derived from the configured MCP client tokens
	// (the single local caller) rather than hard-coded, so the daemon has
	// no baked-in harness runtime concept.
	LocalClientKey string
	// LearnerKey is the MCP client key the passive learner runs as (the
	// first configured principal; equals LocalClientKey in local-trust
	// mode). The learner proposes memories on behalf of the archive owner.
	LearnerKey string
	IDs        identity.Generator
	Log        *slog.Logger
	Session    *retrieval.SessionScope // bound continuity session for this deployment
	// Aliases expands queries through the cross-language entity alias table
	// (P2). Never nil — NewServer wires the built-in table; tests may swap
	// in a custom one.
	Aliases *retrieval.AliasExpander
	// Evaluation sets SemanticSearch explicitly so --semantic controls the
	// exact server path under test rather than only changing client behavior.
	SemanticSearch  *bool
	semanticBlocked atomic.Bool
	Embedder        Embedder
	status          *statusManager
	queryCache      *queryVectorCache
	// SemanticQueryInstruction is an evaluation/advanced configuration hook.
	// Empty preserves the production baseline. When set, only query inputs
	// receive the Qwen retrieval instruction; stored document vectors do not.
	SemanticQueryInstruction string
	// SemanticOnlyExperiment excludes lexical candidates in explicit semantic
	// evaluation. It is never enabled by production configuration.
	SemanticOnlyExperiment          bool
	SemanticMinimumScoreExperiment  *float64
	SemanticRRFFusionExperiment     bool
	SemanticRRFWeightExperiment     float64
	SemanticVectorFirstExperiment   bool
	SemanticHighScoreExperiment     float64
	SemanticMinimumMarginExperiment float64
	SemanticOnlyOnEmptyExperiment   bool
	SemanticTopOneExperiment        bool
}

// NewServer builds the lite control plane from minimal environment config.
// trustLocal: single-user local deployment — see Server.TrustLocal.
func NewServer(store *Store, owner, cursorKey, adminToken string, tokens map[string]config.MCPPrincipalConfig, log *slog.Logger, trustLocal bool) *Server {
	semanticEnabledAtStartup := false
	localKey := ""
	learnerKey := ""
	keys := make([]string, 0, len(tokens))
	for key := range tokens {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if learnerKey == "" {
			learnerKey = key
		}
		if trustLocal && localKey == "" {
			localKey = key
		}
	}
	server := &Server{
		Store:          store,
		Auth:           auth.NewMCPAuthenticator(tokens),
		Cursor:         []byte(cursorKey),
		Owner:          owner,
		AdminToken:     adminToken,
		TrustLocal:     trustLocal,
		LocalClientKey: localKey,
		LearnerKey:     learnerKey,
		IDs:            &identity.UUIDv7Generator{},
		Log:            log,
		Aliases:        retrieval.NewAliasExpander(nil),
		SemanticSearch: &semanticEnabledAtStartup,
		queryCache:     newQueryVectorCache(256),
	}
	server.IntegrityError = store.SetIntegrityKey([]byte(cursorKey))
	return server
}

func (s *Server) Routes() http.Handler {
	return s.recoverPanics(s.requestGuards(s.mux()))
}

type rateWindow struct {
	started time.Time
	count   int
}

func (s *Server) requestGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(128 << 10)
		if r.URL.Path == "/v1/checkpoints" {
			limit = 4 << 20
		} else if r.URL.Path == "/v1/memory/mutations" {
			limit = 256 << 10
		} else if strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			limit = 64 << 10
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		category, perMinute := "", 0
		if r.Method == http.MethodPost && r.URL.Path == "/v1/memory/mutations" {
			category, perMinute = "mutation", 120
		} else if strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			category, perMinute = "admin", 60
		}
		if category != "" && !s.allowRequest(category+"\x00"+remoteHost(r.RemoteAddr), perMinute, time.Now()) {
			w.Header().Set("Retry-After", "60")
			httpapi.WriteJSON(w, http.StatusTooManyRequests, map[string]any{
				"code": "RATE_LIMITED", "message": "request rate limit exceeded", "retryable": true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func remoteHost(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

func (s *Server) allowRequest(key string, limit int, now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateWindows == nil {
		s.rateWindows = map[string]rateWindow{}
	}
	window := s.rateWindows[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= limit {
		return false
	}
	window.count++
	s.rateWindows[key] = window
	return true
}

// recoverPanics wraps the mux so a panicking handler cannot kill the
// single-process daemon. The daemon keeps its state in memory and persists
// at checkpoints/Close, so a panic mid-request must degrade to a 500
// response for that request while the process (and everything already
// committed) survives. The recovered value is logged with the request
// context for diagnosis.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("handler panic recovered", "panic", rec, "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
				httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]any{
					"code": "INTERNAL_ERROR", "message": "internal error", "retryable": false,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.healthLive)
	mux.HandleFunc("GET /health/ready", s.healthReady)
	mux.HandleFunc("GET /v1/system/status", s.systemStatus)
	mux.HandleFunc("POST /v1/checkpoints", s.checkpoint)
	mux.HandleFunc("POST /v1/memory/mutations", s.mutate)
	mux.HandleFunc("GET /v1/context/sessions/{id}", s.session)
	mux.HandleFunc("POST /v1/context/search", s.search)
	mux.HandleFunc("GET /v1/context/memories/{id}", s.recall)
	mux.HandleFunc("POST /v1/context/packet", s.packet)
	mux.HandleFunc("POST /v1/context/reflex", s.reflex)
	mux.HandleFunc("POST /v1/context/relevance", s.relevance)
	mux.HandleFunc("POST /v1/context/feedback", s.feedback)
	mux.HandleFunc("POST /v1/context/diff", s.diff)
	mux.HandleFunc("POST /v1/context/artifacts/search", s.artifactSearch)
	mux.HandleFunc("GET /v1/context/artifacts/{id}/read", s.artifactRead)
	mux.HandleFunc("POST /v1/admin/embed", s.adminEmbed)
	mux.HandleFunc("POST /v1/admin/vectors/rebuild", s.adminVectorRebuild)
	mux.HandleFunc("POST /v1/admin/learner/extract", s.adminLearnerExtract)
	mux.HandleFunc("GET /v1/admin/ops", s.adminOps)
	mux.HandleFunc("GET /v1/admin/proposals", s.adminListProposals)
	mux.HandleFunc("POST /v1/admin/proposals/{id}/approve", s.adminApproveProposal)
	mux.HandleFunc("POST /v1/admin/proposals/{id}/reject", s.adminRejectProposal)
	mux.HandleFunc("POST /v1/admin/memories/{id}/retire", s.adminRetireMemory)
	mux.HandleFunc("POST /v1/admin/snapshot", s.adminSnapshot)
	return mux
}

// --- health ---

func (s *Server) healthLive(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Server) healthReady(w http.ResponseWriter, r *http.Request) {
	store := "healthy"
	if _, err := s.Store.ContinuityHead(r.Context()); err != nil || s.IntegrityError != nil {
		store = "unavailable"
	}
	components := map[string]string{
		"artifact_store": "healthy",
		"owner":          "healthy",
		"store":          store,
		"schema":         "current",
		"staging":        "healthy",
	}
	if s.status != nil {
		components["embeddings"] = strings.ToLower(s.status.get().Embeddings)
	}
	status := "ready"
	for _, value := range components {
		if value == "unavailable" || value == "not_ready" {
			status = "not_ready"
			break
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"components": components, "status": status})
}

// InitializeStatus performs the startup-only comparison between configured
// embedding identity and the active derived generation. Later MCP calls read
// the cached result; they do not repeat filesystem or provider checks.
func (s *Server) InitializeStatus(storage StorageConfig, cfg EmbeddingConfig, semantic bool) SystemStatus {
	s.status = newStatusManager(s.Store, storage, cfg, semantic)
	status := s.status.get()
	s.semanticBlocked.Store(status.State == SystemActionRequired || status.Embeddings == "DISABLED")
	return status
}

func (s *Server) SystemStatus() SystemStatus {
	if s.status == nil {
		return unconfiguredSystemStatus(time.Now().UTC())
	}
	status := s.status.get()
	status.Configuration = sanitizedReadOnlyConfig(s.status.storage, s.status.config, s.status.semantic, s.semanticEnabled())
	status.Statistics = s.Store.ReadOnlyStatistics()
	if status.Embeddings == "DISABLED" {
		status.Statistics.Vectors.State = "DISABLED"
	}
	return status
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		if _, err := s.authenticate(r, config.MCPContextRead); err != nil {
			s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
			return
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, s.SystemStatus())
}

// --- auth helpers ---

func (s *Server) authenticate(r *http.Request, capability config.MCPClientCapability) (auth.Principal, error) {
	if s.TrustLocal {
		// Single-user local deployment: every request reaching loopback is
		// from the owner, and the local MCP client is the only caller — so
		// bind its configured client key (session ownership) without token
		// checks.
		return auth.Principal{Key: s.LocalClientKey, Type: auth.PrincipalMCP}, nil
	}
	token, err := bearer(r)
	if err != nil {
		return auth.Principal{}, err
	}
	return s.Auth.Authenticate(token, capability)
}

func bearer(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", apperror.New(apperror.AuthRequired, false, nil)
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), nil
}

// adminAuthorized always requires the operator credential. Loopback prevents
// remote reachability; it does not authenticate browsers or local processes.
func (s *Server) adminAuthorized(r *http.Request) bool {
	presented := r.Header.Get("X-Admin-Token")
	return s.AdminToken != "" && len(presented) == len(s.AdminToken) &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(s.AdminToken)) == 1
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	httpapi.WriteError(w, err, s.IDs.New())
}

// ops records one operational event into the journal (nerves of Mindmory).
func (s *Server) ops(event string, principal auth.Principal, sessionID string, outcome, reason, resourceID string, duration time.Duration, details map[string]any) {
	if s.Store == nil || s.Store.Ops == nil {
		return
	}
	s.Store.Ops.Record(OpsEvent{
		Event: event, Principal: principal.Key, SessionID: sessionID,
		Outcome: outcome, Reason: reason, ResourceID: resourceID,
		DurationMS: duration.Milliseconds(), Details: details,
	})
}

// decodeStrict decodes one JSON object and rejects trailing content.
func decodeStrict(r *http.Request, out any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return apperror.New(apperror.InvalidRequest, false, nil)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return apperror.New(apperror.InvalidRequest, false, nil)
	}
	return nil
}

// --- checkpoint ---

type checkpointMessage struct {
	ExternalMessageID string       `json:"external_message_id"`
	Role              archive.Role `json:"role"`
	ContentType       string       `json:"content_type"`
	Content           string       `json:"content"`
	OccurredAt        time.Time    `json:"occurred_at"`
	AssistantID       string       `json:"assistant_id,omitempty"`
	AssistantName     string       `json:"assistant_name,omitempty"`
	Hash              string       `json:"-"`
}

type checkpointRequest struct {
	ExternalSessionID string              `json:"external_session_id"`
	Title             string              `json:"title,omitempty"`
	ProjectKey        string              `json:"project_key,omitempty"`
	Mode              string              `json:"mode"`
	Messages          []checkpointMessage `json:"messages"`
	ToolEvents        []json.RawMessage   `json:"tool_events,omitempty"`
}

type checkpointResult struct {
	SessionID      string            `json:"session_id"`
	MessageIDs     map[string]string `json:"message_ids"`
	ArtifactIDs    map[string]string `json:"artifact_ids"`
	Replayed       bool              `json:"replayed"`
	ProcessingJobs int               `json:"processing_jobs"`
}

func (s *Server) checkpoint(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPArchiveCheckpoint)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request checkpointRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Mode != "INCREMENTAL" || len(request.Messages) == 0 || len(request.Messages) > 1000 {
		s.fail(w, apperror.New(apperror.InvalidRequest, false, nil))
		return
	}
	session, err := s.Store.UpsertSession(r.Context(), principal, request.ExternalSessionID, request.Title, request.ProjectKey, time.Now().UTC())
	if err != nil {
		s.fail(w, apperror.New(apperror.SessionMetadataConflict, false, nil))
		return
	}
	result := checkpointResult{SessionID: session.SessionID, MessageIDs: map[string]string{}, ArtifactIDs: map[string]string{}}
	started := time.Now()
	for _, message := range request.Messages {
		if message.Role.Validate() != nil || strings.TrimSpace(message.Content) == "" || message.ExternalMessageID == "" {
			s.fail(w, apperror.New(apperror.InvalidRequest, false, nil))
			return
		}
		message.Hash = checkpointMessageHash(message)
		message.OccurredAt = message.OccurredAt.UTC()
		id, replayed, err := s.Store.InsertMessage(r.Context(), session.SessionID, message)
		if err != nil {
			s.Log.Error("checkpoint insert failed", "error", err)
			if errors.Is(err, errMessageConflict) {
				s.fail(w, apperror.New(apperror.InvalidRequest, false, nil))
				return
			}
			s.fail(w, err)
			return
		}
		if replayed {
			result.Replayed = true
		}
		result.MessageIDs[message.ExternalMessageID] = id
	}
	s.ops("CHECKPOINT", principal, session.SessionID, "ok", "", session.SessionID, time.Since(started), map[string]any{
		"messages": len(request.Messages), "replayed": result.Replayed, "session": session.SessionID,
	})
	// A checkpoint is the natural persistence anchor: every user turn ends
	// here, so persist the access bumps accumulated since the last one.
	// Best-effort — a flush failure must not fail the archive.
	if err := s.Store.FlushAccessBumps(); err != nil {
		s.Log.Error("access bump flush failed", "error", err)
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

// --- mutations ---

type mutationRequest struct {
	SessionID      string              `json:"session_id"`
	MessageID      string              `json:"message_id"`
	Mutation       domain.MutationKind `json:"mutation"`
	MemoryKind     domain.Kind         `json:"memory_kind,omitempty"`
	Scope          domain.ScopeType    `json:"scope,omitempty"`
	Subject        string              `json:"subject,omitempty"`
	EvidenceQuote  string              `json:"evidence_quote"`
	TargetMemoryID string              `json:"target_memory_id,omitempty"`
	Replacement    string              `json:"replacement,omitempty"`
}

type mutationResult struct {
	ProposalID         string  `json:"proposal_id"`
	Outcome            string  `json:"outcome"`
	ReasonCode         string  `json:"reason_code"`
	MemoryID           string  `json:"memory_id,omitempty"`
	DuplicateOf        string  `json:"duplicate_of,omitempty"`
	RepeatCount        int64   `json:"repeat_count,omitempty"`
	Importance         float64 `json:"importance,omitempty"`
	ContinuityRevision int64   `json:"continuity_revision,omitempty"`
	Replay             bool    `json:"replay,omitempty"`
}

func (s *Server) mutate(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPMemoryPropose)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request mutationRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Mutation.Validate() != nil || strings.TrimSpace(request.EvidenceQuote) == "" ||
		!isUUID(request.SessionID) || !isUUID(request.MessageID) {
		s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
		return
	}
	switch request.Mutation {
	case domain.MutationRemember:
		if request.MemoryKind.Validate() != nil || request.Scope.Validate() != nil || strings.TrimSpace(request.Subject) == "" ||
			request.TargetMemoryID != "" || request.Replacement != "" {
			s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
			return
		}
	case domain.MutationCorrect:
		if request.TargetMemoryID == "" || strings.TrimSpace(request.Replacement) == "" || request.MemoryKind != "" ||
			request.Scope != "" || request.Subject != "" || !isUUID(request.TargetMemoryID) {
			s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
			return
		}
	case domain.MutationForget:
		if request.TargetMemoryID == "" || request.Replacement != "" || request.MemoryKind != "" ||
			request.Scope != "" || request.Subject != "" || !isUUID(request.TargetMemoryID) {
			s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
			return
		}
	}
	started := time.Now()
	result, err := s.applyMutation(r.Context(), principal, request)
	if err != nil {
		s.Log.Error("mutation failed", "error", err, "mutation", request.Mutation, "session", request.SessionID)
		s.ops("MUTATION", principal, request.SessionID, "error", apperror.Code(err), request.MessageID, time.Since(started), map[string]any{
			"mutation": string(request.Mutation), "subject": request.Subject,
		})
		s.fail(w, err)
		return
	}
	s.ops("MUTATION", principal, request.SessionID, strings.ToLower(result.Outcome), result.ReasonCode, result.MemoryID, time.Since(started), map[string]any{
		"mutation": string(request.Mutation), "subject": request.Subject,
		"proposal_id": result.ProposalID, "revision": result.ContinuityRevision, "replay": result.Replay,
	})
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) applyMutation(ctx context.Context, principal auth.Principal, request mutationRequest) (mutationResult, error) {
	return s.applyMutationInternal(ctx, principal, request, false)
}

// applyMutationAllowOld is applyMutation for the passive learner: it permits
// mutations against archived non-current user turns so older cue-bearing
// messages STAGE for review (CURRENT_USER_EVIDENCE_REQUIRED) instead of
// being rejected. Role and evidence binding still fully apply; only the
// current-turn requirement is lifted, and the governed decision then stages.
func (s *Server) applyMutationAllowOld(ctx context.Context, principal auth.Principal, request mutationRequest) (mutationResult, error) {
	return s.applyMutationInternal(ctx, principal, request, true)
}

func (s *Server) applyMutationInternal(ctx context.Context, principal auth.Principal, request mutationRequest, allowOld bool) (mutationResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	session, err := s.Store.ResolveSession(ctx, principal, request.SessionID)
	if err != nil {
		return mutationResult{}, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil)
	}
	evidence, err := s.Store.LoadMessageEvidence(ctx, request.SessionID, request.MessageID)
	if err != nil {
		return mutationResult{}, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil)
	}
	evidence.ClientID = principal.Key
	evidence.SessionID = session.SessionID
	if latest, err := s.Store.LatestUserMessageID(ctx, request.SessionID); err == nil {
		evidence.CurrentUserTurn = latest == request.MessageID
	}
	if evidence.Role != archive.RoleUser || (!allowOld && !evidence.CurrentUserTurn) {
		return mutationResult{}, apperror.New(apperror.MemoryCurrentTurnRequired, false, nil)
	}

	binding := bindExactQuote(evidence.Content, request.EvidenceQuote)
	projectKey := ""
	if request.Mutation == domain.MutationRemember && request.Scope == domain.ScopeProject {
		projectKey = session.ProjectKey
	}
	target, targetRow, err := s.mutationTarget(ctx, request)
	if err != nil {
		return mutationResult{}, err
	}

	identityValue := domain.ProposalIdentity{
		ClientKey: principal.Key, SessionID: request.SessionID, MessageID: request.MessageID,
		Mutation: request.Mutation, TargetMemoryID: request.TargetMemoryID, ProposedKind: request.MemoryKind,
		Scope: request.Scope, ProjectKey: projectKey, Subject: strings.TrimSpace(request.Subject),
		Replacement: strings.TrimSpace(request.Replacement), RequestEvidenceHash: quoteHash(request.EvidenceQuote),
		EvidenceContentHash: hashContent(evidence.Content),
	}
	if binding.ReasonCode == "" {
		identityValue.Evidence = &domain.MessageQuote{Hash: binding.Hash, StartByte: binding.StartByte, EndByte: binding.EndByte}
	}
	requestHash := domain.RequestHash(identityValue)

	// Idempotent replay: an identical proposal already exists.
	if existing, found, err := s.Store.FindProposalByHash(ctx, requestHash); err == nil && found {
		return proposalResult(existing, true), nil
	}

	proposalID := newID()
	proposal := domain.Proposal{ID: proposalID, RequestHash: requestHash, Identity: identityValue, Status: domain.ProposalPending}
	if binding.ReasonCode != "" {
		proposal.GateClass = domain.GateStructural
		if err := s.Store.StageProposal(ctx, proposal, binding.ReasonCode); err != nil {
			return mutationResult{}, err
		}
		return mutationResult{ProposalID: proposalID, Outcome: "STAGED", ReasonCode: binding.ReasonCode}, nil
	}

	mr := domain.MutationRequest{
		Kind: request.Mutation, ClientID: principal.Key, SessionID: request.SessionID, MessageID: request.MessageID,
		EvidenceQuote: request.EvidenceQuote, Subject: strings.TrimSpace(request.Subject),
		TargetMemoryID: request.TargetMemoryID, Replacement: strings.TrimSpace(request.Replacement),
	}
	decision := domain.VerifyMutation(mr, evidence, target)
	applyReason := decision.Reason
	if decision.Outcome != domain.MutationApply {
		proposal.GateClass = decision.Class
		if err := s.Store.StageProposal(ctx, proposal, decision.Reason); err != nil {
			return mutationResult{}, err
		}
		return mutationResult{ProposalID: proposalID, Outcome: "STAGED", ReasonCode: decision.Reason}, nil
	}

	// Content-level dedupe (REMEMBER only): a proposal whose subject is
	// near-identical to an existing ACTIVE memory in the same scope is not
	// silently duplicated. Repetition is an importance signal:
	//
	// A cue-verified duplicate promotes the existing memory: importance
	// bumps one grade, RepeatCount increments, and activation is rewarmed.
	//
	// Nothing is auto-deleted and nothing is auto-created.
	if request.Mutation == domain.MutationRemember {
		scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: principal.Key, ProjectKey: session.ProjectKey}
		rows, err := s.Store.EligibleMemories(ctx, scope, nil, 0)
		if err != nil {
			s.Log.Error("dedupe scan failed", "error", err)
		} else if dup := findDuplicateSubject(rows, request.Subject, request.Scope); dup != "" {
			duplicate, err := s.Store.LoadMemoryRow(ctx, dup)
			if err != nil {
				return mutationResult{}, err
			}
			if err := s.Store.StageProposal(ctx, proposal, ""); err != nil {
				return mutationResult{}, err
			}
			duplicate.Importance = promoteImportance(duplicate.Importance)
			duplicate.RepeatCount++
			duplicate.Activation = 1.0
			duplicate.StateVersion++
			duplicate.UpdatedAt = time.Now().UTC()
			committed, err := s.Store.CommitMutation(ctx, MutationCommit{
				Mutation: request.Mutation, ProposalID: proposalID, RequestHash: requestHash,
				ResolutionReason: "DUPLICATE_PROMOTED", UpdatedMemory: &duplicate,
				TargetMemoryID: dup, ExpectedTargetVersion: duplicate.StateVersion - 1,
				Evidence: MessageEvidenceRow{MemoryID: dup, MessageID: request.MessageID,
					QuoteHash: binding.Hash, QuoteStart: binding.StartByte, QuoteEnd: binding.EndByte, Relation: "REASSERTS"},
				ContinuityKind: "MEMORY_REINFORCED", ProjectKey: duplicate.ProjectKey,
				Sensitivity: duplicate.Sensitivity, TraceID: s.IDs.New(),
			})
			if err != nil {
				return mutationResult{}, err
			}
			if committed.ProjectionError != nil {
				s.Log.Error("promotion committed; projection refresh deferred", "event_id", committed.EventID, "error", committed.ProjectionError)
			}
			s.ops("MUTATION", principal, request.SessionID, "applied", "DUPLICATE_PROMOTED", dup, 0, map[string]any{
				"mutation": string(request.Mutation), "subject": request.Subject, "duplicate_of": dup,
				"importance": duplicate.Importance, "repeat_count": duplicate.RepeatCount,
			})
			return mutationResult{ProposalID: proposalID, Outcome: "APPLIED", ReasonCode: "DUPLICATE_PROMOTED",
				MemoryID: dup, DuplicateOf: dup, RepeatCount: duplicate.RepeatCount, Importance: duplicate.Importance,
				ContinuityRevision: committed.Revision}, nil
		}
	}

	// Create the PENDING proposal row first so the apply path can resolve it.
	if err := s.Store.StageProposal(ctx, proposal, ""); err != nil {
		return mutationResult{}, err
	}
	memoryID, revision, err := s.apply(ctx, request, proposalID, identityValue, target, targetRow, evidence, applyReason)
	if err != nil {
		// CommitMutation appends the complete event before applying any state.
		// Projection failures are returned separately after commit, so an error
		// here means no event was committed and rejecting the staged proposal is
		// safe; there can be no orphaned memory/evidence/continuity mixture.
		s.Log.Error("apply failed after staging", "error", err, "proposal", proposalID, "mutation", request.Mutation)
		if rerr := s.Store.RejectProposal(ctx, proposalID, "COMMIT_FAILED_PRE_EVENT"); rerr != nil {
			s.Log.Error("reject after commit failure failed", "error", rerr, "proposal", proposalID)
		}
		s.ops("MUTATION", principal, request.SessionID, "error", "COMMIT_FAILED_PRE_EVENT", proposalID, 0, map[string]any{
			"mutation": string(request.Mutation), "subject": request.Subject, "apply_error": err.Error(),
		})
		return mutationResult{}, err
	}
	return mutationResult{ProposalID: proposalID, Outcome: "APPLIED", ReasonCode: applyReason,
		MemoryID: memoryID, ContinuityRevision: revision}, nil
}

func (s *Server) mutationTarget(ctx context.Context, request mutationRequest) (*domain.MutationTarget, *MemoryRow, error) {
	if request.Mutation == domain.MutationRemember {
		return nil, nil, nil
	}
	row, err := s.Store.LoadMemoryRow(ctx, request.TargetMemoryID)
	if err != nil {
		return nil, nil, apperror.New(apperror.MemoryTargetNotFound, false, nil)
	}
	if row.Lifecycle != string(domain.LifecycleActive) || row.Sensitivity != "NORMAL" {
		return nil, nil, apperror.New(apperror.MemoryTargetInactive, false, nil)
	}
	return &domain.MutationTarget{
		MemoryID: row.MemoryID, Kind: domain.Kind(row.Kind), Subject: row.Subject, Content: row.Content,
		Lifecycle: domain.Lifecycle(row.Lifecycle), Sensitivity: policySensitivity(row.Sensitivity),
	}, &row, nil
}

func (s *Server) apply(ctx context.Context, request mutationRequest, proposalID string,
	identityValue domain.ProposalIdentity, target *domain.MutationTarget, targetRow *MemoryRow,
	evidence archive.MessageEvidence, reason string) (string, int64, error) {
	traceID := s.IDs.New()
	binding := *identityValue.Evidence
	projectKey := identityValue.ProjectKey
	evidenceSensitivity := evidence.Sensitivity
	commit := MutationCommit{
		Mutation: request.Mutation, ProposalID: proposalID, RequestHash: identityValueHash(identityValue),
		ResolutionReason: reason, ProjectKey: projectKey, Sensitivity: evidenceSensitivity.String(), TraceID: traceID,
	}
	if reason == "REVIEWER_APPROVED" {
		commit.ReviewAuthorization = "ADMIN_INTENT_REVIEW"
	}
	switch request.Mutation {
	case domain.MutationRemember:
		memoryID := newID()
		record := MemoryRow{MemoryID: memoryID, Kind: string(request.MemoryKind), Subject: strings.TrimSpace(request.Subject),
			Content: request.EvidenceQuote, ContentHash: hashContent(request.EvidenceQuote), Lifecycle: string(domain.LifecycleActive),
			EpistemicStatus: "USER_ACCEPTED", Confidence: 1.0, Importance: domain.Importance(request.EvidenceQuote),
			Sensitivity: evidenceSensitivity.String(), ScopeType: string(request.Scope), ProjectKey: projectKey,
			SecretLike: evidence.SecretLike, InstructionLike: evidence.InstructionLike, Activation: 1.0}
		commit.NewMemory = &record
		commit.Evidence = MessageEvidenceRow{MemoryID: memoryID, MessageID: request.MessageID,
			QuoteHash: binding.Hash, QuoteStart: binding.StartByte, QuoteEnd: binding.EndByte, Relation: "ASSERTS"}
		commit.ContinuityKind = "MEMORY_CREATED"
	case domain.MutationCorrect:
		memoryID := newID()
		content := target.Subject + ": " + strings.TrimSpace(request.Replacement)
		var scopeType, projectKey string
		if targetRow != nil {
			scopeType, projectKey = targetRow.ScopeType, targetRow.ProjectKey
		}
		record := MemoryRow{MemoryID: memoryID, Kind: string(target.Kind), Subject: target.Subject, Content: content,
			ContentHash: hashContent(content), Lifecycle: string(domain.LifecycleActive), EpistemicStatus: "USER_ACCEPTED",
			Confidence: 1.0, Importance: domain.Importance(request.EvidenceQuote), Sensitivity: inheritSensitivity(target.Sensitivity, evidenceSensitivity).String(),
			ScopeType: scopeType, ProjectKey: projectKey, SupersedesMemoryID: target.MemoryID,
			SecretLike:      evidence.SecretLike || (targetRow != nil && targetRow.SecretLike),
			InstructionLike: evidence.InstructionLike || (targetRow != nil && targetRow.InstructionLike),
			Activation:      1.0}
		commit.NewMemory = &record
		commit.TargetMemoryID = target.MemoryID
		commit.ExpectedTargetVersion = targetRow.StateVersion
		commit.TargetLifecycle = domain.LifecycleSuperseded
		commit.RelatedMemoryID = target.MemoryID
		commit.ProjectKey = projectKey
		commit.Sensitivity = inheritSensitivity(target.Sensitivity, evidenceSensitivity).String()
		commit.Evidence = MessageEvidenceRow{MemoryID: memoryID, MessageID: request.MessageID,
			QuoteHash: binding.Hash, QuoteStart: binding.StartByte, QuoteEnd: binding.EndByte, Relation: "CORRECTS"}
		commit.ContinuityKind = "MEMORY_CORRECTED"
	case domain.MutationForget:
		if targetRow != nil {
			commit.ProjectKey = targetRow.ProjectKey
		}
		commit.TargetMemoryID = target.MemoryID
		commit.ExpectedTargetVersion = targetRow.StateVersion
		commit.TargetLifecycle = domain.LifecycleForgotten
		commit.Sensitivity = inheritSensitivity(target.Sensitivity, evidenceSensitivity).String()
		commit.Evidence = MessageEvidenceRow{MemoryID: target.MemoryID, MessageID: request.MessageID,
			QuoteHash: binding.Hash, QuoteStart: binding.StartByte, QuoteEnd: binding.EndByte, Relation: "FORGETS"}
		commit.ContinuityKind = "MEMORY_FORGOTTEN"
	default:
		return "", 0, apperror.New(apperror.MemoryProposalInvalid, false, nil)
	}
	result, err := s.Store.CommitMutation(ctx, commit)
	if err != nil {
		return "", 0, err
	}
	if result.ProjectionError != nil {
		s.Log.Error("mutation committed; projection refresh deferred to replay", "event_id", result.EventID, "error", result.ProjectionError)
	}
	return result.MemoryID, result.Revision, nil
}

func identityValueHash(identityValue domain.ProposalIdentity) string {
	return domain.RequestHash(identityValue)
}

// --- session resolution ---

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessionID := r.PathValue("id")
	sessionRow, err := s.Store.ResolveSession(r.Context(), principal, sessionID)
	if err != nil {
		s.fail(w, apperror.New(apperror.ContextSessionNotFound, false, nil))
		return
	}
	scope := retrieval.SessionScope{SessionID: sessionRow.SessionID, ClientKey: sessionRow.ClientKey, ProjectKey: sessionRow.ProjectKey}
	if r.URL.Query().Get("latest") == "1" {
		messageID, err := s.Store.LatestUserMessageID(r.Context(), sessionID)
		if err != nil {
			s.fail(w, apperror.New(apperror.ContextSessionNotFound, false, nil))
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, retrieval.TurnScope{Session: scope, MessageID: messageID, IsCurrentUser: true})
		return
	}
	if messageID := r.URL.Query().Get("message_id"); messageID != "" {
		evidence, err := s.Store.LoadMessageEvidence(r.Context(), sessionID, messageID)
		if err != nil {
			s.fail(w, apperror.New(apperror.ContextSessionNotFound, false, nil))
			return
		}
		latest, _ := s.Store.LatestUserMessageID(r.Context(), sessionID)
		httpapi.WriteJSON(w, http.StatusOK, retrieval.TurnScope{Session: scope, MessageID: messageID, IsCurrentUser: latest == messageID && evidence.Role == archive.RoleUser})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, scope)
}

// --- search ---

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request retrieval.SearchRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Validate() != nil {
		s.fail(w, apperror.New(apperror.ContextQueryInvalid, false, nil))
		return
	}
	scope, err := s.sessionScope(r.Context(), principal, request.SessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	started := time.Now()
	hits, err := s.searchMemories(r.Context(), scope, request, true)
	if err != nil {
		s.Log.Error("search failed", "error", err)
		s.ops("SEARCH", principal, request.SessionID, "error", apperror.Code(err), "", time.Since(started), map[string]any{"query": request.Query})
		s.fail(w, apperror.New(apperror.DatabaseUnavailable, false, nil))
		return
	}
	s.ops("SEARCH", principal, request.SessionID, "ok", "", "", time.Since(started), map[string]any{
		"query": request.Query, "hits": len(hits), "semantic": s.Store.VectorStore != nil && s.Store.VectorStore.Size() > 0,
	})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"results": hits})
}

// --- packet / reflex / recall ---

func (s *Server) packet(w http.ResponseWriter, r *http.Request) {
	s.contextPacket(w, r, retrieval.ExplicitMode)
}

func (s *Server) reflex(w http.ResponseWriter, r *http.Request) {
	s.contextPacket(w, r, retrieval.ReflexMode)
}

func (s *Server) contextPacket(w http.ResponseWriter, r *http.Request, forcedMode string) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request retrieval.ContextRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Mode == "" {
		request.Mode = forcedMode
	}
	if request.Validate() != nil {
		s.fail(w, apperror.New(apperror.ContextQueryInvalid, false, nil))
		return
	}
	scope, err := s.sessionScope(r.Context(), principal, request.SessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	started := time.Now()
	var out retrieval.ContextPacket
	if request.EffectiveMode() == retrieval.ReflexMode {
		out, err = s.reflexPacket(r.Context(), scope, request)
	} else {
		out, err = s.explicitPacket(r.Context(), scope, request)
	}
	if err != nil {
		s.ops("CONTEXT", principal, request.SessionID, "error", apperror.Code(err), "", time.Since(started), map[string]any{"mode": request.EffectiveMode()})
		s.fail(w, err)
		return
	}
	s.ops("CONTEXT", principal, request.SessionID, "ok", "", "", time.Since(started), map[string]any{
		"mode": request.EffectiveMode(), "core": len(out.Core), "important": len(out.Important),
		"delta": len(out.Delta), "memories": len(out.Memories), "truncated": out.Truncated,
	})
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) explicitPacket(ctx context.Context, scope retrieval.SessionScope, request retrieval.ContextRequest) (retrieval.ContextPacket, error) {
	pc, err := s.projectContext(ctx, scope.ProjectKey)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	head, err := s.Store.ContinuityHead(ctx)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	cursor, err := retrieval.SignCursor(s.Cursor, retrieval.CursorPayload{Version: 1, SessionID: scope.SessionID, ProjectKeyHash: retrieval.ProjectHash(scope.ProjectKey), Revision: head})
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	out := retrieval.ContextPacket{Session: scope, ContinuityCursor: cursor, ProjectContext: pc,
		Memories: []retrieval.MemoryHit{}}
	if request.Query != "" {
		hits, err := s.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: request.SessionID, Query: request.Query, Limit: 12, Mode: retrieval.SearchLexical}, true)
		if err != nil {
			return retrieval.ContextPacket{}, err
		}
		var project, global []retrieval.MemoryHit
		for _, h := range hits {
			if h.Scope == "PROJECT" && len(project) < 8 {
				project = append(project, h)
			} else if h.Scope == "GLOBAL" && len(global) < 4 {
				global = append(global, h)
			}
		}
		out.Memories = append(project, global...)
	} else {
		project, err := s.recentMemories(ctx, scope, "PROJECT", 8)
		if err == nil {
			global, err2 := s.recentMemories(ctx, scope, "GLOBAL", 4)
			if err2 == nil {
				out.Memories = append(project, global...)
			}
		}
	}
	budget := request.EffectiveMaxChars()
	if pc != nil {
		b, _ := json.Marshal(pc)
		if len(b) > budget {
			out.ProjectContext = nil
			out.Truncated = true
		} else {
			budget -= len(b)
			out.ReturnedChars += len(b)
		}
	}
	var kept []retrieval.MemoryHit
	for _, h := range out.Memories {
		n := len(h.Subject) + len(h.Content)
		if n > budget {
			out.Truncated = true
			continue
		}
		kept = append(kept, h)
		budget -= n
		out.ReturnedChars += n
	}
	out.Memories = kept
	// Mark what was returned as surfaced for this session so per-step
	// relevance injection never duplicates what the model just saw.
	s.Store.MarkSurfaced(scope.SessionID, hitsSurfaced(out.Memories))
	return out, nil
}

// hitsSurfaced extracts the memory ids from a hit slice.
func hitsSurfaced(hits []retrieval.MemoryHit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.MemoryID != "" {
			ids = append(ids, h.MemoryID)
		}
	}
	return ids
}

func (s *Server) reflexPacket(ctx context.Context, scope retrieval.SessionScope, request retrieval.ContextRequest) (retrieval.ContextPacket, error) {
	pc, err := s.projectContext(ctx, scope.ProjectKey)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	head, err := s.Store.ContinuityHead(ctx)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	cursor, err := retrieval.SignProjectCursor(s.Cursor, scope.ProjectKey, head)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	out := retrieval.ContextPacket{Session: scope, ContinuityCursor: cursor,
		Memories: []retrieval.MemoryHit{}, Core: []retrieval.MemoryHit{}, Important: []retrieval.MemoryHit{},
		OpenLoops: []string{}, Delta: []retrieval.ContinuityChange{}}
	if pc != nil {
		used := retrieval.EstimatedTokens(pc.Objective + " " + pc.CurrentState + string(pc.Decisions) + string(pc.OpenQuestions) + string(pc.NextActions))
		if used <= retrieval.ReflexProjectBudget {
			out.ProjectContext = pc
		} else {
			out.Truncated = true
		}
	}
	global, err := s.recentMemories(ctx, scope, "GLOBAL", 50)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	project, err := s.recentMemories(ctx, scope, "PROJECT", 50)
	if err != nil {
		return retrieval.ContextPacket{}, err
	}
	core := retrieval.ReflexCoreBudget
	for _, h := range global {
		if !reflexIdentityKind(h.Kind) {
			continue
		}
		if len(out.Core) >= retrieval.ReflexCoreMaxItems {
			out.Truncated = true
			break
		}
		used := retrieval.EstimatedTokens(h.Subject + " " + h.Content)
		if used > core {
			out.Truncated = true
			continue
		}
		out.Core = append(out.Core, h)
		core -= used
	}
	important := retrieval.ReflexImportantBudget
	for _, h := range project {
		if !reflexImportantKind(h.Kind) {
			continue
		}
		if len(out.Important) >= retrieval.ReflexImportantMaxItems {
			out.Truncated = true
			break
		}
		used := retrieval.EstimatedTokens(h.Subject + " " + h.Content)
		if used > important {
			out.Truncated = true
			continue
		}
		out.Important = append(out.Important, h)
		important -= used
	}
	if pc != nil {
		out.OpenLoops = reflexLoops(pc, retrieval.ReflexLoopsBudget, &out.Truncated)
	}
	changes, _, err := s.Store.ContinuityChanges(ctx, 0, scope.ProjectKey, 25, true)
	if err == nil {
		surfaced := map[string]bool{}
		for _, h := range out.Core {
			surfaced[h.MemoryID] = true
		}
		for _, h := range out.Important {
			surfaced[h.MemoryID] = true
		}
		delta := retrieval.ReflexDeltaBudget
		for i := len(changes) - 1; i >= 0; i-- {
			change := changes[i]
			if change.TargetKind == "COGNITIVE_MEMORY" && surfaced[change.TargetID] {
				continue
			}
			change.TargetID, change.RelatedTargetID = "", ""
			used := reflexChangeTokens(change)
			if used > delta {
				out.Truncated = true
				continue
			}
			out.Delta = append(out.Delta, change)
			delta -= used
		}
	}
	// Mark the surfaced core/important ids so per-step relevance injection
	// does not re-inject what the wake-up packet already showed.
	var surfacedIDs []string
	for _, h := range out.Core {
		surfacedIDs = append(surfacedIDs, h.MemoryID)
	}
	for _, h := range out.Important {
		surfacedIDs = append(surfacedIDs, h.MemoryID)
	}
	s.Store.MarkSurfaced(scope.SessionID, surfacedIDs)
	return out, nil
}

func (s *Server) recall(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	memoryID := r.PathValue("id")
	sessionID := r.URL.Query().Get("session_id")
	scope, err := s.sessionScope(r.Context(), principal, sessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	row, err := s.Store.LoadMemoryRow(r.Context(), memoryID)
	if err != nil {
		s.fail(w, apperror.New(apperror.MemoryReadNotFound, false, nil))
		return
	}
	// Recall is an exact read by id: it must surface any lifecycle (a
	// FORGOTTEN or SUPERSEDED memory is still readable so the agent can
	// confirm the transition), unlike search which filters to ACTIVE.
	// Scope and sensitivity still gate visibility.
	if !memoryRowEligible(scope, row, true) {
		s.fail(w, apperror.New(apperror.MemoryReadNotFound, false, nil))
		return
	}
	_ = s.Store.RecordAccess(r.Context(), memoryID, scope.SessionID, retrieval.AccessRecall)
	evidenceRows, _ := s.Store.MessageEvidenceFor(r.Context(), memoryID)
	availability := "ONLINE"
	if len(evidenceRows) == 0 {
		availability = "AVAILABLE"
	}
	out := retrieval.MemoryRecall{
		MemoryID: row.MemoryID, Kind: row.Kind, Subject: row.Subject, Content: row.Content,
		Scope: row.ScopeType, ProjectKey: row.ProjectKey, EpistemicStatus: row.EpistemicStatus,
		RepeatCount: row.RepeatCount,
		Lifecycle:   row.Lifecycle, SupersedesMemoryID: row.SupersedesMemoryID, ContentAvailability: availability,
		Importance: row.Importance, Heat: row.Activation, AccessCount: row.AccessCount,
		MessageEvidence: []retrieval.MessageEvidence{}, ArtifactEvidence: []retrieval.ArtifactEvidence{},
	}
	out.EffectiveHeat = s.activationEffective(r.Context(), scope.SessionID, row)
	out.Grade = retrieval.HeatGrade(row.Importance, out.EffectiveHeat)
	s.ops("RECALL", principal, sessionID, "ok", "", memoryID, 0, map[string]any{"subject": row.Subject, "evidence": len(evidenceRows)})
	for _, e := range evidenceRows {
		evidence := retrieval.MessageEvidence{Type: "MESSAGE", Relation: e.Relation, MessageID: e.MessageID,
			OccurredAt: formatMicros(e.OccurredAt), QuoteHash: e.QuoteHash}
		if e.QuoteStart >= 0 && e.QuoteEnd <= len(e.MessageContent) && e.QuoteStart < e.QuoteEnd {
			quote := e.MessageContent[e.QuoteStart:e.QuoteEnd]
			if hashContent(quote) == e.QuoteHash && utf8Valid(quote) {
				evidence.Quote = quote
				evidence.Availability = "ONLINE"
			} else {
				evidence.Availability = "CONTENT_UNAVAILABLE"
			}
		} else {
			evidence.Availability = "CONTENT_RESTRICTED"
		}
		out.MessageEvidence = append(out.MessageEvidence, evidence)
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// --- feedback ---

func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request retrieval.FeedbackRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Validate() != nil {
		s.fail(w, apperror.New(apperror.ContextQueryInvalid, false, nil))
		return
	}
	scope, err := s.sessionScope(r.Context(), principal, request.SessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	row, err := s.Store.LoadMemoryRow(r.Context(), request.MemoryID)
	if err != nil || !memoryRowEligible(scope, row, false) {
		s.fail(w, apperror.New(apperror.MemoryReadNotFound, false, nil))
		return
	}
	_ = s.Store.ApplyFeedback(r.Context(), request.MemoryID, scope.SessionID, request.Outcome)
	s.ops("FEEDBACK", principal, request.SessionID, "ok", "", request.MemoryID, 0, map[string]any{"outcome": request.Outcome, "note": request.Note})
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

// --- diff ---

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r, config.MCPContextRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request retrieval.DiffRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Validate() != nil {
		s.fail(w, apperror.New(apperror.CursorInvalid, false, nil))
		return
	}
	scope, err := s.sessionScope(r.Context(), principal, request.SessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	after := int64(0)
	recent := request.Cursor == ""
	if !recent {
		payload, err := retrieval.VerifyCursor(s.Cursor, request.Cursor)
		if err != nil || payload.ProjectKeyHash != retrieval.ProjectHash(scope.ProjectKey) ||
			(payload.Version == 1 && payload.SessionID != scope.SessionID) {
			s.fail(w, apperror.New(apperror.CursorInvalid, false, nil))
			return
		}
		after = payload.Revision
	}
	changes, highest, err := s.Store.ContinuityChanges(r.Context(), after, scope.ProjectKey, request.EffectiveLimit(), recent)
	if err != nil {
		s.fail(w, err)
		return
	}
	if highest < after {
		highest = after
	}
	out := retrieval.DiffResult{Changes: changes, NextCursor: ""}
	if out.Changes == nil {
		out.Changes = []retrieval.ContinuityChange{}
	}
	next, err := retrieval.SignProjectCursor(s.Cursor, scope.ProjectKey, highest)
	if err != nil {
		s.fail(w, err)
		return
	}
	out.NextCursor = next
	s.ops("DIFF", principal, request.SessionID, "ok", "", "", 0, map[string]any{"changes": len(out.Changes), "cursor": request.Cursor != ""})
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// --- artifacts (empty store: contract shape only) ---

func (s *Server) artifactSearch(w http.ResponseWriter, r *http.Request) {
	_, err := s.authenticate(r, config.MCPArtifactSearch)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request retrieval.ArtifactSearchRequest
	if err := decodeStrict(r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Validate() != nil {
		s.fail(w, apperror.New(apperror.ArtifactSearchInvalid, false, nil))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"results": []retrieval.ArtifactHit{}})
}

func (s *Server) artifactRead(w http.ResponseWriter, r *http.Request) {
	_, err := s.authenticate(r, config.MCPArtifactRead)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.fail(w, apperror.New(apperror.ArtifactReadNotFound, false, nil))
}

// adminEmbed backfills embeddings for active memories inside the running
// daemon — no second process, so the canonical JSONL is never contended.
// Requires the admin token (X-Admin-Token header).
func (s *Server) adminEmbed(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	embedder := s.Embedder
	if embedder == nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "EMBEDDING_DISABLED", "message": "embedding provider is disabled"})
		return
	}
	count, err := s.Store.EmbedAll(ctx, embedder)
	if err != nil {
		s.Log.Error("embed failed", "error", err)
		s.ops("EMBED", auth.Principal{Key: "admin"}, "", "error", apperror.Code(err), "", time.Since(time.Now()), nil)
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	if s.status != nil {
		refreshed := s.status.refresh(s.Store)
		s.semanticBlocked.Store(refreshed.State == SystemActionRequired)
	}
	indexed := 0
	if s.Store.VectorStore != nil {
		indexed = s.Store.VectorStore.Size()
	}
	s.ops("VECTOR_SYNC_COMPLETE", auth.Principal{Key: "admin"}, "", "ok", "", "", 0, map[string]any{"embedded": count, "indexed_vectors": indexed})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"embedded": count, "indexed_vectors": indexed})
}

type vectorRebuildRequest struct {
	IncidentID string `json:"incident_id"`
	Confirm    bool   `json:"confirm"`
}

// adminVectorRebuild is the explicit operator-authorized remediation path.
// MCP never receives this credential or invokes this route.
func (s *Server) adminVectorRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	var request vectorRebuildRequest
	if err := decodeStrict(r, &request); err != nil || !request.Confirm || request.IncidentID == "" {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]any{"code": "CONFIRMATION_REQUIRED", "message": "incident_id and confirm=true are required"})
		return
	}
	status := s.SystemStatus()
	matched := false
	for _, incident := range status.Incidents {
		if incident.IncidentID == request.IncidentID {
			matched = true
			break
		}
	}
	if !matched || (status.State != SystemActionRequired && status.State != SystemDegraded) {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]any{"code": "INCIDENT_CHANGED", "message": "the incident is resolved or configuration changed; run mindmoryctl vectors status"})
		return
	}
	if !s.vectorRebuildMu.TryLock() {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]any{"code": "VECTOR_REBUILD_RUNNING", "message": "a vector rebuild is already running"})
		return
	}
	defer s.vectorRebuildMu.Unlock()
	if s.Embedder == nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "EMBEDDING_DISABLED", "message": "embedding provider is disabled"})
		return
	}
	if s.status != nil {
		s.status.building(request.IncidentID)
	}
	s.semanticBlocked.Store(true)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	summary, err := s.Store.SyncVectors(ctx, s.Embedder, VectorSyncOptions{})
	if err != nil || summary.Failed > 0 {
		if s.status != nil {
			s.status.rebuildFailed(s.Store, request.IncidentID)
		}
		s.Log.Error("vector rebuild failed", "incident_id", request.IncidentID, "error", err, "failed_rows", summary.Failed)
		s.ops("VECTOR_REBUILD_FAILED", auth.Principal{Key: "admin"}, "", "error", "VECTOR_REBUILD_FAILED", request.IncidentID, 0, nil)
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]any{"code": "VECTOR_REBUILD_FAILED", "message": "vector rebuild failed; the incident remains active"})
		return
	}
	if s.status != nil {
		refreshed := s.status.refresh(s.Store)
		s.semanticBlocked.Store(refreshed.State == SystemActionRequired)
	}
	s.ops("VECTOR_REBUILD_COMPLETE", auth.Principal{Key: "admin"}, "", "ok", "", request.IncidentID, 0, map[string]any{"embedded": summary.Embedded, "already_current": summary.AlreadyCurrent, "failed": summary.Failed})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": s.SystemStatus(), "summary": summary})
}

// adminLearnerExtract runs one passive-learner extract pass over eligible
// archived user turns. Authorized by admin (X-Admin-Token) or local trust.
// Optional ?limit=N bounds the scan (default 50, max 500).
func (s *Server) adminLearnerExtract(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	summary, err := s.LearnerExtract(r.Context(), s.LearnerPrincipal(), limit)
	if err != nil {
		s.Log.Error("learner extract failed", "error", err)
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, summary)
}

// adminOps returns the last N operational journal events. Authorized either
// by the admin token (X-Admin-Token) or by an MCP principal holding OPS_READ
// — so the nerves are reachable through the MCP tool surface.
func (s *Server) adminOps(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		if _, err := s.authenticate(r, config.MCPOpsRead); err != nil {
			s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
			return
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	events, err := s.Store.Ops.Recent(limit)
	if err != nil {
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	if events == nil {
		events = []OpsEvent{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
}

// adminListProposals lists proposals filtered by status (PENDING default).
func (s *Server) adminListProposals(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "PENDING"
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	proposals, err := s.Store.ListProposals(r.Context(), status, limit)
	if err != nil {
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	out := make([]map[string]any, 0, len(proposals))
	for _, p := range proposals {
		out = append(out, map[string]any{
			"proposal_id":   p.ID,
			"mutation":      string(p.Identity.Mutation),
			"kind":          string(p.Identity.ProposedKind),
			"scope":         string(p.Identity.Scope),
			"subject":       p.Identity.Subject,
			"replacement":   p.Identity.Replacement,
			"target_memory": p.Identity.TargetMemoryID,
			"status":        string(p.Status),
			"reason_code":   p.ReasonCode,
			"gate_class":    string(p.GateClass),
			"created_at":    p.CreatedAt,
			"evidence_hash": p.Identity.RequestEvidenceHash,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"proposals": out, "count": len(out)})
}

// adminApproveProposal permits a reviewer to override only intent uncertainty.
// It rehydrates and revalidates canonical evidence and target state, then uses
// the same apply path as automatic application.
func (s *Server) adminApproveProposal(w http.ResponseWriter, r *http.Request) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	proposalID := r.PathValue("id")
	proposal, ok, err := s.Store.GetProposal(r.Context(), proposalID)
	if err != nil || !ok {
		s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
		return
	}
	if proposal.Status != domain.ProposalPending {
		s.fail(w, apperror.New(apperror.MemoryMutationConflict, false, nil))
		return
	}
	if proposal.GateClass != domain.GateIntent || proposal.ReasonCode != "EXPLICIT_INTENT_NOT_VERIFIED" {
		s.fail(w, apperror.New(apperror.MemoryMutationConflict, false, nil))
		return
	}
	principal := auth.Principal{Key: proposal.Identity.ClientKey, Type: auth.PrincipalMCP}
	if _, err := s.Store.ResolveSession(r.Context(), principal, proposal.Identity.SessionID); err != nil {
		s.fail(w, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil))
		return
	}
	evidence, err := s.Store.LoadMessageEvidence(r.Context(), proposal.Identity.SessionID, proposal.Identity.MessageID)
	if err != nil || proposal.Identity.Evidence == nil {
		s.fail(w, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil))
		return
	}
	evidence.ClientID = principal.Key
	evidence.SessionID = proposal.Identity.SessionID
	// GateIntent is created only after the original current-turn check passed.
	// Review may happen later, so revalidate the immutable evidence while
	// preserving that original current-turn authorization fact.
	evidence.CurrentUserTurn = true
	if proposal.Identity.EvidenceContentHash == "" ||
		hashContent(evidence.Content) != proposal.Identity.EvidenceContentHash {
		s.fail(w, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil))
		return
	}
	ref := proposal.Identity.Evidence
	if ref.StartByte < 0 || ref.EndByte > len(evidence.Content) || ref.StartByte >= ref.EndByte {
		s.fail(w, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil))
		return
	}
	quote := evidence.Content[ref.StartByte:ref.EndByte]
	if !utf8Valid(quote) || quoteHash(quote) != ref.Hash || ref.Hash != proposal.Identity.RequestEvidenceHash {
		s.fail(w, apperror.New(apperror.MemoryEvidenceUnavailable, false, nil))
		return
	}
	request := mutationRequest{
		SessionID: proposal.Identity.SessionID, MessageID: proposal.Identity.MessageID,
		Mutation: proposal.Identity.Mutation, MemoryKind: proposal.Identity.ProposedKind,
		Scope: proposal.Identity.Scope, Subject: proposal.Identity.Subject,
		EvidenceQuote:  quote,
		TargetMemoryID: proposal.Identity.TargetMemoryID, Replacement: proposal.Identity.Replacement,
	}
	target, targetRow, err := s.mutationTarget(r.Context(), request)
	if err != nil {
		s.fail(w, err)
		return
	}
	decision := domain.VerifyMutationForReview(domain.MutationRequest{
		Kind: request.Mutation, ClientID: principal.Key, SessionID: request.SessionID,
		MessageID: request.MessageID, EvidenceQuote: request.EvidenceQuote,
		Subject: request.Subject, TargetMemoryID: request.TargetMemoryID, Replacement: request.Replacement,
	}, evidence, target)
	if decision.Outcome != domain.MutationApply {
		s.fail(w, apperror.New(apperror.MemoryMutationConflict, false, nil))
		return
	}
	memoryID, revision, err := s.apply(r.Context(), request, proposalID, proposal.Identity, target, targetRow, evidence, "REVIEWER_APPROVED")
	if err != nil {
		s.ops("PROPOSAL_APPROVE", principal, request.SessionID, "error", apperror.Code(err), proposalID, 0, nil)
		s.fail(w, err)
		return
	}
	s.ops("PROPOSAL_APPROVE", principal, request.SessionID, "ok", "", proposalID, 0, map[string]any{"memory_id": memoryID, "revision": revision})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"outcome": "APPLIED", "memory_id": memoryID, "continuity_revision": revision})
}

// adminRejectProposal marks a PENDING proposal REJECTED.
func (s *Server) adminRejectProposal(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	proposalID := r.PathValue("id")
	proposal, ok, err := s.Store.GetProposal(r.Context(), proposalID)
	if err != nil || !ok {
		s.fail(w, apperror.New(apperror.MemoryProposalInvalid, false, nil))
		return
	}
	if proposal.Status != domain.ProposalPending {
		s.fail(w, apperror.New(apperror.MemoryMutationConflict, false, nil))
		return
	}
	reason := "REVIEWER_REJECTED"
	if raw := r.URL.Query().Get("reason"); raw != "" && len(raw) < 200 {
		reason = raw
	}
	if err := s.Store.RejectProposal(r.Context(), proposalID, reason); err != nil {
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	s.ops("PROPOSAL_REJECT", auth.Principal{Key: "admin"}, proposal.Identity.SessionID, "ok", reason, proposalID, 0, nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"outcome": "REJECTED", "proposal_id": proposalID})
}

// adminRetireMemory marks an ACTIVE memory SUPERSEDED. This is the undo
// path for the review lane: a reviewer who approved a duplicate or a
// mistake can retire the resulting memory without deleting canonical data.
// The row keeps its history (lifecycle SUPERSEDED, updated_at bumped); the
// FTS index is refreshed so it stops matching; a MEMORY_SUPERSEDED
// continuity entry records the change. Nothing is ever hard-deleted.
func (s *Server) adminRetireMemory(w http.ResponseWriter, r *http.Request) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	memoryID := r.PathValue("id")
	row, err := s.Store.LoadMemoryRow(r.Context(), memoryID)
	if err != nil || row.MemoryID == "" {
		s.fail(w, apperror.New(apperror.MemoryTargetNotFound, false, nil))
		return
	}
	if row.Lifecycle != string(domain.LifecycleActive) {
		s.fail(w, apperror.New(apperror.MemoryMutationConflict, false, nil))
		return
	}
	projectKey := ""
	if row.ProjectKey != "" {
		projectKey = row.ProjectKey
	}
	committed, err := s.Store.CommitMutation(r.Context(), MutationCommit{
		Mutation: domain.MutationForget, RequestHash: hashContent("admin-retire:" + memoryID + ":" + strconv.FormatInt(row.StateVersion, 10)),
		ResolutionReason: "REVIEWER_RETIRED", ReviewAuthorization: "ADMIN_RETIRE",
		TargetMemoryID: memoryID, ExpectedTargetVersion: row.StateVersion, TargetLifecycle: domain.LifecycleSuperseded,
		ContinuityKind: "MEMORY_SUPERSEDED", ProjectKey: projectKey, Sensitivity: row.Sensitivity, TraceID: s.IDs.New(),
	})
	if err != nil {
		s.Log.Error("retire failed", "error", err, "memory_id", memoryID)
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	if committed.ProjectionError != nil {
		s.Log.Error("retire committed; projection refresh deferred", "event_id", committed.EventID, "error", committed.ProjectionError)
	}
	s.ops("MEMORY_RETIRE", auth.Principal{Key: "admin"}, "", "ok", "REVIEWER_RETIRED", memoryID, 0, map[string]any{
		"revision": committed.Revision, "reason": "duplicate/mistake cleanup",
	})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"outcome": "SUPERSEDED", "memory_id": memoryID, "continuity_revision": committed.Revision})
}

// adminSnapshot creates a frozen, hash-manifested canonical snapshot.
func (s *Server) adminSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		s.fail(w, apperror.New(apperror.AuthRequired, false, nil))
		return
	}
	result, err := s.Store.CreateSnapshot()
	if err != nil {
		s.Log.Error("snapshot creation failed", "error", err)
		s.fail(w, apperror.New(apperror.InternalError, false, nil))
		return
	}
	s.ops("SNAPSHOT", auth.Principal{Key: "admin"}, "", "ok", "FROZEN", result.SnapshotID, 0, nil)
	httpapi.WriteJSON(w, http.StatusOK, result)
}

// --- helpers ---

func (s *Server) sessionScope(ctx context.Context, principal auth.Principal, sessionID string) (retrieval.SessionScope, error) {
	row, err := s.Store.ResolveSession(ctx, principal, sessionID)
	if err != nil {
		return retrieval.SessionScope{}, apperror.New(apperror.ContextSessionNotFound, false, nil)
	}
	return retrieval.SessionScope{SessionID: row.SessionID, ClientKey: row.ClientKey, ProjectKey: row.ProjectKey}, nil
}

func (s *Server) projectContext(ctx context.Context, projectKey string) (*retrieval.ProjectContext, error) {
	if projectKey == "" {
		return nil, nil
	}
	row, err := s.Store.CurrentProjectContext(ctx, projectKey)
	if err != nil || row == nil {
		return nil, nil
	}
	return &retrieval.ProjectContext{
		Revision: row.Revision, Objective: row.Objective, CurrentState: row.CurrentState,
		Decisions: json.RawMessage(row.Decisions), OpenQuestions: json.RawMessage(row.OpenQuestions),
		NextActions: json.RawMessage(row.NextActions),
	}, nil
}

// ranked pairs a memory with its deterministic ranking key and the wire
// hit. Both keyword and semantic candidates produce these; they are merged
// and sorted once by RankKey.
type ranked struct {
	row MemoryRow
	key RankKey
	hit retrieval.MemoryHit
}

func (s *Server) searchMemories(ctx context.Context, scope retrieval.SessionScope, request retrieval.SearchRequest, recordAccess bool) ([]retrieval.MemoryHit, error) {
	// Candidate retrieval is SQL: the FTS5 trigram index finds plausible
	// memories (with LIKE fallback for short CJK queries). Final ranking
	// uses the deterministic Go scorer (matches Postgres ranking).
	//
	// Cross-language alias expansion (P2): the query is expanded through
	// the entity alias table (e.g. "the warmth that waits on the near
	// bank" -> 余烬永温) and every expansion contributes candidates. The
	// union is deduplicated by memory id; ranking always uses the ORIGINAL
	// query, so expansions widen the pool without reordering hits.
	// candidateRow pairs a recalled memory with the query that surfaced it.
	// The original query is what the user asked; an expansion (canonical
	// term) is what the index could match. ClassifyMatch must run against
	// the query that actually matched, otherwise an alias-recovered CJK
	// memory scores MatchNone against the English original and is dropped.
	type candidateRow struct {
		row   MemoryRow
		query string
	}
	var candidates []candidateRow
	if s.Store.Index != nil {
		kinds := make([]string, 0, len(request.Kinds))
		for _, kind := range request.Kinds {
			kinds = append(kinds, string(kind))
		}
		seen := map[string]bool{}
		for _, q := range s.Aliases.Expand(request.Query) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ids, err := s.Store.Index.SearchCandidates(q, scope.ProjectKey, kinds, 200)
			if err != nil {
				s.Log.Error("index search failed", "error", err)
				// Fall back to full scan on index trouble — canonical JSONL
				// is authoritative, the index is derived.
				continue
			}
			rows, err := s.Store.Index.LoadMemories(ids)
			if err != nil {
				s.Log.Error("candidate hydration failed", "error", err)
				continue
			}
			for _, row := range rows {
				if seen[row.MemoryID] {
					continue
				}
				if !memoryRowEligible(scope, row, false) || !memoryKindRequested(row, request.Kinds) {
					continue
				}
				seen[row.MemoryID] = true
				candidates = append(candidates, candidateRow{row: row, query: q})
			}
		}
	}
	if len(candidates) == 0 {
		rows, err := s.Store.EligibleMemories(ctx, scope, request.Kinds, 0)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			candidates = append(candidates, candidateRow{row: row, query: request.Query})
		}
	}
	hits := []retrieval.MemoryHit{}
	var rankedHits []ranked
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := candidate.row
		// A candidate may have been recalled by an alias expansion that
		// matches it far better than the original query does (e.g. the
		// memory "Example Person 的中文名是示例用户, LinkedIn is…" is recalled by
		// the reverse expansion "example person" from the query "示例用户 linkedin",
		// and matches "example person" strongly but the original query only
		// weakly). Take whichever query scores the STRONGER match, so an
		// alias-recovered hit is not penalized for the original query's
		// language mismatch; a genuinely better original-query match still
		// wins because it is evaluated and ranked first.
		// The original query is the user's intent: it ranks first. An
		// alias/prefix-recovered candidate uses the recalling query only
		// when the original scores nothing — the expansion widens the
		// candidate pool, it never re-ranks a hit above what the original
		// query says. (This is why the reverse-alias recall of "example person"
		// cannot promote every Example Person memory above the memory that
		// actually matches "示例用户 linkedin".)
		match := ClassifyMatch(row, request.Query)
		if match.Class == MatchNone {
			alt := ClassifyMatch(row, candidate.query)
			if alt.Class != MatchNone {
				match = alt
			}
		}
		// The index may have recalled this candidate via a truncated CJK
		// prefix (the "薪尽火传的传" case: FTS/LIKE found 薪尽火传 inside
		// it). Evaluate progressively shorter CJK prefixes of the original
		// query too, so such a hit is not dropped at scoring time. This
		// applies only when the query is entirely CJK: a mixed query like
		// "示例用户 linkedin" already has alias reverse-expansion doing the
		// recall, and prefix-scoring it would re-rank candidates by a
		// fragment ("黄庸") that over-promotes unrelated identity memories.
		if match.Class == MatchNone && isAllCJK(request.Query) {
			for _, prefix := range cjkPrefixes(request.Query) {
				p := ClassifyMatch(row, prefix)
				if p.Class != MatchNone && (match.Class == MatchNone || strongerMatch(p, match)) {
					match = p
				}
			}
		}
		if match.Class == MatchNone {
			continue
		}
		sessionsSince := s.sessionsSince(ctx, scope.SessionID, row)
		key := RankKeyFor(row, match, sessionsSince)
		score := DisplayScore(match, ActivationFor(row.Kind, row.Activation, sessionsSince, row.Importance), row.Confidence, row.Disputed, row.ScopeType == "PROJECT")
		rankedHits = append(rankedHits, ranked{row: row, key: key, hit: s.toHit(row, sessionsSince, score, match)})
	}
	// Semantic layer: vector candidates join the SAME ranking pool as
	// keyword hits (unified RankKey sort below), so a strong semantic match
	// can surface even when keyword weak-matches (the Fuzzy platform)
	// monopolize the candidate slots. Keyword hits are never displaced —
	// the union is sorted once by the same deterministic key; a strong
	// semantic hit (high cosine) is classified at MatchContent level so it
	// outranks the weak keyword matches, weaker ones stay MatchSemantic and
	// rank after all keyword hits. Controlled by MINDMORY_SEMANTIC_SEARCH
	// (default "0" — lexical-first is the evaluated default).
	mode := request.Mode
	if mode == "" {
		if s.semanticEnabled() {
			mode = retrieval.SearchSemanticFallback
		} else {
			mode = retrieval.SearchLexical
		}
	}
	strongLexical := false
	for _, r := range rankedHits {
		if r.hit.MatchClass >= int(MatchContent) {
			strongLexical = true
			break
		}
	}
	runSemantic := mode == retrieval.SearchSemantic || (mode == retrieval.SearchSemanticFallback && !strongLexical)
	if mode == retrieval.SearchSemanticFallback && s.SemanticOnlyOnEmptyExperiment {
		runSemantic = len(rankedHits) == 0
	}
	if mode == retrieval.SearchSemantic && s.SemanticOnlyExperiment {
		rankedHits = nil
	}
	fusedOrder := false
	if runSemantic && s.semanticEnabled() && s.Store.VectorStore != nil && s.Store.VectorStore.Size() > 0 {
		if semantic, err := s.vectorHits(ctx, scope, request); err == nil {
			if s.SemanticTopOneExperiment {
				sort.SliceStable(rankedHits, func(i, j int) bool { return lessKey(rankedHits[i].key, rankedHits[j].key) })
				seen := map[string]bool{}
				merged := make([]ranked, 0, len(rankedHits)+1)
				if len(semantic) > 0 {
					merged = append(merged, semantic[0])
					seen[semantic[0].row.MemoryID] = true
				}
				for _, candidate := range rankedHits {
					if !seen[candidate.row.MemoryID] {
						seen[candidate.row.MemoryID] = true
						merged = append(merged, candidate)
					}
				}
				rankedHits = merged
				fusedOrder = true
			} else if s.SemanticVectorFirstExperiment {
				sort.SliceStable(rankedHits, func(i, j int) bool { return lessKey(rankedHits[i].key, rankedHits[j].key) })
				seen := map[string]bool{}
				merged := make([]ranked, 0, len(semantic)+len(rankedHits))
				for _, candidate := range semantic {
					if !seen[candidate.row.MemoryID] {
						seen[candidate.row.MemoryID] = true
						merged = append(merged, candidate)
					}
				}
				for _, candidate := range rankedHits {
					if !seen[candidate.row.MemoryID] {
						seen[candidate.row.MemoryID] = true
						merged = append(merged, candidate)
					}
				}
				rankedHits = merged
				fusedOrder = true
			} else if s.SemanticRRFFusionExperiment {
				semanticWeight := s.SemanticRRFWeightExperiment
				if semanticWeight <= 0 {
					semanticWeight = 2
				}
				rankedHits = fuseRankedCandidates(rankedHits, semantic, 1, semanticWeight)
				fusedOrder = true
			} else {
				seen := map[string]bool{}
				for _, r := range rankedHits {
					seen[r.row.MemoryID] = true
				}
				for _, r := range semantic {
					if !seen[r.row.MemoryID] {
						seen[r.row.MemoryID] = true
						rankedHits = append(rankedHits, r)
					}
				}
			}
		}
	}
	if !fusedOrder {
		sort.SliceStable(rankedHits, func(i, j int) bool { return lessKey(rankedHits[i].key, rankedHits[j].key) })
	}
	for _, r := range rankedHits {
		hits = append(hits, r.hit)
	}
	// Identity-core memories are not truncated by the caller's limit: they
	// are the anchors of who Ember is, and dropping one because a search
	// asked for 5 results loses real identity context. All MATCHED identity
	// hits are kept (ranked by the same RankKey), bounded by IdentityHitsMax
	// so an identity-heavy future corpus cannot unboundedly grow responses.
	// Non-identity hits keep the caller's limit; identity hits are additive,
	// so a search for "ember" returns its identity core plus the top-N
	// project records. The overall order is still the RankKey order (identity
	// already sorts first within a match class).
	limit := request.EffectiveLimit()
	identityKept, nonIdentityKept := 0, 0
	kept := make([]retrieval.MemoryHit, 0, len(hits))
	for _, h := range hits {
		// Identity hits bypass the caller's limit only when the match is
		// strong enough to be genuinely about this identity: the query term
		// appears in the subject (MatchSubject/MatchExact) or content
		// (MatchContent). Fuzzy trigram overlap is not enough — it would
		// surface unrelated identity preferences ("fertilizer" for "ember").
		if retrieval.IdentityKind(h.Kind) && h.MatchClass >= int(MatchSubject) {
			if identityKept < retrieval.IdentityHitsMax {
				kept = append(kept, h)
				identityKept++
			}
			continue
		}
		if nonIdentityKept < limit {
			kept = append(kept, h)
			nonIdentityKept++
		}
	}
	hits = kept
	// Access recording is a USE signal (search = the model asked). The
	// per-step relevance lane passes recordAccess=false so injection never
	// warms a memory into a self-reinforcing re-injection loop.
	if recordAccess {
		for i := range hits {
			_ = s.Store.RecordAccess(ctx, hits[i].MemoryID, scope.SessionID, retrieval.AccessSearchHit)
		}
	}
	return hits, nil
}

func fuseRankedCandidates(lexical, semantic []ranked, lexicalWeight, semanticWeight float64) []ranked {
	sort.SliceStable(lexical, func(i, j int) bool { return lessKey(lexical[i].key, lexical[j].key) })
	type fused struct {
		candidate ranked
		score     float64
	}
	byID := map[string]fused{}
	for i, candidate := range lexical {
		byID[candidate.row.MemoryID] = fused{candidate: candidate, score: lexicalWeight / float64(60+i+1)}
	}
	for i, candidate := range semantic {
		id := candidate.row.MemoryID
		entry, ok := byID[id]
		if !ok {
			entry.candidate = candidate
		} else if lessKey(candidate.key, entry.candidate.key) {
			entry.candidate = candidate
		}
		entry.score += semanticWeight / float64(60+i+1)
		byID[id] = entry
	}
	out := make([]fused, 0, len(byID))
	for _, entry := range byID {
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score == out[j].score {
			return lessKey(out[i].candidate.key, out[j].candidate.key)
		}
		return out[i].score > out[j].score
	})
	result := make([]ranked, len(out))
	for i := range out {
		result[i] = out[i].candidate
	}
	return result
}

// vectorHits embeds the query locally and returns top semantic candidates
// that are eligible in this scope, scored by cosine similarity (0.4-0.8
// band: below keyword threshold of 0.82 substring, above nothing).
// semanticEnabled: lexical-first is the evaluated default (Recall@5 0.917
// vs 0.792 with blending on the frozen corpus — blending polluted negative
// queries like "heat"). Enable semantic blending explicitly only when the
// embedding model is strong enough that its noise floor stays below the
// adversarially evaluated minimum threshold.
func (s *Server) semanticEnabled() bool {
	if s.semanticBlocked.Load() {
		return false
	}
	if s.SemanticSearch != nil {
		return *s.SemanticSearch
	}
	return false
}

func (s *Server) vectorHits(ctx context.Context, scope retrieval.SessionScope, request retrieval.SearchRequest) ([]ranked, error) {
	embedder := s.Embedder
	if embedder == nil {
		return nil, fmt.Errorf("semantic embedding provider unavailable")
	}
	generation := s.Store.VectorStore.Generation()
	if s.queryCache == nil {
		s.queryCache = newQueryVectorCache(256)
	}
	queryInput := request.Query
	if instruction := strings.TrimSpace(s.SemanticQueryInstruction); instruction != "" {
		queryInput = "Instruct: " + instruction + "\nQuery: " + request.Query
	}
	queryVector, cached := s.queryCache.get(generation, queryInput)
	if !cached {
		vectors, err := embedder.Embed(ctx, []string{queryInput})
		if err != nil || len(vectors) == 0 {
			return nil, err
		}
		queryVector = vectors[0]
		s.queryCache.put(generation, queryInput, queryVector)
	}
	minimumScore := semanticMinimumThreshold
	if s.SemanticMinimumScoreExperiment != nil {
		minimumScore = *s.SemanticMinimumScoreExperiment
	}
	candidates, err := s.Store.VectorStore.Search(ctx, queryVector, vectorstore.SearchOptions{Limit: request.EffectiveLimit() * 3, MinimumScore: minimumScore})
	if err != nil {
		return nil, err
	}
	if len(candidates) > 0 && s.SemanticMinimumMarginExperiment > 0 && s.SemanticHighScoreExperiment > 0 && candidates[0].Score < s.SemanticHighScoreExperiment {
		if len(candidates) < 2 || candidates[0].Score-candidates[1].Score < s.SemanticMinimumMarginExperiment {
			return nil, nil
		}
	}
	resolved := map[string]string{}
	if s.Store.Index != nil {
		resolved, err = s.Store.Index.ResolveVectorCandidates(s.Store.VectorStore.Generation(), scope.ProjectKey, candidates)
		if err != nil {
			return nil, err
		}
	} else {
		for _, candidate := range candidates {
			resolved[candidate.SourceID] = candidate.EmbeddingInputHash
		}
	}
	var out []ranked
	for _, candidate := range candidates {
		id := candidate.SourceID
		row, err := s.Store.LoadMemoryRow(ctx, id)
		if err != nil || !memoryRowEligible(scope, row, false) {
			continue
		}
		if resolved[id] != candidate.EmbeddingInputHash || EmbeddingInputHash(row) != candidate.EmbeddingInputHash {
			continue
		}
		cos := candidate.Score
		sessionsSince := s.sessionsSince(ctx, scope.SessionID, row)
		// A strong semantic hit (cos >= semanticStrongThreshold) is a
		// genuine content-level match and outranks the keyword weak-match
		// platform; weaker semantic hits stay at MatchSemantic (after all
		// keyword hits).
		class := MatchSemantic
		if cos >= semanticStrongThreshold {
			class = MatchContent
		}
		match := MatchResult{Class: class, Strength: cos}
		key := RankKeyFor(row, match, sessionsSince)
		score := DisplayScore(match, ActivationFor(row.Kind, row.Activation, sessionsSince, row.Importance), row.Confidence, row.Disputed, row.ScopeType == "PROJECT")
		out = append(out, ranked{row: row, key: key, hit: s.toHit(row, sessionsSince, round6(score), match)})
	}
	return out, nil
}

func memoryKindRequested(row MemoryRow, kinds []domain.Kind) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, kind := range kinds {
		if row.Kind == string(kind) {
			return true
		}
	}
	return false
}

// semanticStrongThreshold is the cosine above which a vector hit is treated
// as a strong semantic match (ranked at Content level, above the keyword
// weak-match platform). Below it, the hit stays MatchSemantic and ranks
// after all keyword hits. The 200-case fixture found unrelated candidates in
// the low 0.60s while true paraphrase pairs remained around 0.73.
const semanticMinimumThreshold = 0.68
const semanticStrongThreshold = 0.78

func (s *Server) recentMemories(ctx context.Context, scope retrieval.SessionScope, scopeType string, limit int) ([]retrieval.MemoryHit, error) {
	rows, err := s.Store.EligibleMemories(ctx, scope, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []retrieval.MemoryHit
	for _, row := range rows {
		if row.ScopeType != scopeType {
			continue
		}
		sessionsSince := s.sessionsSince(ctx, scope.SessionID, row)
		eff := ActivationFor(row.Kind, row.Activation, sessionsSince, row.Importance)
		out = append(out, s.toHit(row, sessionsSince, round6(eff), MatchResult{Class: MatchNone}))
	}
	// recent = highest effective activation first (used recently), not
	// merely most recently edited.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EffectiveHeat != out[j].EffectiveHeat {
			return out[i].EffectiveHeat > out[j].EffectiveHeat
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sessionsSince counts sessions started after the memory's last use — the
// decay clock of §A3. The decay unit is SESSIONS, not days: τ₀ is expressed
// in sessions, so a memory untouched across N sessions (whatever the wall
// time) decays by N. Mirrors the original Postgres query
//
//	select count(*) from sessions s
//	where s.started_at > coalesce(m.last_accessed_at, m.updated_at)
//
// Absence is immune: zero sessions since last use yields zero decay.
// sessionsSince computes decay Δs from the monotonic session ordinal: the
// current session's seq minus the memory's last meaningful use seq (P0-6).
// Immune to wall-clock changes, concurrent sessions, and import history.
// A memory with no recorded use since import (lastUsedSeq=0) is treated as
// used at the first session so it does not instantly decay.
func (s *Server) sessionsSince(ctx context.Context, sessionID string, row MemoryRow) int64 {
	cur, ok := s.Store.SessionSeq(ctx, sessionID)
	if !ok {
		return 0
	}
	last := row.LastUsedSeq
	if last <= 0 {
		last = 1 // first session = baseline
	}
	if cur <= last {
		return 0
	}
	return cur - last
}

// toHit renders a memory row as a retrieval hit. EffectiveHeat is the
// session-decayed activation (no importance floor); the wire Heat field
// carries activation for contract compatibility; score is the display
// score passed by the caller (lexicographic ranking happens before).
func (s *Server) toHit(row MemoryRow, sessionsSince int64, score float64, match MatchResult) retrieval.MemoryHit {
	eff := ActivationFor(row.Kind, row.Activation, sessionsSince, row.Importance)
	return retrieval.MemoryHit{
		MemoryID: row.MemoryID, Kind: row.Kind, Subject: row.Subject, Content: row.Content,
		Scope: row.ScopeType, ProjectKey: row.ProjectKey, EpistemicStatus: row.EpistemicStatus,
		Score: score, Importance: row.Importance, Heat: row.Activation, EffectiveHeat: eff,
		AccessCount: row.AccessCount, RepeatCount: row.RepeatCount, Grade: retrieval.HeatGrade(row.Importance, eff),
		UpdatedAt: row.UpdatedAt, MatchClass: int(match.Class), MatchStrength: match.Strength,
	}
}

// activationEffective computes the session-decayed activation for a row.
func (s *Server) activationEffective(ctx context.Context, sessionID string, row MemoryRow) float64 {
	return ActivationFor(row.Kind, row.Activation, s.sessionsSince(ctx, sessionID, row), row.Importance)
}

// updatedAt returns the memory's update time (time.Time field).
func (m MemoryRow) updatedAt() time.Time { return m.UpdatedAt }

func reflexIdentityKind(kind string) bool {
	switch domain.Kind(kind) {
	case domain.KindUserPreference, domain.KindPersonalGoal, domain.KindPersonalConstraint:
		return true
	default:
		return false
	}
}

func reflexImportantKind(kind string) bool {
	switch domain.Kind(kind) {
	case domain.KindProjectDecision, domain.KindPersonalGoal, domain.KindLesson:
		return true
	default:
		return false
	}
}

func reflexLoops(pc *retrieval.ProjectContext, budget int, truncated *bool) []string {
	var questions, actions []string
	_ = json.Unmarshal(pc.OpenQuestions, &questions)
	_ = json.Unmarshal(pc.NextActions, &actions)
	out := make([]string, 0, len(questions)+len(actions))
	for _, value := range append(questions, actions...) {
		if strings.TrimSpace(value) == "" {
			continue
		}
		used := retrieval.EstimatedTokens(value)
		if used > budget {
			*truncated = true
			continue
		}
		out = append(out, value)
		budget -= used
	}
	return out
}

func reflexChangeTokens(change retrieval.ContinuityChange) int {
	line := change.ChangeKind + " " + change.TargetKind + " " + change.ProjectKey + " " + change.CreatedAt.Format(time.RFC3339Nano)
	return retrieval.EstimatedTokens(line) + 14
}

func proposalResult(proposal domain.Proposal, replay bool) mutationResult {
	return mutationResult{ProposalID: proposal.ID, Outcome: string(proposal.Status), ReasonCode: proposal.ReasonCode,
		MemoryID: proposal.AppliedMemoryID, Replay: replay}
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func checkpointMessageHash(m checkpointMessage) string {
	return tupleHash(string(m.Role), m.ContentType, m.Content, archive.FormatCanonicalTimestamp(m.OccurredAt))
}

// formatMicros renders timestamps the way the live recall contract does:
// fixed microsecond precision in UTC.
func formatMicros(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func utf8Valid(value string) bool {
	return utf8.ValidString(value)
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

// findDuplicateSubject returns the memory_id of an existing ACTIVE memory
// whose subject is near-identical to the proposed subject in the same
// scope, or "" when no duplicate exists. Similarity is trigram-based, so
// small edits ("compliance rules" vs "compliance rules (war-room)") are
// caught while genuinely distinct subjects ("我是双语宝宝" vs "我的中文名
// 是余烬") are not. Scope is respected: a GLOBAL proposal is compared
// against GLOBAL memories; a PROJECT proposal against memories in the same
// project.
func findDuplicateSubject(rows []MemoryRow, subject string, scope domain.ScopeType) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	subjectNorm := normalizeForDedupe(subject)
	best := ""
	bestSim := 0.0
	for _, row := range rows {
		if row.ScopeType != string(scope) {
			continue
		}
		sim := trigramSimilarity(subjectNorm, normalizeForDedupe(row.Subject))
		if sim > bestSim {
			bestSim = sim
			best = row.MemoryID
		}
	}
	if bestSim >= duplicateSubjectThreshold {
		return best
	}
	return ""
}

// normalizeForDedupe lowercases and strips punctuation/whitespace so that
// near-identical subjects differing only in spacing or punctuation
// ("rules (war-room): no" vs "rules (war-room):no") are recognized as the
// same content. Genuinely distinct subjects stay distinct after this pass.
func normalizeForDedupe(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', isCJK(r):
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// isCJK reports whether r is a CJK character using the same table as the
// rest of the codebase (unicode.Han + Hiragana + Katakana + Hangul), so the
// dedupe normalizer and the alias expander agree on what counts as CJK.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// strongerMatch reports whether match a outranks match b: higher class wins;
// same class, higher strength wins.
func strongerMatch(a, b MatchResult) bool {
	if a.Class != b.Class {
		return a.Class > b.Class
	}
	return a.Strength > b.Strength
}

// cjkPrefixes returns progressively shorter CJK prefixes of a query (from
// len-1 down to 2 runes), matching the index layer's LIKE fallback strategy
// for CJK strings without word boundaries.
func cjkPrefixes(query string) []string {
	var cjk strings.Builder
	for _, run := range cjkRuns(query) {
		cjk.WriteString(run)
	}
	runes := []rune(cjk.String())
	if len(runes) < 3 {
		return nil
	}
	out := make([]string, 0, len(runes)-2)
	for n := len(runes) - 1; n >= 2; n-- {
		out = append(out, string(runes[:n]))
	}
	return out
}

// isAllCJK reports whether the query consists solely of CJK characters
// (ignoring whitespace and punctuation). Prefix scoring is restricted to
// such queries; mixed-language queries rely on alias expansion instead.
func isAllCJK(query string) bool {
	for _, r := range query {
		if r == ' ' || r == '\t' || r == '\n' {
			continue
		}
		if !isCJK(r) {
			return false
		}
	}
	return len(strings.TrimSpace(query)) > 0
}
