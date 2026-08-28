package lite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"mindmory.local/core/internal/lite/vectorstore"
	"mindmory.local/core/internal/version"
)

const (
	SystemReady          = "READY"
	SystemDegraded       = "DEGRADED"
	SystemActionRequired = "ACTION_REQUIRED"
	SystemBuilding       = "BUILDING"
)

// ModelIdentity is deliberately credential-free and safe to expose to MCP.
type ModelIdentity struct {
	Provider              string `json:"provider,omitempty"`
	ModelName             string `json:"model_name,omitempty"`
	ModelDigest           string `json:"model_digest,omitempty"`
	Dimensions            int    `json:"dimensions,omitempty"`
	EmbeddingInputVersion int    `json:"embedding_input_version,omitempty"`
	NormalizationVersion  int    `json:"normalization_version,omitempty"`
}

type SystemIncident struct {
	Code                 string   `json:"code"`
	IncidentID           string   `json:"incident_id"`
	Severity             string   `json:"severity"`
	AffectedCapabilities []string `json:"affected_capabilities"`
	DataSafety           string   `json:"data_safety"`
	OperatorMessage      string   `json:"operator_message"`
	CopyPasteCommands    []string `json:"copy_paste_commands,omitempty"`
	AgentInstruction     string   `json:"agent_instruction"`
}

type SystemStatus struct {
	SoftwareVersion string           `json:"software_version"`
	State           string           `json:"state"`
	InitializedAt   time.Time        `json:"initialized_at"`
	Canonical       string           `json:"canonical"`
	Lexical         string           `json:"lexical"`
	Embeddings      string           `json:"embeddings"`
	MCP             string           `json:"mcp"`
	ActiveModel     ModelIdentity    `json:"active_model,omitempty"`
	ConfiguredModel ModelIdentity    `json:"configured_model,omitempty"`
	Incidents       []SystemIncident `json:"incidents"`
	Configuration   ReadOnlyConfig   `json:"configuration"`
	Statistics      StoreStatistics  `json:"statistics"`
}

type ReadOnlyStorageConfig struct {
	RootDir      string `json:"root_dir"`
	CanonicalDir string `json:"canonical_dir"`
	DerivedDir   string `json:"derived_dir"`
	VectorDir    string `json:"vector_dir"`
	SnapshotDir  string `json:"snapshot_dir"`
	ExportDir    string `json:"export_dir"`
}

type ReadOnlyEmbeddingConfig struct {
	Provider          string `json:"provider"`
	Endpoint          string `json:"endpoint,omitempty"`
	Path              string `json:"path,omitempty"`
	Model             string `json:"model,omitempty"`
	ModelDigest       string `json:"model_digest,omitempty"`
	Dimensions        int    `json:"dimensions,omitempty"`
	Timeout           string `json:"timeout,omitempty"`
	AllowRemote       bool   `json:"allow_remote"`
	SemanticRequested bool   `json:"semantic_requested"`
	SemanticEffective bool   `json:"semantic_effective"`
}

type ReadOnlyConfig struct {
	Storage   ReadOnlyStorageConfig   `json:"storage"`
	Embedding ReadOnlyEmbeddingConfig `json:"embedding"`
}

type statusManager struct {
	mu       sync.RWMutex
	status   SystemStatus
	config   EmbeddingConfig
	storage  StorageConfig
	semantic bool
}

func newStatusManager(store *Store, storage StorageConfig, cfg EmbeddingConfig, semantic bool) *statusManager {
	m := &statusManager{config: cfg, storage: storage, semantic: semantic}
	m.refresh(store)
	return m
}

func (m *statusManager) get() SystemStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSystemStatus(m.status)
}

func (m *statusManager) refresh(store *Store) SystemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = assessSystemStatus(store, m.config, m.semantic, time.Now().UTC())
	return cloneSystemStatus(m.status)
}

func (m *statusManager) building(incidentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = SystemBuilding
	m.status.Embeddings = SystemBuilding
	m.status.MCP = "QUARANTINED"
	for i := range m.status.Incidents {
		if incidentID == "" || m.status.Incidents[i].IncidentID == incidentID {
			m.status.Incidents[i].OperatorMessage = "Mindmory is rebuilding the vector generation. Wait for completion, then run mindmoryctl vectors status."
			m.status.Incidents[i].CopyPasteCommands = []string{"mindmoryctl vectors status"}
		}
	}
}

