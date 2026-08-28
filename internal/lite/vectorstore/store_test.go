package vectorstore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistentAppendReopenSearch(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, GenerationSpec{ModelName: "fixture", ModelDigest: "sha256:model", Dimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "sha256:a", []float32{3, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "b", "sha256:b", []float32{0, 2, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "sha256:a", []float32{0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if store.Size() != 2 {
		t.Fatalf("idempotent append size=%d", store.Size())
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hits, err := store.Search(context.Background(), []float32{1, 0, 0}, SearchOptions{Limit: 1, MinimumScore: .5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SourceID != "a" || hits[0].Score < .999 {
		t.Fatalf("hits=%+v", hits)
	}
	if err = store.Verify(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestBuildingGenerationDoesNotReplaceCurrentUntilActivation(t *testing.T) {
	root := t.TempDir()
	current, err := Create(root, GenerationSpec{ModelName: "model-alpha", ModelDigest: "revision-1", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if _, err := current.Append("MEMORY", "old", "hash-old", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	building, err := CreateBuilding(root, GenerationSpec{ModelName: "model-beta", ModelDigest: "revision-2", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer building.Close()
	if _, err := building.Append("MEMORY", "new", "hash-new", []float32{0, 1}); err != nil {
		t.Fatal(err)
	}
	stillCurrent, err := OpenCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if stillCurrent.Manifest().ModelName != "model-alpha" {
		t.Fatalf("building generation became current: %+v", stillCurrent.Manifest())
	}
	_ = stillCurrent.Close()
	if err := building.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated, err := OpenCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	defer activated.Close()
	if activated.Manifest().ModelName != "model-beta" || activated.Manifest().State != "READY" {
		t.Fatalf("activated=%+v", activated.Manifest())
	}
}

func TestRecoveryTruncatesUncommittedTails(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "h", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	dir := store.dir
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	vectorPath := filepath.Join(dir, "vectors.bin")
	f, err := os.OpenFile(vectorPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{1, 2, 3})
	_ = f.Close()
	mapPath := filepath.Join(dir, "vector-map.jsonl")
	f, err = os.OpenFile(mapPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"generation":"bad","ordinal":1}` + "\n")
	_ = f.Close()
	store, err = OpenCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, _ := os.Stat(vectorPath)
	if want := int64(HeaderSize + 8); info.Size() != want {
		t.Fatalf("vector size=%d want=%d", info.Size(), want)
	}
	if len(store.Refs()) != 1 {
		t.Fatalf("refs=%d", len(store.Refs()))
	}
}

func TestShortCommittedFilesAreCorrupt(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "h", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	dir := store.dir
	_ = store.Close()
	f, err := os.OpenFile(filepath.Join(dir, "vectors.bin"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := make([]byte, 8)
	binary.LittleEndian.PutUint64(count, 2)
	_, _ = f.WriteAt(count, 16)
	_ = f.Close()
	_, err = OpenCurrent(root)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err=%v", err)
	}
}

func TestShortCommittedMapAndChecksumAreCorrupt(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "h", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	dir := store.dir
	_ = store.Close()
	if err = os.WriteFile(filepath.Join(dir, "vector-map.jsonl"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenCurrent(root); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short map err=%v", err)
	}

	root = t.TempDir()
	store, err = Create(root, GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append("MEMORY", "a", "h", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	dir = store.dir
	_ = store.Close()
	f, err := os.OpenFile(filepath.Join(dir, "vectors.bin"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte{0xff}, HeaderSize); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	store, err = OpenCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Verify(context.Background(), true); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("checksum err=%v", err)
	}
}

func TestSearchCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _ = store.Append("MEMORY", "a", "h", []float32{1, 0})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Search(ctx, []float32{1, 0}, SearchOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestConcurrentSearchAndAppendRemap(t *testing.T) {
	store, err := Create(t.TempDir(), GenerationSpec{ModelName: "fixture", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Append("MEMORY", "seed", "h", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for i := 0; i < 100; i++ {
			if _, searchErr := store.Search(context.Background(), []float32{1, 0}, SearchOptions{Limit: 5}); searchErr != nil {
				t.Errorf("search: %v", searchErr)
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("m-%d", i)
			if _, appendErr := store.Append("MEMORY", id, "h", []float32{1, float32(i + 1)}); appendErr != nil {
				t.Errorf("append: %v", appendErr)
				return
			}
		}
	}()
	wait.Wait()
}
