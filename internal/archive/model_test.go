package archive

import (
	"testing"

	"mindmory.local/core/internal/artifact/policy"
)

func TestEpisodeSensitivityInheritsIncludedMessages(t *testing.T) {
	messages := []MessageEvidence{
		{MessageID: "normal", Sensitivity: policy.SensitivityNormal},
		{MessageID: "secret", Sensitivity: policy.SensitivitySecret},
	}
	if got := EpisodeSensitivity(messages, map[string]bool{"normal": true}); got != policy.SensitivityNormal {
		t.Fatalf("normal episode became %s", got)
	}
	if got := EpisodeSensitivity(messages, map[string]bool{"normal": true, "secret": true}); got != policy.SensitivitySecret {
		t.Fatalf("sensitive source was downgraded to %s", got)
	}
}
