package debugnode

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Node identifies a stable diagnostic checkpoint.
type Node string

const (
	UploadChannelAuth          Node = "UPLOAD.CHANNEL_AUTH"
	ArtifactOriginAssign       Node = "ARTIFACT.ORIGIN_ASSIGN"
	ArtifactApprovalChange     Node = "ARTIFACT.APPROVAL_CHANGE"
	SensitivityInherit         Node = "SENSITIVITY.INHERIT"
	SensitivityDowngrade       Node = "SENSITIVITY.DOWNGRADE"
	MaterializeValidate        Node = "MATERIALIZE.VALIDATE"
	EvidenceEligibility        Node = "EVIDENCE.ELIGIBILITY"
	MutationEvidenceVerify     Node = "MUTATION.EVIDENCE_VERIFY"
	MutationApply              Node = "MUTATION.APPLY"
	MutationStage              Node = "MUTATION.STAGE"
	LineageCycleCheck          Node = "LINEAGE.CYCLE_CHECK"
	DBConnect                  Node = "DB.CONNECT"
	DBSchemaVerify             Node = "DB.SCHEMA_VERIFY"
	DBTransactionBegin         Node = "DB.TRANSACTION_BEGIN"
	DBTransactionCommit        Node = "DB.TRANSACTION_COMMIT"
	DBTransactionRollback      Node = "DB.TRANSACTION_ROLLBACK"
	OwnerResolve               Node = "OWNER.RESOLVE"
	OwnerHealthCheck           Node = "OWNER.HEALTH_CHECK"
	OwnerMissing               Node = "OWNER.MISSING"
	OwnerMismatch              Node = "OWNER.MISMATCH"
	OwnerImmutabilityReject    Node = "OWNER.IMMUTABILITY_REJECT"
	BlobLockAcquire            Node = "BLOB_LOCK.ACQUIRE"
	BlobLockBusy               Node = "BLOB_LOCK.BUSY"
	BlobLockAcquired           Node = "BLOB_LOCK.ACQUIRED"
	BlobLockRelease            Node = "BLOB_LOCK.RELEASE"
	AuthMCP                    Node = "AUTH.MCP"
	AuthIngestion              Node = "AUTH.INGESTION"
	UploadStream               Node = "UPLOAD.STREAM"
	UploadSizeVerify           Node = "UPLOAD.SIZE_VERIFY"
	UploadHashVerify           Node = "UPLOAD.HASH_VERIFY"
	UploadCASCommit            Node = "UPLOAD.CAS_COMMIT"
	UploadBlobReverify         Node = "UPLOAD.BLOB_REVERIFY"
	UploadBlobRestore          Node = "UPLOAD.BLOB_RESTORE"
	UploadHandleCreate         Node = "UPLOAD.HANDLE_CREATE"
	UploadExpire               Node = "UPLOAD.EXPIRE"
	ArchiveSessionResolve      Node = "ARCHIVE.SESSION_RESOLVE"
	ArchiveMessageInsert       Node = "ARCHIVE.MESSAGE_INSERT"
	ArchiveMessageReplay       Node = "ARCHIVE.MESSAGE_REPLAY"
	ArchiveIdempotencyConflict Node = "ARCHIVE.IDEMPOTENCY_CONFLICT"
	ArchiveCheckpoint          Node = "ARCHIVE.CHECKPOINT"
	ArtifactBlobUpsert         Node = "ARTIFACT.BLOB_UPSERT"
	ArtifactCatalogCommit      Node = "ARTIFACT.CATALOG_COMMIT"
	ArtifactVersionCreate      Node = "ARTIFACT.VERSION_CREATE"
	ArtifactLineageCommit      Node = "ARTIFACT.LINEAGE_COMMIT"
	ArtifactUploadClaim        Node = "ARTIFACT.UPLOAD_CLAIM"
	JobEnqueue                 Node = "JOB.ENQUEUE"
	ReconcileUpload            Node = "RECONCILE.UPLOAD"
	ReconcileBlob              Node = "RECONCILE.BLOB"
	HealthReadiness            Node = "HEALTH.READINESS"
	ToolEventHash              Node = "TOOL_EVENT.HASH"
	ToolEventReplay            Node = "TOOL_EVENT.REPLAY"
	ToolEventConflict          Node = "TOOL_EVENT.CONFLICT"
	ReconcileCycle             Node = "RECONCILE.CYCLE"
	ReconcileLock              Node = "RECONCILE.LOCK"
	ReconcileStoreUnavailable  Node = "RECONCILE.STORE_UNAVAILABLE"
	ReconcileIntegrityUpdate   Node = "RECONCILE.INTEGRITY_UPDATE"
	ReconcileOrphanLock        Node = "RECONCILE.ORPHAN_LOCK"
	ReconcileOrphanRecheck     Node = "RECONCILE.ORPHAN_RECHECK"
	ReconcileOrphanSkip        Node = "RECONCILE.ORPHAN_SKIP_REFERENCED"
	ReconcileOrphanDelete      Node = "RECONCILE.ORPHAN_DELETE"
	CleanupUnclaimedCandidate  Node = "CLEANUP.UNCLAIMED_CANDIDATE"
	CleanupUnclaimedRecheck    Node = "CLEANUP.UNCLAIMED_RECHECK"
	CleanupUnclaimedDelete     Node = "CLEANUP.UNCLAIMED_DELETE"
	CleanupUnclaimedSkip       Node = "CLEANUP.UNCLAIMED_SKIP_REFERENCED"
	HealthStoreProbe           Node = "HEALTH.STORE_PROBE"
	ClientProvenanceConflict   Node = "CLIENT.PROVENANCE_CONFLICT"
	TimestampCanonicalize      Node = "TIMESTAMP.CANONICALIZE"
	JobRetry                   Node = "JOB.RETRY"
	JobPermanentFail           Node = "JOB.PERMANENT_FAIL"
	JobDead                    Node = "JOB.DEAD"
	JobLeaseExpire             Node = "JOB.LEASE_EXPIRE"
	ProcessJobClaim            Node = "PROCESS.JOB_CLAIM"
	ProcessMaterialize         Node = "PROCESS.MATERIALIZE"
	ProcessSourceVerify        Node = "PROCESS.SOURCE_VERIFY"
	ProcessRegistrySelect      Node = "PROCESS.REGISTRY_SELECT"
	ProcessorStart             Node = "PROCESSOR.START"
	ProcessorComplete          Node = "PROCESSOR.COMPLETE"
	ProcessorFail              Node = "PROCESSOR.FAIL"
	ProcessorText              Node = "PROCESSOR.TEXT"
	ProcessorJSON              Node = "PROCESSOR.JSON"
	ProcessorCSV               Node = "PROCESSOR.CSV"
	ProcessorSource            Node = "PROCESSOR.SOURCE"
	ProcessorPDF               Node = "PROCESSOR.PDF"
	ProcessorXLSX              Node = "PROCESSOR.XLSX"
	ProcessResultValidate      Node = "PROCESS.RESULT_VALIDATE"
	ProcessRepresentationHash  Node = "PROCESS.REPRESENTATION_HASH"
	ProcessFragmentStream      Node = "PROCESS.FRAGMENT_STREAM"
	ProcessFragmentValidate    Node = "PROCESS.FRAGMENT_VALIDATE"
	ProcessSensitivity         Node = "PROCESS.SENSITIVITY"
	RepresentationCASCommit    Node = "REPRESENTATION.CAS_COMMIT"
	RepresentationDBCommit     Node = "REPRESENTATION.DB_COMMIT"
	ProcessStatus              Node = "PROCESS.STATUS"
	WorkRunCreate              Node = "WORK_RUN.CREATE"
	WorkRunInput               Node = "WORK_RUN.INPUT"
	WorkRunOutput              Node = "WORK_RUN.OUTPUT"
	WorkProductPromote         Node = "WORK_PRODUCT.PROMOTE"
	RetentionAssign            Node = "RETENTION.ASSIGN"
	RetentionPromote           Node = "RETENTION.PROMOTE"
	RetentionPin               Node = "RETENTION.PIN"
	StorageEvaluate            Node = "STORAGE.EVALUATE"
	StorageCandidate           Node = "STORAGE.CANDIDATE"
	StorageEvict               Node = "STORAGE.EVICT"
	StorageRestore             Node = "STORAGE.RESTORE"
	ArtifactCardCreate         Node = "ARTIFACT_CARD.CREATE"
	ArtifactCardUpdate         Node = "ARTIFACT_CARD.UPDATE"
	EvidenceAvailability       Node = "EVIDENCE.AVAILABILITY"
	StorageQuotaEvaluate       Node = "STORAGE.QUOTA_EVALUATE"
	StorageLeaseAcquire        Node = "STORAGE.LEASE_ACQUIRE"
	StorageLeaseRelease        Node = "STORAGE.LEASE_RELEASE"
	StorageEvictionBegin       Node = "STORAGE.EVICTION_BEGIN"
	StorageLogicalEvict        Node = "STORAGE.LOGICAL_EVICT"
	StoragePhysicalDelete      Node = "STORAGE.PHYSICAL_DELETE"
	StoragePendingRecover      Node = "STORAGE.PENDING_RECOVER"
)

