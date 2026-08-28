package lite

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type IntegrityReport struct {
	MutationEvents  int    `json:"mutation_events"`
	MessageRecords  int    `json:"message_records"`
	MessageSegments int    `json:"message_segments"`
	KeyID           string `json:"key_id"`
}

// VerifyDataDir performs a read-only verification of both authoritative hash
// chains. It does not repair, truncate, rebuild, or otherwise mutate the data
// directory, making it safe for operator and backup checks.
func VerifyDataDir(dir string, key []byte) (IntegrityReport, error) {
	if len(key) < 32 {
		return IntegrityReport{}, fmt.Errorf("integrity key must be at least 32 bytes")
	}
	report := IntegrityReport{KeyID: integrityKeyID(key)}
	rotationState := &Store{dir: dir, integrityKey: append([]byte(nil), key...)}
	if err := rotationState.loadKeyRotations(); err != nil {
		return report, err
	}
	eventRaw, err := os.ReadFile(filepath.Join(dir, "memory_events.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	if len(eventRaw) > 0 && eventRaw[len(eventRaw)-1] != '\n' {
		return report, fmt.Errorf("memory_events.jsonl: incomplete final record")
	}
	var previousSeq int64
	var previousHash string
	anchorReached := rotationState.integrityAnchorHash == ""
	for _, line := range bytes.Split(eventRaw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event MemoryMutationEvent
		if json.Unmarshal(line, &event) != nil || (event.EventVersion != 2 && event.EventVersion != 3) || event.EventSeq != previousSeq+1 ||
			event.PreviousEventHash != previousHash || mutationEventHash(event) != event.EventHash {
			return report, fmt.Errorf("memory_events.jsonl: verification failed at sequence %d", previousSeq+1)
		}
		if !anchorReached {
			if event.EventHash == rotationState.integrityAnchorHash {
				anchorReached = true
			}
		} else if event.KeyID != integrityKeyID(key) ||
			!hmac.Equal([]byte(mutationEventHMAC(key, event.EventHash)), []byte(event.HMAC)) {
			return report, fmt.Errorf("memory_events.jsonl: signature failed at sequence %d", event.EventSeq)
		}
		previousSeq, previousHash = event.EventSeq, event.EventHash
		report.MutationEvents++
	}
	if !anchorReached {
		return report, fmt.Errorf("memory_events.jsonl: key rotation anchor not found")
	}

	paths, err := filepath.Glob(filepath.Join(dir, "messages", "messages-*.jsonl"))
	if err != nil {
		return report, err
	}
	sort.Strings(paths)
	var messageSeq int64
	var messageHash string
	for i, path := range paths {
		expected := fmt.Sprintf("messages-%06d.jsonl", i+1)
		if filepath.Base(path) != expected {
			return report, fmt.Errorf("message segment gap: expected %s", expected)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return report, err
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			return report, fmt.Errorf("%s: incomplete final record", expected)
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var row MessageRow
			if json.Unmarshal(line, &row) != nil || (row.SchemaVersion != 0 && row.SchemaVersion != 1) ||
				row.MessageSeq != messageSeq+1 || row.PreviousRecordHash != messageHash ||
				row.ExactContentHash != hashContent(row.Content) || messageRecordHash(row) != row.RecordHash {
				return report, fmt.Errorf("%s: verification failed at message sequence %d", expected, messageSeq+1)
			}
			messageSeq, messageHash = row.MessageSeq, row.RecordHash
			report.MessageRecords++
		}
		report.MessageSegments++
	}
	return report, nil
}
