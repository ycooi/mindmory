package lite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/config"
	domain "mindmory.local/core/internal/memory"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestRecoverPanicsKeepsServerAlive verifies that a panicking handler is
// converted into a 500 response and the process (this test) survives.
func TestRecoverPanicsKeepsServerAlive(t *testing.T) {
	s := &Server{Log: testLogger()}
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := s.recoverPanics(panicking)
	req := httptest.NewRequest("GET", "/boom", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["code"] != "INTERNAL_ERROR" {
		t.Fatalf("body = %v", body)
	}
	// A subsequent request still works — the middleware did not corrupt
	// the handler chain.
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/again", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("second status = %d", rec.Code)
	}
}

// TestSemanticStrongThresholdClass asserts the threshold constant lives in
// the expected range: clearly above the measured noise floor and below
// near-perfect cosine, so strong semantic hits classify at Content level.
func TestSemanticStrongThresholdClass(t *testing.T) {
	if semanticMinimumThreshold < 0.65 || semanticMinimumThreshold >= semanticStrongThreshold {
		t.Fatalf("semanticMinimumThreshold = %v", semanticMinimumThreshold)
	}
	if semanticStrongThreshold < 0.7 || semanticStrongThreshold > 0.9 {
		t.Fatalf("semanticStrongThreshold = %v, want 0.7-0.9", semanticStrongThreshold)
	}
	// A cosine at or above the threshold classifies at Content level.
	match := MatchResult{Class: MatchSemantic, Strength: semanticStrongThreshold}
	if match.Class != MatchSemantic {
		t.Fatalf("class must start semantic, got %d", match.Class)
	}
}

func governanceFixture(t *testing.T) (*Server, *Store, auth.Principal, SessionRow) {
	t.Helper()
	store := newTestStore(t)
	principal := testPrincipal()
	session, err := store.UpsertSession(context.Background(), principal, "governance", "", "project-a", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]config.MCPPrincipalConfig{
		principal.Key: {Token: "test-token", Capabilities: []config.MCPClientCapability{
			config.MCPContextRead, config.MCPArchiveCheckpoint, config.MCPMemoryPropose,
		}},
	}
	server := NewServer(store, "owner", strings.Repeat("k", 32), "admin", tokens, testLogger(), true)
	return server, store, principal, session
}

func addGovernanceMessage(t *testing.T, store *Store, sessionID, externalID, content string) string {
	t.Helper()
	at := time.Now().UTC()
	msg := checkpointMessage{
		ExternalMessageID: externalID, Role: archive.RoleUser, ContentType: "text/plain",
		Content: content, OccurredAt: at,
	}
	msg.Hash = checkpointMessageHash(msg)
	id, replay, err := store.InsertMessage(context.Background(), sessionID, msg)
	if err != nil || replay {
		t.Fatalf("insert message: err=%v replay=%v", err, replay)
	}
	return id
}

func approveProposal(t *testing.T, server *Server, proposalID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/proposals/"+proposalID+"/approve", nil)
	req.SetPathValue("id", proposalID)
	req.Header.Set("X-Admin-Token", "admin")
	rec := httptest.NewRecorder()
	server.adminApproveProposal(rec, req)
	return rec
}

func TestApprovalRehydratesEvidenceAndCannotBypassSecurity(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	ctx := context.Background()
	content := "Architecture before implementation."
	messageID := addGovernanceMessage(t, store, session.SessionID, "intent", content)
	staged, err := server.applyMutation(ctx, principal, mutationRequest{
		SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "Architecture", EvidenceQuote: content,
	})
	if err != nil || staged.Outcome != "STAGED" {
		t.Fatalf("intent proposal: %+v err=%v", staged, err)
	}
	proposal, ok, _ := store.GetProposal(ctx, staged.ProposalID)
	if !ok || proposal.GateClass != domain.GateIntent || proposal.Identity.Evidence == nil {
		t.Fatalf("proposal lost typed evidence: %+v", proposal)
	}
	if rec := approveProposal(t, server, staged.ProposalID); rec.Code != http.StatusOK {
		t.Fatalf("intent approval status=%d body=%s", rec.Code, rec.Body.String())
	}
	applied, _, _ := store.GetProposal(ctx, staged.ProposalID)
	row, err := store.LoadMemoryRow(ctx, applied.AppliedMemoryID)
	if err != nil || row.Content != content || row.Sensitivity != "NORMAL" {
		t.Fatalf("approved memory not evidence-derived: %+v err=%v", row, err)
	}
	links, _ := store.MessageEvidenceFor(ctx, row.MemoryID)
	if len(links) != 1 || links[0].QuoteHash != quoteHash(content) {
		t.Fatalf("approved evidence missing: %+v", links)
	}

	secretContent := "Remember that my password is hunter2."
	secretMessageID := addGovernanceMessage(t, store, session.SessionID, "secret", secretContent)
	security, err := server.applyMutation(ctx, principal, mutationRequest{
		SessionID: session.SessionID, MessageID: secretMessageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindUserPreference, Scope: domain.ScopeGlobal,
		Subject: "password", EvidenceQuote: secretContent,
	})
	if err != nil || security.Outcome != "STAGED" {
		t.Fatalf("security proposal: %+v err=%v", security, err)
	}
	blocked, _, _ := store.GetProposal(ctx, security.ProposalID)
	if blocked.GateClass != domain.GateSecurity {
		t.Fatalf("security gate not classified: %+v", blocked)
	}
	if rec := approveProposal(t, server, security.ProposalID); rec.Code == http.StatusOK {
		t.Fatalf("security proposal was approvable: %s", rec.Body.String())
	}
}

