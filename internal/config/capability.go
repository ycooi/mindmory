package config

import "errors"

// MCPClientCapability is a model-facing operation and can never authorize raw ingestion.
type MCPClientCapability string

const (
	MCPContextRead       MCPClientCapability = "CONTEXT_READ"
	MCPArchiveCheckpoint MCPClientCapability = "ARCHIVE_CHECKPOINT"
	MCPMemoryPropose     MCPClientCapability = "MEMORY_PROPOSE"
	MCPArtifactSearch    MCPClientCapability = "ARTIFACT_SEARCH"
	MCPArtifactRead      MCPClientCapability = "ARTIFACT_READ"
	MCPResourceRead      MCPClientCapability = "RESOURCE_READ"
	MCPOpsRead           MCPClientCapability = "OPS_READ"
)

// Validate rejects operations outside the frozen model-facing surface.
func (c MCPClientCapability) Validate() error {
	switch c {
	case MCPContextRead, MCPArchiveCheckpoint, MCPMemoryPropose, MCPArtifactSearch, MCPArtifactRead, MCPResourceRead, MCPOpsRead:
		return nil
	default:
		return errors.New("invalid MCP client capability")
	}
}

// IngestionCapability belongs to a separate host credential domain.
type IngestionCapability string

const (
	IngestionHostAttachment    IngestionCapability = "HOST_ATTACHMENT_UPLOAD"
	IngestionGeneratedArtifact IngestionCapability = "GENERATED_ARTIFACT_UPLOAD"
)

// Validate rejects operations outside the non-model ingestion surface.
func (c IngestionCapability) Validate() error {
	switch c {
	case IngestionHostAttachment, IngestionGeneratedArtifact:
		return nil
	default:
		return errors.New("invalid ingestion capability")
	}
}

// MCPToken and IngestionToken prevent accidental credential interchange in configuration code.
type MCPToken string
type IngestionToken string

// MCPPrincipalConfig can contain only model-facing capabilities.
type MCPPrincipalConfig struct {
	Token        MCPToken              `json:"token"`
	Capabilities []MCPClientCapability `json:"capabilities"`
}

// IngestionPrincipalConfig can contain only non-model upload capabilities.
type IngestionPrincipalConfig struct {
	Token        IngestionToken        `json:"token"`
	Capabilities []IngestionCapability `json:"capabilities"`
}
