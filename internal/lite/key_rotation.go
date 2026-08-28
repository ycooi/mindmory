package lite

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type IntegrityKeyRotation struct {
	Version              int       `json:"version"`
	Sequence             int64     `json:"sequence"`
	RotationID           string    `json:"rotation_id"`
	PreviousRotationHash string    `json:"previous_rotation_hash,omitempty"`
	EventHeadHash        string    `json:"event_head_hash,omitempty"`
	PreviousKeyID        string    `json:"previous_key_id"`
	NewKeyID             string    `json:"new_key_id"`
	RotationHash         string    `json:"rotation_hash"`
	PreviousKeyHMAC      string    `json:"previous_key_hmac"`
	NewKeyHMAC           string    `json:"new_key_hmac"`
	CreatedAt            time.Time `json:"created_at"`
}

// RotateIntegrityKey appends a bridge signed by both keys. The new key can
// subsequently verify the hash-chained history through EventHeadHash without
// retaining the retired key, while all later events must carry the new HMAC.
func (s *Store) RotateIntegrityKey(newKey []byte) (IntegrityKeyRotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.integrityKey) < 32 || len(newKey) < 32 {
		return IntegrityKeyRotation{}, fmt.Errorf("both integrity keys must be at least 32 bytes")
	}
	if hmac.Equal(s.integrityKey, newKey) {
		return IntegrityKeyRotation{}, fmt.Errorf("new integrity key must differ")
	}
	record := IntegrityKeyRotation{Version: 1, Sequence: s.keyRotationSeq + 1, RotationID: newID(),
		PreviousRotationHash: s.lastRotationHash, EventHeadHash: s.lastEventHash,
		PreviousKeyID: integrityKeyID(s.integrityKey), NewKeyID: integrityKeyID(newKey), CreatedAt: time.Now().UTC()}
	record.RotationHash = keyRotationHash(record)
	record.PreviousKeyHMAC = keyRotationHMAC(s.integrityKey, record.RotationHash)
	record.NewKeyHMAC = keyRotationHMAC(newKey, record.RotationHash)
	line, err := json.Marshal(record)
	if err != nil {
		return IntegrityKeyRotation{}, err
	}
	line = append(line, '\n')
	path := s.path("key_rotations")
	if err := ensureAppendFile(path, s.dir, 0o600); err != nil {
		return IntegrityKeyRotation{}, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return IntegrityKeyRotation{}, err
	}
	if _, err = file.Write(line); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return IntegrityKeyRotation{}, err
	}
	_ = closeErr // file.Sync above is the authoritative commit point.
	s.integrityKey = append([]byte(nil), newKey...)
	s.integrityAnchorHash = record.EventHeadHash
	s.keyRotationSeq = record.Sequence
	s.lastRotationHash = record.RotationHash
	return record, nil
}

func (s *Store) loadKeyRotations() error {
	raw, err := os.ReadFile(s.path("key_rotations"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return fmt.Errorf("key_rotations.jsonl: incomplete final record")
	}
	var previousHash string
	var last IntegrityKeyRotation
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record IntegrityKeyRotation
		if json.Unmarshal(line, &record) != nil || record.Version != 1 || record.Sequence != s.keyRotationSeq+1 ||
			record.PreviousRotationHash != previousHash || keyRotationHash(record) != record.RotationHash {
			return fmt.Errorf("key_rotations.jsonl: invalid rotation at sequence %d", s.keyRotationSeq+1)
		}
		s.keyRotationSeq, previousHash, last = record.Sequence, record.RotationHash, record
	}
	s.lastRotationHash = previousHash
	if s.keyRotationSeq == 0 {
		return nil
	}
	if len(s.integrityKey) < 32 || last.NewKeyID != integrityKeyID(s.integrityKey) ||
		!hmac.Equal([]byte(last.NewKeyHMAC), []byte(keyRotationHMAC(s.integrityKey, last.RotationHash))) {
		return fmt.Errorf("key_rotations.jsonl: current key does not verify latest rotation")
	}
	s.integrityAnchorHash = last.EventHeadHash
	return nil
}

func keyRotationHash(record IntegrityKeyRotation) string {
	record.RotationHash = ""
	record.PreviousKeyHMAC = ""
	record.NewKeyHMAC = ""
	raw, _ := json.Marshal(record)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func keyRotationHMAC(key []byte, hash string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(hash))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}
