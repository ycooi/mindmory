package ingest

import (
	"strings"
	"testing"

	"mindmory.local/core/internal/config"
)

func TestUploadRejectsAuthorityFields(t *testing.T) {
	valid := `{"logical_key":"reports/a","title":"A","original_filename":"a.pdf","declared_media_type":"application/pdf"}`
	if _, err := DecodeUploadInput(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"trust_class", "origin_class", "approval_state", "epistemic_status"} {
		payload := strings.TrimSuffix(valid, "}") + `,"` + field + `":"USER_APPROVED"}`
		if _, err := DecodeUploadInput(strings.NewReader(payload)); err == nil {
			t.Fatalf("authority field %s accepted", field)
		}
	}
}

func TestIngestionCapabilityAssignsServerChannel(t *testing.T) {
	tests := map[config.IngestionCapability]Channel{
		config.IngestionHostAttachment:    ChannelHostUserAttachment,
		config.IngestionGeneratedArtifact: ChannelGeneratedArtifact,
	}
	for capability, expected := range tests {
		channel, err := ChannelForCapability(capability)
		if err != nil || channel != expected {
			t.Fatalf("capability=%s channel=%s err=%v", capability, channel, err)
		}
	}
	if _, err := ChannelForCapability(config.IngestionCapability("CONTEXT_READ")); err == nil {
		t.Fatal("MCP-like capability assigned an ingestion channel")
	}
}
