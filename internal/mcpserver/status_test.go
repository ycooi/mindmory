package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionRequiredStatusQuarantinesOrdinaryMCPRequests(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/system/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "ACTION_REQUIRED", "configuration": map[string]any{"storage": map[string]any{"canonical_dir": "/safe/canonical"}}, "statistics": map[string]any{"memories": 12}, "incidents": []map[string]any{{
				"code": "EMBEDDING_MODEL_MISMATCH", "incident_id": "inc_test", "severity": "ACTION_REQUIRED",
				"data_safety": "Canonical memory remains safe.", "operator_message": "Model identity changed.",
				"copy_paste_commands": []string{"mindmoryctl vectors rebuild --incident-id inc_test --confirm"},
				"agent_instruction":   "Show this warning to the user exactly.",
			}}})
			return
		}
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL, Token: "token", HTTP: server.Client()}
	_, err := client.Do(context.Background(), http.MethodGet, "/ordinary", nil)
	var action *ActionRequiredError
	if !errors.As(err, &action) || called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	if !strings.Contains(err.Error(), "inc_test") || !strings.Contains(err.Error(), "mindmoryctl vectors rebuild") {
		t.Fatalf("agent warning=%q", err)
	}
	status, err := client.SystemStatus(context.Background())
	if err != nil || status.State != "ACTION_REQUIRED" || status.Incidents[0].IncidentID != "inc_test" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if status.Configuration["storage"] == nil || status.Statistics["memories"] != float64(12) {
		t.Fatalf("read-only information lost: %+v", status)
	}
}
