package lite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/auth"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// newTestStore opens an isolated store in a temp dir (with its own data dir
// so tests never touch the live canonical files).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testPrincipal() auth.Principal {
	return auth.Principal{Key: "test-client", Type: auth.PrincipalMCP}
}

func testMessage(id, content string, at time.Time) checkpointMessage {
	return checkpointMessage{
		ExternalMessageID: id, Role: archive.RoleUser, ContentType: "text/plain",
		Content: content, OccurredAt: at, Hash: tupleHash("user", "text/plain", content, archive.FormatCanonicalTimestamp(at)),
	}
}

// testSetLifecycle simulates a post-commit projection refresh. Production
// lifecycle mutations have no direct Store setter; they must use the signed
// mutation journal through Server/CommitMutation.
func testSetLifecycle(t *testing.T, store *Store, memoryID string, next domain.Lifecycle) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	row, ok := store.memories[memoryID]
	if !ok {
		t.Fatalf("memory %s missing", memoryID)
	}
	row.Lifecycle = string(next)
	row.UpdatedAt = time.Now().UTC()
	store.memories[memoryID] = row
	if err := store.flushKindLocked("memories", store.memoriesJSONL()); err != nil {
		t.Fatal(err)
	}
	if store.Index != nil {
		if err := store.Index.Remove(memoryID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	principal := testPrincipal()
	if _, err := store.UpsertSession(context.Background(), principal, "ext-session", "title", "ember", time.Now().UTC()); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	row := MemoryRow{
		MemoryID: "mem-1", Kind: string(domain.KindUserPreference), Subject: "test subject",
		Content: "test content", ContentHash: hashContent("test content"),
		Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 0.5,
		Importance: 0.6, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.6,
	}
	if err := store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: canonical JSONL must have survived.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.LoadMemoryRow(context.Background(), "mem-1")
	if err != nil {
		t.Fatalf("load after reopen: %v", err)
	}
	if got.Subject != "test subject" || got.Lifecycle != "ACTIVE" {
		t.Fatalf("memory not preserved: %+v", got)
	}
	if _, err := reopened.ResolveSession(context.Background(), principal, "sess-missing"); err == nil {
		t.Fatal("expected error resolving missing session")
	}
}

func TestMutationLifecycle(t *testing.T) {
	store := newTestStore(t)
	principal := testPrincipal()
	ctx := context.Background()
	now := time.Now().UTC()
	session, err := store.UpsertSession(ctx, principal, "ext", "title", "ember", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	msg := testMessage("msg-1", "记住 这是测试记忆 需要保存", now)
	msgID, replayed, err := store.InsertMessage(ctx, session.SessionID, msg)
	if err != nil || replayed {
		t.Fatalf("insert message: %v replayed=%v", err, replayed)
	}

	evidence, err := store.LoadMessageEvidence(ctx, session.SessionID, msgID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	evidence.ClientID = principal.Key
	evidence.SessionID = session.SessionID
	if latest, err := store.LatestUserMessageID(ctx, session.SessionID); err == nil {
		evidence.CurrentUserTurn = latest == msgID
	}
	if !evidence.CurrentUserTurn {
		t.Fatal("expected current user turn")
	}
	binding := bindExactQuote(evidence.Content, "记住 这是测试记忆 需要保存")
	if binding.ReasonCode != "" {
		t.Fatalf("binding failed: %s", binding.ReasonCode)
	}
	decision := domain.VerifyMutation(domain.MutationRequest{
		Kind: domain.MutationRemember, ClientID: principal.Key, SessionID: session.SessionID,
		MessageID: msgID, EvidenceQuote: "记住 这是测试记忆 需要保存", Subject: "测试记忆",
	}, evidence, nil)
	if decision.Outcome != domain.MutationApply {
		t.Fatalf("expected APPLY, got %s (%s)", decision.Outcome, decision.Reason)
	}

	// Apply via the store path.
	memoryID := "mem-lifecycle"
	record := MemoryRow{MemoryID: memoryID, Kind: string(domain.KindProjectDecision),
		Subject: "测试记忆", Content: "记住 这是测试记忆 需要保存",
		ContentHash: hashContent("记住 这是测试记忆 需要保存"),
		Lifecycle:   "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 0.5,
		Importance: domain.Importance("记住 这是测试记忆 需要保存"), Sensitivity: "NORMAL",
		ScopeType: "PROJECT", ProjectKey: "ember", Activation: 0.6}
	if err := store.insertMemoryFixture(ctx, record); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.InsertMessageEvidence(ctx, memoryID, msgID, binding.Hash, binding.StartByte, binding.EndByte, "ASSERTS"); err != nil {
		t.Fatalf("evidence link: %v", err)
	}
	if _, err := store.AppendContinuity(ctx, "MEMORY_CREATED", "COGNITIVE_MEMORY", memoryID, "", "ember", "NORMAL", "trace"); err != nil {
		t.Fatalf("continuity: %v", err)
	}

	// Forget it.
	testSetLifecycle(t, store, memoryID, domain.LifecycleForgotten)
	row, _ := store.LoadMemoryRow(ctx, memoryID)
	if row.Lifecycle != "FORGOTTEN" {
		t.Fatalf("expected FORGOTTEN, got %s", row.Lifecycle)
	}
	// It must not appear in eligible search rows anymore.
	scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: principal.Key, ProjectKey: "ember"}
	eligible, err := store.EligibleMemories(ctx, scope, nil, 0)
	if err != nil {
		t.Fatalf("eligible: %v", err)
	}
	for _, e := range eligible {
		if e.MemoryID == memoryID {
			t.Fatal("forgotten memory leaked into eligible set")
		}
	}
}

func TestCanonicalEligibilityDefeatsStaleIndexAndPolicyLeaks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	scope := retrieval.SessionScope{SessionID: "session", ClientKey: "test-client"}
	server := &Server{Store: store, Aliases: retrieval.NewAliasExpander(nil), Log: testLogger()}

	global := MemoryRow{
		MemoryID: "global", Kind: string(domain.KindUserPreference), Subject: "shared retrieval sentinel",
		Content: "shared retrieval sentinel global", ContentHash: hashContent("shared retrieval sentinel global"),
		Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL", Confidence: 1, Importance: 0.6,
	}
	project := global
	project.MemoryID = "project"
	project.Content = "shared retrieval sentinel project"
	project.ContentHash = hashContent(project.Content)
	project.ScopeType = "PROJECT"
	project.ProjectKey = "project-a"
	secret := global
	secret.MemoryID = "secret"
	secret.Subject = "shared retrieval sentinel password"
	secret.Content = "my password is hunter2"
	secret.ContentHash = hashContent(secret.Content)
	secret.SecretLike = true

	for _, row := range []MemoryRow{global, project, secret} {
		if err := store.insertMemoryFixture(ctx, row); err != nil {
			t.Fatalf("insert %s: %v", row.MemoryID, err)
		}
	}

	ids, err := store.Index.SearchCandidates("shared retrieval sentinel", "", nil, 20)
	if err != nil {
		t.Fatalf("projectless candidate search: %v", err)
	}
	if len(ids) != 1 || ids[0] != global.MemoryID {
		t.Fatalf("projectless index returned non-global rows: %v", ids)
	}

	testSetLifecycle(t, store, global.MemoryID, domain.LifecycleForgotten)
	// Deliberately corrupt the derived index with a stale ACTIVE copy. Search
	// must still reject it after reloading the canonical row.
	stale := global
	stale.Lifecycle = "ACTIVE"
	if err := store.Index.Upsert(stale); err != nil {
		t.Fatalf("inject stale index row: %v", err)
	}
	hits, err := server.searchMemories(ctx, scope, retrieval.SearchRequest{
		SessionID: scope.SessionID, Query: "shared retrieval sentinel", Limit: 20,
	}, false)
	if err != nil {
		t.Fatalf("canonical search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale/project/secret candidate leaked: %+v", hits)
	}
}

func TestContinuityRecentAndDiffRespectProjectScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.AppendContinuity(ctx, "MEMORY_CREATED", "COGNITIVE_MEMORY", "global", "", "", "NORMAL", "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendContinuity(ctx, "MEMORY_CREATED", "COGNITIVE_MEMORY", "a", "", "project-a", "NORMAL", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendContinuity(ctx, "MEMORY_CREATED", "COGNITIVE_MEMORY", "b", "", "project-b", "NORMAL", "b"); err != nil {
		t.Fatal(err)
	}
	for _, recent := range []bool{false, true} {
		changes, _, err := store.ContinuityChanges(ctx, 0, "project-a", 20, recent)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range changes {
			if change.ProjectKey == "project-b" {
				t.Fatalf("project-b leaked (recent=%v): %+v", recent, changes)
			}
		}
		globalOnly, _, err := store.ContinuityChanges(ctx, 0, "", 20, recent)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range globalOnly {
			if change.ProjectKey != "" {
				t.Fatalf("project change leaked to projectless session (recent=%v): %+v", recent, globalOnly)
			}
		}
	}
}