const ArtifactPublicationCompensate Node = "ARTIFACT_PUBLICATION_COMPENSATE"
const StorageLeaseRenew Node = "STORAGE.LEASE_RENEW"

const (
	MemoryProposalReceive      Node = "MEMORY_PROPOSAL_RECEIVE"
	MemoryEvidenceHydrate      Node = "MEMORY_EVIDENCE_HYDRATE"
	MemoryMutationVerify       Node = "MEMORY_MUTATION_VERIFY"
	MemoryMutationApply        Node = "MEMORY_MUTATION_APPLY"
	MemoryMutationStage        Node = "MEMORY_MUTATION_STAGE"
	MemoryContinuityAppend     Node = "MEMORY_CONTINUITY_APPEND"
	MemoryContinuityLockWait   Node = "MEMORY_CONTINUITY_LOCK_WAIT"
	MemorySessionAuthorityLock Node = "MEMORY_SESSION_AUTHORITY_LOCK"
	MemoryProjectScopeStage    Node = "MEMORY_PROJECT_SCOPE_STAGE"
	MemoryMutationLanguageCue  Node = "MEMORY_MUTATION_LANGUAGE_CUE"
	CoreRevisionPromote        Node = "CORE_REVISION_PROMOTE"
	ProjectRevisionPromote     Node = "PROJECT_REVISION_PROMOTE"
	EpisodeRevisionPromote     Node = "EPISODE_REVISION_PROMOTE"
	RevisionWithdraw           Node = "REVISION_WITHDRAW"
	RetrievalSearch            Node = "RETRIEVAL_SEARCH"
	RetrievalContext           Node = "RETRIEVAL_CONTEXT"
	RetrievalRecall            Node = "RETRIEVAL_RECALL"
	RetrievalArtifactSearch    Node = "RETRIEVAL_ARTIFACT_SEARCH"
	RetrievalArtifactRead      Node = "RETRIEVAL_ARTIFACT_READ"
	RetrievalDiff              Node = "RETRIEVAL_DIFF"
	MCPToolCall                Node = "MCP_TOOL_CALL"
	MCPToolSuccess             Node = "MCP_TOOL_SUCCESS"
	MCPToolError               Node = "MCP_TOOL_ERROR"
)

