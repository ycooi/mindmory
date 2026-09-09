package lite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"mindmory.local/core/internal/auth"
	"mindmory.local/core/internal/config"
	domain "mindmory.local/core/internal/memory"
	"mindmory.local/core/internal/retrieval"
)

// CodingLifeSession and CodingLifeQuestion intentionally match the public
// coding-agent-life-v1 benchmark format. The evaluator reads an external
// fixture directory so upstream synthetic data does not become Mindmory
// production or canonical memory.
type CodingLifeSession struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

type CodingLifeQuestion struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Question       string   `json:"question"`
	Answer         string   `json:"answer"`
	GoldSessionIDs []string `json:"goldSessionIds"`
}

type CodingLifeQueryResult struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Question        string   `json:"question"`
	GoldSessionIDs  []string `json:"gold_session_ids"`
	ReturnedIDs     []string `json:"returned_ids"`
	PrecisionAtK    float64  `json:"precision_at_k"`
	RecallAtK       float64  `json:"recall_at_k"`
	Hit             bool     `json:"hit"`
	TopGoldRank     int      `json:"top_gold_rank,omitempty"`
	LatencyUS       int64    `json:"latency_us"`
	EstimatedTokens int      `json:"estimated_tokens"`
	CompactTokens   int      `json:"compact_estimated_tokens"`
}

type CodingLifeTypeMetrics struct {
	Queries      int     `json:"queries"`
	PrecisionAtK float64 `json:"precision_at_k"`
	RecallAtK    float64 `json:"recall_at_k"`
	HitRate      float64 `json:"hit_rate"`
}

type CodingLifeReport struct {
	Dataset                string                           `json:"dataset"`
	SessionCount           int                              `json:"session_count"`
	QueryCount             int                              `json:"query_count"`
	K                      int                              `json:"k"`
	CommitHash             string                           `json:"commit_hash"`
	Semantic               bool                             `json:"semantic"`
	Model                  string                           `json:"model"`
	ModelDigest            string                           `json:"model_digest,omitempty"`
	Environment            map[string]string                `json:"environment"`
	Timestamp              time.Time                        `json:"timestamp"`
	PrecisionAtK           float64                          `json:"precision_at_k"`
	RecallAtK              float64                          `json:"recall_at_k"`
	HitRate                float64                          `json:"hit_rate"`
	MRR                    float64                          `json:"mrr"`
	SearchP50US            int64                            `json:"search_p50_us"`
	SearchP95US            int64                            `json:"search_p95_us"`
	AverageEstimatedTokens float64                          `json:"average_estimated_tokens"`
	AverageCompactTokens   float64                          `json:"average_compact_estimated_tokens"`
	ByType                 map[string]CodingLifeTypeMetrics `json:"by_type"`
	Queries                []CodingLifeQueryResult          `json:"queries"`
}

