package mcpserver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type benchmarkRoundTrip func(*http.Request) (*http.Response, error)

func (f benchmarkRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func benchmarkRuntime() Runtime {
	transport := benchmarkRoundTrip(func(request *http.Request) (*http.Response, error) {
		body := `{"results":[]}`
		switch request.URL.Path {
		case "/v1/system/status":
			body = `{"state":"READY","incidents":[]}`
		case "/v1/context/packet":
			body = `{"continuity_cursor":"opaque","memories":[],"truncated":false,"returned_chars":0}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	return Runtime{
		Client: Client{
			Endpoint: "http://mindmory.benchmark",
			Token:    "benchmark-client-token-0123456789",
			HTTP:     &http.Client{Transport: transport},
		},
		SessionID: "benchmark-session",
	}
}

func BenchmarkMemorySearchDispatch(b *testing.B) {
	runtime := benchmarkRuntime()
	ctx := context.Background()
	request := &mcp.CallToolRequest{}
	input := SearchInput{Query: "release policy", Limit: 4}
	gateway := GatewayInput{Action: "memory_search", Args: map[string]any{"query": input.Query, "limit": input.Limit}}

	b.Run("full_direct", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := runtime.contextSearch(ctx, request, input); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compact_gateway", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := runtime.gateway(ctx, request, gateway); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkMemoryContextDispatch(b *testing.B) {
	runtime := benchmarkRuntime()
	ctx := context.Background()
	request := &mcp.CallToolRequest{}
	input := ContextInput{Query: "release policy", MaxChars: compactContextChars}
	gateway := GatewayInput{Action: "memory_context", Args: map[string]any{"query": input.Query, "max_chars": input.MaxChars}}

	b.Run("full_direct", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := runtime.memoryContext(ctx, request, input); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compact_gateway", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := runtime.gateway(ctx, request, gateway); err != nil {
				b.Fatal(err)
			}
		}
	})
}
