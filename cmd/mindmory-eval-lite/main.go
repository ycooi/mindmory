// Command mindmory-eval-lite runs the independent fixture-owned retrieval
// corpus locally. It never connects to Docker or production memory data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mindmory.local/core/internal/lite"
)

func main() {
	corpus := flag.String("corpus", "tests/corpus/lite-eval-v2.json", "fixture corpus path")
	codingLifeDir := flag.String("coding-life-dir", "", "external coding-agent-life-v1 fixture directory containing sessions.json and queries.json")
	k := flag.Int("k", 5, "retrieval cutoff for --coding-life-dir evaluation")
	scale := flag.Bool("scale", false, "run isolated synthetic short/medium/long/superlong lexical scale evaluation")
	scaleSizes := flag.String("scale-sizes", "240,1000,10000,50000", "comma-separated memory counts for --scale")
	scaleQueries := flag.Int("scale-queries", 50, "scored queries per corpus size for --scale")
	output := flag.String("output", "", "result JSON path (required)")
	semantic := flag.Bool("semantic", false, "enable semantic retrieval in the evaluated server")
	model := flag.String("model", "qwen3-embedding:0.6b", "embedding model identity")
	modelDigest := flag.String("model-digest", "", "embedding model digest for reproducibility")
	commitHash := flag.String("commit", os.Getenv("MINDMORY_COMMIT_HASH"), "source commit/revision identifier")
	ollama := flag.String("ollama", "http://127.0.0.1:11434", "local Ollama endpoint")
	queryInstruction := flag.String("query-instruction", "", "experiment: instruction prepended to semantic queries")
	semanticOnly := flag.Bool("semantic-only", false, "experiment: exclude lexical candidates from explicit semantic ranking")
	minimumScore := flag.Float64("semantic-min-score", 0.68, "experiment: minimum cosine score")
	rrfFusion := flag.Bool("semantic-rrf", false, "experiment: weighted reciprocal-rank fusion for weak lexical plus semantic candidates")
	semanticWeight := flag.Float64("semantic-weight", 2, "experiment: semantic weight in reciprocal-rank fusion")
	vectorFirst := flag.Bool("semantic-vector-first", false, "experiment: preserve vector order, then fill with weak lexical candidates")
	semanticFallback := flag.Bool("semantic-fallback", false, "experiment: use production lexical-first semantic fallback mode")
	highScore := flag.Float64("semantic-high-score", 0, "experiment: accept below this score only when the top-candidate margin passes")
	minimumMargin := flag.Float64("semantic-min-margin", 0, "experiment: required top1-top2 margin below the high-confidence score")
	onlyOnEmpty := flag.Bool("semantic-only-on-empty", false, "experiment: fallback only when lexical ranking returns no hits")
	topOne := flag.Bool("semantic-top-one", false, "experiment: admit only the top accepted semantic rescue candidate")
	timeout := flag.Duration("timeout", 20*time.Minute, "evaluation timeout")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "--output is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	options := lite.EvaluationOptions{
		Semantic: *semantic, Model: *model, ModelDigest: *modelDigest, OllamaURL: *ollama, CommitHash: *commitHash,
		QueryInstruction: *queryInstruction,
		SemanticOnly:     *semanticOnly,
		MinimumScore:     *minimumScore,
		RRFFusion:        *rrfFusion,
		SemanticWeight:   *semanticWeight,
		VectorFirst:      *vectorFirst,
		SemanticFallback: *semanticFallback,
		HighScore:        *highScore,
		MinimumMargin:    *minimumMargin,
		OnlyOnEmpty:      *onlyOnEmpty,
		TopOne:           *topOne,
	}
	if *scale {
		sizes, err := parseSizes(*scaleSizes)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid --scale-sizes:", err)
			os.Exit(2)
		}
		report, err := lite.RunScaleEvaluation(ctx, sizes, *scaleQueries, options)
		if err != nil {
			fmt.Fprintln(os.Stderr, "scale evaluation failed:", err)
			os.Exit(1)
		}
		if err := writeReport(*output, report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, result := range report.Results {
			fmt.Printf("scale=%s memories=%d queries=%d r@1=%.3f r@5=%.3f fpr=%.3f stable=%.3f p50=%.2fms p95=%.2fms tokens=%.1f compact_tokens=%.1f\n",
				result.Label, result.CorpusMemories, result.QueryCount, result.RecallAt1, result.RecallAt5,
				result.NegativeFalsePositive, result.StableRepeatRate, float64(result.SearchP50US)/1000,
				float64(result.SearchP95US)/1000, result.AverageEstimatedTokens, result.AverageCompactTokens)
		}
		return
	}
	if *codingLifeDir != "" {
		report, err := lite.RunCodingLifeEvaluation(ctx, *codingLifeDir, options, *k)
		if err != nil {
			fmt.Fprintln(os.Stderr, "coding-life evaluation failed:", err)
			os.Exit(1)
		}
		if err := writeReport(*output, report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("dataset=%s sessions=%d queries=%d p@%d=%.3f r@%d=%.3f hit=%.3f mrr=%.3f tokens=%.1f compact_tokens=%.1f output=%s\n",
			report.Dataset, report.SessionCount, report.QueryCount, report.K, report.PrecisionAtK,
			report.K, report.RecallAtK, report.HitRate, report.MRR, report.AverageEstimatedTokens, report.AverageCompactTokens, *output)
		return
	}
	report, err := lite.RunEvaluation(ctx, *corpus, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
	if err := writeReport(*output, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cases=%d recall@1=%.3f recall@5=%.3f recall@10=%.3f mrr@10=%.3f negative_fpr=%.3f leakage=%.3f output=%s\n",
		report.CorpusCases, report.RecallAt1, report.RecallAt5, report.RecallAt10, report.MRRAt10,
		report.NegativeFalsePositiveRate, report.SecretInstructionLeakRate+report.CrossProjectLeakRate+report.LifecycleLeakRate, *output)
}

func parseSizes(value string) ([]int, error) {
	var sizes []int
	for _, part := range strings.Split(value, ",") {
		size, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		sizes = append(sizes, size)
	}
	return sizes, nil
}

func writeReport(path string, report any) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o640)
}
