package archive

import (
	"strings"
	"testing"
)

func TestExperienceSourceVocabularyAndGenericAssignment(t *testing.T) {
	source := GenericCheckpointSource("session-1")
	if source.Type != ExperienceSourceGenericCheckpoint || source.Name != GenericCheckpointSourceName || source.SessionID != "session-1" {
		t.Fatalf("unexpected generic source: %+v", source)
	}
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []ExperienceSource{
		{Type: "DEEPSEEK_ONLY", Name: "deepseek-harness", SessionID: "s"},
		{Type: ExperienceSourceHarnessNativeLog, Name: "DeepSeek Harness", SessionID: "s"},
		{Type: ExperienceSourceImport, Name: "historical-import", SessionID: ""},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid source: %+v", invalid)
		}
	}
}

func TestSourceEventRequiresCanonicalCompleteIdentity(t *testing.T) {
	if err := (SourceEvent{}).Validate(); err != nil {
		t.Fatal("generic checkpoint empty provenance", err)
	}
	sequence := int64(137)
	valid := SourceEvent{ID: "evt-abc", Sequence: &sequence, Hash: "sha256:" + strings.Repeat("1", 64)}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []SourceEvent{
		{ID: "evt-abc"},
		{ID: "evt-abc", Hash: "xyz"},
		{Sequence: &sequence, Hash: "sha256:" + strings.Repeat("1", 64)},
		{ID: "evt-abc", Sequence: pointer(-1), Hash: "sha256:" + strings.Repeat("1", 64)},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid source event: %+v", invalid)
		}
	}
}

func pointer(value int64) *int64 { return &value }
