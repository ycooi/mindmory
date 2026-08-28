// Command mindmoryctl is the authenticated operator client.
package main

import (
	"os"

	"mindmory.local/core/internal/command"
	"mindmory.local/core/internal/version"
)

func main() { os.Exit(command.Run("mindmoryctl", version.Value, os.Args[1:])) }
