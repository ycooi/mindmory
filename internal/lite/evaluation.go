package lite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/config"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

type EvaluationOptions struct {
	Semantic         bool
	Model            string
	ModelDigest      string
	OllamaURL        string
	CommitHash       string
	QueryInstruction string
	SemanticOnly     bool
	MinimumScore     float64
	RRFFusion        bool
	SemanticWeight   float64
	VectorFirst      bool
	SemanticFallback bool
	HighScore        float64
	MinimumMargin    float64
	OnlyOnEmpty      bool
	TopOne           bool
}

type evaluationCorpus struct {
	Version   string                   `json:"version"`
	Templates []evaluationCaseTemplate `json:"templates"`
}

type evaluationCaseTemplate struct {
	Category        string `json:"category"`
	Count           int    `json:"count"`
	Memory          string `json:"memory"`
	Query           string `json:"query"`
	Expect          string `json:"expect"`
	Lifecycle       string `json:"lifecycle,omitempty"`
	Sensitivity     string `json:"sensitivity,omitempty"`
	Project         string `json:"project,omitempty"`
	SecretLike      bool   `json:"secret_like,omitempty"`
	InstructionLike bool   `json:"instruction_like,omitempty"`
	Alias           bool   `json:"alias,omitempty"`
}

type EvaluationQueryResult struct {
	ID                     string    `json:"id"`
	Category               string    `json:"category"`
	Query                  string    `json:"query"`
	ExpectedID             string    `json:"expected_id,omitempty"`
	ExpectNone             bool      `json:"expect_none"`
	ReturnedIDs            []string  `json:"returned_ids"`
	ReturnedMatchStrengths []float64 `json:"returned_match_strengths,omitempty"`
	LatencyUS              int64     `json:"latency_us"`
}

type EvaluationReport struct {
	CorpusVersion             string                  `json:"corpus_version"`
	CorpusCases               int                     `json:"corpus_cases"`
	CommitHash                string                  `json:"commit_hash"`
	Semantic                  bool                    `json:"semantic"`
	Model                     string                  `json:"model"`
	ModelDigest               string                  `json:"model_digest,omitempty"`
	Environment               map[string]string       `json:"environment"`
	Timestamp                 time.Time               `json:"timestamp"`
	RecallAt1                 float64                 `json:"recall_at_1"`
	RecallAt5                 float64                 `json:"recall_at_5"`
	RecallAt10                float64                 `json:"recall_at_10"`
	MRRAt10                   float64                 `json:"mrr_at_10"`
	NegativeFalsePositiveRate float64                 `json:"negative_false_positive_rate"`
	SecretInstructionLeakRate float64                 `json:"secret_instruction_leakage_rate"`
	CrossProjectLeakRate      float64                 `json:"cross_project_leakage_rate"`
	LifecycleLeakRate         float64                 `json:"lifecycle_leakage_rate"`
	SearchP50US               int64                   `json:"search_p50_us"`
	SearchP95US               int64                   `json:"search_p95_us"`
	IndexRebuildMS            int64                   `json:"index_rebuild_ms"`
	StoreStartupMS            int64                   `json:"store_startup_ms"`
	ContextCharacters         int                     `json:"context_characters"`
	ContextEstimatedTokens    int                     `json:"context_estimated_tokens"`
	Queries                   []EvaluationQueryResult `json:"queries"`
}

type expandedEvaluationCase struct {
	id, category, query, memoryID string
	expectNone                    bool
}

