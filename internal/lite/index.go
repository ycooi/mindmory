// Package lite: complete SQLite read projection (derived, disposable).
//
// JSONL is the canonical truth; SQLite is a derived index that can always
// be rebuilt from the JSONL files. It exists because JSONL is hard to
// index and search — SQL does that work. On boot the index rebuilds when
// its fingerprint no longer matches the canonical store; every mutation
// upserts into it so it stays warm between rebuilds. If the index file is
// ever lost or corrupted, deleting it and restarting rebuilds it from JSONL.
package lite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"mindmory.local/core/internal/lite/vectorstore"

	_ "modernc.org/sqlite"
)

// IndexDBPath is the SQLite search index location (derived from JSONL).
const IndexDBPath = "var/index.db"

const indexSchema = `
CREATE TABLE IF NOT EXISTS index_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS memories_current (
  memory_id TEXT PRIMARY KEY, kind TEXT NOT NULL, subject TEXT NOT NULL,
  content TEXT NOT NULL, content_hash TEXT NOT NULL,
  embedding_input_hash TEXT NOT NULL, lifecycle TEXT NOT NULL,
  sensitivity TEXT NOT NULL, scope TEXT NOT NULL,
  project_key TEXT NOT NULL DEFAULT '', disputed INTEGER NOT NULL DEFAULT 0,
  secret_like INTEGER NOT NULL DEFAULT 0,
  instruction_like INTEGER NOT NULL DEFAULT 0,
  updated_at_unix_nano INTEGER NOT NULL,
  record_json BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS messages_current (
  message_id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
  external_message_id TEXT NOT NULL, role TEXT NOT NULL,
  message_seq INTEGER NOT NULL, turn_seq INTEGER NOT NULL,
  record_json BLOB NOT NULL,
  UNIQUE(session_id, external_message_id)
);
CREATE INDEX IF NOT EXISTS messages_session_order
  ON messages_current(session_id, turn_seq DESC, message_seq DESC);
CREATE TABLE IF NOT EXISTS evidence_current (
  memory_id TEXT NOT NULL, message_id TEXT NOT NULL,
  quote_hash TEXT NOT NULL, relation TEXT NOT NULL,
  created_at_unix_nano INTEGER NOT NULL, record_json BLOB NOT NULL,
  PRIMARY KEY(memory_id, message_id, quote_hash, relation)
);
CREATE INDEX IF NOT EXISTS evidence_memory_order
  ON evidence_current(memory_id, created_at_unix_nano);
CREATE TABLE IF NOT EXISTS vector_refs (
  generation TEXT NOT NULL, vector_ordinal INTEGER NOT NULL,
  source_type TEXT NOT NULL, source_id TEXT NOT NULL,
  embedding_input_hash TEXT NOT NULL, stale INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (generation, vector_ordinal),
  UNIQUE (generation, source_type, source_id, embedding_input_hash)
);
CREATE INDEX IF NOT EXISTS vector_refs_source ON vector_refs(generation, source_type, source_id);
CREATE TABLE IF NOT EXISTS projection_state (
  projection_name TEXT PRIMARY KEY, projection_version INTEGER NOT NULL,
  source_fingerprint TEXT NOT NULL, updated_at_unix_nano INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  subject, content, kind, scope, project_key, lifecycle, sensitivity,
  memory_id UNINDEXED,
  tokenize = 'trigram'
);
`

// MemoryIndex is the SQLite-backed read projection for memories, archived
// messages, evidence, FTS search, and vector references.
type MemoryIndex struct {
	db *sql.DB
}

type ReadProjectionCounts struct {
	Messages, Memories, ActiveMemories, InactiveMemories   int
	SecretLikeMemories, InstructionMemories, EvidenceLinks int
}

// VectorProjectionCounts summarizes vector freshness without materializing
// the memory archive. This keeps routine status/MCP health calls bounded in
// low-RAM mode even when the archive contains hundreds of thousands of rows.
type VectorProjectionCounts struct {
	Active, Missing, Stale, Tombstoned int
}

