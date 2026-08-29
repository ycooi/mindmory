// Package lite implements a single-process Mindmory control plane whose
// canonical data is JSONL. One file per record kind under var/data:
//
//	sessions.jsonl, messages.jsonl, memories.jsonl, proposals.jsonl,
//	continuity.jsonl, project_context.jsonl
//
// Every record is one JSON object per line. The JSONL files are the source
// of truth — human-readable, diffable, trivially backed up, never a schema
// migration. At boot the store loads them into an in-memory index; every
// mutation rewrites the affected file atomically (temp + rename + fsync).
// SQLite/faiss, if ever added, are derived indexes rebuildable from these
// files — never an alternate authority.
package lite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/artifact/policy"
	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/lite/vectorstore"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// DataDir is the canonical JSONL store location (relative to the daemon's
// working directory or absolute).
const DataDir = "var/data"

// SchemaVersion is the current canonical memory-row schema. Bump it when a
// memory field is added/renamed/removed, and add a migration step keyed by
// the version a row was written under.
//
//	v1 (2026-08-19): single "heat" field carried importance+activation mix.
//	v2 (2026-08-25): heat split into activation (decays) + confidence +
//	                 disputed; last_used_seq for session-ordinal decay.
//	v3 (2026-08-27): persist secret/instruction policy flags on memory rows.
//	v4 (2026-08-27): add optimistic state versions for governed mutations.
const SchemaVersion = 4

// Store is the JSONL-backed in-memory store. A single mutex guards all
// state; the workload is not read-intensive, so a global lock is fine.
type Store struct {
	mu sync.RWMutex

	dir string

	sessions      map[string]SessionRow           // session_id -> row
	sessionsExt   map[string]string               // client_key + "\x00" + external -> session_id
	messages      map[string]MessageRow           // message_id -> row
	memories      map[string]MemoryRow            // memory_id -> row
	evidence      map[string][]MessageEvidenceRow // memory_id -> rows
	proposals     map[string]domain.Proposal      // proposal_id -> row
	proposalsHash map[string]string               // request_hash -> proposal_id
	continuity    []ContinuityRow                 // ordered by revision_number
	projectCtx    map[string]ProjectContextRow    // project_key -> current
	vectorRows    map[string]VectorProjectionRow  // transient legacy vectors, cleared after import
	accessBumps   map[string]int64                // memory_id -> pending access bumps not yet persisted
	// surfaced tracks memory ids already surfaced (injected/retrieved) per
	// session, so per-step relevance injection never re-injects what this
	// session has already seen. Ephemeral, in-memory only: it resets on
	// restart, and a one-time re-surface after a restart is harmless.
	surfaced            map[string]map[string]time.Time // session_id -> memory_id -> surfaced_at
	Index               *MemoryIndex                    // derived SQLite search index
	VectorStore         *vectorstore.Store              // persistent, mmap-backed semantic projection
	derivedDir          string
	vectorDir           string
	snapshotDir         string
	vectorIssue         string
	Ops                 *OpsLog  // operational journal (ops.jsonl)
	lockFile            *os.File // exclusive flock on the data dir
	sessionSeq          int64    // append-only session ordinal (P0-6)
	messageSeq          int64    // server-owned archive insertion order
	turnSeq             int64    // server-owned user-turn order
	mutationEventSeq    int64    // canonical mutation journal ordinal
	integrityKey        []byte
	lastEventHash       string
	integrityAnchorHash string
	keyRotationSeq      int64
	lastRotationHash    string
	lastMessageHash     string
	messageSegment      int
	startupRecoveries   []OpsEvent
	lowRAM              bool // experimental: complete payloads live in SQLite, not Go maps
}

// EnableLowRAMExperiment releases the three archive-sized Go maps after the
// complete SQLite read projection has been verified. Small governance maps
// remain resident. Call only after imports and one-shot maintenance commands.
func (s *Store) EnableLowRAMExperiment() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Index == nil {
		return fmt.Errorf("SQLite read projection unavailable")
	}
	if _, err := s.Index.Counts(); err != nil {
		return fmt.Errorf("verify SQLite read projection: %w", err)
	}
	s.memories = nil
	s.messages = nil
	s.evidence = nil
	s.lowRAM = true
	return nil
}

func (s *Store) LowRAMEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lowRAM
}

