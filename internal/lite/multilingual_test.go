package lite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mindmory.local/core/internal/retrieval"
)

// Ten distinct subjects across ten query classes. IDs never occur in query or
// memory text. Aliases are a fixed vocabulary, not generated from test queries.
func TestMultilingual100(t *testing.T) {
	names := []string{"青岚计划", "星河档案", "松林工程", "银泉方案", "赤峰路线", "紫竹规范", "白鹭手册", "碧海协议", "金桥流程", "雪原任务"}
	phrases := []string{"blue mist project", "stellar river archive", "pine forest engineering", "silver spring proposal", "crimson peak route", "violet bamboo standard", "white heron handbook", "jade sea protocol", "golden bridge workflow", "snowfield assignment"}
	server, store, _, session := governanceFixture(t)
	entries := []retrieval.AliasEntry{}
	for i, name := range names {
		entries = append(entries, retrieval.AliasEntry{Canonical: name, Aliases: []string{phrases[i]}})
		for _, variant := range []string{"target", "secret", "retired", "foreign"} {
			row := MemoryRow{MemoryID: fmt.Sprintf("fixture-%s-%d", variant, i), Kind: "DOCUMENT_FACT", Subject: name, Content: name + "采用独立审核制度", Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL", Confidence: 1, Importance: 0.5}
			if variant == "secret" {
				row.Sensitivity = "SECRET"
				row.SecretLike = true
			}
			if variant == "retired" {
				row.Lifecycle = "RETIRED"
			}
			if variant == "foreign" {
				row.ScopeType = "PROJECT"
				row.ProjectKey = "other-project"
			}
			row.ContentHash = hashContent(row.Content)
			if err := store.insertMemoryFixture(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Real, shorter entity distractors exercise longest-name selection.
	for i, name := range names {
		short := string([]rune(name)[:2])
		entries = append(entries, retrieval.AliasEntry{Canonical: short, Aliases: []string{"unrelated shorter description"}})
		row := MemoryRow{MemoryID: fmt.Sprintf("short-%d", i), Kind: "DOCUMENT_FACT", Subject: short, Content: short + "另有定义", Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL", Confidence: 1}
		row.ContentHash = hashContent(row.Content)
		if err := store.insertMemoryFixture(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(entries)
	path := filepath.Join(t.TempDir(), "aliases.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	server.LoadAliases(path)
	levels := []string{"exact_cjk", "english_alias", "embedded_cjk", "embedded_english", "punctuation", "mixed_language", "long_context", "two_entities", "negative", "case_and_spacing"}
	count := 0
	for level, label := range levels {
		for i, name := range names {
			t.Run(fmt.Sprintf("%s/%d", label, i), func(t *testing.T) {
				next := (i + 1) % len(names)
				queries := []string{name, phrases[i], "请介绍" + name + "的安排", "please explain the purpose of " + phrases[i], "「" + name + "」，进展如何？", "Please summarize " + name + " 的安排", "准备讨论时需要先确认背景，请详细说明" + name + "的目标以及后续安排", "请比较" + name + "和" + names[next] + "的区别", "unlisted zeppelin horticulture " + string(rune('a'+i)), "  " + strings.ToUpper(phrases[i]) + "  "}
				request := retrieval.SearchRequest{Query: queries[level], Limit: 10}
				scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: "test-client", ProjectKey: "project-a"}
				hits, err := server.searchMemories(context.Background(), scope, request, false)
				if err != nil {
					t.Fatal(err)
				}
				again, err := server.searchMemories(context.Background(), scope, request, false)
				if err != nil || !reflect.DeepEqual(hits, again) {
					t.Fatal("unstable repeated retrieval", err)
				}
				for _, hit := range hits {
					if strings.Contains(hit.MemoryID, "secret") || strings.Contains(hit.MemoryID, "retired") || strings.Contains(hit.MemoryID, "foreign") {
						t.Fatalf("excluded memory leaked: %s", hit.MemoryID)
					}
				}
				if level == 8 {
					if len(hits) != 0 {
						t.Fatalf("negative returned %v", hits)
					}
					return
				}
				wanted := fmt.Sprintf("fixture-target-%d", i)
				rank := 0
				for j, hit := range hits {
					if hit.MemoryID == wanted {
						rank = j + 1
					}
				}
				if rank == 0 || rank > 5 {
					t.Fatalf("target outside top five: rank=%d query=%q hits=%v", rank, request.Query, hits)
				}
				if level != 7 && rank != 1 {
					t.Fatalf("target rank=%d, want first", rank)
				}
				if level == 7 {
					found := false
					for j, hit := range hits {
						if j < 5 && hit.MemoryID == fmt.Sprintf("fixture-target-%d", next) {
							found = true
						}
					}
					if !found {
						t.Fatal("second independently mentioned entity absent from top five")
					}
				}
			})
			count++
		}
	}
	if count != 100 {
		t.Fatalf("case count %d", count)
	}
}
