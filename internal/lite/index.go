// Package lite: SQLite search index (derived, disposable).
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
  updated_at_unix_nano INTEGER NOT NULL
);
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

// MemoryIndex is the SQLite-backed search index over memories.
type MemoryIndex struct {
	db *sql.DB
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
	if _, err := db.Exec(indexSchema); err != nil {
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
	_, _ = hasher.Write([]byte("index-schema=2;tokenizer=fts5-trigram;normalization=1\n"))
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
		VALUES('memory',2,?,?) ON CONFLICT(projection_name) DO UPDATE SET projection_version=excluded.projection_version,
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
	_, err := tx.Exec(`INSERT INTO memories_current(memory_id,kind,subject,content,content_hash,embedding_input_hash,lifecycle,sensitivity,scope,project_key,disputed,updated_at_unix_nano)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id) DO UPDATE SET kind=excluded.kind,subject=excluded.subject,
		content=excluded.content,content_hash=excluded.content_hash,embedding_input_hash=excluded.embedding_input_hash,lifecycle=excluded.lifecycle,
		sensitivity=excluded.sensitivity,scope=excluded.scope,project_key=excluded.project_key,disputed=excluded.disputed,updated_at_unix_nano=excluded.updated_at_unix_nano`,
		row.MemoryID, row.Kind, row.Subject, row.Content, row.ContentHash, EmbeddingInputHash(row), row.Lifecycle,
		row.Sensitivity, row.ScopeType, row.ProjectKey, row.Disputed, row.UpdatedAt.UnixNano())
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