func (s *Store) allMemoryRowsLocked() ([]MemoryRow, error) {
	if s.lowRAM {
		return s.Index.AllMemories()
	}
	rows := make([]MemoryRow, 0, len(s.memories))
	for _, row := range s.memories {
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) memoryRowLocked(id string) (MemoryRow, bool) {
	if s.lowRAM {
		row, err := s.Index.LoadMemory(id)
		return row, err == nil
	}
	row, ok := s.memories[id]
	return row, ok
}

// ContinuityRow is a stored continuity revision.
type ContinuityRow struct {
	RevisionNumber   int64     `json:"revision_number"`
	EventID          string    `json:"event_id"`
	ChangeKind       string    `json:"change_kind"`
	TargetKind       string    `json:"target_kind"`
	TargetID         string    `json:"target_id"`
	RelatedTargetID  string    `json:"related_target_id,omitempty"`
	ProjectKey       string    `json:"project_key,omitempty"`
	Sensitivity      string    `json:"sensitivity"`
	TraceID          string    `json:"trace_id"`
	SafeMetadataJSON string    `json:"safe_metadata_json"`
	CreatedAt        time.Time `json:"created_at"`
}

// Open loads the JSONL store from dir, creating it if absent.
func Open(dir string) (*Store, error) {
	return openStore(dir, nil, StorageConfig{})
}

// OpenVerified loads canonical state only after verifying the signed mutation
// journal with the configured owner key.
func OpenVerified(dir string, integrityKey []byte) (*Store, error) {
	if len(integrityKey) < 32 {
		return nil, fmt.Errorf("integrity key must be at least 32 bytes")
	}
	return openStore(dir, integrityKey, StorageConfig{})
}

// OpenConfigured uses one validated storage layout rather than consulting
// environment variables inside the store.
func OpenConfigured(storage StorageConfig, integrityKey []byte) (*Store, error) {
	if len(integrityKey) < 32 {
		return nil, fmt.Errorf("integrity key must be at least 32 bytes")
	}
	return openStore(storage.DataDir, integrityKey, storage)
}

func openStore(dir string, integrityKey []byte, storage StorageConfig) (*Store, error) {
	if dir == "" {
		dir = DataDir
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	lock, err := acquireDirLock(dir)
	if err != nil {
		return nil, err
	}
	opened := false
	var s *Store
	defer func() {
		if opened {
			return
		}
		if s != nil && s.Index != nil {
			_ = s.Index.Close()
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	s = &Store{
		lockFile:      lock,
		dir:           dir,
		sessions:      map[string]SessionRow{},
		sessionsExt:   map[string]string{},
		messages:      map[string]MessageRow{},
		memories:      map[string]MemoryRow{},
		evidence:      map[string][]MessageEvidenceRow{},
		proposals:     map[string]domain.Proposal{},
		proposalsHash: map[string]string{},
		projectCtx:    map[string]ProjectContextRow{},
		vectorRows:    map[string]VectorProjectionRow{},
		accessBumps:   map[string]int64{},
		surfaced:      map[string]map[string]time.Time{},
		integrityKey:  append([]byte(nil), integrityKey...),
	}
	s.derivedDir = storage.DerivedDir
	s.vectorDir = storage.VectorDir
	s.snapshotDir = storage.SnapshotDir
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.openIndex(); err != nil {
		return nil, err
	}
	if err := s.openVectors(); err != nil && !errors.Is(err, os.ErrNotExist) {
		// A corrupt/missing semantic projection must never make canonical or
		// lexical storage unavailable. Operators can inspect/rebuild it later.
		s.startupRecoveries = append(s.startupRecoveries, OpsEvent{Event: "VECTOR_DEGRADED", Outcome: "degraded", Details: map[string]any{"reason": err.Error()}})
		s.vectorIssue = "VECTOR_GENERATION_CORRUPT"
	}
	if err := s.persistSchema(); err != nil {
		return nil, err
	}
	ops, err := OpenOps(dir)
	if err != nil {
		return nil, err
	}
	s.Ops = ops
	for _, event := range s.startupRecoveries {
		s.Ops.Record(event)
	}
	s.startupRecoveries = nil
	opened = true
	return s, nil
}

type VectorSyncOptions struct{}

type VectorSyncSummary struct {
	Scanned, AlreadyCurrent, ImportedLegacy, Embedded, SkippedInactive, Failed int
}

// SyncVectors embeds only missing active, normal memories and commits each
// vector to the persistent derived generation. It never rewrites canonical
// memory JSONL and is safe to repeat after interruption.
func (s *Store) SyncVectors(ctx context.Context, embedder Embedder, _ VectorSyncOptions) (VectorSyncSummary, error) {
	var summary VectorSyncSummary
	model, digest := embeddingModelName(embedder), embeddingModelDigest(embedder)
	s.mu.Lock()
	type pending struct {
		id, inputHash, text string
	}
	var queue []pending
	allRows, err := s.allMemoryRowsLocked()
	if err != nil {
		s.mu.Unlock()
		return summary, err
	}
	for _, row := range allRows {
		id := row.MemoryID
		summary.Scanned++
		if row.Lifecycle != "ACTIVE" || row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike {
			summary.SkippedInactive++
			continue
		}
		text := EmbeddingInput(row)
		hash := EmbeddingInputHash(row)
		if s.VectorStore != nil {
			manifest := s.VectorStore.Manifest()
			if manifest.ModelName == model && (digest == "" || manifest.ModelDigest == digest) && s.VectorStore.Has(id, hash) {
				summary.AlreadyCurrent++
				continue
			}
		}
		queue = append(queue, pending{id: id, inputHash: hash, text: text})
	}
	s.mu.Unlock()
	if len(queue) == 0 {
		return summary, nil
	}
	texts := make([]string, 0, len(queue))
	for _, p := range queue {
		texts = append(texts, p.text)
	}
	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return summary, err
	}
	if len(vectors) != len(queue) {
		return summary, fmt.Errorf("embed count mismatch: got %d for %d rows", len(vectors), len(queue))
	}
	if len(vectors[0]) == 0 {
		return summary, fmt.Errorf("embedding provider returned empty vectors")
	}
	for _, vector := range vectors[1:] {
		if len(vector) != len(vectors[0]) {
			return summary, fmt.Errorf("embedding provider changed dimensions during one sync")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	needsGeneration := s.VectorStore == nil || s.VectorStore.Manifest().ModelName != model || (digest != "" && s.VectorStore.Manifest().ModelDigest != digest) || s.VectorStore.Manifest().Dimensions != len(vectors[0])
	if needsGeneration {
		building, createErr := vectorstore.CreateBuilding(s.vectorDir, vectorstore.GenerationSpec{ModelName: model, ModelDigest: digest, Dimensions: len(vectors[0])})
		if createErr != nil {
			return summary, createErr
		}
		refs := make([]vectorstore.Ref, 0, len(queue))
		for i, p := range queue {
			row, ok := s.memoryRowLocked(p.id)
			if !ok || row.Lifecycle != "ACTIVE" || EmbeddingInputHash(row) != p.inputHash {
				_ = building.Close()
				return summary, fmt.Errorf("canonical memory changed during vector generation build; retry sync")
			}
			ref, appendErr := building.Append("MEMORY", p.id, p.inputHash, vectors[i])
			if appendErr != nil {
				_ = building.Close()
				return summary, appendErr
			}
			refs = append(refs, ref)
		}
		if activateErr := building.Activate(ctx); activateErr != nil {
			_ = building.Close()
			return summary, activateErr
		}
		old := s.VectorStore
		s.VectorStore = building
		s.vectorIssue = ""
		if old != nil {
			_ = old.Close()
		}
		if s.Index != nil {
			for _, ref := range refs {
				_ = s.Index.UpsertVectorRef(ref)
			}
		}
		summary.Embedded += len(refs)
		return summary, nil
	}
	for i, p := range queue {
		row, ok := s.memoryRowLocked(p.id)
		if !ok || row.Lifecycle != "ACTIVE" || EmbeddingInputHash(row) != p.inputHash {
			summary.Failed++
			continue
		}
		ref, appendErr := s.VectorStore.Append("MEMORY", p.id, p.inputHash, vectors[i])
		if appendErr != nil {
			summary.Failed++
			continue
		}
		if s.Index != nil {
			_ = s.Index.UpsertVectorRef(ref)
		}
		summary.Embedded++
	}
	return summary, nil
}

// EmbedAll is a deprecated compatibility alias for SyncVectors.
func (s *Store) EmbedAll(ctx context.Context, embedder Embedder) (int, error) {
	summary, err := s.SyncVectors(ctx, embedder, VectorSyncOptions{})
	return summary.Embedded, err
}

// openIndex opens the derived SQLite search index and rebuilds it when its
// fingerprint no longer matches the canonical JSONL store.
func (s *Store) openIndex() error {
	if s.derivedDir == "" {
		s.derivedDir = filepath.Join(filepath.Dir(s.dir), "derived")
	}
	if s.vectorDir == "" {
		s.vectorDir = filepath.Join(s.derivedDir, "vectors")
	}
	if filepath.Clean(s.vectorDir) == filepath.Clean(s.dir) {
		return fmt.Errorf("vector directory cannot equal canonical data directory")
	}
	if err := os.MkdirAll(s.derivedDir, 0o750); err != nil {
		return err
	}
	idx, err := OpenMemoryIndex(filepath.Join(s.derivedDir, "index.db"))
	if err != nil {
		return err
	}
	s.Index = idx
	stored, err := idx.storedFingerprint()
	if err != nil {
		return err
	}
	rows := make([]MemoryRow, 0, len(s.memories))
	for _, row := range s.memories {
		rows = append(rows, row)
	}
	current := Fingerprint(rows)
	if stored != current {
		if err := idx.RebuildFrom(rows); err != nil {
			return fmt.Errorf("index rebuild: %w", err)
		}
	}
	messages := make([]MessageRow, 0, len(s.messages))
	for _, row := range s.messages {
		messages = append(messages, row)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].MessageSeq < messages[j].MessageSeq })
	var evidence []MessageEvidenceRow
	for _, rows := range s.evidence {
		evidence = append(evidence, rows...)
	}
	supportStored, err := idx.storedSupportFingerprint()
	if err != nil {
		return err
	}
	if supportStored != SupportFingerprint(messages, evidence) {
		if err := idx.ReplaceReadSupport(messages, evidence); err != nil {
			return fmt.Errorf("supporting read projection rebuild: %w", err)
		}
	}
	return nil
}

func (s *Store) openVectors() error {
	store, err := vectorstore.OpenCurrent(s.vectorDir)
	if errors.Is(err, os.ErrNotExist) && len(s.vectorRows) > 0 {
		return s.importLegacyVectorProjection()
	}
	if err != nil {
		return err
	}
	if err := store.Verify(context.Background(), false); err != nil {
		_ = store.Close()
		return err
	}
	s.VectorStore = store
	if s.Index != nil {
		_ = s.Index.ReplaceVectorRefs(store.Refs())
	}
	return nil
}

// importLegacyVectorProjection upgrades the former disposable vectors.jsonl
// sidecar in place. Canonical JSONL is not touched, and the legacy file is
// retained as a rollback artifact.
func (s *Store) importLegacyVectorProjection() error {
	var first VectorProjectionRow
	for _, projection := range s.vectorRows {
		first = projection
		break
	}
	if first.Model == "" || first.Dimensions <= 0 {
		return os.ErrNotExist
	}
	store, err := vectorstore.Create(s.vectorDir, vectorstore.GenerationSpec{ModelName: first.Model, Dimensions: first.Dimensions})
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(s.vectorRows))
	for id := range s.vectorRows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		projection := s.vectorRows[id]
		row, ok := s.memories[id]
		if !ok || projection.Model != first.Model || projection.Dimensions != first.Dimensions || projection.ContentHash != row.ContentHash ||
			row.Lifecycle != "ACTIVE" || row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike {
			continue
		}
		ref, appendErr := store.Append("MEMORY", id, EmbeddingInputHash(row), projection.Vector)
		if appendErr != nil {
			_ = store.Close()
			return appendErr
		}
		if s.Index != nil {
			_ = s.Index.UpsertVectorRef(ref)
		}
	}
	s.VectorStore = store
	s.vectorRows = nil
	return nil
}

// RebuildIndex forces a full index rebuild from canonical state.
func (s *Store) RebuildIndex() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.allMemoryRowsLocked()
	if err != nil {
		return err
	}
	return s.Index.RebuildFrom(rows)
}