// RunCodingLifeEvaluation evaluates Mindmory against the public
// coding-agent-life-v1 session/query schema and the upstream score definition:
// precision is hits/K, recall is hits/gold sessions, and hit means any gold
// session appeared in the top K. It operates only on an isolated temporary
// store populated from the supplied synthetic fixture directory.
func RunCodingLifeEvaluation(ctx context.Context, fixtureDir string, options EvaluationOptions, k int) (CodingLifeReport, error) {
	if k <= 0 || k > 50 {
		return CodingLifeReport{}, fmt.Errorf("k must be between 1 and 50, got %d", k)
	}
	var sessions []CodingLifeSession
	if err := readEvaluationJSON(filepath.Join(fixtureDir, "sessions.json"), &sessions); err != nil {
		return CodingLifeReport{}, err
	}
	var questions []CodingLifeQuestion
	if err := readEvaluationJSON(filepath.Join(fixtureDir, "queries.json"), &questions); err != nil {
		return CodingLifeReport{}, err
	}
	if err := validateCodingLifeFixture(sessions, questions); err != nil {
		return CodingLifeReport{}, err
	}

	tempDir, err := os.MkdirTemp("", "mindmory-coding-life-eval-")
	if err != nil {
		return CodingLifeReport{}, err
	}
	defer os.RemoveAll(tempDir)
	store, err := Open(filepath.Join(tempDir, "data"))
	if err != nil {
		return CodingLifeReport{}, err
	}
	principal := auth.Principal{Key: "evaluation", Type: auth.PrincipalMCP}
	session, err := store.UpsertSession(ctx, principal, "coding-life-evaluation", "coding-agent-life-v1", "project-a", time.Now().UTC())
	if err != nil {
		store.Close()
		return CodingLifeReport{}, err
	}
	for _, source := range sessions {
		content := strings.TrimSpace(source.Content)
		row := MemoryRow{
			MemoryID:        source.ID,
			Kind:            string(domain.KindProjectDecision),
			Subject:         firstRunes(content, 80),
			Content:         content,
			ContentHash:     hashContent(content),
			Lifecycle:       "ACTIVE",
			EpistemicStatus: "SOURCE_VERIFIED",
			Confidence:      1,
			Importance:      0.5,
			Sensitivity:     "NORMAL",
			ScopeType:       "PROJECT",
			ProjectKey:      "project-a",
			Activation:      0.5,
			StateVersion:    1,
		}
		if err := store.insertMemoryFixture(ctx, row); err != nil {
			store.Close()
			return CodingLifeReport{}, err
		}
	}
	dataDir := store.Dir()
	if err := store.Close(); err != nil {
		return CodingLifeReport{}, err
	}
	store, err = Open(dataDir)
	if err != nil {
		return CodingLifeReport{}, err
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
	server.Aliases = retrieval.NewAliasExpander(nil)
	if semantic {
		embedder := &OllamaEmbedder{Endpoint: options.OllamaURL, Model: options.Model, Digest: options.ModelDigest}
		if options.ModelDigest == "" {
			options.ModelDigest = resolveOllamaDigest(ctx, embedder.EndpointURL(), embedder.ModelName())
			embedder.Digest = options.ModelDigest
		}
		server.Embedder = embedder
		if _, err := store.EmbedAll(ctx, embedder); err != nil {
			return CodingLifeReport{}, fmt.Errorf("coding-life embedding: %w", err)
		}
	}
	if options.ModelDigest == "" {
		options.ModelDigest = "not-applicable"
	}
	if options.CommitHash == "" {
		options.CommitHash = evaluationCommit()
	}
	if err := store.RebuildIndex(); err != nil {
		return CodingLifeReport{}, err
	}

	report := CodingLifeReport{
		Dataset:      "coding-agent-life-v1",
		SessionCount: len(sessions),
		QueryCount:   len(questions),
		K:            k,
		CommitHash:   options.CommitHash,
		Semantic:     semantic,
		Model:        options.Model,
		ModelDigest:  options.ModelDigest,
		Timestamp:    time.Now().UTC(),
		ByType:       map[string]CodingLifeTypeMetrics{},
		Environment: map[string]string{
			"go":                      runtime.Version(),
			"os_arch":                 runtime.GOOS + "/" + runtime.GOARCH,
			"retrieval":               map[bool]string{true: "lexical+semantic", false: "lexical"}[semantic],
			"semantic_minimum_cosine": fmt.Sprintf("%.2f", minimumScore),
			"query_instruction":       strings.TrimSpace(options.QueryInstruction),
			"semantic_only":           fmt.Sprintf("%t", options.SemanticOnly),
			"rrf_fusion":              fmt.Sprintf("%t", options.RRFFusion),
			"semantic_weight":         fmt.Sprintf("%.2f", options.SemanticWeight),
			"vector_first":            fmt.Sprintf("%t", options.VectorFirst),
			"semantic_fallback":       fmt.Sprintf("%t", options.SemanticFallback),
			"semantic_high_score":     fmt.Sprintf("%.2f", options.HighScore),
			"semantic_minimum_margin": fmt.Sprintf("%.2f", options.MinimumMargin),
			"semantic_only_on_empty":  fmt.Sprintf("%t", options.OnlyOnEmpty),
			"semantic_top_one":        fmt.Sprintf("%t", options.TopOne),
		},
	}
	scope := retrieval.SessionScope{SessionID: session.SessionID, ClientKey: principal.Key, ProjectKey: "project-a"}
	latencies := make([]int64, 0, len(questions))
	typeSums := map[string]struct {
		queries int
		p, r    float64
		hits    int
	}{}
	var precisionSum, recallSum, reciprocalSum, tokenSum, compactTokenSum float64
	var hitCount int
	for _, question := range questions {
		mode := retrieval.SearchLexical
		if semantic {
			mode = retrieval.SearchSemantic
			if options.SemanticFallback {
				mode = retrieval.SearchSemanticFallback
			}
		}
		started := time.Now()
		hits, err := server.searchMemories(ctx, scope, retrieval.SearchRequest{SessionID: scope.SessionID, Query: question.Question, Limit: 50, Mode: mode}, false)
		latency := time.Since(started).Microseconds()
		if err != nil {
			return CodingLifeReport{}, err
		}
		if len(hits) > k {
			hits = hits[:k]
		}
		gold := make(map[string]bool, len(question.GoldSessionIDs))
		for _, id := range question.GoldSessionIDs {
			gold[id] = true
		}
		returned := make([]string, 0, len(hits))
		goldHits, topGoldRank := 0, 0
		for i, hit := range hits {
			returned = append(returned, hit.MemoryID)
			if gold[hit.MemoryID] {
				goldHits++
				if topGoldRank == 0 {
					topGoldRank = i + 1
				}
			}
		}
		precision := float64(goldHits) / float64(k)
		recall := float64(goldHits) / float64(len(gold))
		hit := goldHits > 0
		if hit {
			hitCount++
			reciprocalSum += 1 / float64(topGoldRank)
		}
		encoded, _ := json.Marshal(hits)
		tokens := retrieval.EstimatedTokens(string(encoded))
		compactEncoded, _ := json.Marshal(map[string]any{"hits": compactScaleHits(hits)})
		compactTokens := retrieval.EstimatedTokens(string(compactEncoded))
		precisionSum += precision
		recallSum += recall
		tokenSum += float64(tokens)
		compactTokenSum += float64(compactTokens)
		latencies = append(latencies, latency)
		report.Queries = append(report.Queries, CodingLifeQueryResult{
			ID: question.ID, Type: question.Type, Question: question.Question,
			GoldSessionIDs: append([]string(nil), question.GoldSessionIDs...), ReturnedIDs: returned,
			PrecisionAtK: precision, RecallAtK: recall, Hit: hit, TopGoldRank: topGoldRank,
			LatencyUS: latency, EstimatedTokens: tokens, CompactTokens: compactTokens,
		})
		sum := typeSums[question.Type]
		sum.queries++
		sum.p += precision
		sum.r += recall
		if hit {
			sum.hits++
		}
		typeSums[question.Type] = sum
	}
	count := float64(len(questions))
	report.PrecisionAtK = precisionSum / count
	report.RecallAtK = recallSum / count
	report.HitRate = float64(hitCount) / count
	report.MRR = reciprocalSum / count
	report.AverageEstimatedTokens = tokenSum / count
	report.AverageCompactTokens = compactTokenSum / count
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	report.SearchP50US = percentile(latencies, 0.50)
	report.SearchP95US = percentile(latencies, 0.95)
	for name, sum := range typeSums {
		n := float64(sum.queries)
		report.ByType[name] = CodingLifeTypeMetrics{
			Queries: sum.queries, PrecisionAtK: sum.p / n, RecallAtK: sum.r / n, HitRate: float64(sum.hits) / n,
		}
	}
	return report, nil
}

func readEvaluationJSON(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func validateCodingLifeFixture(sessions []CodingLifeSession, questions []CodingLifeQuestion) error {
	if len(sessions) == 0 || len(questions) == 0 {
		return fmt.Errorf("coding-life fixture requires non-empty sessions and queries")
	}
	ids := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Content) == "" {
			return fmt.Errorf("coding-life session id and content are required")
		}
		if ids[session.ID] {
			return fmt.Errorf("duplicate coding-life session id %q", session.ID)
		}
		ids[session.ID] = true
	}
	questionIDs := map[string]bool{}
	for _, question := range questions {
		if strings.TrimSpace(question.ID) == "" || strings.TrimSpace(question.Question) == "" || len(question.GoldSessionIDs) == 0 {
			return fmt.Errorf("coding-life question id, text, and goldSessionIds are required")
		}
		if questionIDs[question.ID] {
			return fmt.Errorf("duplicate coding-life question id %q", question.ID)
		}
		questionIDs[question.ID] = true
		for _, id := range question.GoldSessionIDs {
			if !ids[id] {
				return fmt.Errorf("question %q references unknown gold session %q", question.ID, id)
			}
		}
	}
	return nil
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
