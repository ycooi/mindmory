// Package version exposes the Mindmory release version to commands and MCP.
package version

// Value is the semantic release version. Release builds may override it with:
//
//	-ldflags "-X mindmory.local/core/internal/version.Value=<version>"
var Value = "0.1.2"
