package lite

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mindmory.local/core/internal/config"
)

// EnvConfig is the minimal environment contract for the lite daemon. It
// reads exactly what the harness needs — nothing more.
type EnvConfig struct {
	Owner            string
	CursorKey        string
	MCPClients       map[string]config.MCPPrincipalConfig
	Storage          StorageConfig
	Embedding        EmbeddingConfig
	SemanticEnabled  bool
	LowRAMExperiment bool
}

type StorageConfig struct {
	RootDir, DataDir, DerivedDir, VectorDir, SnapshotDir, ExportDir string
}

type EmbeddingConfig struct {
	Provider, Endpoint, Path, Model, ModelDigest, APIKey string
	Dimensions                                           int
	Timeout                                              time.Duration
	AllowRemote                                          bool
}

// LoadEnv parses the lite daemon environment.
func LoadEnv(lookup func(string) (string, bool)) (EnvConfig, error) {
	value := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	cfg := EnvConfig{Owner: value("MINDMORY_OWNER"), CursorKey: value("MINDMORY_CURSOR_SIGNING_KEY")}
	if cfg.Owner == "" {
		return cfg, errors.New("MINDMORY_OWNER is required")
	}
	if len(cfg.CursorKey) < 32 {
		return cfg, errors.New("MINDMORY_CURSOR_SIGNING_KEY must be at least 32 characters")
	}
	raw := value("MINDMORY_MCP_CLIENT_TOKENS_JSON")
	if raw == "" {
		return cfg, errors.New("MINDMORY_MCP_CLIENT_TOKENS_JSON is required")
	}
	var principals map[string]config.MCPPrincipalConfig
	if err := json.Unmarshal([]byte(raw), &principals); err != nil {
		return cfg, fmt.Errorf("MINDMORY_MCP_CLIENT_TOKENS_JSON: %w", err)
	}
	if len(principals) == 0 {
		return cfg, errors.New("MINDMORY_MCP_CLIENT_TOKENS_JSON: at least one principal required")
	}
	for key, principal := range principals {
		if strings.TrimSpace(key) == "" || len(principal.Token) < 24 || len(principal.Capabilities) == 0 {
			return cfg, errors.New("MINDMORY_MCP_CLIENT_TOKENS_JSON: invalid principal entry")
		}
	}
	cfg.MCPClients = principals
	root := value("MINDMORY_ROOT_DIR")
	if root == "" {
		root = "."
	}
	cfg.Storage.RootDir = filepath.Clean(root)
	cfg.Storage.DataDir = storagePath(root, value("MINDMORY_DATA_DIR"), filepath.Join("var", "data"))
	cfg.Storage.DerivedDir = storagePath(root, value("MINDMORY_DERIVED_DIR"), filepath.Join("var", "derived"))
	cfg.Storage.VectorDir = storagePath(root, value("MINDMORY_VECTOR_DIR"), filepath.Join("var", "derived", "vectors"))
	cfg.Storage.SnapshotDir = storagePath(root, value("MINDMORY_SNAPSHOT_DIR"), filepath.Join("var", "data", "snapshots"))
	cfg.Storage.ExportDir = storagePath(root, value("MINDMORY_EXPORT_DIR"), filepath.Join("var", "export"))
	if pathsOverlap(cfg.Storage.DataDir, cfg.Storage.DerivedDir) || pathsOverlap(cfg.Storage.DataDir, cfg.Storage.VectorDir) {
		return cfg, errors.New("derived/vector directories must not overlap canonical data directory")
	}

	cfg.Embedding.Provider = strings.ToLower(defaultString(value("MINDMORY_EMBED_PROVIDER"), "ollama"))
	cfg.Embedding.Model = defaultString(value("MINDMORY_EMBED_MODEL"), "qwen3-embedding:0.6b")
	cfg.Embedding.ModelDigest = value("MINDMORY_EMBED_MODEL_DIGEST")
	cfg.Embedding.APIKey = value("MINDMORY_EMBED_API_KEY")
	cfg.Embedding.AllowRemote = value("MINDMORY_EMBED_ALLOW_REMOTE") == "1"
	cfg.Embedding.Path = value("MINDMORY_EMBED_PATH")
	cfg.Embedding.Timeout = 60 * time.Second
	if rawTimeout := value("MINDMORY_EMBED_TIMEOUT"); rawTimeout != "" {
		parsed, parseErr := time.ParseDuration(rawTimeout)
		if parseErr != nil || parsed <= 0 || parsed > 10*time.Minute {
			return cfg, errors.New("MINDMORY_EMBED_TIMEOUT must be between 1ns and 10m")
		}
		cfg.Embedding.Timeout = parsed
	}
	if rawDimensions := value("MINDMORY_EMBED_DIMENSIONS"); rawDimensions != "" {
		dimensions, parseErr := strconv.Atoi(rawDimensions)
		if parseErr != nil || dimensions <= 0 || dimensions > 65536 {
			return cfg, errors.New("MINDMORY_EMBED_DIMENSIONS must be between 1 and 65536")
		}
		cfg.Embedding.Dimensions = dimensions
	}
	switch cfg.Embedding.Provider {
	case "disabled":
		cfg.Embedding.Endpoint = ""
	case "ollama":
		cfg.Embedding.Endpoint = defaultString(value("MINDMORY_EMBED_ENDPOINT"), "http://127.0.0.1:11434")
		if cfg.Embedding.Path == "" {
			cfg.Embedding.Path = "/api/embed"
		}
	case "openai-compatible":
		cfg.Embedding.Endpoint = value("MINDMORY_EMBED_ENDPOINT")
		if cfg.Embedding.Endpoint == "" {
			return cfg, errors.New("MINDMORY_EMBED_ENDPOINT is required for openai-compatible provider")
		}
		if cfg.Embedding.Path == "" {
			cfg.Embedding.Path = "/v1/embeddings"
		}
		if cfg.Embedding.ModelDigest == "" {
			return cfg, errors.New("MINDMORY_EMBED_MODEL_DIGEST is required for openai-compatible provider")
		}
	default:
		return cfg, errors.New("MINDMORY_EMBED_PROVIDER must be disabled, ollama, or openai-compatible")
	}
	if cfg.Embedding.Provider != "disabled" {
		if err := validateEmbeddingEndpoint(cfg.Embedding); err != nil {
			return cfg, err
		}
	}
	cfg.SemanticEnabled = value("MINDMORY_SEMANTIC_SEARCH") == "1"
	cfg.LowRAMExperiment = value("MINDMORY_LOW_RAM_EXPERIMENT") == "1"
	return cfg, nil
}

