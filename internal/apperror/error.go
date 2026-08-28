// Package apperror defines safe machine-readable failures shared by services and HTTP.
package apperror

import (
	"errors"
	"fmt"
)

const (
	AuthRequired                   = "AUTH_REQUIRED"
	CapabilityDenied               = "CAPABILITY_DENIED"
	SchemaMigrationRequired        = "SCHEMA_MIGRATION_REQUIRED"
	SchemaVersionTooNew            = "SCHEMA_VERSION_TOO_NEW"
	OwnerIdentityMismatch          = "OWNER_IDENTITY_MISMATCH"
	OwnerAuthorityMissing          = "OWNER_AUTHORITY_MISSING"
	BlobCoordinationTimeout        = "BLOB_COORDINATION_TIMEOUT"
	BlobCoordinationFailed         = "BLOB_COORDINATION_FAILED"
	UploadTooLarge                 = "UPLOAD_TOO_LARGE"
	UploadNotFound                 = "UPLOAD_NOT_FOUND"
	UploadExpired                  = "UPLOAD_EXPIRED"
	UploadAlreadyClaimed           = "UPLOAD_ALREADY_CLAIMED"
	UploadTargetMismatch           = "UPLOAD_TARGET_MISMATCH"
	UploadIntegrityError           = "UPLOAD_INTEGRITY_ERROR"
	IdempotencyConflict            = "IDEMPOTENCY_CONFLICT"
	ToolEventIdempotencyConflict   = "TOOL_EVENT_IDEMPOTENCY_CONFLICT"
	SourceEventIdempotencyConflict = "SOURCE_EVENT_IDEMPOTENCY_CONFLICT"
	ArtifactStoreUnavailable       = "ARTIFACT_STORE_UNAVAILABLE"
	PrincipalKeyDomainConflict     = "PRINCIPAL_KEY_DOMAIN_CONFLICT"
	PrincipalIdentityConflict      = "PRINCIPAL_IDENTITY_CONFLICT"
	JobLeaseLost                   = "JOB_LEASE_LOST"
	JobRetryExhausted              = "JOB_RETRY_EXHAUSTED"
	ReconciliationFailed           = "RECONCILIATION_FAILED"
	UnclaimedCleanupFailed         = "UNCLAIMED_CLEANUP_FAILED"
	SessionMetadataConflict        = "SESSION_METADATA_CONFLICT"
	ArtifactVersionConflict        = "ARTIFACT_VERSION_CONFLICT"
	ArtifactIntegrityError         = "ARTIFACT_INTEGRITY_ERROR"
	ArtifactBlobPurged             = "ARTIFACT_BLOB_PURGED"
	ToolPayloadRequiresArtifact    = "TOOL_PAYLOAD_REQUIRES_ARTIFACT"
	DatabaseUnavailable            = "DATABASE_UNAVAILABLE"
	StorageUnavailable             = "STORAGE_UNAVAILABLE"
	InvalidRequest                 = "INVALID_REQUEST"
	InternalError                  = "INTERNAL_ERROR"
	ProcessorUnavailable           = "PROCESSOR_UNAVAILABLE"
	ProcessorLimitUnavailable      = "PROCESSOR_LIMIT_UNAVAILABLE"
	ProcessorSandboxUnavailable    = "PROCESSOR_SANDBOX_UNAVAILABLE"
	ProcessorFailed                = "PROCESSOR_FAILED"
	ProcessorTimeout               = "PROCESSOR_TIMEOUT"
	ProcessorOutputInvalid         = "PROCESSOR_OUTPUT_INVALID"
	ProcessorOutputTooLarge        = "PROCESSOR_OUTPUT_TOO_LARGE"
	ProcessorNondeterministic      = "PROCESSOR_NONDETERMINISTIC"
	SourceIntegrityError           = "SOURCE_INTEGRITY_ERROR"
	SourceUnavailable              = "SOURCE_UNAVAILABLE"
	FragmentInvalid                = "FRAGMENT_INVALID"
	FragmentHashMismatch           = "FRAGMENT_HASH_MISMATCH"
	LocatorInvalid                 = "LOCATOR_INVALID"
	ArchiveTraversalDetected       = "ARCHIVE_TRAVERSAL_DETECTED"
	ArchiveResourceLimit           = "ARCHIVE_RESOURCE_LIMIT"
	ArchiveSymlinkRejected         = "ARCHIVE_SYMLINK_REJECTED"
	PDFEncrypted                   = "PDF_ENCRYPTED"
	PDFMalformed                   = "PDF_MALFORMED"
	PDFPageLimit                   = "PDF_PAGE_LIMIT"
	OCRRequired                    = "OCR_REQUIRED"
	XLSXMalformed                  = "XLSX_MALFORMED"
	XLSXResourceLimit              = "XLSX_RESOURCE_LIMIT"
	XLSXMacroUnsupported           = "XLSX_MACRO_UNSUPPORTED"
	UnsupportedEncoding            = "UNSUPPORTED_ENCODING"
	ResourceLimitExceeded          = "RESOURCE_LIMIT_EXCEEDED"
	WorkRunNotFound                = "WORK_RUN_NOT_FOUND"
	WorkRunOutputConflict          = "WORK_RUN_OUTPUT_CONFLICT"
	WorkProductPromotionDenied     = "WORK_PRODUCT_PROMOTION_DENIED"
	RetentionPolicyInvalid         = "RETENTION_POLICY_INVALID"
	ArtifactPinned                 = "ARTIFACT_PINNED"
	ArtifactNotEvictable           = "ARTIFACT_NOT_EVICTABLE"
	StorageDependencyBlocked       = "STORAGE_DEPENDENCY_BLOCKED"
	StorageSharedBlobRequired      = "STORAGE_SHARED_BLOB_REQUIRED"
	StorageRestoreRequired         = "STORAGE_RESTORE_REQUIRED"
	StorageRestoreHashMismatch     = "STORAGE_RESTORE_HASH_MISMATCH"
	ArtifactBytesEvicted           = "ARTIFACT_BYTES_EVICTED"
	ArtifactOffline                = "ARTIFACT_OFFLINE"
	ArtifactInUse                  = "ARTIFACT_IN_USE"
	StorageReconciliationFailed    = "STORAGE_RECONCILIATION_FAILED"
	StorageStateConflict           = "STORAGE_STATE_CONFLICT"
	ArtifactReadLeaseLost          = "ARTIFACT_READ_LEASE_LOST"
	PublicationCompensationPending = "PUBLICATION_COMPENSATION_PENDING"
	MemoryProposalInvalid          = "MEMORY_PROPOSAL_INVALID"
	MemoryTargetNotFound           = "MEMORY_TARGET_NOT_FOUND"
	MemoryTargetInactive           = "MEMORY_TARGET_INACTIVE"
	MemoryEvidenceRequired         = "MEMORY_EVIDENCE_REQUIRED"
	MemoryEvidenceAmbiguous        = "MEMORY_EVIDENCE_AMBIGUOUS"
	MemoryCurrentTurnRequired      = "MEMORY_CURRENT_TURN_REQUIRED"
	MemoryProjectScopeUnavailable  = "MEMORY_PROJECT_SCOPE_UNAVAILABLE"
	MemoryMutationConflict         = "MEMORY_MUTATION_CONFLICT"
	MemoryRevisionConflict         = "MEMORY_REVISION_CONFLICT"
	MemoryEvidenceUnavailable      = "MEMORY_EVIDENCE_UNAVAILABLE"
	ContextSessionNotFound         = "CONTEXT_SESSION_NOT_FOUND"
	ContextQueryInvalid            = "CONTEXT_QUERY_INVALID"
	ContextResultLimitInvalid      = "CONTEXT_RESULT_LIMIT_INVALID"
	MemoryReadNotFound             = "MEMORY_READ_NOT_FOUND"
	MemoryContentRestricted        = "MEMORY_CONTENT_RESTRICTED"
	ArtifactSearchInvalid          = "ARTIFACT_SEARCH_INVALID"
	ArtifactReadNotFound           = "ARTIFACT_READ_NOT_FOUND"
	ArtifactContentUnavailable     = "ARTIFACT_CONTENT_UNAVAILABLE"
	CursorInvalid                  = "CURSOR_INVALID"
)

type Error struct {
	Code      string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code string, retryable bool, cause error) error {
	return &Error{Code: code, Retryable: retryable, Cause: cause}
}

func Code(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return InternalError
}