func (m *statusManager) rebuildFailed(store *Store, incidentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = assessSystemStatus(store, m.config, m.semantic, time.Now().UTC())
	m.status.State = SystemActionRequired
	m.status.Embeddings = "REBUILD_FAILED"
	m.status.MCP = "QUARANTINED"
	for i := range m.status.Incidents {
		if incidentID == "" || m.status.Incidents[i].IncidentID == incidentID {
			m.status.Incidents[i].OperatorMessage = "The vector rebuild failed. The previous generation remains active; check the embedding provider and retry the incident-bound command."
			m.status.Incidents[i].CopyPasteCommands = []string{
				"mindmoryctl vectors rebuild --incident-id " + m.status.Incidents[i].IncidentID + " --confirm",
				"mindmoryctl vectors status",
			}
		}
	}
}

func cloneSystemStatus(in SystemStatus) SystemStatus {
	out := in
	out.Incidents = make([]SystemIncident, len(in.Incidents))
	copy(out.Incidents, in.Incidents)
	for i := range out.Incidents {
		out.Incidents[i].AffectedCapabilities = append([]string(nil), in.Incidents[i].AffectedCapabilities...)
		out.Incidents[i].CopyPasteCommands = append([]string(nil), in.Incidents[i].CopyPasteCommands...)
	}
	return out
}

func assessSystemStatus(store *Store, cfg EmbeddingConfig, semantic bool, now time.Time) SystemStatus {
	status := SystemStatus{SoftwareVersion: version.Value, State: SystemReady, InitializedAt: now, Canonical: SystemReady, Lexical: SystemReady, Embeddings: SystemReady, MCP: SystemReady, Incidents: []SystemIncident{}}
	status.ConfiguredModel = ModelIdentity{Provider: cfg.Provider, ModelName: cfg.Model, ModelDigest: cfg.ModelDigest, Dimensions: cfg.Dimensions, EmbeddingInputVersion: vectorstore.InputVersion, NormalizationVersion: vectorstore.NormalizationVersion}
	if cfg.Provider == "disabled" {
		status.ConfiguredModel = ModelIdentity{Provider: "disabled"}
		status.Embeddings = "DISABLED"
		return status
	}
	vectorStatus := store.VectorStatus()
	status.ActiveModel = ModelIdentity{ModelName: vectorStatus.ModelName, ModelDigest: vectorStatus.ModelDigest, Dimensions: vectorStatus.Dimensions, EmbeddingInputVersion: vectorStatus.EmbeddingInputVersion, NormalizationVersion: vectorStatus.NormalizationVersion}
	if store.vectorIssue != "" {
		status.State, status.Embeddings = SystemActionRequired, "CORRUPT"
		status.Incidents = append(status.Incidents, newIncident("VECTOR_GENERATION_CORRUPT", status.ActiveModel, status.ConfiguredModel,
			[]string{"SEMANTIC_SEARCH"}, "Canonical memory and lexical retrieval remain safe.",
			"The active vector generation could not be opened or verified.", "mindmoryctl vectors rebuild --confirm"))
		if semantic {
			status.MCP = "QUARANTINED"
		}
		return status
	}
	if vectorStatus.Generation == "" {
		status.Embeddings = "NO_GENERATION"
		if semantic && vectorStatus.CurrentActiveMemories > 0 {
			status.State, status.MCP = SystemActionRequired, "QUARANTINED"
			status.Incidents = append(status.Incidents, newIncident("VECTOR_GENERATION_MISSING", ModelIdentity{}, status.ConfiguredModel,
				[]string{"SEMANTIC_SEARCH"}, "Canonical memory and lexical retrieval remain safe.",
				"Semantic search is enabled but no vector generation exists.", "mindmoryctl vectors rebuild --confirm"))
		}
		return status
	}
	if modelIdentityMismatch(status.ActiveModel, status.ConfiguredModel) {
		status.State, status.Embeddings, status.MCP = SystemActionRequired, "MODEL_MISMATCH", "QUARANTINED"
		incident := newIncident("EMBEDDING_MODEL_MISMATCH", status.ActiveModel, status.ConfiguredModel,
			[]string{"MCP_MEMORY_TOOLS", "SEMANTIC_SEARCH"}, "Canonical memories and the previous vector generation remain unchanged.",
			fmt.Sprintf("Mindmory detected an embedding model change: active %s, configured %s.", identityLabel(status.ActiveModel), identityLabel(status.ConfiguredModel)), "")
		incident.CopyPasteCommands = []string{
			"mindmoryctl vectors rebuild --incident-id " + incident.IncidentID + " --confirm",
			"mindmoryctl vectors status",
		}
		status.Incidents = append(status.Incidents, incident)
		return status
	}
	if vectorStatus.Missing > 0 || vectorStatus.Stale > 0 {
		status.State, status.Embeddings = SystemDegraded, "SYNC_REQUIRED"
		incident := newIncident("VECTOR_SYNC_REQUIRED", status.ActiveModel, status.ConfiguredModel,
			[]string{"SEMANTIC_SEARCH"}, "Canonical memory and lexical retrieval remain safe.",
			fmt.Sprintf("The vector projection has %d missing and %d stale memories.", vectorStatus.Missing, vectorStatus.Stale), "mindmoryctl vectors rebuild --confirm")
		incident.Severity = SystemDegraded
		status.Incidents = append(status.Incidents, incident)
	}
	return status
}