func TestApprovalRejectsChangedArchivedEvidence(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	content := "This preference should use local backups."
	messageID := addGovernanceMessage(t, store, session.SessionID, "approval-tamper", content)
	staged, err := server.applyMutation(context.Background(), principal, mutationRequest{
		SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindUserPreference, Scope: domain.ScopeGlobal,
		Subject: "local backups", EvidenceQuote: content,
	})
	if err != nil || staged.Outcome != "STAGED" || staged.ReasonCode != "EXPLICIT_INTENT_NOT_VERIFIED" {
		t.Fatalf("stage: %+v err=%v", staged, err)
	}
	store.mu.Lock()
	message := store.messages[messageID]
	message.Content = "changed after proposal"
	store.messages[messageID] = message
	store.mu.Unlock()
	if rec := approveProposal(t, server, staged.ProposalID); rec.Code == http.StatusOK {
		t.Fatalf("changed archived evidence was approved: %s", rec.Body.String())
	}
	if len(store.memories) != 0 {
		t.Fatalf("approval created memory from changed evidence: %+v", store.memories)
	}
}

func TestMutationAndAdminRateLimits(t *testing.T) {
	server := &Server{}
	now := time.Now()
	if !server.allowRequest("mutation\x00local", 2, now) || !server.allowRequest("mutation\x00local", 2, now) {
		t.Fatal("requests below limit rejected")
	}
	if server.allowRequest("mutation\x00local", 2, now) {
		t.Fatal("request over limit accepted")
	}
	if !server.allowRequest("mutation\x00local", 2, now.Add(time.Minute)) {
		t.Fatal("rate window did not reset")
	}
}

func TestApprovalSupportsCorrectAndForgetWithoutBypassingTargets(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	ctx := context.Background()
	target := MemoryRow{
		MemoryID: "target", Kind: string(domain.KindProjectDecision), Subject: "Mindmory architecture",
		Content: "Mindmory architecture is distributed", ContentHash: hashContent("Mindmory architecture is distributed"),
		Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "PROJECT", ProjectKey: "project-a",
		Confidence: 1, Importance: 0.6,
	}
	if err := store.insertMemoryFixture(ctx, target); err != nil {
		t.Fatal(err)
	}
	correction := "Mindmory architecture uses local-first storage."
	correctionMessage := addGovernanceMessage(t, store, session.SessionID, "correct", correction)
	staged, err := server.applyMutation(ctx, principal, mutationRequest{
		SessionID: session.SessionID, MessageID: correctionMessage, Mutation: domain.MutationCorrect,
		TargetMemoryID: target.MemoryID, Replacement: "local-first storage", EvidenceQuote: correction,
	})
	if err != nil || staged.ReasonCode != "EXPLICIT_INTENT_NOT_VERIFIED" {
		t.Fatalf("correction did not stage for intent: %+v err=%v", staged, err)
	}
	if rec := approveProposal(t, server, staged.ProposalID); rec.Code != http.StatusOK {
		t.Fatalf("correct approval status=%d body=%s", rec.Code, rec.Body.String())
	}
	old, _ := store.LoadMemoryRow(ctx, target.MemoryID)
	if old.Lifecycle != "SUPERSEDED" {
		t.Fatalf("correction did not supersede target: %+v", old)
	}

	forgetText := "Mindmory architecture is obsolete."
	forgetMessage := addGovernanceMessage(t, store, session.SessionID, "forget", forgetText)
	// Corrected memory id is stored on the applied proposal.
	applied, _, _ := store.GetProposal(ctx, staged.ProposalID)
	forget, err := server.applyMutation(ctx, principal, mutationRequest{
		SessionID: session.SessionID, MessageID: forgetMessage, Mutation: domain.MutationForget,
		TargetMemoryID: applied.AppliedMemoryID, EvidenceQuote: forgetText,
	})
	if err != nil || forget.ReasonCode != "EXPLICIT_INTENT_NOT_VERIFIED" {
		t.Fatalf("forget did not stage for intent: %+v err=%v", forget, err)
	}
	if rec := approveProposal(t, server, forget.ProposalID); rec.Code != http.StatusOK {
		t.Fatalf("forget approval status=%d body=%s", rec.Code, rec.Body.String())
	}
	forgotten, _ := store.LoadMemoryRow(ctx, applied.AppliedMemoryID)
	if forgotten.Lifecycle != "FORGOTTEN" {
		t.Fatalf("approved forget did not update lifecycle: %+v", forgotten)
	}
}

