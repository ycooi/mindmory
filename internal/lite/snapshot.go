package lite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type SnapshotFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type SnapshotManifest struct {
	Version    int            `json:"version"`
	SnapshotID string         `json:"snapshot_id"`
	CreatedAt  time.Time      `json:"created_at"`
	Files      []SnapshotFile `json:"files"`
}

type SnapshotResult struct {
	SnapshotID string           `json:"snapshot_id"`
	Path       string           `json:"path"`
	Manifest   SnapshotManifest `json:"manifest"`
}

var canonicalSnapshotFiles = []string{
	"schema.jsonl",
	"meta.jsonl",
	"sessions.jsonl",
	"messages.jsonl",
	"memories.jsonl",
	"proposals.jsonl",
	"evidence.jsonl",
	"continuity.jsonl",
	"project_context.jsonl",
	"memory_events.jsonl",
	"key_rotations.jsonl",
}

// CreateSnapshot freezes canonical files under the mutation lock and emits a
// manifest. Derived SQLite/vector state is intentionally excluded.
func (s *Store) CreateSnapshot() (SnapshotResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushAccessBumpsLocked(); err != nil {
		return SnapshotResult{}, err
	}
	if err := s.flushAllLocked(); err != nil {
		return SnapshotResult{}, err
	}

	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z") + "-" + newID()
	root := s.snapshotDir
	if root == "" {
		root = filepath.Join(s.dir, "snapshots")
	}
	path := filepath.Join(root, id)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return SnapshotResult{}, err
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		return SnapshotResult{}, err
	}
	manifest := SnapshotManifest{Version: 1, SnapshotID: id, CreatedAt: now}
	for _, name := range canonicalSnapshotFiles {
		source := filepath.Join(s.dir, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return SnapshotResult{}, err
		}
		entry, err := copySnapshotFile(source, filepath.Join(path, name), name)
		if err != nil {
			return SnapshotResult{}, err
		}
		manifest.Files = append(manifest.Files, entry)
	}
	messageSegments, err := s.messageSegmentPaths()
	if err != nil {
		return SnapshotResult{}, err
	}
	for _, source := range messageSegments {
		name := filepath.Join("messages", filepath.Base(source))
		entry, err := copySnapshotFile(source, filepath.Join(path, name), name)
		if err != nil {
			return SnapshotResult{}, err
		}
		manifest.Files = append(manifest.Files, entry)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return SnapshotResult{}, err
	}
	raw = append(raw, '\n')
	manifestPath := filepath.Join(path, "manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0o640); err != nil {
		return SnapshotResult{}, err
	}
	if err := syncFile(manifestPath); err != nil {
		return SnapshotResult{}, err
	}
	if err := syncDir(path); err != nil {
		return SnapshotResult{}, err
	}
	if err := syncDir(root); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{SnapshotID: id, Path: path, Manifest: manifest}, nil
}

func copySnapshotFile(sourcePath, destinationPath, manifestPath string) (SnapshotFile, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return SnapshotFile{}, err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o750); err != nil {
		return SnapshotFile{}, err
	}
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return SnapshotFile{}, err
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(destination, hasher), source)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil {
		return SnapshotFile{}, copyErr
	}
	if syncErr != nil {
		return SnapshotFile{}, syncErr
	}
	if closeErr != nil {
		return SnapshotFile{}, closeErr
	}
	if size < 0 {
		return SnapshotFile{}, fmt.Errorf("invalid snapshot file size")
	}
	return SnapshotFile{
		Path: manifestPath, Size: size, SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}
