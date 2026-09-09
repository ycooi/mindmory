package mcpserver

import "testing"

func TestCompactMemoryHitOmitsDuplicateSnippet(t *testing.T) {
	item := map[string]any{
		"memory_id": "memory-1", "subject": "same text", "content": "same text",
		"score": 0.9, "scope": "GLOBAL",
	}
	hit := compactMemoryHit(item)
	if _, exists := hit["snippet"]; exists {
		t.Fatalf("duplicate snippet was not omitted: %#v", hit)
	}
	if hit["title"] != "same text" {
		t.Fatalf("title=%v", hit["title"])
	}
}

func TestCompactMemoryHitKeepsDistinctBoundedSnippet(t *testing.T) {
	item := map[string]any{
		"memory_id": "memory-1", "subject": "short title", "content": "distinct supporting detail",
		"score": 0.9, "scope": "PROJECT",
	}
	hit := compactMemoryHit(item)
	if hit["snippet"] != "distinct supporting detail" {
		t.Fatalf("snippet=%v", hit["snippet"])
	}
	if hit["project_scope"] != "CURRENT_PROJECT" {
		t.Fatalf("project_scope=%v", hit["project_scope"])
	}
}
