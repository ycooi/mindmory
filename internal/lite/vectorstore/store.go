package vectorstore

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Manifest struct {
	FormatVersion         int    `json:"format_version"`
	Generation            string `json:"generation"`
	State                 string `json:"state"`
	ModelName             string `json:"model_name"`
	ModelDigest           string `json:"model_digest"`
	Dimensions            int    `json:"dimensions"`
	DType                 string `json:"dtype"`
	Normalized            bool   `json:"normalized"`
	NormalizationVersion  int    `json:"normalization_version"`
	EmbeddingInputVersion int    `json:"embedding_input_version"`
	CommittedVectors      uint64 `json:"committed_vectors"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

type Store struct {
	mu       sync.RWMutex
	dir      string
	header   Header
	manifest Manifest
	refs     []Ref
	bySource map[string]uint64
	file     *os.File
	mapped   *mappedFile
	closed   bool
}

func Create(root string, spec GenerationSpec) (*Store, error) {
	return createGeneration(root, prepareSpec(spec), true)
}

// CreateBuilding creates a detached generation. It does not modify CURRENT;
// callers must fully populate, verify, and Activate it before queries use it.
func CreateBuilding(root string, spec GenerationSpec) (*Store, error) {
	spec = prepareSpec(spec)
	if spec.Name == generationName(spec) {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		spec.Name += "-building-" + hex.EncodeToString(nonce[:])
	}
	return createGeneration(root, spec, false)
}

func prepareSpec(spec GenerationSpec) GenerationSpec {
	if spec.FormatVersion == 0 {
		spec.FormatVersion = FormatVersion
	}
	if spec.DType == 0 {
		spec.DType = DTypeFloat32LE
	}
	if spec.InputVersion == 0 {
		spec.InputVersion = InputVersion
	}
	if spec.NormalizationVersion == 0 {
		spec.NormalizationVersion = NormalizationVersion
	}
	if spec.Name == "" {
		spec.Name = generationName(spec)
	}
	return spec
}

func createGeneration(root string, spec GenerationSpec, activate bool) (*Store, error) {
	if spec.FormatVersion != FormatVersion || spec.DType != DTypeFloat32LE {
		return nil, fmt.Errorf("unsupported generation spec")
	}
	header, err := NewHeader(spec.Dimensions)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, spec.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "vectors.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if errors.Is(err, os.ErrExist) {
		existing, openErr := Open(root, spec.Name)
		if openErr != nil {
			return nil, openErr
		}
		if activate {
			if existing.Manifest().State != "READY" {
				_ = existing.Close()
				return nil, fmt.Errorf("existing generation is not READY")
			}
			if switchErr := writeAtomic(filepath.Join(root, "CURRENT"), []byte(spec.Name+"\n")); switchErr != nil {
				_ = existing.Close()
				return nil, switchErr
			}
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	b, _ := header.Encode()
	if _, err = file.Write(b); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := "BUILDING"
	if activate {
		state = "READY"
	}
	s := &Store{dir: dir, header: header, file: file, refs: []Ref{}, bySource: map[string]uint64{},
		manifest: Manifest{FormatVersion: FormatVersion, Generation: spec.Name, State: state, ModelName: spec.ModelName,
			ModelDigest: spec.ModelDigest, Dimensions: spec.Dimensions, DType: "FLOAT32_LE", Normalized: true,
			NormalizationVersion: spec.NormalizationVersion, EmbeddingInputVersion: spec.InputVersion, CreatedAt: now, UpdatedAt: now}}
	if err := s.writeManifestLocked(); err != nil {
		file.Close()
		return nil, err
	}
	if activate {
		if err := writeAtomic(filepath.Join(root, "CURRENT"), []byte(spec.Name+"\n")); err != nil {
			file.Close()
			return nil, err
		}
	}
	return s, nil
}

// Activate verifies and atomically selects a completed detached generation.
func (s *Store) Activate(ctx context.Context) error {
	if err := s.Verify(ctx, true); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.manifest.State != "BUILDING" && s.manifest.State != "READY" {
		return fmt.Errorf("generation is not activatable")
	}
	if err := s.file.Sync(); err != nil {
		return err
	}
	s.manifest.State = "READY"
	s.manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.writeManifestLocked(); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(filepath.Dir(s.dir), "CURRENT"), []byte(s.manifest.Generation+"\n"))
}

func OpenCurrent(root string) (*Store, error) {
	b, err := os.ReadFile(filepath.Join(root, "CURRENT"))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(string(b))
	if name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid CURRENT")
	}
	store, err := Open(root, name)
	if err != nil {
		return nil, err
	}
	if store.Manifest().State != "READY" {
		_ = store.Close()
		return nil, fmt.Errorf("%w: CURRENT generation is not READY", ErrCorrupt)
	}
	return store, nil
}

func Open(root, name string) (*Store, error) {
	dir := filepath.Join(root, name)
	file, err := os.OpenFile(filepath.Join(dir, "vectors.bin"), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, file: file, bySource: map[string]uint64{}}
	if err := s.recoverLocked(); err != nil {
		file.Close()
		return nil, err
	}
	if err := s.remapLocked(); err != nil {
		file.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) recoverLocked() error {
	b := make([]byte, HeaderSize)
	if _, err := io.ReadFull(io.NewSectionReader(s.file, 0, HeaderSize), b); err != nil {
		return fmt.Errorf("%w: header: %v", ErrCorrupt, err)
	}
	h, err := DecodeHeader(b)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	s.header = h
	manifestBytes, err := os.ReadFile(filepath.Join(s.dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	if err := json.Unmarshal(manifestBytes, &s.manifest); err != nil || s.manifest.Dimensions != int(h.Dimensions) {
		return fmt.Errorf("%w: inconsistent manifest", ErrCorrupt)
	}
	mapPath := filepath.Join(s.dir, "vector-map.jsonl")
	mapFile, err := os.OpenFile(mapPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return err
	}
	defer mapFile.Close()
	scanner := bufio.NewScanner(mapFile)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	all := make([]Ref, 0, h.CommittedCount)
	malformedTail := false
	for scanner.Scan() {
		var ref Ref
		if json.Unmarshal(scanner.Bytes(), &ref) != nil {
			malformedTail = true
			break
		}
		all = append(all, ref)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if uint64(len(all)) < h.CommittedCount {
		return fmt.Errorf("%w: mapping shorter than commit", ErrCorrupt)
	}
	seen := map[string]bool{}
	for i := uint64(0); i < h.CommittedCount; i++ {
		ref := all[i]
		if ref.Ordinal != i || ref.Generation != s.manifest.Generation || ref.SourceID == "" || seen[ref.SourceID+"\x00"+ref.EmbeddingInputHash] {
			return fmt.Errorf("%w: invalid mapping at %d", ErrCorrupt, i)
		}
		seen[ref.SourceID+"\x00"+ref.EmbeddingInputHash] = true
		s.bySource[ref.SourceID+"\x00"+ref.EmbeddingInputHash] = i
	}
	s.refs = append([]Ref(nil), all[:h.CommittedCount]...)
	expected := int64(HeaderSize) + int64(h.CommittedCount*h.RecordSize)
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < expected {
		return fmt.Errorf("%w: vector file shorter than commit", ErrCorrupt)
	}
	if info.Size() > expected {
		if err := s.file.Truncate(expected); err != nil {
			return err
		}
		if err := s.file.Sync(); err != nil {
			return err
		}
	}
	if uint64(len(all)) > h.CommittedCount || malformedTail {
		if err := rewriteRefs(mapPath, s.refs); err != nil {
			return err
		}
	}
	if s.manifest.CommittedVectors != h.CommittedCount {
		s.manifest.CommittedVectors = h.CommittedCount
		s.manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.writeManifestLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Append(sourceType, sourceID, inputHash string, vector []float32) (Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Ref{}, os.ErrClosed
	}
	key := sourceID + "\x00" + inputHash
	if ordinal, ok := s.bySource[key]; ok {
		return s.refs[ordinal], nil
	}
	record, err := normalizeEncode(vector, int(s.header.Dimensions))
	if err != nil {
		return Ref{}, err
	}
	sum := sha256.Sum256(record)
	ordinal := s.header.CommittedCount
	offset, err := VectorOffset(s.header, ordinal)
	if err != nil {
		return Ref{}, err
	}
	if _, err = s.file.WriteAt(record, offset); err != nil {
		return Ref{}, err
	}
	if err = s.file.Sync(); err != nil {
		return Ref{}, err
	}
	ref := Ref{Generation: s.manifest.Generation, Ordinal: ordinal, SourceType: sourceType, SourceID: sourceID,
		EmbeddingInputHash: inputHash, VectorChecksum: "sha256:" + hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	line, _ := json.Marshal(ref)
	mapFile, err := os.OpenFile(filepath.Join(s.dir, "vector-map.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return Ref{}, err
	}
	if _, err = mapFile.Write(append(line, '\n')); err == nil {
		err = mapFile.Sync()
	}
	closeErr := mapFile.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Ref{}, err
	}
	s.header.CommittedCount++
	count := make([]byte, 8)
	binary.LittleEndian.PutUint64(count, s.header.CommittedCount)
	if _, err = s.file.WriteAt(count, 16); err != nil {
		return Ref{}, err
	}
	if err = s.file.Sync(); err != nil {
		return Ref{}, err
	}
	s.refs = append(s.refs, ref)
	s.bySource[key] = ordinal
	s.manifest.CommittedVectors = s.header.CommittedCount
	s.manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = s.writeManifestLocked(); err != nil {
		return Ref{}, err
	}
	if err = s.remapLocked(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func (s *Store) Has(sourceID, inputHash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bySource[sourceID+"\x00"+inputHash]
	return ok
}
func (s *Store) Size() int          { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.refs) }
func (s *Store) Generation() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.manifest.Generation }
func (s *Store) Manifest() Manifest { s.mu.RLock(); defer s.mu.RUnlock(); return s.manifest }
func (s *Store) Refs() []Ref {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Ref(nil), s.refs...)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.mapped != nil {
		_ = s.mapped.close()
	}
	return s.file.Close()
}
func (s *Store) remapLocked() error {
	if s.mapped != nil {
		_ = s.mapped.close()
	}
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	s.mapped, err = mapReadOnly(s.file, int(info.Size()))
	return err
}
func (s *Store) writeManifestLocked() error {
	b, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "manifest.json"), append(b, '\n'))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o640); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func rewriteRefs(path string, refs []Ref) error {
	var b strings.Builder
	for _, ref := range refs {
		line, _ := json.Marshal(ref)
		b.Write(line)
		b.WriteByte('\n')
	}
	return writeAtomic(path, []byte(b.String()))
}
func generationName(spec GenerationSpec) string {
	sum := sha256.Sum256([]byte(spec.ModelName + "\x00" + spec.ModelDigest))
	return fmt.Sprintf("gen-%s-%d-f32-v1", hex.EncodeToString(sum[:6]), spec.Dimensions)
}

func (s *Store) Verify(ctx context.Context, full bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	indices := make([]int, 0, len(s.refs))
	if full {
		for i := range s.refs {
			indices = append(indices, i)
		}
	} else if len(s.refs) > 0 {
		indices = []int{0, len(s.refs) / 2, len(s.refs) - 1}
		sort.Ints(indices)
	}
	last := -1
	for _, i := range indices {
		if i == last {
			continue
		}
		last = i
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := s.readRecordLocked(uint64(i))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(record)
		if "sha256:"+hex.EncodeToString(sum[:]) != s.refs[i].VectorChecksum {
			return fmt.Errorf("%w: checksum ordinal %d", ErrCorrupt, i)
		}
	}
	return nil
}