// Dir returns the canonical store directory.
func (s *Store) Dir() string { return s.dir }

// Close flushes pending access bumps and fsyncs all files.
// FlushAll persists all pending in-memory state (access bumps + every
// JSONL file) in one pass. Used by the admin snapshot endpoint so a backup
// can copy a consistent, fully-persisted store.
func (s *Store) FlushAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushAccessBumpsLocked(); err != nil {
		return err
	}
	return s.flushAllLocked()
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Persist while the SQLite projection is still open; low-RAM mode uses it
	// to materialize compatibility JSONL files for snapshots and restart.
	var flushErr error
	if err := s.flushAccessBumpsLocked(); err != nil {
		flushErr = err
	} else {
		flushErr = s.flushAllLocked()
	}
	if s.VectorStore != nil {
		_ = s.VectorStore.Close()
	}
	if s.Index != nil {
		_ = s.Index.Close()
	}
	if s.Ops != nil {
		_ = s.Ops.Close()
	}
	if s.lockFile != nil {
		_ = syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN)
		_ = s.lockFile.Close()
	}
	return flushErr
}

// acquireDirLock takes an exclusive advisory lock on a .lock file inside the
// data dir. Only one process may hold the JSONL store at a time — a second
// process (e.g. an out-of-band --embed run while the daemon is up) would
// otherwise clobber the canonical files on flush.
func acquireDirLock(dir string) (*os.File, error) {
	lockPath := filepath.Join(dir, ".store.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("data dir %s is locked by another mindmoryd process (stop it first): %w", dir, err)
	}
	return file, nil
}

