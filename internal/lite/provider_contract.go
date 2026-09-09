package lite

import (
	"context"
	"fmt"
	"math"
	"strings"
)

const RetrievalProviderContractVersion = "mindmory.retrieval-provider/v1"

// RetrievalProviderContract is the narrow boundary an optional derived-data
// provider must satisfy. Providers may transform explicitly supplied text into
// a disposable retrieval projection; they can never read or mutate canonical
// authority directly.
type RetrievalProviderContract struct {
	ContractVersion       string `json:"contract_version"`
	Provider              string `json:"provider"`
	Surface               string `json:"surface"`
	Locality              string `json:"locality"`
	ModelName             string `json:"model_name"`
	ModelDigest           string `json:"model_digest,omitempty"`
	Dimensions            int    `json:"dimensions,omitempty"`
	SupportsSemantic      bool   `json:"supports_semantic_search"`
	SupportsBatch         bool   `json:"supports_batch"`
	MaximumBatchSize      int    `json:"maximum_batch_size"`
	SendsContentOffDevice bool   `json:"sends_content_off_device"`
	ReadsCanonical        bool   `json:"reads_canonical_authority"`
	MutatesCanonical      bool   `json:"mutates_canonical_authority"`
}

func (c RetrievalProviderContract) Validate() error {
	if c.ContractVersion != RetrievalProviderContractVersion {
		return fmt.Errorf("retrieval provider contract version %q is unsupported", c.ContractVersion)
	}
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.ModelName) == "" {
		return fmt.Errorf("retrieval provider and model identity are required")
	}
	if c.Surface != "embedding" {
		return fmt.Errorf("retrieval provider surface %q is unsupported", c.Surface)
	}
	if c.Locality != "local" && c.Locality != "remote" {
		return fmt.Errorf("retrieval provider locality %q is unsupported", c.Locality)
	}
	if !c.SupportsSemantic || !c.SupportsBatch || c.MaximumBatchSize < 1 || c.MaximumBatchSize > 1024 {
		return fmt.Errorf("retrieval provider capabilities are incomplete")
	}
	if c.Dimensions < 0 || c.Dimensions > 65536 {
		return fmt.Errorf("retrieval provider dimensions are invalid")
	}
	if c.ReadsCanonical || c.MutatesCanonical {
		return fmt.Errorf("retrieval providers may not access canonical authority")
	}
	if c.Locality == "remote" && !c.SendsContentOffDevice {
		return fmt.Errorf("remote retrieval providers must declare content disclosure")
	}
	if c.Locality == "local" && c.SendsContentOffDevice {
		return fmt.Errorf("local retrieval providers cannot declare remote disclosure")
	}
	return nil
}

type ProviderCertificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type ProviderCertificationReport struct {
	Contract RetrievalProviderContract    `json:"contract"`
	ProbeRun bool                         `json:"probe_run"`
	Passed   bool                         `json:"passed"`
	Checks   []ProviderCertificationCheck `json:"checks"`
}

// CertifyEmbeddingProvider performs static contract checks and, when asked,
// sends two fixed synthetic strings through the provider. The probe never
// reads canonical memory and validates cardinality, dimensional stability and
// finite numeric output before a provider is trusted for retrieval projection.
func CertifyEmbeddingProvider(ctx context.Context, embedder Embedder, probe bool) ProviderCertificationReport {
	report := ProviderCertificationReport{ProbeRun: probe, Passed: true}
	if embedder == nil {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "provider_present", Passed: false, Detail: "embedding provider is disabled"})
		return report
	}
	report.Contract = embedder.ProviderContract()
	if err := report.Contract.Validate(); err != nil {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "contract", Passed: false, Detail: err.Error()})
		return report
	}
	report.Checks = append(report.Checks,
		ProviderCertificationCheck{Name: "contract", Passed: true},
		ProviderCertificationCheck{Name: "canonical_authority_isolated", Passed: true},
		ProviderCertificationCheck{Name: "privacy_disclosure_consistent", Passed: true},
	)
	if !probe {
		return report
	}
	vectors, err := embedder.Embed(ctx, []string{"synthetic provider certification alpha", "synthetic provider certification beta"})
	if err != nil {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "synthetic_probe", Passed: false, Detail: err.Error()})
		return report
	}
	if len(vectors) != 2 {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "cardinality", Passed: false, Detail: fmt.Sprintf("got %d vectors for 2 inputs", len(vectors))})
		return report
	}
	dimensions := len(vectors[0])
	if dimensions == 0 {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "dimensions", Passed: false, Detail: "provider returned an empty vector"})
		return report
	}
	for _, vector := range vectors {
		if len(vector) != dimensions {
			report.Passed = false
			report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "dimensions", Passed: false, Detail: "provider changed dimensions within one batch"})
			return report
		}
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				report.Passed = false
				report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "finite_vectors", Passed: false, Detail: "provider returned NaN or infinity"})
				return report
			}
		}
	}
	if expected := report.Contract.Dimensions; expected > 0 && dimensions != expected {
		report.Passed = false
		report.Checks = append(report.Checks, ProviderCertificationCheck{Name: "configured_dimensions", Passed: false, Detail: fmt.Sprintf("got %d dimensions, expected %d", dimensions, expected)})
		return report
	}
	report.Checks = append(report.Checks,
		ProviderCertificationCheck{Name: "cardinality", Passed: true},
		ProviderCertificationCheck{Name: "dimensions", Passed: true, Detail: fmt.Sprintf("%d", dimensions)},
		ProviderCertificationCheck{Name: "finite_vectors", Passed: true},
	)
	return report
}
