package lite

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/config"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// ScaleEvaluationReport measures the upgraded lexical path over isolated
// synthetic corpora. No fixture is written to the caller's canonical store.
type ScaleEvaluationReport struct {
	Name              string                  `json:"name"`
	CommitHash        string                  `json:"commit_hash"`
	Timestamp         time.Time               `json:"timestamp"`
	Environment       map[string]string       `json:"environment"`
	TotalCases        int                     `json:"total_cases"`
	SearchInvocations int                     `json:"search_invocations"`
	Results           []ScaleEvaluationResult `json:"results"`
}

type ScaleEvaluationResult struct {
	Label                  string                 `json:"label"`
	CorpusMemories         int                    `json:"corpus_memories"`
	CorpusEstimatedTokens  int                    `json:"corpus_estimated_tokens"`
	QueryCount             int                    `json:"query_count"`
	PositiveQueries        int                    `json:"positive_queries"`
	NegativeQueries        int                    `json:"negative_queries"`
	RecallAt1              float64                `json:"recall_at_1"`
	RecallAt5              float64                `json:"recall_at_5"`
	RecallAt10             float64                `json:"recall_at_10"`
	MRRAt10                float64                `json:"mrr_at_10"`
	NegativeFalsePositive  float64                `json:"negative_false_positive_rate"`
	StableRepeatRate       float64                `json:"stable_repeat_rate"`
	SearchP50US            int64                  `json:"search_p50_us"`
	SearchP95US            int64                  `json:"search_p95_us"`
	SearchP99US            int64                  `json:"search_p99_us"`
	SearchMaxUS            int64                  `json:"search_max_us"`
	AverageReturned        float64                `json:"average_returned_results"`
	AverageEstimatedTokens float64                `json:"average_estimated_tokens"`
	AverageCompactTokens   float64                `json:"average_compact_estimated_tokens"`
	CanonicalWriteMS       int64                  `json:"canonical_write_ms"`
	IndexBuildMS           int64                  `json:"index_build_ms"`
	StoreStartupMS         int64                  `json:"store_startup_ms"`
	CanonicalBytes         int64                  `json:"canonical_bytes"`
	DerivedBytes           int64                  `json:"derived_bytes"`
	Queries                []ScaleEvaluationQuery `json:"queries"`
}

type ScaleEvaluationQuery struct {
	ID              string   `json:"id"`
	Category        string   `json:"category"`
	Question        string   `json:"question"`
	ExpectedID      string   `json:"expected_id,omitempty"`
	ExpectNone      bool     `json:"expect_none"`
	ReturnedIDs     []string `json:"returned_ids"`
	Rank            int      `json:"rank,omitempty"`
	LatencyUS       int64    `json:"latency_us"`
	EstimatedTokens int      `json:"estimated_tokens"`
	RepeatStable    bool     `json:"repeat_stable"`
}

type scaleScenarioQuery struct {
	id, category, question, expectedID string
	expectNone                         bool
}