func unconfiguredSystemStatus(now time.Time) SystemStatus {
	return SystemStatus{SoftwareVersion: version.Value, State: SystemReady, InitializedAt: now, Canonical: SystemReady, Lexical: SystemReady, Embeddings: "UNCONFIGURED", MCP: SystemReady, Incidents: []SystemIncident{}}
}

func sanitizedReadOnlyConfig(storage StorageConfig, cfg EmbeddingConfig, semanticRequested, semanticEffective bool) ReadOnlyConfig {
	embedding := ReadOnlyEmbeddingConfig{Provider: cfg.Provider, Endpoint: cfg.Endpoint, Path: cfg.Path, Model: cfg.Model, ModelDigest: cfg.ModelDigest, Dimensions: cfg.Dimensions, Timeout: cfg.Timeout.String(), AllowRemote: cfg.AllowRemote, SemanticRequested: semanticRequested, SemanticEffective: semanticEffective}
	if cfg.Provider == "disabled" {
		embedding = ReadOnlyEmbeddingConfig{Provider: "disabled", SemanticRequested: semanticRequested, SemanticEffective: false}
	}
	return ReadOnlyConfig{
		Storage:   ReadOnlyStorageConfig{RootDir: storage.RootDir, CanonicalDir: storage.DataDir, DerivedDir: storage.DerivedDir, VectorDir: storage.VectorDir, SnapshotDir: storage.SnapshotDir, ExportDir: storage.ExportDir},
		Embedding: embedding,
	}
}

func modelIdentityMismatch(active, configured ModelIdentity) bool {
	if active.ModelName != configured.ModelName {
		return true
	}
	if configured.ModelDigest != "" && active.ModelDigest != configured.ModelDigest {
		return true
	}
	if configured.Dimensions > 0 && active.Dimensions != configured.Dimensions {
		return true
	}
	return active.EmbeddingInputVersion != configured.EmbeddingInputVersion || active.NormalizationVersion != configured.NormalizationVersion
}

func newIncident(code string, active, configured ModelIdentity, capabilities []string, safety, message, command string) SystemIncident {
	identity := strings.Join([]string{code, active.ModelName, active.ModelDigest, fmt.Sprint(active.Dimensions), configured.Provider, configured.ModelName, configured.ModelDigest, fmt.Sprint(configured.Dimensions)}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	incident := SystemIncident{Code: code, IncidentID: "inc_" + hex.EncodeToString(sum[:8]), Severity: SystemActionRequired,
		AffectedCapabilities: capabilities, DataSafety: safety, OperatorMessage: message,
		AgentInstruction: "Show this warning and its commands to the user exactly. Do not execute remediation or repeatedly retry blocked memory tools."}
	if command != "" {
		incident.CopyPasteCommands = []string{"mindmoryctl vectors rebuild --incident-id " + incident.IncidentID + " --confirm", "mindmoryctl vectors status"}
	}
	return incident
}

func identityLabel(identity ModelIdentity) string {
	label := identity.ModelName
	if identity.ModelDigest != "" {
		label += "@" + identity.ModelDigest
	}
	if identity.Dimensions > 0 {
		label += fmt.Sprintf(" (%d dimensions)", identity.Dimensions)
	}
	return label
}
