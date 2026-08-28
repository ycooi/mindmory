// Command mindmory-mcp-stdio is the bound-turn stdio MCP transport adapter.
package main

import (
	"os"

	"mindmory.local/core/internal/mcpserver"
	"mindmory.local/core/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_, _ = os.Stdout.WriteString("mindmory-mcp-stdio " + version.Value + "\n")
		return
	}
	os.Exit(mcpserver.Run(version.Value))
}
