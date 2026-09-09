package lite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunCodingLifeEvaluationUsesUpstreamScoringShape(t *testing.T) {
	dir := t.TempDir()
	sessions := `[
  {"id":"s1","timestamp":"2026-01-01T00:00:00Z","content":"fixed authentication token precedence in pull request eleven"},
  {"id":"s2","timestamp":"2026-01-02T00:00:00Z","content":"added multi architecture container builds"}
]`
	queries := `[
  {"id":"q1","type":"bug","question":"authentication token precedence","answer":"PR eleven","goldSessionIds":["s1"]}
]`
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(sessions), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "queries.json"), []byte(queries), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := RunCodingLifeEvaluation(context.Background(), dir, EvaluationOptions{Model: "none"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.PrecisionAtK != 1 || report.RecallAtK != 1 || report.HitRate != 1 || report.MRR != 1 {
		t.Fatalf("unexpected metrics: %+v", report)
	}
	if len(report.Queries) != 1 || report.Queries[0].ReturnedIDs[0] != "s1" || report.Queries[0].EstimatedTokens == 0 {
		t.Fatalf("unexpected query evidence: %+v", report.Queries)
	}
	if report.AverageCompactTokens <= 0 || report.AverageCompactTokens > report.AverageEstimatedTokens {
		t.Fatalf("unexpected compact token metrics: %+v", report)
	}
}

func TestValidateCodingLifeFixtureRejectsUnknownGoldSession(t *testing.T) {
	err := validateCodingLifeFixture(
		[]CodingLifeSession{{ID: "s1", Content: "memory"}},
		[]CodingLifeQuestion{{ID: "q1", Question: "query", GoldSessionIDs: []string{"missing"}}},
	)
	if err == nil {
		t.Fatal("unknown gold session accepted")
	}
}