// OpenMemoryIndex opens (creating if needed) the SQLite index at path.
func OpenMemoryIndex(path string) (*MemoryIndex, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	db.SetMaxOpenConns(1)
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=10000",
		"PRAGMA synchronous=NORMAL",
	}
	for _, statement := range pragmas {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	var schemaVersion int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("read index schema version: %w", err)
	}
	// This database is disposable. Recreating an older projection is safer
	// than carrying a chain of migrations for data that canonical JSONL can
	// reproduce exactly.
	if schemaVersion != 4 {
		if _, err := db.Exec(`DROP TABLE IF EXISTS memories_fts;
DROP TABLE IF EXISTS evidence_current;
DROP TABLE IF EXISTS messages_current;
DROP TABLE IF EXISTS vector_refs;
DROP TABLE IF EXISTS memories_current;
DROP TABLE IF EXISTS projection_state;
DROP TABLE IF EXISTS index_meta;`); err != nil {
			db.Close()
			return nil, fmt.Errorf("reset old index schema: %w", err)
		}
	}
	if _, err := db.Exec(indexSchema + "\nPRAGMA user_version=4;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("index schema: %w", err)
	}
	return &MemoryIndex{db: db}, nil
}

// Close releases the index database.
func (m *MemoryIndex) Close() error { return m.db.Close() }

// Checkpoint merges the WAL into the main database file so that a plain
// file copy of index.db captures every write. The daemon runs WAL mode;
// without this, a backup that copies only index.db silently drops whatever
// still lives in index.db-wal. Safe to call anytime; cheap when the WAL is
// already small.
func (m *MemoryIndex) Checkpoint() error {
	_, err := m.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Fingerprint derives a stable hash of all memory rows — a change in any
// row changes the fingerprint, so the index knows when it is stale.
func Fingerprint(rows []MemoryRow) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("index-schema=4;tokenizer=fts5-trigram;normalization=1\n"))
	rows = append([]MemoryRow(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MemoryID < rows[j].MemoryID })
	for _, row := range rows {
		line, _ := json.Marshal(struct {
			ID              string
			Kind            string
			Subject         string
			Content         string
			Lifecycle       string
			Sensitivity     string
			SecretLike      bool
			InstructionLike bool
			Scope           string
			ProjectKey      string
			UpdatedAt       time.Time
		}{row.MemoryID, row.Kind, row.Subject, row.Content, row.Lifecycle, row.Sensitivity, row.SecretLike, row.InstructionLike, row.ScopeType, row.ProjectKey, row.UpdatedAt})
		_, _ = hasher.Write(line)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// storedFingerprint reads the last-built fingerprint from index_meta.
func (m *MemoryIndex) storedFingerprint() (string, error) {
	var value string
	err := m.db.QueryRow("SELECT value FROM index_meta WHERE key='fingerprint'").Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (m *MemoryIndex) storedSupportFingerprint() (string, error) {
	var value string
	err := m.db.QueryRow("SELECT value FROM index_meta WHERE key='support_fingerprint'").Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func SupportFingerprint(messages []MessageRow, evidence []MessageEvidenceRow) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("support-schema=1\n"))
	messages = append([]MessageRow(nil), messages...)
	sort.Slice(messages, func(i, j int) bool { return messages[i].MessageID < messages[j].MessageID })
	for _, row := range messages {
		raw, _ := json.Marshal(row)
		_, _ = hasher.Write(raw)
	}
	evidence = append([]MessageEvidenceRow(nil), evidence...)
	sort.Slice(evidence, func(i, j int) bool {
		left := evidence[i].MemoryID + "\x00" + evidence[i].MessageID + "\x00" + evidence[i].QuoteHash + "\x00" + evidence[i].Relation
		right := evidence[j].MemoryID + "\x00" + evidence[j].MessageID + "\x00" + evidence[j].QuoteHash + "\x00" + evidence[j].Relation
		return left < right
	})
	for _, row := range evidence {
		row.MessageContent = ""
		row.OccurredAt = time.Time{}
		raw, _ := json.Marshal(row)
		_, _ = hasher.Write(raw)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// RebuildFrom replaces the entire index content with rows and records the
// fingerprint. It is the recovery path after JSONL migration or index loss.
func (m *MemoryIndex) RebuildFrom(rows []MemoryRow) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM memories_fts"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM memories_current"); err != nil {
		return err
	}
	rows = append([]MemoryRow(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MemoryID < rows[j].MemoryID })
	for _, row := range rows {
		if err := upsertCurrentRow(tx, row); err != nil {
			return err
		}
		if memoryRowPolicyAllowed(row) && row.Lifecycle == "ACTIVE" {
			if err := insertIndexedRow(tx, row); err != nil {
				return err
			}
		}
	}
	fp := Fingerprint(rows)
	if _, err := tx.Exec(`INSERT INTO projection_state(projection_name,projection_version,source_fingerprint,updated_at_unix_nano)
		VALUES('memory',4,?,?) ON CONFLICT(projection_name) DO UPDATE SET projection_version=excluded.projection_version,
		source_fingerprint=excluded.source_fingerprint,updated_at_unix_nano=excluded.updated_at_unix_nano`, fp, time.Now().UnixNano()); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO index_meta(key,value) VALUES('fingerprint',?) "+
		"ON CONFLICT(key) DO UPDATE SET value=excluded.value", fp); err != nil {
		return err
	}
	return tx.Commit()
}

