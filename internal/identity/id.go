// Package identity centralizes authoritative internal ID generation.
package identity

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"
)

// Generator creates server-owned authoritative identifiers.
type Generator interface {
	New() string
}

// UUIDv7Generator emits RFC 9562 UUIDv7 identifiers. A mutex preserves
// monotonic millisecond/random generation within this process.
type UUIDv7Generator struct {
	mu sync.Mutex
}

func (g *UUIDv7Generator) New() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var value [16]byte
	millis := uint64(time.Now().UTC().UnixMilli())
	value[0] = byte(millis >> 40)
	value[1] = byte(millis >> 32)
	value[2] = byte(millis >> 24)
	value[3] = byte(millis >> 16)
	value[4] = byte(millis >> 8)
	value[5] = byte(millis)
	if _, err := rand.Read(value[6:]); err != nil {
		panic("secure UUID randomness unavailable")
	}
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return formatUUID(value)
}

func formatUUID(value [16]byte) string {
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded)
}

// DeterministicGenerator is intended for tests and fixtures.
type DeterministicGenerator struct {
	mu   sync.Mutex
	next uint64
}

func (g *DeterministicGenerator) New() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	var value [16]byte
	binary.BigEndian.PutUint64(value[8:], g.next)
	value[6] = 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return formatUUID(value)
}
