package lite

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStorageMemoryProbe is an opt-in reproducible storage experiment. It is
// excluded from normal tests unless MINDMORY_MEMORY_PROBE_DIR is set.
func TestStorageMemoryProbe(t *testing.T) {
	root := os.Getenv("MINDMORY_MEMORY_PROBE_DIR")
	if root == "" {
		t.Skip("memory probe disabled")
	}
	dataDir := filepath.Join(root, "data")
	if os.Getenv("MINDMORY_MEMORY_PROBE_GENERATE") == "1" {
		if err := os.MkdirAll(dataDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "schema.jsonl"), []byte(fmt.Sprintf("{\"version\":%d}\n", SchemaVersion)), 0o640); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(dataDir, "memories.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		writer := bufio.NewWriterSize(file, 1<<20)
		now := time.Now().UTC()
		for i := 0; i < 100_000; i++ {
			content := fmt.Sprintf("durable synthetic memory record %06d with enough payload to represent a realistic preference or project decision", i)
			row := MemoryRow{SchemaVersion: SchemaVersion, MemoryID: fmt.Sprintf("probe-%06d", i), Kind: "USER_PREFERENCE",
				Subject: fmt.Sprintf("synthetic memory %06d", i), Content: content, ContentHash: hashContent(content),
				Lifecycle: "ACTIVE", EpistemicStatus: "USER_ACCEPTED", Confidence: 1, Importance: .6,
				Sensitivity: "NORMAL", StateVersion: 1, ScopeType: "GLOBAL", Activation: .5,
				CreatedAt: now, UpdatedAt: now}
			line, _ := json.Marshal(row)
			if _, err := writer.Write(append(line, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MINDMORY_MEMORY_PROBE_LOW_RAM") == "1" {
		if err := store.EnableLowRAMExperiment(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	fmt.Printf("MEMORY_PROBE low_ram=%v heap_alloc=%d heap_inuse=%d heap_objects=%d\n",
		store.LowRAMEnabled(), stats.HeapAlloc, stats.HeapInuse, stats.HeapObjects)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
