package debugnode

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogObserverEmitsOnlySafeFields(t *testing.T) {
	var output bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&output, nil))}
	observer.Observe(context.Background(), Event{Node: MutationStage, TraceID: "trace-1", Status: "complete", ReasonCode: "NO_AUTHORITY"})
	value := output.String()
	for _, required := range []string{`"debug_node":"MUTATION.STAGE"`, `"trace_id":"trace-1"`, `"reason_code":"NO_AUTHORITY"`} {
		if !strings.Contains(value, required) {
			t.Fatalf("missing %s in %s", required, value)
		}
	}
	for _, forbidden := range []string{"message_text", "evidence_quote", "artifact_bytes", "credential"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("unsafe field %q in log", forbidden)
		}
	}
}
