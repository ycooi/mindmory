// Package lite: configurable embedding + vector search.
//
// The vector layer completes the three-layer design specified by the project owner:
// JSONL canonical, SQLite keyword index, and a vector index for semantic
// search. Embeddings may come from local Ollama or an explicitly configured
// OpenAI-compatible endpoint and live only in a disposable vector projection.
// Canonical memory JSONL keeps human-readable text and hashes; it never
// carries large derived vectors.
//
// The corpus projection is persisted and scanned through vectorstore; this
// file intentionally contains only embedding-provider clients.
package lite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Embedder embeds text into vectors via a configured provider.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

func embeddingModelName(embedder Embedder) string {
	if named, ok := embedder.(interface{ ModelName() string }); ok {
		return named.ModelName()
	}
	return "unknown"
}

func embeddingModelDigest(embedder Embedder) string {
	if identified, ok := embedder.(interface{ ModelDigest() string }); ok {
		return identified.ModelDigest()
	}
	return ""
}

// OllamaEmbedder calls the local Ollama embedding API.
type OllamaEmbedder struct {
	Endpoint   string // e.g. http://localhost:11434
	Model      string // e.g. qwen3-embedding:0.6b
	Digest     string // stable model identity, e.g. sha256:...
	Path       string
	APIKey     string
	Dimensions int
	Client     *http.Client
}

func (o *OllamaEmbedder) EndpointURL() string {
	if o.Endpoint == "" {
		return "http://localhost:11434"
	}
	return o.Endpoint
}

func (o *OllamaEmbedder) ModelName() string {
	if o.Model == "" {
		return "qwen3-embedding:0.6b"
	}
	return o.Model
}

func (o *OllamaEmbedder) ModelDigest() string { return strings.TrimSpace(o.Digest) }

type ollamaEmbedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns one vector per input text. The 0.6b model embeds a batch in
// one call; batch size is bounded to keep the request small.
func (o *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 60 * time.Second}
	}
	const batch = 16
	var out [][]float32
	for start := 0; start < len(texts); start += batch {
		end := start + batch
		if end > len(texts) {
			end = len(texts)
		}
		body, err := json.Marshal(ollamaEmbedRequest{Model: o.ModelName(), Input: texts[start:end], Dimensions: o.Dimensions})
		if err != nil {
			return nil, err
		}
		path := o.Path
		if path == "" {
			path = "/api/embed"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(o.EndpointURL(), "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if o.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+o.APIKey)
		}
		resp, err := o.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama embed: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("ollama embed: HTTP %d", resp.StatusCode)
		}
		var parsed ollamaEmbedResponse
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&parsed)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, parsed.Embeddings...)
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("ollama embed count mismatch: got %d want %d", len(out), len(texts))
	}
	return out, nil
}

// OpenAICompatibleEmbedder supports hosted services exposing the standard
// POST /v1/embeddings request/response shape. Configuration validation owns
// remote opt-in, HTTPS, API-key, and stable model-digest requirements.
type OpenAICompatibleEmbedder struct {
	Endpoint, Path, Model, Digest, APIKey string
	Dimensions                            int
	Client                                *http.Client
}

func (o *OpenAICompatibleEmbedder) ModelName() string   { return o.Model }
func (o *OpenAICompatibleEmbedder) ModelDigest() string { return o.Digest }

type compatibleEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}
type compatibleEmbedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (o *OpenAICompatibleEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	path := o.Path
	if path == "" {
		path = "/v1/embeddings"
	}
	const batch = 16
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := start + batch
		if end > len(texts) {
			end = len(texts)
		}
		body, err := json.Marshal(compatibleEmbedRequest{Model: o.Model, Input: texts[start:end], Dimensions: o.Dimensions, EncodingFormat: "float"})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.Endpoint, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("external embed: %w", err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, fmt.Errorf("external embed: HTTP %d", response.StatusCode)
		}
		var parsed compatibleEmbedResponse
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&parsed)
		response.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("external embed response: %w", decodeErr)
		}
		sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
		if len(parsed.Data) != end-start {
			return nil, fmt.Errorf("external embed count mismatch: got %d want %d", len(parsed.Data), end-start)
		}
		for i, item := range parsed.Data {
			if item.Index != i || len(item.Embedding) == 0 {
				return nil, fmt.Errorf("external embed invalid index/vector")
			}
			out = append(out, item.Embedding)
		}
	}
	return out, nil
}

func NewConfiguredEmbedder(cfg EmbeddingConfig) (Embedder, error) {
	client := &http.Client{Timeout: cfg.Timeout}
	switch cfg.Provider {
	case "disabled":
		return nil, nil
	case "ollama":
		return &OllamaEmbedder{Endpoint: cfg.Endpoint, Path: cfg.Path, Model: cfg.Model, Digest: cfg.ModelDigest, APIKey: cfg.APIKey, Dimensions: cfg.Dimensions, Client: client}, nil
	case "openai-compatible":
		return &OpenAICompatibleEmbedder{Endpoint: cfg.Endpoint, Path: cfg.Path, Model: cfg.Model, Digest: cfg.ModelDigest, APIKey: cfg.APIKey, Dimensions: cfg.Dimensions, Client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", cfg.Provider)
	}
}
