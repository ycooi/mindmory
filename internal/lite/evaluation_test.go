package lite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIndependentEvaluationCorpusMeetsGateH(t *testing.T) {
	report, err := RunEvaluation(context.Background(), filepath.Join("..", "..", "tests", "corpus", "lite-eval-v2.json"),
		EvaluationOptions{Semantic: false, Model: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if report.CorpusCases != 200 || len(report.Queries) != 200 {
		t.Fatalf("fixture expansion=%d query results=%d", report.CorpusCases, len(report.Queries))
	}
	if report.CommitHash == "" || report.ModelDigest == "" || report.Timestamp.IsZero() {
		t.Fatalf("reproducibility metadata missing: commit=%q digest=%q timestamp=%v", report.CommitHash, report.ModelDigest, report.Timestamp)
	}
	counts := map[string]int{}
	for _, result := range report.Queries {
		counts[result.Category]++
	}
	want := map[string]int{"exact_en": 20, "exact_zh": 20, "mixed": 20, "typo": 20, "paraphrase": 30,
		"negative": 30, "lifecycle": 15, "sensitivity": 15, "project_isolation": 15, "alias": 15}
	for category, count := range want {
		if counts[category] != count {
			t.Fatalf("category %s count=%d want=%d", category, counts[category], count)
		}
	}
	if report.RecallAt5 < 0.95 || report.RecallAt10 < 0.95 || report.MRRAt10 < 0.80 {
		t.Fatalf("retrieval regression: r5=%.3f r10=%.3f mrr=%.3f", report.RecallAt5, report.RecallAt10, report.MRRAt10)
	}
	if report.NegativeFalsePositiveRate != 0 || report.SecretInstructionLeakRate != 0 ||
		report.CrossProjectLeakRate != 0 || report.LifecycleLeakRate != 0 {
		t.Fatalf("evaluation leakage/fpr: %+v", report)
	}
	if report.SearchP50US <= 0 || report.SearchP95US < report.SearchP50US || report.ContextCharacters <= 0 {
		t.Fatalf("missing operational metrics: %+v", report)
	}
}

func TestSemanticEvaluationFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("MINDMORY_SEMANTIC_SEARCH", "1")
	disabled := false
	server := &Server{SemanticSearch: &disabled}
	if server.semanticEnabled() {
		t.Fatal("explicit --semantic=false did not control server")
	}
	enabled := true
	server.SemanticSearch = &enabled
	if !server.semanticEnabled() {
		t.Fatal("explicit --semantic=true did not control server")
	}
}

func TestSemanticChallengeCorpusHasNoLexicalShortcut(t *testing.T) {
	report, err := RunEvaluation(context.Background(), filepath.Join("..", "..", "tests", "corpus", "lite-semantic-challenge-v1.json"), EvaluationOptions{Semantic: false, Model: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if report.CorpusCases != 80 {
		t.Fatalf("cases=%d", report.CorpusCases)
	}
	if report.RecallAt1 != 0 || report.RecallAt5 > 0.20 {
		t.Fatalf("semantic challenge has lexical shortcut: r1=%.3f r5=%.3f", report.RecallAt1, report.RecallAt5)
	}
	if report.NegativeFalsePositiveRate != 0 || report.SecretInstructionLeakRate != 0 || report.CrossProjectLeakRate != 0 || report.LifecycleLeakRate != 0 {
		t.Fatalf("challenge leakage/fpr: %+v", report)
	}
}
