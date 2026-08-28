// Command mindmory-eval-lite runs the independent fixture-owned retrieval
// corpus locally. It never connects to Docker or production memory data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mindmory.local/core/internal/lite"
)

func main() {
	corpus := flag.String("corpus", "tests/corpus/lite-eval-v2.json", "fixture corpus path")
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
	report, err := lite.RunEvaluation(ctx, *corpus, lite.EvaluationOptions{
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
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(*output, raw, 0o640); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cases=%d recall@1=%.3f recall@5=%.3f recall@10=%.3f mrr@10=%.3f negative_fpr=%.3f leakage=%.3f output=%s\n",
		report.CorpusCases, report.RecallAt1, report.RecallAt5, report.RecallAt10, report.MRRAt10,
		report.NegativeFalsePositiveRate, report.SecretInstructionLeakRate+report.CrossProjectLeakRate+report.LifecycleLeakRate, *output)
}
