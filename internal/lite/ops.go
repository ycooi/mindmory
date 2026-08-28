// Package lite: the operational journal — Mindmory's nerves.
//
// ops.jsonl is an append-only, structured record of everything Mindmory
// does over time: checkpoints, mutations (applied/staged with reasons),
// retrievals (search/recall/reflex/diff/feedback), index rebuilds,
// embedding backfills, admin actions, errors, and lifecycle events.
// It is canonical data like the other JSONL files — it lives in the data
// dir, rides the nightly backup, and can be read back at any time to see
// what Mindmory has done. One JSON object per line.
package lite

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// OpsEvent is one structured operation record.
type OpsEvent struct {
	Time       string         `json:"time"`
	Event      string         `json:"event"`
	Principal  string         `json:"principal,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
	Outcome    string         `json:"outcome,omitempty"` // ok | staged | error | replay
	Reason     string         `json:"reason,omitempty"`
	ResourceID string         `json:"resource_id,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// OpsLog appends structured events to ops.jsonl. Append + fsync per event:
// the daemon is not write-intensive, so correctness beats batching here.
type OpsLog struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	events int64 // events written since open (for the read path)
	// ring holds the most recent in-memory events (searches and other
	// high-frequency diagnostics) that are NOT persisted per event. They
	// still appear in Recent() so the agent sees a complete picture, but
	// they never hit the disk, so a busy session does not fsync per search.
	ring   []OpsEvent
	ringAt int // next write slot
	ringN  int // number of live entries
}

// opsRingCapacity bounds the in-memory recent-event ring. It covers the
// typical "what happened lately" window without unbounded memory growth.
const opsRingCapacity = 512

// opsMaxFileBytes caps the persisted journal. When the file exceeds this
// on a write, it is truncated and restarted (the in-memory ring keeps the
// recent window intact either way). SEARCH events no longer persist, so
// reaching this takes a very long time in normal use; the cap is a
// safety valve against unbounded growth, not a routine event.
const opsMaxFileBytes = 8 << 20

// OpenOps opens (creating if needed) the ops journal inside dir.
func OpenOps(dir string) (*OpsLog, error) {
	path := fmt.Sprintf("%s/ops.jsonl", dir)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open ops journal: %w", err)
	}
	return &OpsLog{file: file, path: path}, nil
}

// Record appends one structured event. High-frequency diagnostic events
// (searches) are kept only in the in-memory ring — they would dominate the
// journal and cost an fsync each, for near-zero diagnostic value beyond the
// recent window. Everything else is persisted with fsync as before.
func (o *OpsLog) Record(event OpsEvent) {
	if o == nil || o.file == nil {
		return
	}
	if event.Time == "" {
		event.Time = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if event.Event == "SEARCH" {
		o.pushRing(event)
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	if info, statErr := o.file.Stat(); statErr == nil && info.Size() > opsMaxFileBytes {
		// Safety valve: rotate the journal by truncating it. The current
		// event is still recorded; history before the cap is dropped, which
		// is acceptable for a diagnostic journal.
		_ = o.file.Truncate(0)
		_, _ = o.file.Seek(0, 0)
	}
	_, _ = o.file.Write(append(line, '\n'))
	_ = o.file.Sync()
	o.events++
}

// pushRing adds an event to the in-memory ring, evicting the oldest when
// full. Caller holds o.mu.
func (o *OpsLog) pushRing(event OpsEvent) {
	if len(o.ring) != opsRingCapacity {
		o.ring = make([]OpsEvent, opsRingCapacity)
	}
	o.ring[o.ringAt] = event
	o.ringAt = (o.ringAt + 1) % opsRingCapacity
	if o.ringN < opsRingCapacity {
		o.ringN++
	}
}

// Recent returns the last n events, oldest first. It reads the file fresh
// so an external look at the journal reflects everything written so far.
func (o *OpsLog) Recent(n int) ([]OpsEvent, error) {
	if o == nil {
		return nil, nil
	}
	o.mu.Lock()
	path := o.path
	var ring []OpsEvent
	if o.ringN > 0 {
		start := o.ringAt - o.ringN
		if start < 0 {
			start += opsRingCapacity
		}
		ring = make([]OpsEvent, 0, o.ringN)
		for i := 0; i < o.ringN; i++ {
			ring = append(ring, o.ring[(start+i)%opsRingCapacity])
		}
	}
	o.mu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var all []OpsEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event OpsEvent
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		all = append(all, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Merge persisted history with the in-memory ring (newest first in
	// time), then take the most recent n.
	all = append(all, ring...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time < all[j].Time })
	if n <= 0 || n > len(all) {
		n = len(all)
	}
	return all[len(all)-n:], nil
}

// Close closes the journal.
func (o *OpsLog) Close() error {
	if o == nil || o.file == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.file.Close()
}
