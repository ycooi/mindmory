package lite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const messageSegmentMaxBytes int64 = 256 << 20

func (s *Store) messageSegmentPaths() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, "messages", "messages-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Store) loadMessageSegments(paths []string) error {
	var previousSeq int64
	var previousHash string
	for fileIndex, path := range paths {
		expectedName := fmt.Sprintf("messages-%06d.jsonl", fileIndex+1)
		if filepath.Base(path) != expectedName {
			return fmt.Errorf("message segment gap: expected %s, found %s", expectedName, filepath.Base(path))
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			if fileIndex != len(paths)-1 {
				return fmt.Errorf("%s: incomplete interior segment", filepath.Base(path))
			}
			lastNewline := bytes.LastIndexByte(raw, '\n')
			completeEnd := lastNewline + 1
			tail := append([]byte(nil), raw[completeEnd:]...)
			if len(tail) > 0 {
				quarantine := path + ".quarantine-" + time.Now().UTC().Format("20060102T150405.000000000Z")
				if err := os.WriteFile(quarantine, tail, 0o600); err != nil {
					return err
				}
				if err := syncFile(quarantine); err != nil {
					return err
				}
				s.startupRecoveries = append(s.startupRecoveries, OpsEvent{Event: "ARCHIVE_RECOVERY", Outcome: "ok",
					Reason: "INCOMPLETE_FINAL_RECORD_QUARANTINED", ResourceID: filepath.Base(path),
					Details: map[string]any{"quarantine": filepath.Base(quarantine), "tail_bytes": len(tail)}})
			}
			if err := os.Truncate(path, int64(completeEnd)); err != nil {
				return err
			}
			if err := syncFile(path); err != nil {
				return err
			}
			raw = raw[:completeEnd]
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var row MessageRow
			if err := json.Unmarshal(line, &row); err != nil {
				return fmt.Errorf("%s: invalid interior record: %w", filepath.Base(path), err)
			}
			if row.SchemaVersion != 0 && row.SchemaVersion != 1 {
				return fmt.Errorf("%s: unsupported message schema %d", filepath.Base(path), row.SchemaVersion)
			}
			if row.MessageSeq != previousSeq+1 || row.PreviousRecordHash != previousHash ||
				row.ExactContentHash != hashContent(row.Content) || messageRecordHash(row) != row.RecordHash {
				return fmt.Errorf("%s: message hash chain invalid at sequence %d", filepath.Base(path), row.MessageSeq)
			}
			if existing, ok := s.messages[row.MessageID]; ok && existing.ExternalMessageID != row.ExternalMessageID {
				return fmt.Errorf("%s: conflicting message id", filepath.Base(path))
			}
			s.messages[row.MessageID] = row
			previousSeq = row.MessageSeq
			previousHash = row.RecordHash
			if row.TurnSeq > s.turnSeq {
				s.turnSeq = row.TurnSeq
			}
		}
		s.messageSegment = fileIndex + 1
	}
	s.messageSeq = previousSeq
	s.lastMessageHash = previousHash
	return nil
}

func (s *Store) initializeMessageJournal() error {
	if len(s.messages) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.messages))
	for id := range s.messages {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := s.messages[ids[i]], s.messages[ids[j]]
		if left.MessageSeq != right.MessageSeq {
			return left.MessageSeq < right.MessageSeq
		}
		return left.MessageID < right.MessageID
	})
	s.lastMessageHash = ""
	s.messageSegment = 0
	for _, id := range ids {
		legacy := s.messages[id]
		if legacy.SchemaVersion == 0 {
			legacy.SchemaVersion = 1
		}
		row, err := s.appendMessageRecordLocked(legacy)
		if err != nil {
			return err
		}
		s.messages[id] = row
	}
	return s.flushKindLocked("messages", s.messagesJSONL())
}

func (s *Store) appendMessageRecordLocked(row MessageRow) (MessageRow, error) {
	dir := filepath.Join(s.dir, "messages")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return MessageRow{}, err
	}
	if s.messageSegment <= 0 {
		s.messageSegment = 1
	}
	row.PreviousRecordHash = s.lastMessageHash
	row.RecordHash = messageRecordHash(row)
	line, err := json.Marshal(row)
	if err != nil {
		return MessageRow{}, err
	}
	line = append(line, '\n')
	path := filepath.Join(dir, fmt.Sprintf("messages-%06d.jsonl", s.messageSegment))
	if stat, err := os.Stat(path); err == nil && stat.Size()+int64(len(line)) > messageSegmentMaxBytes {
		s.messageSegment++
		path = filepath.Join(dir, fmt.Sprintf("messages-%06d.jsonl", s.messageSegment))
	} else if err != nil && !os.IsNotExist(err) {
		return MessageRow{}, err
	}
	if err := ensureAppendFile(path, dir, 0o640); err != nil {
		return MessageRow{}, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return MessageRow{}, err
	}
	if _, err = file.Write(line); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return MessageRow{}, err
	}
	_ = closeErr // file.Sync above is the authoritative commit point.
	s.lastMessageHash = row.RecordHash
	return row, nil
}

func messageRecordHash(row MessageRow) string {
	row.RecordHash = ""
	raw, _ := json.Marshal(row)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
