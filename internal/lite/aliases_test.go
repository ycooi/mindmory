package lite

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mindmory.local/core/internal/retrieval"
)

func TestAliasOverlayConfiguration(t *testing.T) {
	for _, tc := range []struct{ root, data, file, want string }{
		{"workspace", "", "", "workspace/var/data/aliases.json"},
		{"workspace", "canonical", "", "workspace/canonical/aliases.json"},
		{"workspace", "canonical", "custom.json", "workspace/custom.json"},
		{"workspace", "canonical", "/opt/shared/aliases.json", "/opt/shared/aliases.json"},
	} {
		env := validLiteEnv()
		env["MINDMORY_ROOT_DIR"] = tc.root
		env["MINDMORY_DATA_DIR"] = tc.data
		env["MINDMORY_ALIAS_FILE"] = tc.file
		cfg, err := loadTestEnv(env)
		if err != nil || cfg.AliasFile != tc.want {
			t.Fatalf("%+v: %q %v", tc, cfg.AliasFile, err)
		}
	}
}

func TestAliasOverlayValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		invalid    bool
	}{
		{"missing", "", false}, {"empty", "[]", false}, {"malformed", "{private text", true},
		{"null", "null", true}, {"object", "{}", true}, {"empty_canonical", `[{"Canonical":"","Aliases":["a"]}]`, true},
		{"empty_alias", `[{"Canonical":"example","Aliases":[" "]}]`, true},
		{"no_aliases", `[{"Canonical":"example"}]`, true}, {"oversize", strings.Repeat(" ", 1<<20) + "[]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aliases.json")
			if tc.name != "missing" {
				if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readAliasOverlay(path)
			if (err != nil) != tc.invalid {
				t.Fatalf("validation: %v", err)
			}
			var logs bytes.Buffer
			server := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil))}
			server.LoadAliases(path)
			if !slices.Contains(server.Aliases.Expand("the warmth that waits on the near bank"), "余烬永温") {
				t.Fatal("defaults lost")
			}
			if tc.invalid && logs.Len() == 0 {
				t.Fatal("missing warning")
			}
			if strings.Contains(logs.String(), path) || strings.Contains(logs.String(), "private text") {
				t.Fatal("private data in warning")
			}
		})
	}
}

func TestAliasOverlayMergeAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	body := `[{"Canonical":"余烬永温","Aliases":["synthetic enduring warmth"]},{"Canonical":"余烬永温","Aliases":["synthetic enduring warmth","another lasting warmth"]}]`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		entries, err := readAliasOverlay(path)
		if err != nil {
			t.Fatal(err)
		}
		x := retrieval.NewAliasExpander(entries)
		for _, q := range []string{"synthetic enduring warmth", "another lasting warmth", "the warmth that waits on the near bank"} {
			if !slices.Contains(x.Expand(q), "余烬永温") {
				t.Fatal(q)
			}
		}
	}
	if len(retrieval.NewAliasExpander(nil).Expand("synthetic enduring warmth")) != 1 {
		t.Fatal("default table mutated")
	}
}

func TestAliasOverlayHTTPChineseToEnglish(t *testing.T) {
	server, store, _, session := governanceFixture(t)
	row := MemoryRow{MemoryID: "allocation", Kind: "DOCUMENT_FACT", Subject: "savings allocation", Content: "savings allocation follows a reserve policy", Lifecycle: "ACTIVE", Sensitivity: "NORMAL", ScopeType: "GLOBAL", Confidence: 1}
	row.ContentHash = hashContent(row.Content)
	if err := store.insertMemoryFixture(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "aliases.json")
	if err := os.WriteFile(path, []byte(`[{"canonical":"savings allocation","aliases":["储蓄如何分配"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	server.LoadAliases(path)
	for _, query := range []string{"储蓄如何分配", "请解释储蓄如何分配才合适", "Please explain 储蓄如何分配"} {
		body, _ := json.Marshal(retrieval.SearchRequest{SessionID: session.SessionID, Query: query, Limit: 5})
		req := httptest.NewRequest(http.MethodPost, "/v1/context/search", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Results []retrieval.MemoryHit `json:"results"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Results) == 0 || response.Results[0].MemoryID != "allocation" {
			t.Fatalf("%q: %s", query, rec.Body.String())
		}
	}
}
