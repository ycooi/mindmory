package auth

import (
	"testing"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/config"
)

func TestCredentialDomainsAndCapabilities(t *testing.T) {
	mcp := NewMCPAuthenticator(map[string]config.MCPPrincipalConfig{"codex": {Token: config.MCPToken("mcp-012345678901234567890123"), Capabilities: []config.MCPClientCapability{config.MCPArchiveCheckpoint}}})
	ingest := NewIngestionAuthenticator(map[string]config.IngestionPrincipalConfig{"host": {Token: config.IngestionToken("ingest-012345678901234567890"), Capabilities: []config.IngestionCapability{config.IngestionHostAttachment}}})
	if _, err := mcp.Authenticate("mcp-012345678901234567890123", config.MCPArchiveCheckpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Authenticate("mcp-012345678901234567890123", config.IngestionHostAttachment); apperror.Code(err) != apperror.AuthRequired {
		t.Fatal("MCP token crossed into ingestion domain")
	}
	if _, err := ingest.Authenticate("ingest-012345678901234567890", config.IngestionGeneratedArtifact); apperror.Code(err) != apperror.CapabilityDenied {
		t.Fatal("missing ingestion capability was granted")
	}
}

func TestBearerParsing(t *testing.T) {
	if token, err := Bearer("Bearer opaque"); err != nil || token != "opaque" {
		t.Fatal("valid bearer rejected")
	}
	if _, err := Bearer("opaque"); err == nil {
		t.Fatal("malformed authorization accepted")
	}
}