// RunScaleEvaluation executes queriesPerSize scored cases at each requested
// corpus size. Each query is repeated once and compared by ordered memory IDs
// to detect unstable ranking without counting the repeat as another case.
func RunScaleEvaluation(ctx context.Context, sizes []int, queriesPerSize int, options EvaluationOptions) (ScaleEvaluationReport, error) {
	if len(sizes) == 0 {
		return ScaleEvaluationReport{}, fmt.Errorf("at least one scale size is required")
	}
	if queriesPerSize < 10 || queriesPerSize > 500 || queriesPerSize%5 != 0 {
		return ScaleEvaluationReport{}, fmt.Errorf("queries per size must be 10-500 and divisible by 5, got %d", queriesPerSize)
	}
	for _, size := range sizes {
		if size < queriesPerSize || size > 500_000 {
			return ScaleEvaluationReport{}, fmt.Errorf("scale size must be between queries per size and 500000, got %d", size)
		}
	}
	if options.Semantic {
		return ScaleEvaluationReport{}, fmt.Errorf("scale evaluation currently measures the lexical daemon path only")
	}
	if options.CommitHash == "" {
		options.CommitHash = evaluationCommit()
	}
	report := ScaleEvaluationReport{
		Name:              "mindmory-lexical-scale-v1",
		CommitHash:        options.CommitHash,
		Timestamp:         time.Now().UTC(),
		TotalCases:        len(sizes) * queriesPerSize,
		SearchInvocations: len(sizes) * queriesPerSize * 2,
		Environment: map[string]string{
			"go":        runtime.Version(),
			"os_arch":   runtime.GOOS + "/" + runtime.GOARCH,
			"retrieval": "lexical",
		},
	}
	labels := []string{"short", "medium", "long", "superlong"}
	for i, size := range sizes {
		label := fmt.Sprintf("size-%d", size)
		if i < len(labels) {
			label = labels[i]
		}
		result, err := runScaleSize(ctx, label, size, queriesPerSize)
		if err != nil {
			return ScaleEvaluationReport{}, fmt.Errorf("%s corpus: %w", label, err)
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func runScaleSize(ctx context.Context, label string, corpusSize, queryCount int) (ScaleEvaluationResult, error) {
	rows, queries := generateScaleScenario(corpusSize, queryCount)
	tempDir, err := os.MkdirTemp("", "mindmory-scale-eval-")
	if err != nil {
		return ScaleEvaluationResult{}, err
	}
	defer os.RemoveAll(tempDir)
	dataDir := filepath.Join(tempDir, "data")
	store, err := Open(dataDir)
	if err != nil {
		return ScaleEvaluationResult{}, err
	}
	principal := auth.Principal{Key: "scale-evaluation", Type: auth.PrincipalMCP}
	session, err := store.UpsertSession(ctx, principal, "scale-evaluation", label, "project-a", time.Now().UTC())
	if err != nil {
		store.Close()
		return ScaleEvaluationResult{}, err
	}
	writeStarted := time.Now()
	if err := insertScaleFixtureBatch(store, rows); err != nil {
		store.Close()
		return ScaleEvaluationResult{}, err
	}
	writeDuration := time.Since(writeStarted)
	indexStarted := time.Now()
	if err := store.RebuildIndex(); err != nil {
		store.Close()
		return ScaleEvaluationResult{}, err
	}
	indexDuration := time.Since(indexStarted)
	if err := store.Close(); err != nil {
		return ScaleEvaluationResult{}, err
	}
	startupStarted := time.Now()
	store, err = Open(dataDir)
	startupDuration := time.Since(startupStarted)
	if err != nil {
		return ScaleEvaluationResult{}, err
	}
	defer store.Close()

	semantic := false
	server := NewServer(store, "scale-evaluation", "evaluation-integrity-key-at-least-32-bytes", "evaluation-admin-token",
		map[string]config.MCPPrincipalConfig{"scale-evaluation": {Token: "evaluation-token-at-least-24", Capabilities: []config.MCPClientCapability{config.MCPContextRead}}},
		testlessLogger(), false)
	server.SemanticSearch = &semantic
	server.Aliases = retrieval.NewAliasExpander(nil)
	scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: principal.Key, ProjectKey: "project-a"}
	if len(queries) > 0 {
		_, _ = server.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: scope.SessionID, Query: queries[0].question, Limit: 10, Mode: retrieval.SearchLexical}, false)
	}

	result := ScaleEvaluationResult{
		Label: label, CorpusMemories: corpusSize, QueryCount: len(queries),
		PositiveQueries: queryCount * 4 / 5, NegativeQueries: queryCount / 5,
		CanonicalWriteMS: writeDuration.Milliseconds(), IndexBuildMS: indexDuration.Milliseconds(), StoreStartupMS: startupDuration.Milliseconds(),
	}
	for _, row := range rows {
		result.CorpusEstimatedTokens += retrieval.EstimatedTokens(row.Subject + " " + row.Content)
	}
	result.CanonicalBytes, _ = directoryBytes(dataDir)
	result.DerivedBytes, _ = directoryBytes(filepath.Join(tempDir, "derived"))
	latencies := make([]int64, 0, len(queries))
	var hit1, hit5, hit10, reciprocal, falsePositive, stable, returnedTotal, tokenTotal, compactTokenTotal float64
	for _, query := range queries {
		if err := ctx.Err(); err != nil {
			return ScaleEvaluationResult{}, err
		}
		started := time.Now()
		hits, err := server.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: scope.SessionID, Query: query.question, Limit: 10, Mode: retrieval.SearchLexical}, false)
		latency := time.Since(started).Microseconds()
		if err != nil {
			return ScaleEvaluationResult{}, err
		}
		repeat, err := server.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: scope.SessionID, Query: query.question, Limit: 10, Mode: retrieval.SearchLexical}, false)
		if err != nil {
			return ScaleEvaluationResult{}, err
		}
		ids := memoryHitIDs(hits)
		repeatStable := equalStrings(ids, memoryHitIDs(repeat))
		if repeatStable {
			stable++
		}
		rank := 0
		for i, id := range ids {
			if id == query.expectedID {
				rank = i + 1
				break
			}
		}
		if query.expectNone {
			if len(ids) > 0 {
				falsePositive++
			}
		} else {
			if rank == 1 {
				hit1++
			}
			if rank > 0 && rank <= 5 {
				hit5++
			}
			if rank > 0 && rank <= 10 {
				hit10++
				reciprocal += 1 / float64(rank)
			}
		}
		encoded, _ := json.Marshal(hits)
		tokens := retrieval.EstimatedTokens(string(encoded))
		compactEncoded, _ := json.Marshal(map[string]any{"hits": compactScaleHits(hits)})
		compactTokens := retrieval.EstimatedTokens(string(compactEncoded))
		latencies = append(latencies, latency)
		returnedTotal += float64(len(ids))
		tokenTotal += float64(tokens)
		compactTokenTotal += float64(compactTokens)
		result.Queries = append(result.Queries, ScaleEvaluationQuery{
			ID: query.id, Category: query.category, Question: query.question, ExpectedID: query.expectedID,
			ExpectNone: query.expectNone, ReturnedIDs: ids, Rank: rank, LatencyUS: latency,
			EstimatedTokens: tokens, RepeatStable: repeatStable,
		})
	}
	positive := result.PositiveQueries
	result.RecallAt1 = ratio(hit1, positive)
	result.RecallAt5 = ratio(hit5, positive)
	result.RecallAt10 = ratio(hit10, positive)
	result.MRRAt10 = ratio(reciprocal, positive)
	result.NegativeFalsePositive = ratio(falsePositive, result.NegativeQueries)
	result.StableRepeatRate = ratio(stable, len(queries))
	result.AverageReturned = returnedTotal / float64(len(queries))
	result.AverageEstimatedTokens = tokenTotal / float64(len(queries))
	result.AverageCompactTokens = compactTokenTotal / float64(len(queries))
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result.SearchP50US = percentile(latencies, 0.50)
	result.SearchP95US = percentile(latencies, 0.95)
	result.SearchP99US = percentile(latencies, 0.99)
	result.SearchMaxUS = latencies[len(latencies)-1]
	return result, nil
}