type indexWriter interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertIndexedRow(tx indexWriter, row MemoryRow) error {
	_, err := tx.Exec(`INSERT INTO memories_fts(subject,content,kind,scope,project_key,lifecycle,sensitivity,memory_id)
		VALUES (?,?,?,?,?,?,?,?)`,
		row.Subject, row.Content, row.Kind, row.ScopeType, row.ProjectKey,
		row.Lifecycle, row.Sensitivity, row.MemoryID)
	return err
}

func upsertCurrentRow(tx indexWriter, row MemoryRow) error {
	record, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO memories_current(memory_id,kind,subject,content,content_hash,embedding_input_hash,lifecycle,sensitivity,scope,project_key,disputed,secret_like,instruction_like,updated_at_unix_nano,record_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id) DO UPDATE SET kind=excluded.kind,subject=excluded.subject,
		content=excluded.content,content_hash=excluded.content_hash,embedding_input_hash=excluded.embedding_input_hash,lifecycle=excluded.lifecycle,
		sensitivity=excluded.sensitivity,scope=excluded.scope,project_key=excluded.project_key,disputed=excluded.disputed,updated_at_unix_nano=excluded.updated_at_unix_nano,
		secret_like=excluded.secret_like,instruction_like=excluded.instruction_like,record_json=excluded.record_json`,
		row.MemoryID, row.Kind, row.Subject, row.Content, row.ContentHash, EmbeddingInputHash(row), row.Lifecycle,
		row.Sensitivity, row.ScopeType, row.ProjectKey, row.Disputed, boolInt(row.SecretLike), boolInt(row.InstructionLike), row.UpdatedAt.UnixNano(), record)
	return err
}

// Upsert refreshes a single row after a mutation. Deleting + inserting keeps
// the FTS5 content-addressed document canonical.
func (m *MemoryIndex) Upsert(row MemoryRow) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM memories_fts WHERE memory_id=?", row.MemoryID); err != nil {
		return err
	}
	if err := upsertCurrentRow(tx, row); err != nil {
		return err
	}
	if memoryRowPolicyAllowed(row) && row.Lifecycle == "ACTIVE" {
		if err := insertIndexedRow(tx, row); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadMemory returns the complete disposable read projection for one memory.
func (m *MemoryIndex) LoadMemory(memoryID string) (MemoryRow, error) {
	var raw []byte
	if err := m.db.QueryRow(`SELECT record_json FROM memories_current WHERE memory_id=?`, memoryID).Scan(&raw); err != nil {
		return MemoryRow{}, err
	}
	var row MemoryRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return MemoryRow{}, err
	}
	return row, nil
}

// LoadMemories resolves an FTS candidate batch in one SQLite query and keeps
// the caller's candidate order. This avoids one database round trip per hit.
func (m *MemoryIndex) LoadMemories(memoryIDs []string) ([]MemoryRow, error) {
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	if len(memoryIDs) > 500 {
		return nil, fmt.Errorf("too many memory ids")
	}
	marks := make([]string, len(memoryIDs))
	args := make([]any, len(memoryIDs))
	for i, id := range memoryIDs {
		marks[i] = "?"
		args[i] = id
	}
	rows, err := m.db.Query(`SELECT memory_id,record_json FROM memories_current WHERE memory_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]MemoryRow, len(memoryIDs))
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var row MemoryRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		byID[id] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]MemoryRow, 0, len(memoryIDs))
	for _, id := range memoryIDs {
		if row, ok := byID[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

// EligibleMemoryRows reads complete records from SQLite. JSONL is never
// opened on this path; it is only the rebuild authority.
func (m *MemoryIndex) EligibleMemoryRows(projectKey string, kinds []string, historical bool, limit int) ([]MemoryRow, error) {
	query := `SELECT record_json FROM memories_current WHERE sensitivity='NORMAL'
		AND (scope='GLOBAL' OR (? <> '' AND scope='PROJECT' AND project_key=?))`
	args := []any{projectKey, projectKey}
	if !historical {
		query += ` AND lifecycle='ACTIVE'`
	}
	if len(kinds) > 0 {
		marks := make([]string, len(kinds))
		for i, kind := range kinds {
			marks[i] = "?"
			args = append(args, kind)
		}
		query += ` AND kind IN (` + strings.Join(marks, ",") + `)`
	}
	query += ` ORDER BY updated_at_unix_nano DESC, memory_id ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryRow
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row MemoryRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *MemoryIndex) AllMemories() ([]MemoryRow, error) {
	rows, err := m.db.Query(`SELECT record_json FROM memories_current ORDER BY memory_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryRow
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row MemoryRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *MemoryIndex) AllMessages() ([]MessageRow, error) {
	rows, err := m.db.Query(`SELECT record_json FROM messages_current ORDER BY message_seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRow
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row MessageRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *MemoryIndex) AllEvidence() ([]MessageEvidenceRow, error) {
	rows, err := m.db.Query(`SELECT record_json FROM evidence_current ORDER BY memory_id,created_at_unix_nano`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageEvidenceRow
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row MessageEvidenceRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *MemoryIndex) Counts() (ReadProjectionCounts, error) {
	var c ReadProjectionCounts
	err := m.db.QueryRow(`SELECT
		(SELECT count(*) FROM messages_current),
		(SELECT count(*) FROM memories_current),
		(SELECT count(*) FROM memories_current WHERE lifecycle='ACTIVE'),
		(SELECT count(*) FROM memories_current WHERE lifecycle<>'ACTIVE'),
		(SELECT count(*) FROM memories_current WHERE secret_like=1),
		(SELECT count(*) FROM memories_current WHERE instruction_like=1),
		(SELECT count(*) FROM evidence_current)`).Scan(&c.Messages, &c.Memories, &c.ActiveMemories, &c.InactiveMemories,
		&c.SecretLikeMemories, &c.InstructionMemories, &c.EvidenceLinks)
	return c, err
}

func (m *MemoryIndex) VectorCounts(generation string) (VectorProjectionCounts, error) {
	var c VectorProjectionCounts
	const eligible = `lifecycle='ACTIVE' AND sensitivity='NORMAL' AND secret_like=0 AND instruction_like=0`
	if err := m.db.QueryRow(`SELECT count(*) FROM memories_current WHERE ` + eligible).Scan(&c.Active); err != nil {
		return c, err
	}
	if generation == "" {
		c.Missing = c.Active
		return c, nil
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM memories_current mc
		WHERE mc.`+eligible+` AND NOT EXISTS (
			SELECT 1 FROM vector_refs vr WHERE vr.generation=? AND vr.source_type='MEMORY'
			AND vr.source_id=mc.memory_id AND vr.embedding_input_hash=mc.embedding_input_hash AND vr.stale=0
		)`, generation).Scan(&c.Missing); err != nil {
		return c, err
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM vector_refs vr
		JOIN memories_current mc ON mc.memory_id=vr.source_id
		WHERE vr.generation=? AND vr.source_type='MEMORY' AND mc.`+eligible+`
		AND (vr.stale<>0 OR vr.embedding_input_hash<>mc.embedding_input_hash)`, generation).Scan(&c.Stale); err != nil {
		return c, err
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM vector_refs vr
		LEFT JOIN memories_current mc ON mc.memory_id=vr.source_id
		WHERE vr.generation=? AND vr.source_type='MEMORY' AND (
			mc.memory_id IS NULL OR mc.lifecycle<>'ACTIVE' OR mc.sensitivity<>'NORMAL'
			OR mc.secret_like<>0 OR mc.instruction_like<>0
		)`, generation).Scan(&c.Tombstoned); err != nil {
		return c, err
	}
	return c, nil
}

func (m *MemoryIndex) LearnerMessages(limit int) ([]learnerCandidate, error) {
	rows, err := m.db.Query(`SELECT m.record_json FROM messages_current m
		WHERE m.role='user' AND NOT EXISTS (SELECT 1 FROM evidence_current e WHERE e.message_id=m.message_id)
		ORDER BY m.message_seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []learnerCandidate
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row MessageRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		if row.Sensitivity != "NORMAL" || row.SecretLike || row.InstructionLike {
			continue
		}
		out = append(out, learnerCandidate{sessionID: row.SessionID, messageID: row.MessageID, content: row.Content, occurredAt: row.OccurredAt})
	}
	return out, rows.Err()
}

func upsertMessageRow(tx indexWriter, row MessageRow) error {
	record, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO messages_current(message_id,session_id,external_message_id,role,message_seq,turn_seq,record_json)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET session_id=excluded.session_id,
		external_message_id=excluded.external_message_id,role=excluded.role,message_seq=excluded.message_seq,
		turn_seq=excluded.turn_seq,record_json=excluded.record_json`, row.MessageID, row.SessionID,
		row.ExternalMessageID, row.Role, row.MessageSeq, row.TurnSeq, record)
	return err
}

func (m *MemoryIndex) UpsertMessage(row MessageRow) error {
	return upsertMessageRow(m.db, row)
}

func (m *MemoryIndex) LoadMessage(messageID string) (MessageRow, error) {
	var raw []byte
	if err := m.db.QueryRow(`SELECT record_json FROM messages_current WHERE message_id=?`, messageID).Scan(&raw); err != nil {
		return MessageRow{}, err
	}
	var row MessageRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return MessageRow{}, err
	}
	return row, nil
}

func (m *MemoryIndex) MessageByExternal(sessionID, externalID string) (MessageRow, error) {
	var raw []byte
	if err := m.db.QueryRow(`SELECT record_json FROM messages_current WHERE session_id=? AND external_message_id=?`, sessionID, externalID).Scan(&raw); err != nil {
		return MessageRow{}, err
	}
	var row MessageRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return MessageRow{}, err
	}
	return row, nil
}

