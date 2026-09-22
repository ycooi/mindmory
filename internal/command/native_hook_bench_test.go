package command

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkHookAdapters(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/v1/checkpoints" {
			_, _ = io.WriteString(w, `{"session_id":"00000000-0000-4000-8000-000000000123"}`)
			return
		}
		_, _ = io.WriteString(w, `{"memories":[{"subject":"Rollback rule","content":"Keep the previous verified artifact available."}]}`)
	}))
	b.Cleanup(server.Close)
	b.Setenv("MINDMORY_ENDPOINT", server.URL)
	b.Setenv("MINDMORY_MCP_TOKEN", "client-token-at-least-24-characters")
	input := `{"session_id":"benchmark-thread","turn_id":"benchmark-turn","hook_event_name":"UserPromptSubmit","prompt":"what is the rollback rule?","cwd":"/project"}`

	b.Run("checkpoint-only", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if code := runCheckpointHook([]string{"--host", "codex"}, strings.NewReader(input)); code != 0 {
				b.Fatalf("code=%d", code)
			}
		}
	})
	b.Run("native-checkpoint-and-relevance", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var output bytes.Buffer
			if code := runNativeHook(nil, strings.NewReader(input), &output); code != 0 {
				b.Fatalf("code=%d", code)
			}
		}
	})
}