func compactScaleHits(hits []retrieval.MemoryHit) []map[string]any {
	compact := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		scope := "CURRENT_PROJECT"
		if hit.Scope == "GLOBAL" || hit.Scope == "" {
			scope = "GLOBAL"
		}
		item := map[string]any{
			"type": "MEMORY", "id": hit.MemoryID, "title": hit.Subject,
			"score": hit.Score, "project_scope": scope,
		}
		if snippet := compactScaleSnippet(hit.Content); snippet != "" && snippet != hit.Subject {
			item["snippet"] = snippet
		}
		compact = append(compact, item)
	}
	return compact
}

func compactScaleSnippet(value string) string {
	runes := []rune(value)
	if len(runes) <= 120 {
		return value
	}
	return string(runes[:120]) + "…"
}

func generateScaleScenario(corpusSize, queryCount int) ([]MemoryRow, []scaleScenarioQuery) {
	positive := queryCount * 4 / 5
	perCategory := positive / 4
	rows := make([]MemoryRow, 0, corpusSize)
	queries := make([]scaleScenarioQuery, 0, queryCount)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < corpusSize; i++ {
		id := fmt.Sprintf("scale-%06d", i+1)
		artifact := fmt.Sprintf("nova%06d", i+1)
		content := fmt.Sprintf("Archived engineering observation %06d records deployment shipping rollback checks for decoy artifact %s in module orbit%03d.", i+1, artifact, i%97)
		category := "distractor"
		if i < positive {
			switch {
			case i < perCategory:
				category = "exact"
				content = fmt.Sprintf("Exact scale decision %06d uses cobalt protocol for artifact %s.", i+1, artifact)
				queries = append(queries, scaleScenarioQuery{id: fmt.Sprintf("exact-%03d", i+1), category: category, question: content, expectedID: id})
			case i < perCategory*2:
				category = "inflection"
				content = fmt.Sprintf("The shipping task for artifact %s stopped after checksum checks completed.", artifact)
				queries = append(queries, scaleScenarioQuery{id: fmt.Sprintf("inflection-%03d", i+1), category: category, question: fmt.Sprintf("What shipped for artifact %s after checking?", artifact), expectedID: id})
			case i < perCategory*3:
				category = "typo"
				content = fmt.Sprintf("Deployment sentinel for artifact %s preserves recovery state after a restart.", artifact)
				queries = append(queries, scaleScenarioQuery{id: fmt.Sprintf("typo-%03d", i+1), category: category, question: fmt.Sprintf("deploymnt sentinal artifact %s recovery state", artifact), expectedID: id})
			default:
				category = "temporal"
				day := (i % 28) + 1
				content = fmt.Sprintf("On 2026-04-%02d artifact %s adopted the amber rollback policy after review.", day, artifact)
				queries = append(queries, scaleScenarioQuery{id: fmt.Sprintf("temporal-%03d", i+1), category: category, question: fmt.Sprintf("What rollback policy did artifact %s adopt in April?", artifact), expectedID: id})
			}
		}
		rows = append(rows, MemoryRow{
			MemoryID: id, Kind: string(domain.KindProjectDecision), Subject: content, Content: content,
			ContentHash: hashContent(content), Lifecycle: "ACTIVE", EpistemicStatus: "SOURCE_VERIFIED",
			Confidence: 1, Importance: 0.5, Sensitivity: "NORMAL", ScopeType: "PROJECT", ProjectKey: "project-a",
			Activation: 0.5, StateVersion: 1, CreatedAt: fixed, UpdatedAt: fixed,
		})
	}
	for i := 0; i < queryCount/5; i++ {
		queries = append(queries, scaleScenarioQuery{
			id: fmt.Sprintf("negative-%03d", i+1), category: "negative",
			question: fmt.Sprintf("unrelated quasar propulsion absence%06d", corpusSize+i+501), expectNone: true,
		})
	}
	return rows, queries
}

func insertScaleFixtureBatch(store *Store, rows []MemoryRow) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, row := range rows {
		row.SchemaVersion = SchemaVersion
		store.memories[row.MemoryID] = row
	}
	return store.flushKindLocked("memories", store.memoriesJSONL())
}

func memoryHitIDs(hits []retrieval.MemoryHit) []string {
	ids := make([]string, len(hits))
	for i, hit := range hits {
		ids[i] = hit.MemoryID
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
