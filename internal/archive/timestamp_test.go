package archive

import (
	"testing"
	"time"
)

func TestCanonicalTimestampUsesUTCMicroseconds(t *testing.T) {
	input := time.Date(2026, 8, 18, 12, 30, 40, 123456789, time.FixedZone("offset", 8*60*60))
	got := CanonicalTimestamp(input)
	if got.Location() != time.UTC || got.Nanosecond() != 123456000 {
		t.Fatalf("canonical timestamp=%s", got.Format(time.RFC3339Nano))
	}
	if formatted := FormatCanonicalTimestamp(input); formatted != "2026-08-18T04:30:40.123456Z" {
		t.Fatalf("canonical format=%s", formatted)
	}
}