func TestConcurrentIdenticalMutationsCommitExactlyOnce(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	content := "Remember that the canonical store is JSONL."
	messageID := addGovernanceMessage(t, store, session.SessionID, "concurrent", content)
	request := mutationRequest{
		SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "canonical store", EvidenceQuote: content,
	}
	const workers = 100
	var wg sync.WaitGroup
	results := make(chan mutationResult, workers)
	errors := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := server.applyMutation(context.Background(), principal, request)
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent mutation failed: %v", err)
		}
	}
	var memoryID string
	for result := range results {
		if result.Outcome != "APPLIED" || result.MemoryID == "" {
			t.Fatalf("unexpected result: %+v", result)
		}
		if memoryID == "" {
			memoryID = result.MemoryID
		} else if result.MemoryID != memoryID {
			t.Fatalf("duplicate memories committed: %s != %s", memoryID, result.MemoryID)
		}
	}
	lines, err := readJSONL(store.path("memory_events"))
	if err != nil || len(lines) != 1 {
		t.Fatalf("event count=%d err=%v", len(lines), err)
	}
	changes, _, _ := store.ContinuityChanges(context.Background(), 0, "project-a", 20, false)
	if len(changes) != 1 {
		t.Fatalf("continuity count=%d", len(changes))
	}
}

