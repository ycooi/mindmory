package lite

import (
	"context"
	"errors"
	"math"
	"testing"

	"mindmory.local/core/internal/retrieval"
)

type certificationEmbedder struct {
	contract RetrievalProviderContract
	vectors  [][]float32
	err      error
	calls    int
}

func (e *certificationEmbedder) ProviderContract() RetrievalProviderContract { return e.contract }
func (e *certificationEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	e.calls++
	return e.vectors, e.err
}

func validProviderContract() RetrievalProviderContract {
	return RetrievalProviderContract{
		ContractVersion:  RetrievalProviderContractVersion,
		Provider:         "synthetic",
		Surface:          "embedding",
		Locality:         "local",
		ModelName:        "synthetic-v1",
		SupportsSemantic: true,
		SupportsBatch:    true,
		MaximumBatchSize: 16,
	}
}

func TestConfiguredProvidersExposeValidAuthorityIsolatedContracts(t *testing.T) {
	providers := []Embedder{
		&OllamaEmbedder{Model: "local-model", Digest: "sha256:local", Dimensions: 3},
		&OpenAICompatibleEmbedder{Model: "remote-model", Digest: "sha256:remote", Dimensions: 3},
	}
	for _, provider := range providers {
		contract := provider.ProviderContract()
		if err := contract.Validate(); err != nil {
			t.Fatalf("provider=%s contract rejected: %v", contract.Provider, err)
		}
		if contract.ReadsCanonical || contract.MutatesCanonical {
			t.Fatalf("provider=%s gained canonical authority", contract.Provider)
		}
	}
}

func TestProviderContractDerivesPrivacyFromEndpointRatherThanProviderBrand(t *testing.T) {
	tests := []struct {
		name     string
		provider Embedder
		locality string
		sends    bool
	}{
		{"remote ollama", &OllamaEmbedder{Endpoint: "https://ollama.example.com", Model: "model"}, "remote", true},
		{"local compatible", &OpenAICompatibleEmbedder{Endpoint: "http://127.0.0.1:8080", Model: "model"}, "local", false},
		{"ipv6 loopback compatible", &OpenAICompatibleEmbedder{Endpoint: "http://[::1]:8080", Model: "model"}, "local", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := test.provider.ProviderContract()
			if contract.Locality != test.locality || contract.SendsContentOffDevice != test.sends {
				t.Fatalf("privacy disclosure mismatch: %+v", contract)
			}
			if err := contract.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderContractFailsClosedOnCanonicalAuthorityOrPrivacyMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*RetrievalProviderContract){
		"canonical read":  func(c *RetrievalProviderContract) { c.ReadsCanonical = true },
		"canonical write": func(c *RetrievalProviderContract) { c.MutatesCanonical = true },
		"hidden remote disclosure": func(c *RetrievalProviderContract) {
			c.Locality = "remote"
			c.SendsContentOffDevice = false
		},
		"unsupported version": func(c *RetrievalProviderContract) { c.ContractVersion = "v2" },
	} {
		t.Run(name, func(t *testing.T) {
			contract := validProviderContract()
			mutate(&contract)
			if err := contract.Validate(); err == nil {
				t.Fatal("invalid provider contract accepted")
			}
		})
	}
}

func TestProviderCertificationProbesSyntheticVectors(t *testing.T) {
	provider := &certificationEmbedder{contract: validProviderContract(), vectors: [][]float32{{1, 0, 0}, {0, 1, 0}}}
	report := CertifyEmbeddingProvider(context.Background(), provider, true)
	if !report.Passed || !report.ProbeRun || len(report.Checks) != 6 {
		t.Fatalf("report=%+v", report)
	}
}

func TestProviderCertificationRejectsMalformedOrUnavailableProviderOutput(t *testing.T) {
	tests := []struct {
		name     string
		provider *certificationEmbedder
	}{
		{"offline", &certificationEmbedder{contract: validProviderContract(), err: errors.New("offline")}},
		{"wrong cardinality", &certificationEmbedder{contract: validProviderContract(), vectors: [][]float32{{1}}}},
		{"empty vector", &certificationEmbedder{contract: validProviderContract(), vectors: [][]float32{{}, {}}}},
		{"dimension drift", &certificationEmbedder{contract: validProviderContract(), vectors: [][]float32{{1}, {1, 2}}}},
		{"non finite", &certificationEmbedder{contract: validProviderContract(), vectors: [][]float32{{float32(math.NaN())}, {1}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if report := CertifyEmbeddingProvider(context.Background(), test.provider, true); report.Passed {
				t.Fatalf("malformed provider passed: %+v", report)
			}
		})
	}
}

func TestVectorSyncRejectsProviderBeforeCanonicalTextLeavesStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.insertMemoryFixture(context.Background(), persistentFixture("private-memory", "private subject", "private canonical content")); err != nil {
		t.Fatal(err)
	}
	contract := validProviderContract()
	contract.MutatesCanonical = true
	provider := &certificationEmbedder{contract: contract, vectors: [][]float32{{1}}}
	if _, err := store.SyncVectors(context.Background(), provider, VectorSyncOptions{}); err == nil {
		t.Fatal("provider with canonical authority was accepted")
	}
	if provider.calls != 0 {
		t.Fatalf("rejected provider received canonical-derived text: calls=%d", provider.calls)
	}
}

func TestSemanticQueryRejectsInvalidProviderBeforeProbe(t *testing.T) {
	contract := validProviderContract()
	contract.ReadsCanonical = true
	provider := &certificationEmbedder{contract: contract, vectors: [][]float32{{1}}}
	semantic := true
	server := &Server{Embedder: provider, SemanticSearch: &semantic}
	if _, err := server.vectorHits(context.Background(), retrieval.SessionScope{SessionID: "session"}, retrieval.SearchRequest{SessionID: "session", Query: "safe query"}); err == nil {
		t.Fatal("invalid provider accepted for semantic query")
	}
	if provider.calls != 0 {
		t.Fatalf("rejected provider received query: calls=%d", provider.calls)
	}
}

func BenchmarkProviderContractValidate(b *testing.B) {
	contract := validProviderContract()
	for i := 0; i < b.N; i++ {
		if err := contract.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzProviderContractNeverGrantsCanonicalAuthority(f *testing.F) {
	f.Add("provider", "local", false, false, 16)
	f.Add("provider", "remote", false, false, 16)
	f.Fuzz(func(t *testing.T, provider, locality string, reads, mutates bool, batch int) {
		contract := validProviderContract()
		contract.Provider = provider
		contract.Locality = locality
		contract.ReadsCanonical = reads
		contract.MutatesCanonical = mutates
		contract.MaximumBatchSize = batch
		contract.SendsContentOffDevice = locality == "remote"
		err := contract.Validate()
		if (reads || mutates) && err == nil {
			t.Fatal("contract granted canonical authority")
		}
	})
}