func (s *Store) path(kind string) string {
	return filepath.Join(s.dir, kind+".jsonl")
}

func (s *Store) load() error {
	if err := s.loadSessions(); err != nil {
		return err
	}
	if err := s.loadMessages(); err != nil {
		return err
	}
	if err := s.loadMemories(); err != nil {
		return err
	}
	if err := s.loadProposals(); err != nil {
		return err
	}
	if err := s.loadContinuity(); err != nil {
		return err
	}
	if err := s.loadEvidence(); err != nil {
		return err
	}
	if err := s.loadProjectContext(); err != nil {
		return err
	}
	if err := s.loadKeyRotations(); err != nil {
		return err
	}
	if err := s.loadMutationEvents(); err != nil {
		return err
	}
	if err := s.loadVectorProjection(); err != nil {
		return err
	}
	return s.loadMeta()
}

// loadMeta reads the monotonic session ordinal from meta.jsonl (if any).
func (s *Store) loadMeta() error {
	lines, err := readJSONL(s.path("meta"))
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	var doc struct {
		SessionSeq int64 `json:"session_seq"`
	}
	if json.Unmarshal(lines[len(lines)-1], &doc) != nil {
		return nil // tolerate corrupt meta; seq restarts at max(rows)
	}
	s.sessionSeq = doc.SessionSeq
	// Never go backwards: imported sessions may already carry seqs.
	for _, row := range s.sessions {
		if row.Seq > s.sessionSeq {
			s.sessionSeq = row.Seq
		}
	}
	return nil
}

// persistMeta writes the current session ordinal.
func (s *Store) persistMeta() error {
	doc := struct {
		SessionSeq int64 `json:"session_seq"`
	}{SessionSeq: s.sessionSeq}
	line, _ := json.Marshal(doc)
	return s.flushKindLocked("meta", append(line, '\n'))
}

// persistSchema writes the current schema version marker so future loads
// migrate declaratively instead of guessing.
func (s *Store) persistSchema() error {
	doc := struct {
		Version int `json:"version"`
	}{Version: SchemaVersion}
	line, _ := json.Marshal(doc)
	return s.flushKindLocked("schema", append(line, '\n'))
}