func TestConcurrentCorrectionsProduceOneSuccessor(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	target := MemoryRow{MemoryID: "race-target", Kind: string(domain.KindProjectDecision), Subject: "runtime",
		Content: "runtime is distributed", ContentHash: hashContent("runtime is distributed"), Lifecycle: "ACTIVE",
		Sensitivity: "NORMAL", ScopeType: "PROJECT", ProjectKey: "project-a", Confidence: 1, Importance: 0.5, StateVersion: 1}
	if err := store.insertMemoryFixture(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	content := "更正 runtime should use alpha or beta local storage."
	messageID := addGovernanceMessage(t, store, session.SessionID, "correction-race", content)
	requests := []mutationRequest{
		{SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationCorrect,
			TargetMemoryID: target.MemoryID, Replacement: "alpha local storage", EvidenceQuote: content},
		{SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationCorrect,
			TargetMemoryID: target.MemoryID, Replacement: "beta local storage", EvidenceQuote: content},
	}
	results := make(chan mutationResult, 2)
	var wg sync.WaitGroup
	for _, request := range requests {
		request := request
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _ := server.applyMutation(context.Background(), principal, request)
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	applied := 0
	for result := range results {
		if result.Outcome == "APPLIED" {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("simultaneous corrections applied=%d", applied)
	}
	old, _ := store.LoadMemoryRow(context.Background(), target.MemoryID)
	if old.Lifecycle != "SUPERSEDED" || old.StateVersion != 2 {
		t.Fatalf("target state is not a single valid transition: %+v", old)
	}
	activeSuccessors := 0
	for _, row := range store.memories {
		if row.SupersedesMemoryID == target.MemoryID && row.Lifecycle == "ACTIVE" {
			activeSuccessors++
		}
	}
	if activeSuccessors != 1 {
		t.Fatalf("active successors=%d", activeSuccessors)
	}
}

func TestForgetRacingCorrectionCommitsOneTransition(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	target := MemoryRow{MemoryID: "forget-correct-target", Kind: string(domain.KindProjectDecision), Subject: "storage",
		Content: "storage is remote", ContentHash: hashContent("storage is remote"), Lifecycle: "ACTIVE",
		Sensitivity: "NORMAL", ScopeType: "PROJECT", ProjectKey: "project-a", Confidence: 1, Importance: 0.5, StateVersion: 1}
	if err := store.insertMemoryFixture(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	content := "忘记 old storage; 更正 storage to local authority."
	messageID := addGovernanceMessage(t, store, session.SessionID, "forget-correct-race", content)
	requests := []mutationRequest{
		{SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationForget,
			TargetMemoryID: target.MemoryID, EvidenceQuote: content},
		{SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationCorrect,
			TargetMemoryID: target.MemoryID, Replacement: "local authority", EvidenceQuote: content},
	}
	results := make(chan mutationResult, 2)
	var wg sync.WaitGroup
	for _, request := range requests {
		request := request
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _ := server.applyMutation(context.Background(), principal, request)
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	applied := 0
	for result := range results {
		if result.Outcome == "APPLIED" {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("forget/correct race applied=%d", applied)
	}
	changes, head, err := store.ContinuityChanges(context.Background(), 0, "project-a", 20, false)
	if err != nil || len(changes) != 1 || head != 1 {
		t.Fatalf("continuity revision invalid: head=%d changes=%+v err=%v", head, changes, err)
	}
}

func TestMutationEventReplayRepairsLostProjections(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	content := "Remember that event replay repairs projections."
	messageID := addGovernanceMessage(t, store, session.SessionID, "replay", content)
	result, err := server.applyMutation(context.Background(), principal, mutationRequest{
		SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "event replay", EvidenceQuote: content,
	})
	if err != nil || result.Outcome != "APPLIED" {
		t.Fatalf("apply: %+v err=%v", result, err)
	}
	dir := store.Dir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash immediately after event fsync but before any materialized
	// snapshot was refreshed.
	for _, name := range []string{"memories.jsonl", "proposals.jsonl", "evidence.jsonl", "continuity.jsonl"} {
		if err := os.WriteFile(dir+"/"+name, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen/replay: %v", err)
	}
	defer reopened.Close()
	row, err := reopened.LoadMemoryRow(context.Background(), result.MemoryID)
	if err != nil || row.Content != content {
		t.Fatalf("memory not replayed: %+v err=%v", row, err)
	}
	links, _ := reopened.MessageEvidenceFor(context.Background(), result.MemoryID)
	if len(links) != 1 || links[0].MessageID != messageID {
		t.Fatalf("evidence not replayed: %+v", links)
	}
	replayedProposal, _, _ := reopened.GetProposal(context.Background(), result.ProposalID)
	if replayedProposal.Status != domain.ProposalApplied || replayedProposal.AppliedMemoryID != result.MemoryID {
		t.Fatalf("proposal not replayed: %+v", replayedProposal)
	}
	changes, _, _ := reopened.ContinuityChanges(context.Background(), 0, "project-a", 20, false)
	if len(changes) != 1 || changes[0].TargetID != result.MemoryID {
		t.Fatalf("continuity not replayed: %+v", changes)
	}
}

func TestMutationEventIntegrityDetectsTamperingAndForgery(t *testing.T) {
	for _, test := range []struct {
		name  string
		forge bool
	}{
		{name: "content tamper"},
		{name: "rehashed without owner key", forge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, store, principal, session := governanceFixture(t)
			content := "Remember that signed events are canonical."
			messageID := addGovernanceMessage(t, store, session.SessionID, "signed", content)
			result, err := server.applyMutation(context.Background(), principal, mutationRequest{
				SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
				MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
				Subject: "signed events", EvidenceQuote: content,
			})
			if err != nil || result.Outcome != "APPLIED" {
				t.Fatalf("apply: %+v err=%v", result, err)
			}
			dir := store.Dir()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			lines, err := readJSONL(dir + "/memory_events.jsonl")
			if err != nil || len(lines) != 1 {
				t.Fatalf("events: %d err=%v", len(lines), err)
			}
			var event MemoryMutationEvent
			if err := json.Unmarshal(lines[0], &event); err != nil {
				t.Fatal(err)
			}
			event.NewMemory.Content = "tampered canonical content"
			event.NewMemory.ContentHash = hashContent(event.NewMemory.Content)
			if test.forge {
				event.EventHash = mutationEventHash(event)
				// Attacker cannot recompute the owner-key HMAC.
			}
			line, _ := json.Marshal(event)
			if err := os.WriteFile(dir+"/memory_events.jsonl", append(line, '\n'), 0o640); err != nil {
				t.Fatal(err)
			}
			if reopened, err := OpenVerified(dir, []byte(strings.Repeat("k", 32))); err == nil {
				reopened.Close()
				t.Fatal("tampered event journal passed verified open")
			}
		})
	}
}

func TestIntegrityKeyRotationBridgesOldHistoryAndSignsNewEvents(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	firstContent := "Remember that rotation preserves old history."
	firstMessage := addGovernanceMessage(t, store, session.SessionID, "rotation-old", firstContent)
	first, err := server.applyMutation(context.Background(), principal, mutationRequest{
		SessionID: session.SessionID, MessageID: firstMessage, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "rotation history", EvidenceQuote: firstContent,
	})
	if err != nil || first.Outcome != "APPLIED" {
		t.Fatalf("old-key mutation: %+v err=%v", first, err)
	}
	newKey := []byte(strings.Repeat("n", 32))
	rotation, err := store.RotateIntegrityKey(newKey)
	if err != nil || rotation.EventHeadHash == "" || rotation.PreviousKeyID == rotation.NewKeyID {
		t.Fatalf("rotation: %+v err=%v", rotation, err)
	}
	secondContent := "Remember that new events use the rotated key."
	secondMessage := addGovernanceMessage(t, store, session.SessionID, "rotation-new", secondContent)
	second, err := server.applyMutation(context.Background(), principal, mutationRequest{
		SessionID: session.SessionID, MessageID: secondMessage, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "rotated events", EvidenceQuote: secondContent,
	})
	if err != nil || second.Outcome != "APPLIED" {
		t.Fatalf("new-key mutation: %+v err=%v", second, err)
	}
	dir := store.Dir()
	if report, err := VerifyDataDir(dir, newKey); err != nil || report.MutationEvents != 2 {
		t.Fatalf("verify with rotated key: %+v err=%v", report, err)
	}
	if _, err := VerifyDataDir(dir, []byte(strings.Repeat("k", 32))); err == nil {
		t.Fatal("retired key verified latest history")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenVerified(dir, newKey)
	if err != nil {
		t.Fatalf("rotated store did not reopen: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.LoadMemoryRow(context.Background(), first.MemoryID); err != nil {
		t.Fatalf("old history not anchored by rotation: %v", err)
	}
	if _, err := reopened.LoadMemoryRow(context.Background(), second.MemoryID); err != nil {
		t.Fatalf("new history not signed by rotated key: %v", err)
	}
}

func TestFrozenSnapshotManifestAndRestoreDrill(t *testing.T) {
	server, store, principal, session := governanceFixture(t)
	content := "Remember that backups require restore drills."
	messageID := addGovernanceMessage(t, store, session.SessionID, "snapshot", content)
	result, err := server.applyMutation(context.Background(), principal, mutationRequest{
		SessionID: session.SessionID, MessageID: messageID, Mutation: domain.MutationRemember,
		MemoryKind: domain.KindProjectDecision, Scope: domain.ScopeProject,
		Subject: "restore drills", EvidenceQuote: content,
	})
	if err != nil || result.Outcome != "APPLIED" {
		t.Fatalf("apply: %+v err=%v", result, err)
	}
	snapshot, err := store.CreateSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Manifest.Files) == 0 {
		t.Fatal("empty snapshot manifest")
	}
	restoreDir := filepath.Join(t.TempDir(), "restore")
	if err := os.MkdirAll(restoreDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Manifest.Files {
		if entry.Path == "index.db" || strings.Contains(entry.Path, "index.db") {
			t.Fatalf("derived index included in canonical snapshot: %s", entry.Path)
		}
		raw, err := os.ReadFile(filepath.Join(snapshot.Path, entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if int64(len(raw)) != entry.Size || hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Fatalf("manifest mismatch for %s", entry.Path)
		}
		restorePath := filepath.Join(restoreDir, entry.Path)
		if err := os.MkdirAll(filepath.Dir(restorePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(restorePath, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := OpenVerified(restoreDir, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("restore open: %v", err)
	}
	defer restored.Close()
	row, err := restored.LoadMemoryRow(context.Background(), result.MemoryID)
	if err != nil || row.Content != content {
		t.Fatalf("restored memory mismatch: %+v err=%v", row, err)
	}
	hits, err := restored.Index.SearchCandidates("restore drills", "project-a", nil, 10)
	if err != nil || len(hits) != 1 || hits[0] != result.MemoryID {
		t.Fatalf("derived index did not rebuild after restore: ids=%v err=%v", hits, err)
	}
}
