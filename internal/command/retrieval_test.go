package command

import "testing"

func TestStage5RetrievalCLIMapsToModelHTTP(t *testing.T) {
	tests := [][]string{{"context", "search", "--session", "s", "--query", "MCP Go"}, {"context", "packet", "--session", "s"}, {"memory", "recall", "--session", "s", "--id", "m"}, {"artifact", "search", "--session", "s", "--query", "monthly"}, {"artifact", "read", "--session", "s", "--id", "a"}, {"continuity", "diff", "--session", "s", "--cursor", "opaque"}}
	for _, args := range tests {
		op, ok := retrievalCommand(args)
		if !ok || op.path == "" {
			t.Fatalf("not mapped: %v", args)
		}
	}
}