// nextSessionSeq allocates the next monotonic session ordinal.
func (s *Store) nextSessionSeq() int64 {
	s.sessionSeq++
	return s.sessionSeq
}

func readJSONL(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	return lines, scanner.Err()
}

func (s *Store) loadSessions() error {
	lines, err := readJSONL(s.path("sessions"))
	if err != nil {
		return err
	}
	for _, line := range lines {
		var row SessionRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("sessions.jsonl: %w", err)
		}
		s.sessions[row.SessionID] = row
		s.sessionsExt[row.ClientKey+"\x00"+row.ExternalSessionID] = row.SessionID
	}
	return nil
}

func (s *Store) loadMessages() error {
	segmentPaths, err := s.messageSegmentPaths()
	if err != nil {
		return err
	}
	if len(segmentPaths) > 0 {
		return s.loadMessageSegments(segmentPaths)
	}
	lines, err := readJSONL(s.path("messages"))
	if err != nil {
		return err
	}
	var legacy []string
	migrated := false
	for _, line := range lines {
		var row MessageRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("messages.jsonl: %w", err)
		}
		s.messages[row.MessageID] = row
		if row.MessageSeq <= 0 {
			legacy = append(legacy, row.MessageID)
		} else {
			if row.MessageSeq > s.messageSeq {
				s.messageSeq = row.MessageSeq
			}
			if row.TurnSeq > s.turnSeq {
				s.turnSeq = row.TurnSeq
			}
		}
	}
	sort.Slice(legacy, func(i, j int) bool {
		left, right := s.messages[legacy[i]], s.messages[legacy[j]]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.MessageID < right.MessageID
	})
	for _, id := range legacy {
		row := s.messages[id]
		s.messageSeq++
		if row.Role == string(archive.RoleUser) {
			s.turnSeq++
		}
		row.MessageSeq = s.messageSeq
		row.TurnSeq = s.turnSeq
		if row.ExactContentHash == "" {
			row.ExactContentHash = hashContent(row.Content)
		}
		s.messages[id] = row
		migrated = true
	}
	if len(s.messages) > 0 {
		return s.initializeMessageJournal()
	}
	_ = migrated
	return nil
}

func (s *Store) loadMemories() error {
	lines, err := readJSONL(s.path("memories"))
	if err != nil {
		return err
	}
	// Declarative versioned migration: each row is lifted from the version
	// it was written under to the current SchemaVersion. Version-gated, not
	// guess-gated.
	version := s.schemaVersion()
	migrated := false
	for _, line := range lines {
		var row MemoryRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("memories.jsonl: %w", err)
		}
		before := row.SchemaVersion
		row = migrateMemoryRow(row, line, version)
		if row.SchemaVersion != before || (version < SchemaVersion && row.SchemaVersion == SchemaVersion) {
			migrated = true
		}
		s.memories[row.MemoryID] = row
	}
	if migrated {
		// Persist the lifted rows so the migration is materialized, not
		// re-run on every boot.
		if err := s.flushKindLocked("memories", s.memoriesJSONL()); err != nil {
			return err
		}
	}
	return nil
}

// migrateMemoryRow lifts one memory row to the current schema version.
func migrateMemoryRow(row MemoryRow, rawLine []byte, fromVersion int) MemoryRow {
	if fromVersion < 2 {
		// v1 -> v2: the single "heat" field carried activation. It is not a
		// struct field anymore, so peek the raw line. Confidence defaults to
		// full trust (v1 rows were USER_ACCEPTED); activation defaults to
		// fully-active until first use.
		var raw map[string]any
		_ = json.Unmarshal(rawLine, &raw)
		if legacy, ok := raw["heat"].(float64); ok && row.Activation == 0 {
			row.Activation = legacy
		}
		if row.Confidence == 0 {
			row.Confidence = 1.0
		}
		if row.Activation == 0 {
			row.Activation = 1.0
		}
	}
	if row.StateVersion <= 0 {
		row.StateVersion = 1
	}
	row.SchemaVersion = SchemaVersion
	return row
}

// schemaVersion reads the declared schema version for the memory store.
func (s *Store) schemaVersion() int {
	lines, err := readJSONL(s.path("schema"))
	if err != nil || len(lines) == 0 {
		return 1 // no schema marker = v1 era
	}
	var doc struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(lines[len(lines)-1], &doc) != nil {
		return 1
	}
	if doc.Version <= 0 {
		return 1
	}
	return doc.Version
}

func (s *Store) loadProposals() error {
	lines, err := readJSONL(s.path("proposals"))
	if err != nil {
		return err
	}
	for _, line := range lines {
		var row proposalRecord
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("proposals.jsonl: %w", err)
		}
		proposal := row.toProposal()
		s.proposals[proposal.ID] = proposal
		if proposal.RequestHash != "" {
			s.proposalsHash[proposal.RequestHash] = proposal.ID
		}
	}
	return nil
}

func (s *Store) loadContinuity() error {
	lines, err := readJSONL(s.path("continuity"))
	if err != nil {
		return err
	}
	for _, line := range lines {
		var row ContinuityRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("continuity.jsonl: %w", err)
		}
		s.continuity = append(s.continuity, row)
	}
	sort.SliceStable(s.continuity, func(i, j int) bool {
		return s.continuity[i].RevisionNumber < s.continuity[j].RevisionNumber
	})
	return nil
}

