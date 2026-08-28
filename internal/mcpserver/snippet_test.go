package mcpserver

import "testing"

func TestSnippetTextBoundsLongContent(t *testing.T) {
	short := "记住以后 Mindmory 的 MCP server 优先用 Go。"
	if got := snippetText(short); got != short {
		t.Fatalf("short content altered: %q", got)
	}
	long := make([]rune, 0, 300)
	for i := 0; i < 300; i++ {
		long = append(long, rune('a'+i%26))
	}
	got := snippetText(string(long))
	if len([]rune(got)) != 121 {
		t.Fatalf("snippet length=%d want 121", len([]rune(got)))
	}
	if got[len(got)-3:] != "…" {
		t.Fatalf("snippet missing ellipsis: %q", got)
	}
	if got := snippetText(nil); got != "" {
		t.Fatalf("nil snippet=%q", got)
	}
	if got := snippetText(42); got != "" {
		t.Fatalf("non-string snippet=%q", got)
	}
}
