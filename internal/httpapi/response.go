// Package httpapi exposes the bounded Stage 2 HTTP control-plane surface.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/mcpserver"
)

func WriteJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func WriteError(writer http.ResponseWriter, err error, traceID string) {
	code := apperror.Code(err)
	status := http.StatusInternalServerError
	retryable := false
	var typed *apperror.Error
	if errors.As(err, &typed) {
		retryable = typed.Retryable
	}
	switch code {
	case apperror.AuthRequired:
		status = http.StatusUnauthorized
		writer.Header().Set("WWW-Authenticate", `Bearer realm="mindmory"`)
	case apperror.CapabilityDenied:
		status = http.StatusForbidden
	case apperror.UploadTooLarge:
		status = http.StatusRequestEntityTooLarge
	case apperror.UploadNotFound:
		status = http.StatusNotFound
	case apperror.WorkRunNotFound, apperror.ArtifactOffline:
		status = http.StatusNotFound
	case apperror.MemoryTargetNotFound:
		status = http.StatusNotFound
	case apperror.ContextSessionNotFound, apperror.MemoryReadNotFound, apperror.ArtifactReadNotFound:
		status = http.StatusNotFound
	case apperror.UploadExpired, apperror.UploadAlreadyClaimed, apperror.UploadTargetMismatch, apperror.IdempotencyConflict,
		apperror.ToolEventIdempotencyConflict, apperror.SourceEventIdempotencyConflict, apperror.SessionMetadataConflict, apperror.ArtifactVersionConflict,
		apperror.PrincipalIdentityConflict, apperror.JobLeaseLost, apperror.ArtifactInUse, apperror.StorageStateConflict, apperror.ArtifactBytesEvicted:
		status = http.StatusConflict
	case apperror.ArtifactReadLeaseLost:
		status = http.StatusConflict
	case apperror.InvalidRequest, apperror.ToolPayloadRequiresArtifact, apperror.WorkProductPromotionDenied, apperror.RetentionPolicyInvalid:
		status = http.StatusBadRequest
	case apperror.ContextQueryInvalid, apperror.ContextResultLimitInvalid, apperror.ArtifactSearchInvalid, apperror.CursorInvalid:
		status = http.StatusBadRequest
	case apperror.MemoryProposalInvalid, apperror.MemoryEvidenceRequired, apperror.MemoryEvidenceAmbiguous,
		apperror.MemoryCurrentTurnRequired, apperror.MemoryProjectScopeUnavailable, apperror.MemoryEvidenceUnavailable:
		status = http.StatusBadRequest
	case apperror.MemoryTargetInactive, apperror.MemoryMutationConflict, apperror.MemoryRevisionConflict:
		status = http.StatusConflict
	case apperror.DatabaseUnavailable, apperror.StorageUnavailable, apperror.ArtifactStoreUnavailable, apperror.ReconciliationFailed,
		apperror.StorageReconciliationFailed, apperror.BlobCoordinationTimeout, apperror.BlobCoordinationFailed, apperror.OwnerAuthorityMissing:
		status = http.StatusServiceUnavailable
	}
	envelope := mcpserver.ErrorEnvelope{Code: code, Message: publicMessage(code), Retryable: retryable, TraceID: traceID}
	WriteJSON(writer, status, envelope)
}

func publicMessage(code string) string {
	switch code {
	case apperror.AuthRequired:
		return "authentication_required"
	case apperror.CapabilityDenied:
		return "capability_denied"
	case apperror.UploadTooLarge:
		return "upload_too_large"
	case apperror.UploadNotFound:
		return "upload_not_found"
	case apperror.UploadExpired:
		return "upload_expired"
	case apperror.UploadAlreadyClaimed:
		return "upload_already_claimed"
	case apperror.UploadTargetMismatch:
		return "upload_target_mismatch"
	case apperror.IdempotencyConflict:
		return "idempotency_conflict"
	case apperror.ToolEventIdempotencyConflict:
		return "tool_event_idempotency_conflict"
	case apperror.SourceEventIdempotencyConflict:
		return "source_event_idempotency_conflict"
	case apperror.SessionMetadataConflict:
		return "session_metadata_conflict"
	case apperror.SchemaMigrationRequired:
		return "schema_migration_required"
	case apperror.SchemaVersionTooNew:
		return "schema_version_too_new"
	case apperror.OwnerIdentityMismatch:
		return "owner_identity_mismatch"
	case apperror.DatabaseUnavailable:
		return "database_unavailable"
	case apperror.StorageUnavailable:
		return "storage_unavailable"
	case apperror.InvalidRequest:
		return "invalid_request"
	case apperror.ContextSessionNotFound:
		return "context_session_not_found"
	case apperror.ContextQueryInvalid:
		return "context_query_invalid"
	case apperror.ContextResultLimitInvalid:
		return "context_result_limit_invalid"
	case apperror.MemoryReadNotFound:
		return "memory_read_not_found"
	case apperror.ArtifactSearchInvalid:
		return "artifact_search_invalid"
	case apperror.ArtifactReadNotFound:
		return "artifact_read_not_found"
	case apperror.ArtifactContentUnavailable:
		return "artifact_content_unavailable"
	case apperror.CursorInvalid:
		return "cursor_invalid"
	default:
		return "request_failed"
	}
}