func (s *Store) loadEvidence() error {
	lines, err := readJSONL(s.path("evidence"))
	if err != nil {
		return err
	}
	for _, line := range lines {
		var row MessageEvidenceRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("evidence.jsonl: %w", err)
		}
		// Hydrate quote text + occurrence from the archived message so recall
		// can render evidence without a join.
		// Message content is hydrated by the SQLite evidence/message join on
		// demand. Keeping it here duplicates every cited message in RAM and in
		// evidence.jsonl.
		row.MessageContent = ""
		row.OccurredAt = time.Time{}
		s.evidence[row.MemoryID] = append(s.evidence[row.MemoryID], row)
	}
	return nil
}

func (s *Store) loadProjectContext() error {
	lines, err := readJSONL(s.path("project_context"))
	if err != nil {
		return err
	}
	for _, line := range lines {
		var row ProjectContextRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("project_context.jsonl: %w", err)
		}
		s.projectCtx[row.ProjectKey] = row
	}
	return nil
}

// flushAllLocked rewrites every JSONL file atomically from in-memory state.
// Callers must hold the write lock.
func (s *Store) flushAllLocked() error {
	if err := s.flushKindLocked("sessions", s.sessionsJSONL()); err != nil {
		return err
	}
	if s.lowRAM {
		if err := s.flushMessagesFromIndexLocked(); err != nil {
			return err
		}
		if err := s.flushMemoriesFromIndexLocked(); err != nil {
			return err
		}
	} else {
		if err := s.flushKindLocked("messages", s.messagesJSONL()); err != nil {
			return err
		}
		if err := s.flushKindLocked("memories", s.memoriesJSONL()); err != nil {
			return err
		}
	}
	if err := s.flushKindLocked("proposals", s.proposalsJSONL()); err != nil {
		return err
	}
	if err := s.flushKindLocked("continuity", s.continuityJSONL()); err != nil {
		return err
	}
	if s.lowRAM {
		if err := s.flushEvidenceFromIndexLocked(); err != nil {
			return err
		}
	} else if err := s.flushEvidenceLocked(); err != nil {
		return err
	}
	return s.flushKindLocked("project_context", s.projectContextJSONL())
}