// Event contains only safe diagnostic metadata.
type Event struct {
	Node                Node
	TraceID             string
	Status              string
	ReasonCode          string
	ResourceID          string
	ContentHash         string
	Count               int
	Duration            time.Duration
	Tool                string
	QueryLength         int
	Truncated           bool
	ProjectScopePresent bool
}

var safeMetadata = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{0,256}$`)

// Validate rejects content-bearing or malformed event metadata.
func (e Event) Validate() error {
	if e.Node == "" || strings.TrimSpace(e.Status) == "" || !safeMetadata.MatchString(string(e.Node)) || !safeMetadata.MatchString(e.Status) {
		return errors.New("debug node and status are required")
	}
	for _, value := range []string{e.TraceID, e.ReasonCode, e.ResourceID, e.ContentHash, e.Tool} {
		if strings.ContainsAny(value, "\r\n") || !safeMetadata.MatchString(value) {
			return errors.New("debug metadata is malformed")
		}
	}
	return nil
}

// Observer receives safe diagnostic events.
type Observer interface {
	Observe(context.Context, Event)
}

// NopObserver discards events.
type NopObserver struct{}

// Observe implements Observer.
func (NopObserver) Observe(context.Context, Event) {}

// SlogObserver writes validated fields to a structured logger.
type SlogObserver struct{ Logger *slog.Logger }

// Observe implements Observer without including source content.
func (o SlogObserver) Observe(ctx context.Context, event Event) {
	if event.Validate() != nil {
		return
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "mindmory checkpoint",
		"debug_node", event.Node,
		"trace_id", event.TraceID,
		"status", event.Status,
		"reason_code", event.ReasonCode,
		"resource_id", event.ResourceID,
		"content_hash", event.ContentHash,
		"count", event.Count,
		"duration_ms", event.Duration.Milliseconds(), "tool", event.Tool,
		"query_length", event.QueryLength, "truncated", event.Truncated, "project_scope_present", event.ProjectScopePresent)
}
