package ingest

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"mindmory.local/core/internal/config"
)

// Channel is an authenticated, server-configured origin channel.
type Channel string

const (
	ChannelHostUserAttachment Channel = "HOST_USER_ATTACHMENT"
	ChannelAdminAuthored      Channel = "ADMIN_AUTHORED"
	ChannelGeneratedArtifact  Channel = "GENERATED_ARTIFACT"
	ChannelExternalImport     Channel = "EXTERNAL_IMPORT"
	ChannelTrustedToolCapture Channel = "TRUSTED_TOOL_CAPTURE"
	ChannelUnclassified       Channel = "UNCLASSIFIED"
)

// Validate rejects ingestion channels that are not assigned by a known server route.
func (c Channel) Validate() error {
	switch c {
	case ChannelHostUserAttachment, ChannelAdminAuthored, ChannelGeneratedArtifact,
		ChannelExternalImport, ChannelTrustedToolCapture, ChannelUnclassified:
		return nil
	default:
		return errors.New("invalid ingestion channel")
	}
}

// ChannelForCapability maps an authenticated non-model ingestion principal to server-owned origin routing.
func ChannelForCapability(capability config.IngestionCapability) (Channel, error) {
	if err := capability.Validate(); err != nil {
		return "", err
	}
	switch capability {
	case config.IngestionHostAttachment:
		return ChannelHostUserAttachment, nil
	case config.IngestionGeneratedArtifact:
		return ChannelGeneratedArtifact, nil
	default:
		return "", errors.New("unsupported ingestion capability")
	}
}

// UploadInput is the complete public upload metadata contract. Origin,
// approval, epistemic status, and sensitivity downgrades are deliberately absent.
type UploadInput struct {
	LogicalKey        string `json:"logical_key"`
	Title             string `json:"title"`
	OriginalFilename  string `json:"original_filename"`
	DeclaredMediaType string `json:"declared_media_type"`
	ProjectKey        string `json:"project_key,omitempty"`
}

// DecodeUploadInput rejects unknown fields so authority cannot be smuggled into an upload.
func DecodeUploadInput(reader io.Reader) (UploadInput, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 64*1024))
	decoder.DisallowUnknownFields()
	var input UploadInput
	if err := decoder.Decode(&input); err != nil {
		return UploadInput{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return UploadInput{}, errors.New("upload metadata must contain one JSON object")
	}
	if strings.TrimSpace(input.LogicalKey) == "" || strings.TrimSpace(input.OriginalFilename) == "" {
		return UploadInput{}, errors.New("logical_key and original_filename are required")
	}
	return input, nil
}