func (s *Store) flushMemoriesFromIndexLocked() error {
	rows, err := s.Index.AllMemories()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, row := range rows {
		line, _ := json.Marshal(row)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return s.flushKindLocked("memories", buf.Bytes())
}

func (s *Store) flushMessagesFromIndexLocked() error {
	rows, err := s.Index.AllMessages()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, row := range rows {
		line, _ := json.Marshal(row)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return s.flushKindLocked("messages", buf.Bytes())
}

func (s *Store) flushEvidenceFromIndexLocked() error {
	rows, err := s.Index.AllEvidence()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, row := range rows {
		row.MessageContent = ""
		row.OccurredAt = time.Time{}
		line, _ := json.Marshal(row)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return s.flushKindLocked("evidence", buf.Bytes())
}

func (s *Store) flushKindLocked(kind string, lines []byte) error {
	path := s.path(kind)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, lines, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", kind, err)
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", kind, err)
	}
	return syncDir(s.dir)
}

func (s *Store) sessionsJSONL() []byte {
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var buf bytes.Buffer
	for _, id := range ids {
		line, _ := json.Marshal(s.sessions[id])
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (s *Store) messagesJSONL() []byte {
	ids := make([]string, 0, len(s.messages))
	for id := range s.messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var buf bytes.Buffer
	for _, id := range ids {
		line, _ := json.Marshal(s.messages[id])
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (s *Store) memoriesJSONL() []byte {
	ids := make([]string, 0, len(s.memories))
	for id := range s.memories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var buf bytes.Buffer
	for _, id := range ids {
		line, _ := json.Marshal(s.memories[id])
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (s *Store) proposalsJSONL() []byte {
	ids := make([]string, 0, len(s.proposals))
	for id := range s.proposals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var buf bytes.Buffer
	for _, id := range ids {
		line, _ := json.Marshal(proposalRecordFrom(s.proposals[id]))
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (s *Store) continuityJSONL() []byte {
	var buf bytes.Buffer
	for _, row := range s.continuity {
		line, _ := json.Marshal(row)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (s *Store) projectContextJSONL() []byte {
	keys := make([]string, 0, len(s.projectCtx))
	for key := range s.projectCtx {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, key := range keys {
		line, _ := json.Marshal(s.projectCtx[key])
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// syncFile fsyncs a single file.
func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// ensureAppendFile makes the directory entry durable before any authoritative
// record is appended. Append callers can then treat a successful file fsync as
// the unambiguous commit point without a later directory-fsync error.
func ensureAppendFile(path, dir string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDir(dir)
}

// --- snapshot export (for backups and inspection) ---

// Snapshot returns a canonical JSON document of the entire store.
func (s *Store) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc := map[string]any{
		"sessions":        s.sessions,
		"messages":        s.messages,
		"memories":        s.memories,
		"proposals":       s.proposals,
		"continuity":      s.continuity,
		"project_context": s.projectCtx,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// --- record row types (JSON shapes are the canonical data format) ---

// SessionRow is a stored session.
type SessionRow struct {
	SessionID         string    `json:"session_id"`
	ClientKey         string    `json:"client_key"`
	ExternalSessionID string    `json:"external_session_id"`
	Title             string    `json:"title,omitempty"`
	ProjectKey        string    `json:"project_key,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	LastActivityAt    time.Time `json:"last_activity_at"`
	CreatedAt         time.Time `json:"created_at"`
	// Seq is the append-only monotonic session ordinal (expert P0-6). Decay
	// uses seq differences, immune to clock changes / concurrent sessions.
	Seq int64 `json:"seq,omitempty"`
}

// MessageRow is a stored archived message with its policy state.
type MessageRow struct {
	SchemaVersion     int       `json:"schema_version"`
	MessageID         string    `json:"message_id"`
	SessionID         string    `json:"session_id"`
	ExternalMessageID string    `json:"external_message_id"`
	Role              string    `json:"role"`
	ContentType       string    `json:"content_type"`
	Content           string    `json:"content"`
	ContentHash       string    `json:"content_hash"`
	ExactContentHash  string    `json:"exact_content_hash,omitempty"`
	MessageSeq        int64     `json:"message_seq"`
	TurnSeq           int64     `json:"turn_seq"`
	OccurredAt        time.Time `json:"occurred_at"`
	CreatedAt         time.Time `json:"created_at"`
	SecretLike        bool      `json:"secret_like"`
	InstructionLike   bool      `json:"instruction_like"`
	Sensitivity       string    `json:"sensitivity"`
	// AssistantID/AssistantName identify the assistant that produced an
	// assistant-role message (e.g. "ember" / "余烬"); empty on user messages.
	AssistantID        string `json:"assistant_id,omitempty"`
	AssistantName      string `json:"assistant_name,omitempty"`
	PreviousRecordHash string `json:"previous_record_hash,omitempty"`
	RecordHash         string `json:"record_hash,omitempty"`
}

// MemoryRow is a stored cognitive memory.
type MemoryRow struct {
	SchemaVersion      int        `json:"schema_version,omitempty"`
	MemoryID           string     `json:"memory_id"`
	Kind               string     `json:"memory_kind"`
	Subject            string     `json:"subject"`
	Content            string     `json:"content"`
	ContentHash        string     `json:"content_hash"`
	Lifecycle          string     `json:"lifecycle"`
	EpistemicStatus    string     `json:"epistemic_status"`
	Confidence         float64    `json:"confidence"`
	Importance         float64    `json:"importance"`
	Sensitivity        string     `json:"sensitivity"`
	SecretLike         bool       `json:"secret_like,omitempty"`
	InstructionLike    bool       `json:"instruction_like,omitempty"`
	StateVersion       int64      `json:"state_version"`
	SupersedesMemoryID string     `json:"supersedes_memory_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ScopeType          string     `json:"scope_type"`
	ProjectKey         string     `json:"project_key,omitempty"`
	AccessCount        int64      `json:"access_count"`
	LastAccessedAt     *time.Time `json:"last_accessed_at,omitempty"`
	// Activation is how recently/frequently the memory has been USED (not
	// merely retrieved). It decays by session; there is no importance floor
	// — an unused memory cools toward zero regardless of importance.
	Activation float64 `json:"activation"`
	// Disputed marks a memory contradicted by feedback/evidence; it stays in
	// the record but is down-weighted in retrieval until corrected.
	Disputed bool `json:"disputed,omitempty"`
	// RepeatCount counts how many times the user has explicitly re-requested
	// this same memory (dedupe promote). Repetition is an importance signal:
	// each explicit duplicate request bumps importance and increments this
	// counter instead of creating a new row.
	RepeatCount int64 `json:"repeat_count,omitempty"`
	// LastUsedSeq is the session ordinal of the memory's last meaningful
	// use (recall / reflex surfacing / helped feedback). Decay = current
	// session seq - lastUsedSeq. 0 = never used since import.
	LastUsedSeq int64 `json:"last_used_seq,omitempty"`
}

// VectorProjectionRow is disposable semantic-search state. It is excluded
// from canonical snapshots and mutation events and can always be regenerated.
type VectorProjectionRow struct {
	MemoryID    string    `json:"memory_id"`
	ContentHash string    `json:"content_hash"`
	Model       string    `json:"model"`
	Dimensions  int       `json:"dimensions"`
	Vector      []float32 `json:"vector"`
}

// MessageEvidenceRow is a memory-to-message evidence link (quote slice).
type MessageEvidenceRow struct {
	MemoryID       string    `json:"memory_id"`
	MessageID      string    `json:"message_id"`
	QuoteHash      string    `json:"quote_hash"`
	QuoteStart     int       `json:"quote_start_byte"`
	QuoteEnd       int       `json:"quote_end_byte"`
	Relation       string    `json:"relation"`
	CreatedAt      time.Time `json:"created_at"`
	MessageContent string    `json:"message_content,omitempty"` // hydrated by SQLite join on read
	OccurredAt     time.Time `json:"occurred_at,omitempty"`     // hydrated by SQLite join on read
}

// ProjectContextRow is a stored project context revision.
type ProjectContextRow struct {
	RevisionID    string    `json:"revision_id"`
	ProjectKey    string    `json:"project_key"`
	Revision      int       `json:"revision"`
	Objective     string    `json:"objective"`
	CurrentState  string    `json:"current_state"`
	Decisions     string    `json:"decisions_json"`
	OpenQuestions string    `json:"open_questions_json"`
	NextActions   string    `json:"next_actions_json"`
	Sensitivity   string    `json:"sensitivity"`
	IsCurrent     bool      `json:"is_current"`
	CreatedAt     time.Time `json:"created_at"`
}

// proposalRecord is the canonical JSON shape of a proposal.
type proposalRecord struct {
	ProposalID          string     `json:"proposal_id"`
	SourceClientKey     string     `json:"source_client_key"`
	SourceSessionID     string     `json:"source_session_id"`
	SourceMessageID     string     `json:"source_message_id"`
	MutationKind        string     `json:"mutation_kind"`
	TargetMemoryID      string     `json:"target_memory_id,omitempty"`
	ProposedKind        string     `json:"proposed_kind,omitempty"`
	Subject             string     `json:"subject"`
	ReplacementContent  string     `json:"replacement_content,omitempty"`
	Status              string     `json:"status"`
	ReasonCode          string     `json:"reason_code,omitempty"`
	GateClass           string     `json:"gate_class,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
	RequestHash         string     `json:"request_hash,omitempty"`
	EvidenceQuoteHash   string     `json:"evidence_quote_hash,omitempty"`
	EvidenceContentHash string     `json:"evidence_content_hash,omitempty"`
	EvidenceStartByte   int        `json:"evidence_start_byte,omitempty"`
	EvidenceEndByte     int        `json:"evidence_end_byte,omitempty"`
	ScopeType           string     `json:"scope_type"`
	ProjectKey          string     `json:"project_key,omitempty"`
	AppliedMemoryID     string     `json:"applied_memory_id,omitempty"`
}

func proposalRecordFrom(p domain.Proposal) proposalRecord {
	var evidenceHash string
	var evidenceStart, evidenceEnd int
	if p.Identity.Evidence != nil {
		evidenceHash = p.Identity.Evidence.Hash
		evidenceStart = p.Identity.Evidence.StartByte
		evidenceEnd = p.Identity.Evidence.EndByte
	}
	return proposalRecord{
		ProposalID: p.ID, SourceClientKey: p.Identity.ClientKey, SourceSessionID: p.Identity.SessionID,
		SourceMessageID: p.Identity.MessageID, MutationKind: string(p.Identity.Mutation),
		TargetMemoryID: p.Identity.TargetMemoryID, ProposedKind: string(p.Identity.ProposedKind),
		Subject: p.Identity.Subject, ReplacementContent: p.Identity.Replacement, Status: string(p.Status),
		ReasonCode: p.ReasonCode, GateClass: string(p.GateClass), CreatedAt: p.CreatedAt, ResolvedAt: p.ResolvedAt, RequestHash: p.RequestHash,
		EvidenceQuoteHash: evidenceHash, EvidenceContentHash: p.Identity.EvidenceContentHash,
		EvidenceStartByte: evidenceStart, EvidenceEndByte: evidenceEnd, ScopeType: string(p.Identity.Scope), ProjectKey: p.Identity.ProjectKey,
		AppliedMemoryID: p.AppliedMemoryID,
	}
}

func (r proposalRecord) toProposal() domain.Proposal {
	identity := domain.ProposalIdentity{
		ClientKey: r.SourceClientKey, SessionID: r.SourceSessionID, MessageID: r.SourceMessageID,
		Mutation: domain.MutationKind(r.MutationKind), TargetMemoryID: r.TargetMemoryID,
		ProposedKind: domain.Kind(r.ProposedKind), Scope: domain.ScopeType(r.ScopeType),
		ProjectKey: r.ProjectKey, Subject: r.Subject, Replacement: r.ReplacementContent,
		RequestEvidenceHash: r.EvidenceQuoteHash, EvidenceContentHash: r.EvidenceContentHash,
	}
	if r.EvidenceQuoteHash != "" && r.EvidenceEndByte > r.EvidenceStartByte {
		identity.Evidence = &domain.MessageQuote{Hash: r.EvidenceQuoteHash, StartByte: r.EvidenceStartByte, EndByte: r.EvidenceEndByte}
	}
	return domain.Proposal{
		ID: r.ProposalID, RequestHash: r.RequestHash, ReasonCode: r.ReasonCode, GateClass: domain.GateClass(r.GateClass),
		Identity:        identity,
		AppliedMemoryID: r.AppliedMemoryID, Status: domain.ProposalStatus(r.Status),
		CreatedAt: r.CreatedAt, ResolvedAt: r.ResolvedAt,
	}
}

var _ = context.Background
var _ = retrieval.DefaultSearchLimit
var _ = policy.SensitivityNormal
var _ = archive.RoleUser
var _ = auth.PrincipalMCP
var _ = domain.MutationRemember
var _ = strings.TrimSpace