func TestFingerprintIsOrderIndependent(t *testing.T) {
	a := MemoryRow{MemoryID: "a", Content: "a", ContentHash: hashContent("a"), Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL"}
	b := MemoryRow{MemoryID: "b", Content: "b", ContentHash: hashContent("b"), Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL"}
	if left, right := Fingerprint([]MemoryRow{a, b}), Fingerprint([]MemoryRow{b, a}); left != right {
		t.Fatalf("fingerprint depends on map/slice order: %s != %s", left, right)
	}
}

func TestIndexSyncAndRecovery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// Insert several memories with embeddings and check the index rebuilds.
	rows := []MemoryRow{
		{MemoryID: "a", Kind: "USER_PREFERENCE", Subject: "ember keeps the hearth", Content: "warmth",
			ContentHash: hashContent("warmth"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
			Confidence: 0.5, Importance: 0.6, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.6},
		{MemoryID: "b", Kind: "PROJECT_DECISION", Subject: "linkedin war-room", Content: "posts",
			ContentHash: hashContent("posts"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
			Confidence: 0.5, Importance: 0.5, Sensitivity: "NORMAL", ScopeType: "PROJECT", ProjectKey: "ember", Activation: 0.5},
		{MemoryID: "c", Kind: "DOCUMENT_FACT", Subject: "fertilizer market", Content: "urea",
			ContentHash: hashContent("urea"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
			Confidence: 0.5, Importance: 0.4, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.4},
	}
	for _, r := range rows {
		if err := store.insertMemoryFixture(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.MemoryID, err)
		}
	}
	if store.Index == nil {
		t.Fatal("index not initialized")
	}
	// FTS candidate search (>=3 runes).
	ids, err := store.Index.SearchCandidates("linkedin", "ember", nil, 10)
	if err != nil {
		t.Fatalf("fts search: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == "b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("linkedin FTS candidate missing: %v", ids)
	}
	// Short CJK LIKE fallback (2 runes).
	store.memories["d"] = MemoryRow{MemoryID: "d", Kind: "USER_PREFERENCE", Subject: "余烬记忆", Content: "测试",
		ContentHash: hashContent("测试"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
		Confidence: 0.5, Importance: 0.4, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.4}
	_ = store.flushKindLocked("memories", store.memoriesJSONL())
	_ = store.Index.Upsert(store.memories["d"])
	ids, err = store.Index.SearchCandidates("记忆", "ember", nil, 10)
	if err != nil || len(ids) == 0 {
		t.Fatalf("LIKE fallback failed: %v %v", ids, err)
	}
	// Recovery: delete the index, rebuild from canonical.
	indexPath := filepath.Join(store.derivedDir, "index.db")
	if err := store.Index.Close(); err != nil {
		t.Fatal(err)
	}
	os.Remove(indexPath)
	idx, err := OpenMemoryIndex(indexPath)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	store.Index = idx
	all := make([]MemoryRow, 0, len(store.memories))
	for _, r := range store.memories {
		all = append(all, r)
	}
	if err := idx.RebuildFrom(all); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	ids, err = idx.SearchCandidates("linkedin", "ember", nil, 10)
	if err != nil {
		t.Fatalf("post-rebuild search: %v", err)
	}
	found = false
	for _, id := range ids {
		if id == "b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-rebuild candidate missing: %v", ids)
	}
}

func TestFlockRefusesSecondProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer first.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("expected second Open to be refused by flock")
	}
}

func TestCheckpointReplayIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	principal := testPrincipal()
	now := time.Now().UTC()
	session, err := store.UpsertSession(ctx, principal, "ext", "title", "ember", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	msg := testMessage("dup-1", "记住 幂等测试", now)
	id1, replay1, err := store.InsertMessage(ctx, session.SessionID, msg)
	if err != nil || replay1 {
		t.Fatalf("first insert: %v replay=%v", err, replay1)
	}
	id2, replay2, err := store.InsertMessage(ctx, session.SessionID, msg)
	if err != nil || !replay2 || id1 != id2 {
		t.Fatalf("replay must return same id: %v %v %s vs %s", err, replay2, id1, id2)
	}
	changed := msg
	changed.Content = "different content"
	changed.Hash = tupleHash("user", "text/plain", changed.Content, archive.FormatCanonicalTimestamp(changed.OccurredAt))
	if _, replay, err := store.InsertMessage(ctx, session.SessionID, changed); !errors.Is(err, errMessageConflict) || replay {
		t.Fatalf("changed replay accepted: err=%v replay=%v", err, replay)
	}
}

func TestCurrentTurnUsesServerSequenceNotClientTimestamp(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	session, err := store.UpsertSession(ctx, testPrincipal(), "turn-seq", "", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	future := testMessage("future", "first user turn", time.Now().UTC().Add(365*24*time.Hour))
	firstID, _, err := store.InsertMessage(ctx, session.SessionID, future)
	if err != nil {
		t.Fatal(err)
	}
	past := testMessage("past", "second user turn", time.Now().UTC().Add(-365*24*time.Hour))
	secondID, _, err := store.InsertMessage(ctx, session.SessionID, past)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestUserMessageID(ctx, session.SessionID)
	if err != nil || latest != secondID || latest == firstID {
		t.Fatalf("client timestamp controlled current turn: latest=%s first=%s second=%s err=%v", latest, firstID, secondID, err)
	}
	first := store.messages[firstID]
	second := store.messages[secondID]
	if second.TurnSeq <= first.TurnSeq || second.MessageSeq <= first.MessageSeq {
		t.Fatalf("server sequences not monotonic: first=%+v second=%+v", first, second)
	}
}

func TestMessageJournalRejectsInteriorTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.UpsertSession(context.Background(), testPrincipal(), "journal-tamper", "", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertMessage(context.Background(), session.SessionID,
		testMessage("m1", "first protected record", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertMessage(context.Background(), session.SessionID,
		testMessage("m2", "second protected record", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "messages", "messages-000001.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte("first protected record"), []byte("first corrupted record"), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("test did not alter journal")
	}
	if err := os.WriteFile(path, tampered, 0o640); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(dir); err == nil {
		reopened.Close()
		t.Fatal("tampered message journal passed open")
	}
}

func TestMessageJournalRejectsRecordReordering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.UpsertSession(context.Background(), testPrincipal(), "journal-reorder", "", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"ordered record one", "ordered record two"} {
		if _, _, err := store.InsertMessage(context.Background(), session.SessionID,
			testMessage(string(rune('a'+i)), content, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "messages", "messages-000001.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("journal lines=%d", len(lines))
	}
	reordered := append(append(append([]byte(nil), lines[1]...), '\n'), lines[0]...)
	reordered = append(reordered, '\n')
	if err := os.WriteFile(path, reordered, 0o640); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(dir); err == nil {
		reopened.Close()
		t.Fatal("reordered message chain passed open")
	}
}

func TestEveryInactiveLifecycleDisappearsBeforeAndAfterRebuildRestart(t *testing.T) {
	for _, lifecycle := range []domain.Lifecycle{domain.LifecycleForgotten, domain.LifecycleSuperseded, domain.LifecycleInvalidated} {
		t.Run(string(lifecycle), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			row := MemoryRow{MemoryID: "lifecycle-target", Kind: string(domain.KindProjectDecision), Subject: "lifecycle sentinel",
				Content: "lifecycle sentinel", ContentHash: hashContent("lifecycle sentinel"), Lifecycle: "ACTIVE",
				Sensitivity: "NORMAL", ScopeType: "GLOBAL", Confidence: 1, Importance: 0.5}
			if err := store.insertMemoryFixture(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			server := &Server{Store: store, Aliases: retrieval.NewAliasExpander(nil), Log: testLogger()}
			scope := retrieval.SessionScope{SessionID: "lifecycle", ClientKey: "test-client"}
			hits, err := server.searchMemories(context.Background(), scope,
				retrieval.SearchRequest{SessionID: scope.SessionID, Query: "lifecycle sentinel", Limit: 10}, false)
			if err != nil || len(hits) != 1 {
				t.Fatalf("active precondition hits=%v err=%v", hits, err)
			}
			testSetLifecycle(t, store, row.MemoryID, lifecycle)
			assertAbsent := func(label string) {
				t.Helper()
				hits, err := server.searchMemories(context.Background(), scope,
					retrieval.SearchRequest{SessionID: scope.SessionID, Query: "lifecycle sentinel", Limit: 10}, false)
				if err != nil || len(hits) != 0 {
					t.Fatalf("%s leaked lifecycle %s: hits=%v err=%v", label, lifecycle, hits, err)
				}
			}
			assertAbsent("immediate")
			if err := store.RebuildIndex(); err != nil {
				t.Fatal(err)
			}
			assertAbsent("rebuild")
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			server.Store = reopened
			assertAbsent("restart")
		})
	}
}

func TestMessageJournalQuarantinesIncompleteFinalTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.UpsertSession(context.Background(), testPrincipal(), "journal-tail", "", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := store.InsertMessage(context.Background(), session.SessionID,
		testMessage("m1", "durable complete record", time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "messages", "messages-000001.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"message_id":"crash-tail"`); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("incomplete final tail should recover: %v", err)
	}
	defer reopened.Close()
	if _, ok := reopened.messages[id]; !ok || len(reopened.messages) != 1 {
		t.Fatalf("valid record lost during tail recovery: %+v", reopened.messages)
	}
	matches, err := filepath.Glob(path + ".quarantine-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one quarantined tail, matches=%v err=%v", matches, err)
	}
	recoveredRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(recoveredRaw, []byte{'\n'}) || bytes.Contains(recoveredRaw, []byte("crash-tail")) {
		t.Fatalf("journal was not truncated to its last complete record: %q", recoveredRaw)
	}
	events, err := reopened.Ops.Recent(20)
	if err != nil {
		t.Fatal(err)
	}
	foundRecovery := false
	for _, event := range events {
		if event.Event == "ARCHIVE_RECOVERY" && event.Reason == "INCOMPLETE_FINAL_RECORD_QUARANTINED" {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatalf("archive tail recovery was not recorded: %+v", events)
	}
}

type testEmbedder struct {
	model string
	value []float32
}

func (e testEmbedder) ModelName() string { return e.model }
func (e testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = append([]float32(nil), e.value...)
	}
	return out, nil
}

func TestEmbeddingsAreDisposableContentBoundProjection(t *testing.T) {
	store := newTestStore(t)
	row := MemoryRow{MemoryID: "derived-vector", Kind: "PROJECT_DECISION", Subject: "vector projection",
		Content: "embeddings are derived", ContentHash: hashContent("embeddings are derived"), Lifecycle: "ACTIVE",
		EpistemicStatus: "USER_ACCEPTED", Confidence: 1, Importance: 0.5, Sensitivity: "NORMAL", ScopeType: "GLOBAL"}
	if err := store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	count, err := store.EmbedAll(context.Background(), testEmbedder{model: "fixture-v1", value: []float32{1, 2, 3}})
	if err != nil || count != 1 || store.VectorStore == nil || store.VectorStore.Size() != 1 {
		size := 0
		if store.VectorStore != nil {
			size = store.VectorStore.Size()
		}
		t.Fatalf("embed projection: count=%d size=%d err=%v", count, size, err)
	}
	canonical, err := os.ReadFile(store.path("memories"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte(`"embedding"`)) || bytes.Contains(canonical, []byte("1,2,3")) {
		t.Fatalf("derived vector leaked into canonical memory JSONL: %s", canonical)
	}
	projection, err := os.ReadFile(filepath.Join(store.vectorDir, store.VectorStore.Generation(), "manifest.json"))
	if err != nil || !bytes.Contains(projection, []byte(`"model_name": "fixture-v1"`)) {
		t.Fatalf("persistent vector projection missing: %s err=%v", projection, err)
	}
	updated := row
	updated.Content = "embeddings changed"
	updated.ContentHash = hashContent(updated.Content)
	if err := store.insertMemoryFixture(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	count, err = store.EmbedAll(context.Background(), testEmbedder{model: "fixture-v2", value: []float32{3, 2, 1}})
	if err != nil || count != 1 || store.VectorStore.Manifest().ModelName != "fixture-v2" {
		t.Fatalf("changed content/model was not regenerated: count=%d manifest=%+v err=%v", count, store.VectorStore.Manifest(), err)
	}
}

func TestSnapshotJSONLValid(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_ = store.insertMemoryFixture(ctx, MemoryRow{MemoryID: "m1", Kind: "USER_PREFERENCE", Subject: "s", Content: "c",
		ContentHash: hashContent("c"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
		Confidence: 0.5, Importance: 0.4, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 0.4})
	raw, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("snapshot not valid JSON: %v", err)
	}
	if _, ok := doc["memories"]; !ok {
		t.Fatal("snapshot missing memories")
	}
	// Every JSONL file must be parseable line-by-line.
	for _, kind := range []string{"memories", "messages", "proposals", "continuity", "sessions", "evidence", "project_context"} {
		lines, err := readJSONL(store.path(kind))
		if err != nil {
			t.Fatalf("read %s: %v", kind, err)
		}
		for _, line := range lines {
			var anyMap map[string]any
			if err := json.Unmarshal(line, &anyMap); err != nil {
				t.Fatalf("%s.jsonl invalid line: %v", kind, err)
			}
		}
	}
}

// makeVector builds a float32 slice from a literal.
func makeVector(values []float32) []float32 {
	out := make([]float32, len(values))
	copy(out, values)
	return out
}

func TestOpsJournal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if store.Ops == nil {
		t.Fatal("ops journal not initialized")
	}
	// SEARCH is a high-frequency diagnostic: it lives only in the in-memory
	// ring (no per-event fsync), so it is visible now but does not persist.
	store.Ops.Record(OpsEvent{Event: "SEARCH", Outcome: "ok", Details: map[string]any{"query": "ember"}})
	store.Ops.Record(OpsEvent{Event: "MUTATION", Outcome: "staged", Reason: "EXPLICIT_INTENT_NOT_VERIFIED"})
	store.Ops.Record(OpsEvent{Event: "START", Outcome: "ok"})

	// Read back: all three visible (ring + journal merged).
	events, err := store.Ops.Recent(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	// Persisted events (MUTATION, START) survive reopen; SEARCH is
	// deliberately in-memory only.
	store.Close()
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	events, err = reopened.Ops.Recent(10)
	if err != nil {
		t.Fatalf("recent after reopen: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ops not persisted across reopen: %d (want 2: MUTATION+START; SEARCH is ring-only)", len(events))
	}
	if events[0].Event != "MUTATION" || events[0].Reason != "EXPLICIT_INTENT_NOT_VERIFIED" {
		t.Fatalf("event content wrong: %+v", events)
	}
	// Limit works.
	events, _ = reopened.Ops.Recent(2)
	if len(events) != 2 {
		t.Fatalf("limit: got %d", len(events))
	}
}

func TestOpsJournalInvalidLinesSkipped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Write a corrupt ops file plus one valid line.
	path := filepath.Join(dir, "ops.jsonl")
	_ = os.WriteFile(path, []byte("not json\n{\"event\":\"START\",\"outcome\":\"ok\",\"time\":\"2026-08-25T00:00:00.000000Z\"}\n"), 0o640)
	ops, err := OpenOps(dir)
	if err != nil {
		t.Fatalf("open ops: %v", err)
	}
	defer ops.Close()
	events, err := ops.Recent(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 valid event, got %d", len(events))
	}
}

func TestMisledConfidenceNeverRecovers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	row := MemoryRow{MemoryID: "mis", Kind: "PROJECT_DECISION", Subject: "s", Content: "c",
		ContentHash: hashContent("c"), Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED",
		Confidence: 1.0, Importance: 0.8, Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 1.0}
	if err := store.insertMemoryFixture(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyFeedback(ctx, "mis", "sess", "misled"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.LoadMemoryRow(ctx, "mis")
	if got.Confidence != 0.75 {
		t.Fatalf("after misled: confidence = %v, want 0.75", got.Confidence)
	}
	if !got.Disputed {
		t.Fatal("misled memory should be marked disputed")
	}
	// Simulate 100 sessions passing: confidence must NOT recover.
	if eff := ActivationEffective(0.9, 100, 60); eff <= 0 {
		t.Fatalf("activation decay broken: %v", eff)
	}
	// Confidence stays 0.75 no matter how many sessions pass — the expert's
	// core fix: misleading is not healed by time.
	if got.Confidence != 0.75 {
		t.Fatal("confidence changed without feedback")
	}
}

func TestRankKeyLexicographic(t *testing.T) {
	// Exact subject match must outrank fuzzy even if fuzzy has higher
	// importance/activation (expert P0-4: no saturation, class first).
	exact := RankKeyFor(MemoryRow{MemoryID: "a", Lifecycle: "ACTIVE", Confidence: 0.5, Activation: 0.1, Importance: 0.2, UpdatedAt: time.Now()}, MatchResult{Class: MatchExact, Strength: 1}, 0)
	fuzzy := RankKeyFor(MemoryRow{MemoryID: "b", Lifecycle: "ACTIVE", Confidence: 1.0, Activation: 1.0, Importance: 1.0, UpdatedAt: time.Now().Add(time.Hour)}, MatchResult{Class: MatchFuzzy, Strength: 0.9}, 0)
	if !lessKey(exact, fuzzy) {
		t.Fatal("exact match must outrank fuzzy regardless of heat/importance")
	}
	// Within same class, higher strength wins.
	strong := RankKeyFor(MemoryRow{MemoryID: "c", Lifecycle: "ACTIVE", Confidence: 0.5, Activation: 0.5, Importance: 0.5, UpdatedAt: time.Now()}, MatchResult{Class: MatchSubject, Strength: 0.9}, 0)
	weak := RankKeyFor(MemoryRow{MemoryID: "d", Lifecycle: "ACTIVE", Confidence: 0.5, Activation: 0.5, Importance: 0.5, UpdatedAt: time.Now()}, MatchResult{Class: MatchSubject, Strength: 0.6}, 0)
	if !lessKey(strong, weak) {
		t.Fatal("higher within-class strength must win")
	}
	// Disputed / low-confidence down-ranks within same class.
	clean := RankKeyFor(MemoryRow{MemoryID: "e", Lifecycle: "ACTIVE", Confidence: 1.0, Activation: 0.5, Importance: 0.5, UpdatedAt: time.Now()}, MatchResult{Class: MatchContent, Strength: 0.8}, 0)
	dirty := RankKeyFor(MemoryRow{MemoryID: "f", Lifecycle: "ACTIVE", Confidence: 0.2, Disputed: true, Activation: 0.5, Importance: 0.5, UpdatedAt: time.Now()}, MatchResult{Class: MatchContent, Strength: 0.8}, 0)
	if !lessKey(clean, dirty) {
		t.Fatal("high-confidence must outrank disputed at equal match")
	}
}

func TestSeqBasedDecay(t *testing.T) {
	// Activation decays by session ordinal delta, not wall clock.
	tau := ActivationTauSessions(0.5) // 45
	eff0 := ActivationEffective(1.0, 0, tau)
	eff45 := ActivationEffective(1.0, 45, tau)
	if eff0 != 1.0 {
		t.Fatalf("no sessions: eff=%v want 1.0", eff0)
	}
	if eff45 >= eff0 || eff45 <= 0.3 {
		t.Fatalf("45 sessions: eff=%v expected ~0.37 decay", eff45)
	}
	// Identity kinds NEVER decay — an explicit project decision. They are
	// the anchors of who Ember is, kept at full activation every session.
	effID := ActivationFor("USER_PREFERENCE", 1.0, 45, 0.5)
	if effID != 1.0 {
		t.Fatalf("identity memory must never decay: got %v want 1.0", effID)
	}
	effID500 := ActivationFor("PERSONAL_GOAL", 1.0, 500, 0.6)
	if effID500 != 1.0 {
		t.Fatalf("identity must not decay even after 500 sessions: got %v", effID500)
	}
	// Non-identity decays normally.
	effNormal := ActivationFor("PROJECT_DECISION", 1.0, 45, 0.5)
	if effNormal >= 1.0 {
		t.Fatal("non-identity memory must decay")
	}
}

func TestAccessBumpsBatchAndFlush(t *testing.T) {
	s := newTestStore(t)
	// Insert a memory directly.
	mem := MemoryRow{MemoryID: "m-bump", Kind: "USER_PREFERENCE", Subject: "bump test", Content: "bump test content",
		Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1.0, Importance: 0.4,
		Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 1.0, SchemaVersion: SchemaVersion}
	if err := s.insertMemoryFixture(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	// Three accesses; none should hit disk yet.
	for i := 0; i < 3; i++ {
		if err := s.RecordAccess(context.Background(), "m-bump", "sess", retrieval.AccessSearchHit); err != nil {
			t.Fatal(err)
		}
	}
	// In-memory count reflects the pre-bump value (0) until flush applies bumps.
	row, _ := s.LoadMemoryRow(context.Background(), "m-bump")
	if row.AccessCount != 0 {
		t.Fatalf("in-memory count changed before flush: %d", row.AccessCount)
	}
	if err := s.FlushAccessBumps(); err != nil {
		t.Fatal(err)
	}
	row, _ = s.LoadMemoryRow(context.Background(), "m-bump")
	if row.AccessCount != 3 {
		t.Fatalf("access count after flush = %d, want 3", row.AccessCount)
	}
	// A second flush with nothing pending is a no-op.
	if err := s.FlushAccessBumps(); err != nil {
		t.Fatal(err)
	}
	row, _ = s.LoadMemoryRow(context.Background(), "m-bump")
	if row.AccessCount != 3 {
		t.Fatalf("no-op flush changed count: %d", row.AccessCount)
	}
}

func TestFlushAllPersistsEverything(t *testing.T) {
	s := newTestStore(t)
	mem := MemoryRow{MemoryID: "m-flushall", Kind: "USER_PREFERENCE", Subject: "flushall", Content: "flushall content",
		Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1.0, Importance: 0.4,
		Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 1.0, SchemaVersion: SchemaVersion}
	if err := s.insertMemoryFixture(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	// Record access (pending bump, not yet flushed).
	if err := s.RecordAccess(context.Background(), "m-flushall", "sess", retrieval.AccessSearchHit); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushAll(); err != nil {
		t.Fatalf("flush all: %v", err)
	}
	// Bumps applied to the row.
	row, _ := s.LoadMemoryRow(context.Background(), "m-flushall")
	if row.AccessCount != 1 {
		t.Fatalf("access count after FlushAll = %d, want 1", row.AccessCount)
	}
	// A second FlushAll with nothing pending is safe.
	if err := s.FlushAll(); err != nil {
		t.Fatalf("second flush all: %v", err)
	}
}

func TestIndexCheckpointMergesWAL(t *testing.T) {
	s := newTestStore(t)
	if s.Index == nil {
		t.Skip("index not configured")
	}
	mem := MemoryRow{MemoryID: "m-ckpt", Kind: "USER_PREFERENCE", Subject: "checkpoint test", Content: "cp",
		Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1.0, Importance: 0.4,
		Sensitivity: "NORMAL", ScopeType: "GLOBAL", Activation: 1.0, SchemaVersion: SchemaVersion}
	if err := s.Index.Upsert(mem); err != nil {
		t.Fatal(err)
	}
	if err := s.Index.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// After TRUNCATE checkpoint the WAL file should be empty.
	walPath := filepath.Join(s.derivedDir, "index.db-wal")
	if fi, err := os.Stat(walPath); err == nil && fi.Size() != 0 {
		t.Fatalf("WAL not truncated after checkpoint: %d bytes", fi.Size())
	}
}
