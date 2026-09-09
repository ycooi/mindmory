package lite

import (
	"context"
	"testing"
)

func TestGenerateScaleScenarioShape(t *testing.T) {
	rows, queries := generateScaleScenario(240, 50)
	if len(rows) != 240 || len(queries) != 50 {
		t.Fatalf("unexpected scale shape: rows=%d queries=%d", len(rows), len(queries))
	}
	positive, negative := 0, 0
	for _, query := range queries {
		if query.expectNone {
			negative++
		} else if query.expectedID != "" {
			positive++
		}
	}
	if positive != 40 || negative != 10 {
		t.Fatalf("unexpected query split: positive=%d negative=%d", positive, negative)
	}
}

func TestRunScaleEvaluationSmall(t *testing.T) {
	report, err := RunScaleEvaluation(context.Background(), []int{60}, 10, EvaluationOptions{Model: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalCases != 10 || report.SearchInvocations != 20 || len(report.Results) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	result := report.Results[0]
	if result.RecallAt1 != 1 || result.RecallAt5 != 1 || result.RecallAt10 != 1 || result.NegativeFalsePositive != 0 || result.StableRepeatRate != 1 {
		t.Fatalf("scale gate failed: %+v", result)
	}
	if result.AverageCompactTokens <= 0 || result.AverageCompactTokens > result.AverageEstimatedTokens {
		t.Fatalf("compact token metric invalid: compact=%.1f full=%.1f", result.AverageCompactTokens, result.AverageEstimatedTokens)
	}
}