func (m *MemoryIndex) LatestUserMessage(sessionID string) (MessageRow, error) {
	var raw []byte
	if err := m.db.QueryRow(`SELECT record_json FROM messages_current WHERE session_id=? AND role='user'
		ORDER BY turn_seq DESC,message_seq DESC LIMIT 1`, sessionID).Scan(&raw); err != nil {
		return MessageRow{}, err
	}
	var row MessageRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return MessageRow{}, err
	}
	return row, nil
}

func upsertEvidenceRow(tx indexWriter, row MessageEvidenceRow) error {
	record, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO evidence_current(memory_id,message_id,quote_hash,relation,created_at_unix_nano,record_json)
		VALUES(?,?,?,?,?,?) ON CONFLICT(memory_id,message_id,quote_hash,relation) DO UPDATE SET
		created_at_unix_nano=excluded.created_at_unix_nano,record_json=excluded.record_json`, row.MemoryID,
		row.MessageID, row.QuoteHash, row.Relation, row.CreatedAt.UnixNano(), record)
	return err
}

func (m *MemoryIndex) UpsertEvidence(row MessageEvidenceRow) error {
	return upsertEvidenceRow(m.db, row)
}

func (m *MemoryIndex) EvidenceFor(memoryID string) ([]MessageEvidenceRow, error) {
	rows, err := m.db.Query(`SELECT e.record_json,m.record_json FROM evidence_current e
		LEFT JOIN messages_current m ON m.message_id=e.message_id WHERE e.memory_id=?
		ORDER BY e.created_at_unix_nano ASC`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageEvidenceRow
	for rows.Next() {
		var evidenceRaw []byte
		var messageRaw []byte
		if err := rows.Scan(&evidenceRaw, &messageRaw); err != nil {
			return nil, err
		}
		var evidence MessageEvidenceRow
		if err := json.Unmarshal(evidenceRaw, &evidence); err != nil {
			return nil, err
		}
		if len(messageRaw) > 0 {
			var message MessageRow
			if err := json.Unmarshal(messageRaw, &message); err != nil {
				return nil, err
			}
			evidence.MessageContent = message.Content
			evidence.OccurredAt = message.OccurredAt
		}
		out = append(out, evidence)
	}
	return out, rows.Err()
}

// ReplaceReadSupport rebuilds message and evidence projections in one
// transaction after canonical startup loading or index loss.
func (m *MemoryIndex) ReplaceReadSupport(messages []MessageRow, evidence []MessageEvidenceRow) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM evidence_current`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages_current`); err != nil {
		return err
	}
	for _, row := range messages {
		if err := upsertMessageRow(tx, row); err != nil {
			return err
		}
	}
	for _, row := range evidence {
		// Do not duplicate message bodies in the evidence record; EvidenceFor
		// hydrates them through the indexed message join.
		row.MessageContent = ""
		row.OccurredAt = time.Time{}
		if err := upsertEvidenceRow(tx, row); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO index_meta(key,value) VALUES('support_fingerprint',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, SupportFingerprint(messages, evidence)); err != nil {
		return err
	}
	return tx.Commit()
}

// Remove deletes a row from the index (e.g. after a forget or hard removal).
func (m *MemoryIndex) Remove(memoryID string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM memories_fts WHERE memory_id=?", memoryID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE memories_current SET lifecycle='FORGOTTEN' WHERE memory_id=?", memoryID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *MemoryIndex) UpsertVectorRef(ref vectorstore.Ref) error {
	_, err := m.db.Exec(`INSERT INTO vector_refs(generation,vector_ordinal,source_type,source_id,embedding_input_hash,stale)
		VALUES(?,?,?,?,?,0) ON CONFLICT(generation,vector_ordinal) DO UPDATE SET source_type=excluded.source_type,
		source_id=excluded.source_id,embedding_input_hash=excluded.embedding_input_hash,stale=0`, ref.Generation, ref.Ordinal, ref.SourceType, ref.SourceID, ref.EmbeddingInputHash)
	return err
}

func (m *MemoryIndex) ReplaceVectorRefs(refs []vectorstore.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	generation := refs[0].Generation
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM vector_refs WHERE generation=?", generation); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err = tx.Exec(`INSERT INTO vector_refs(generation,vector_ordinal,source_type,source_id,embedding_input_hash,stale) VALUES(?,?,?,?,?,0)`, ref.Generation, ref.Ordinal, ref.SourceType, ref.SourceID, ref.EmbeddingInputHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResolveVectorCandidates batch-checks current derived state and scope. The
// caller still performs the final canonical eligibility check.
func (m *MemoryIndex) ResolveVectorCandidates(generation, projectKey string, candidates []vectorstore.Candidate) (map[string]string, error) {
	if len(candidates) == 0 {
		return map[string]string{}, nil
	}
	if len(candidates) > 200 {
		return nil, fmt.Errorf("too many vector candidates")
	}
	placeholders := make([]string, len(candidates))
	args := make([]any, 0, len(candidates)+3)
	args = append(args, generation)
	for i, c := range candidates {
		placeholders[i] = "?"
		args = append(args, c.SourceID)
	}
	args = append(args, projectKey, projectKey)
	query := `SELECT vr.source_id,vr.embedding_input_hash FROM vector_refs vr JOIN memories_current mc ON mc.memory_id=vr.source_id
		WHERE vr.generation=? AND vr.source_id IN (` + strings.Join(placeholders, ",") + `) AND vr.stale=0 AND mc.lifecycle='ACTIVE'
		AND mc.sensitivity='NORMAL' AND (mc.scope='GLOBAL' OR (? <> '' AND mc.scope='PROJECT' AND mc.project_key=?))`
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		out[id] = hash
	}
	return out, rows.Err()
}

// SearchCandidates returns memory ids whose indexed text plausibly matches
// the query, using FTS5 trigram for queries of three or more runes and a
// LIKE fallback for shorter CJK/ASCII queries the trigram index cannot
// match. Scope/lifecycle/sensitivity filters are applied in SQL.
func (m *MemoryIndex) SearchCandidates(query string, projectKey string, kinds []string, limit int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	runes := []rune(query)
	var sqlQuery string
	var args []any
	like := "%" + escapeLike(query) + "%"
	if len(runes) >= 3 {
		// FTS5 trigram MATCH: quote the query so special chars are literal.
		match := quoteFTS(query)
		sqlQuery = `SELECT memory_id FROM memories_fts WHERE memories_fts MATCH ?`
		args = append(args, match)
	} else {
		sqlQuery = `SELECT memory_id FROM memories_fts WHERE (subject LIKE ? OR content LIKE ?)`
		args = append(args, like, like)
	}
	sqlQuery += ` AND lifecycle='ACTIVE' AND sensitivity='NORMAL'`
	sqlQuery += ` AND (scope='GLOBAL' OR (? <> '' AND scope='PROJECT' AND project_key=?))`
	args = append(args, projectKey, projectKey)
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		sqlQuery += ` AND kind IN (` + strings.Join(placeholders, ",") + `)`
	}
	sqlQuery += ` LIMIT ?`
	args = append(args, limit)
	rows, err := m.db.Query(sqlQuery, args...)
	if err != nil {
		// Fall back: if the FTS query syntax failed (e.g. odd operators),
		// treat the whole query as a literal LIKE.
		return m.likeFallback(query, projectKey, kinds, limit)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 && len(runes) >= 3 {
		// FTS found nothing; try LIKE in case trigram missed short CJK runs.
		return m.likeFallback(query, projectKey, kinds, limit)
	}
	return ids, nil
}

func (m *MemoryIndex) likeFallback(query, projectKey string, kinds []string, limit int) ([]string, error) {
	// CJK has no word boundaries: a user often types an entity name embedded
	// in extra words ("薪尽火传的传" when the memory is "薪尽火传"). The
	// full-string LIKE misses it, so try progressively shorter CJK prefixes
	// of the query until something matches. English queries are tried whole
	// first (they have spaces and the whole string is usually meaningful).
	candidates := []string{query}
	// Use the longest contiguous CJK run of the query (the entity name the
	// user likely meant) and try progressively shorter prefixes of it.
	var cjk strings.Builder
	for _, run := range cjkRuns(query) {
		cjk.WriteString(run)
	}
	cjkText := cjk.String()
	if len([]rune(cjkText)) >= 3 {
		runes := []rune(cjkText)
		for n := len(runes) - 1; n >= 2; n-- {
			prefix := string(runes[:n])
			if prefix != query {
				candidates = append(candidates, prefix)
			}
		}
	}
	var lastErr error
	for _, candidate := range candidates {
		ids, err := m.likeOnce(candidate, projectKey, kinds, limit)
		if err != nil {
			lastErr = err
			continue
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

func (m *MemoryIndex) likeOnce(likeQuery, projectKey string, kinds []string, limit int) ([]string, error) {
	like := "%" + escapeLike(likeQuery) + "%"
	sqlQuery := `SELECT memory_id FROM memories_fts WHERE (subject LIKE ? OR content LIKE ?)`
	args := []any{like, like}
	sqlQuery += ` AND lifecycle='ACTIVE' AND sensitivity='NORMAL'`
	sqlQuery += ` AND (scope='GLOBAL' OR (? <> '' AND scope='PROJECT' AND project_key=?))`
	args = append(args, projectKey, projectKey)
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		sqlQuery += ` AND kind IN (` + strings.Join(placeholders, ",") + `)`
	}
	sqlQuery += ` LIMIT ?`
	args = append(args, limit)
	rows, err := m.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// quoteFTS wraps a query in double quotes so FTS5 treats it as a literal
// phrase (trigram tokenizer matches substrings inside it).
func quoteFTS(query string) string {
	escaped := strings.ReplaceAll(query, `"`, `""`)
	return `"` + escaped + `"`
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