// RunEvaluation creates an isolated store from fixture templates, exercises
// the real lite retrieval server, and returns reproducible per-query evidence.
func RunEvaluation(ctx context.Context, corpusPath string, options EvaluationOptions) (EvaluationReport, error) {
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		return EvaluationReport{}, err
	}
	var corpus evaluationCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return EvaluationReport{}, err
	}
	tempDir, err := os.MkdirTemp("", "mindmory-lite-eval-")
	if err != nil {
		return EvaluationReport{}, err
	}
	defer os.RemoveAll(tempDir)
	store, err := Open(filepath.Join(tempDir, "data"))
	if err != nil {
		return EvaluationReport{}, err
	}
	principal := auth.Principal{Key: "evaluation", Type: auth.PrincipalMCP}
	session, err := store.UpsertSession(ctx, principal, "evaluation", "fixture evaluation", "project-a", time.Now().UTC())
	if err != nil {
		store.Close()
		return EvaluationReport{}, err
	}
	var cases []expandedEvaluationCase
	var aliases []retrieval.AliasEntry
	for _, template := range corpus.Templates {
		for i := 1; i <= template.Count; i++ {
			id := fmt.Sprintf("%s-%03d", template.Category, i)
			memoryID := "eval-" + id
			content := expandEvaluationText(template.Memory, i)
			queryOrdinal := i
			if template.Category == "negative" {
				// Negative queries must share no fixture ordinal token; otherwise
				// a valid numeric match is incorrectly counted as a false positive.
				queryOrdinal += 500
			}
			query := expandEvaluationText(template.Query, queryOrdinal)
			lifecycle := template.Lifecycle
			if lifecycle == "" {
				lifecycle = "ACTIVE"
			} else if template.Category == "lifecycle" {
				lifecycle = []string{"FORGOTTEN", "SUPERSEDED", "RETIRED"}[(i-1)%3]
			}
			sensitivity := template.Sensitivity
			if sensitivity == "" {
				sensitivity = "NORMAL"
			}
			project := template.Project
			if project == "" {
				project = "project-a"
			}
			instructionLike := template.InstructionLike
			secretLike := template.SecretLike
			if template.Category == "sensitivity" && i%2 == 0 {
				sensitivity, secretLike, instructionLike = "NORMAL", false, true
			}
			row := MemoryRow{MemoryID: memoryID, Kind: string(domain.KindProjectDecision), Subject: content,
				Content: content, ContentHash: hashContent(content), Lifecycle: lifecycle,
				EpistemicStatus: "USER_ACCEPTED", Confidence: 1, Importance: 0.5, Sensitivity: sensitivity,
				SecretLike: secretLike, InstructionLike: instructionLike, ScopeType: "PROJECT", ProjectKey: project,
				Activation: 0.5, StateVersion: 1}
			if err := store.insertMemoryFixture(ctx, row); err != nil {
				store.Close()
				return EvaluationReport{}, err
			}
			if template.Alias {
				aliases = append(aliases, retrieval.AliasEntry{Canonical: fmt.Sprintf("别名实体%03d", i), Aliases: []string{query}})
			}
			cases = append(cases, expandedEvaluationCase{id: id, category: template.Category, query: query,
				memoryID: memoryID, expectNone: template.Expect == "none"})
		}
	}
	minimumCases := 150
	if strings.HasPrefix(corpus.Version, "mindmory-lite-semantic-challenge") {
		minimumCases = 50
	}
	if len(cases) < minimumCases || len(cases) > 300 {
		store.Close()
		return EvaluationReport{}, fmt.Errorf("evaluation corpus must expand to %d-300 cases, got %d", minimumCases, len(cases))
	}
	dataDir := store.Dir()
	if err := store.Close(); err != nil {
		return EvaluationReport{}, err
	}
	startupStarted := time.Now()
	store, err = Open(dataDir)
	startup := time.Since(startupStarted)
	if err != nil {
		return EvaluationReport{}, err
	}
	defer store.Close()
	semantic := options.Semantic
	server := NewServer(store, "evaluation", "evaluation-integrity-key-at-least-32-bytes", "evaluation-admin-token",
		map[string]config.MCPPrincipalConfig{"evaluation": {Token: "evaluation-token-at-least-24", Capabilities: []config.MCPClientCapability{config.MCPContextRead}}},
		testlessLogger(), false)
	server.SemanticSearch = &semantic
	server.SemanticQueryInstruction = options.QueryInstruction
	server.SemanticOnlyExperiment = options.SemanticOnly
	minimumScore := options.MinimumScore
	if minimumScore == 0 {
		minimumScore = semanticMinimumThreshold
	}
	server.SemanticMinimumScoreExperiment = &minimumScore
	server.SemanticRRFFusionExperiment = options.RRFFusion
	server.SemanticRRFWeightExperiment = options.SemanticWeight
	server.SemanticVectorFirstExperiment = options.VectorFirst
	server.SemanticHighScoreExperiment = options.HighScore
	server.SemanticMinimumMarginExperiment = options.MinimumMargin
	server.SemanticOnlyOnEmptyExperiment = options.OnlyOnEmpty
	server.SemanticTopOneExperiment = options.TopOne
	server.Aliases = retrieval.NewAliasExpander(aliases)
	if options.Semantic {
		embedder := &OllamaEmbedder{Endpoint: options.OllamaURL, Model: options.Model, Digest: options.ModelDigest}
		if options.ModelDigest == "" {
			options.ModelDigest = resolveOllamaDigest(ctx, embedder.EndpointURL(), embedder.ModelName())
			embedder.Digest = options.ModelDigest
		}
		server.Embedder = embedder
		if _, err := store.EmbedAll(ctx, embedder); err != nil {
			return EvaluationReport{}, fmt.Errorf("semantic fixture embedding: %w", err)
		}
	}
	if options.ModelDigest == "" {
		options.ModelDigest = "not-applicable"
	}
	if options.CommitHash == "" {
		options.CommitHash = evaluationCommit()
	}
	rebuildStarted := time.Now()
	if err := store.RebuildIndex(); err != nil {
		return EvaluationReport{}, err
	}
	rebuild := time.Since(rebuildStarted)
	report := EvaluationReport{CorpusVersion: corpus.Version, CorpusCases: len(cases), CommitHash: options.CommitHash,
		Semantic: options.Semantic, Model: options.Model, ModelDigest: options.ModelDigest, Timestamp: time.Now().UTC(),
		Environment: map[string]string{"go": runtimeVersion(), "os_arch": runtimePlatform(),
			"retrieval":               map[bool]string{true: "lexical+semantic", false: "lexical"}[options.Semantic],
			"semantic_minimum_cosine": fmt.Sprintf("%.2f", minimumScore), "ollama_url": options.OllamaURL,
			"query_instruction": strings.TrimSpace(options.QueryInstruction)},
		IndexRebuildMS: rebuild.Milliseconds(), StoreStartupMS: startup.Milliseconds()}
	report.Environment["semantic_only"] = fmt.Sprintf("%t", options.SemanticOnly)
	report.Environment["rrf_fusion"] = fmt.Sprintf("%t", options.RRFFusion)
	report.Environment["semantic_weight"] = fmt.Sprintf("%.2f", options.SemanticWeight)
	report.Environment["vector_first"] = fmt.Sprintf("%t", options.VectorFirst)
	report.Environment["semantic_fallback"] = fmt.Sprintf("%t", options.SemanticFallback)
	report.Environment["semantic_high_score"] = fmt.Sprintf("%.2f", options.HighScore)
	report.Environment["semantic_minimum_margin"] = fmt.Sprintf("%.2f", options.MinimumMargin)
	report.Environment["semantic_only_on_empty"] = fmt.Sprintf("%t", options.OnlyOnEmpty)
	report.Environment["semantic_top_one"] = fmt.Sprintf("%t", options.TopOne)
	scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: principal.Key, ProjectKey: "project-a"}
	latencies := make([]int64, 0, len(cases))
	positive, negative, security, project, lifecycle := 0, 0, 0, 0, 0
	var hit1, hit5, hit10, reciprocal, falsePositive, securityLeak, projectLeak, lifecycleLeak float64
	for _, test := range cases {
		started := time.Now()
		mode := retrieval.SearchLexical
		if options.Semantic {
			mode = retrieval.SearchSemantic
			if options.SemanticFallback {
				mode = retrieval.SearchSemanticFallback
			}
		}
		hits, err := server.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: scope.SessionID, Query: test.query, Limit: 10, Mode: mode}, false)
		latency := time.Since(started).Microseconds()
		if err != nil {
			return EvaluationReport{}, err
		}
		returned := make([]string, 0, len(hits))
		strengths := make([]float64, 0, len(hits))
		rank := 0
		for i, hit := range hits {
			returned = append(returned, hit.MemoryID)
			strengths = append(strengths, hit.MatchStrength)
			if hit.MemoryID == test.memoryID && rank == 0 {
				rank = i + 1
			}
		}
		if test.expectNone {
			switch test.category {
			case "negative":
				negative++
				if len(hits) > 0 {
					falsePositive++
				}
			case "sensitivity":
				security++
				if containsIDPrefix(returned, "eval-sensitivity-") {
					securityLeak++
				}
			case "project_isolation":
				project++
				if containsIDPrefix(returned, "eval-project_isolation-") {
					projectLeak++
				}
			case "lifecycle":
				lifecycle++
				if containsIDPrefix(returned, "eval-lifecycle-") {
					lifecycleLeak++
				}
			}
		} else {
			positive++
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
		report.Queries = append(report.Queries, EvaluationQueryResult{ID: test.id, Category: test.category,
			Query: test.query, ExpectedID: test.memoryID, ExpectNone: test.expectNone, ReturnedIDs: returned, ReturnedMatchStrengths: strengths, LatencyUS: latency})
		latencies = append(latencies, latency)
	}
	report.RecallAt1, report.RecallAt5, report.RecallAt10, report.MRRAt10 = ratio(hit1, positive), ratio(hit5, positive), ratio(hit10, positive), ratio(reciprocal, positive)
	report.NegativeFalsePositiveRate = ratio(falsePositive, negative)
	report.SecretInstructionLeakRate = ratio(securityLeak, security)
	report.CrossProjectLeakRate = ratio(projectLeak, project)
	report.LifecycleLeakRate = ratio(lifecycleLeak, lifecycle)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	report.SearchP50US, report.SearchP95US = percentile(latencies, 0.50), percentile(latencies, 0.95)
	packet, err := server.explicitPacket(ctx, scope, retrieval.ContextRequest{SessionID: scope.SessionID, Query: cases[0].query, MaxChars: 4000})
	if err == nil {
		packetRaw, _ := json.Marshal(packet)
		report.ContextCharacters = len([]rune(string(packetRaw)))
		report.ContextEstimatedTokens = retrieval.EstimatedTokens(string(packetRaw))
	}
	return report, nil
}

func expandEvaluationText(template string, ordinal int) string {
	if strings.Contains(template, "%") {
		return fmt.Sprintf(template, ordinal)
	}
	return template
}

func ratio(value float64, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return value / float64(denominator)
}

func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * quantile)
	return values[index]
}

func containsIDPrefix(ids []string, prefix string) bool {
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func evaluationCommit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	if executable, err := os.Executable(); err == nil {
		if raw, readErr := os.ReadFile(executable); readErr == nil {
			sum := sha256.Sum256(raw)
			return "binary-sha256:" + hex.EncodeToString(sum[:])
		}
	}
	return "unavailable"
}

func resolveOllamaDigest(ctx context.Context, endpoint, model string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return "unknown"
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "unknown"
	}
	defer response.Body.Close()
	var body struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if json.NewDecoder(response.Body).Decode(&body) != nil {
		return "unknown"
	}
	for _, candidate := range body.Models {
		if candidate.Name == model || candidate.Model == model {
			return candidate.Digest
		}
	}
	return "unknown"
}

func runtimeVersion() string  { return runtime.Version() }
func runtimePlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }
func testlessLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