func storagePath(root, value, fallbackRelative string) string {
	selected := strings.TrimSpace(value)
	if selected == "" {
		selected = fallbackRelative
	}
	if filepath.IsAbs(selected) {
		return filepath.Clean(selected)
	}
	return filepath.Clean(filepath.Join(root, selected))
}
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
func pathsOverlap(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return contains(aa, bb) || contains(bb, aa)
}
func validateEmbeddingEndpoint(cfg EmbeddingConfig) error {
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("MINDMORY_EMBED_ENDPOINT must be a clean http(s) base URL")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback {
		if !cfg.AllowRemote {
			return errors.New("remote embedding endpoint requires MINDMORY_EMBED_ALLOW_REMOTE=1")
		}
		if parsed.Scheme != "https" {
			return errors.New("remote embedding endpoint must use HTTPS")
		}
		if cfg.APIKey == "" {
			return errors.New("remote embedding endpoint requires MINDMORY_EMBED_API_KEY")
		}
	}
	if !strings.HasPrefix(cfg.Path, "/") || strings.Contains(cfg.Path, "..") || strings.ContainsAny(cfg.Path, "?#") {
		return errors.New("MINDMORY_EMBED_PATH must be an absolute clean URL path")
	}
	return nil
}

// LookupEnv is os.LookupEnv.
func LookupEnv(key string) (string, bool) { return os.LookupEnv(key) }
