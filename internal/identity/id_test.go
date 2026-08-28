package identity

import (
	"regexp"
	"testing"
)

func TestUUIDv7AndDeterministicGenerators(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	generated := (&UUIDv7Generator{}).New()
	if !pattern.MatchString(generated) {
		t.Fatalf("invalid UUIDv7 %q", generated)
	}
	deterministic := &DeterministicGenerator{}
	first, second := deterministic.New(), deterministic.New()
	if first == second || !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatal("deterministic generator did not produce stable distinct UUID-shaped IDs")
	}
}
