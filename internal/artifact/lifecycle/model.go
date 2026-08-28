// Package lifecycle defines logical artifact roles separately from physical byte retention.
package lifecycle

import (
	"errors"
	"time"
)

type Role string

const (
	RoleSource      Role = "SOURCE"
	RoleWorkProduct Role = "WORK_PRODUCT"
	RoleFinalOutput Role = "FINAL_OUTPUT"
)

func (v Role) Validate() error {
	switch v {
	case RoleSource, RoleWorkProduct, RoleFinalOutput:
		return nil
	default:
		return errors.New("invalid artifact role")
	}
}

type RetentionClass string

const (
	RetentionSession   RetentionClass = "SESSION"
	RetentionTemporary RetentionClass = "TEMPORARY"
	RetentionRetained  RetentionClass = "RETAINED"
	RetentionPermanent RetentionClass = "PERMANENT"
)

func (v RetentionClass) Validate() error {
	switch v {
	case RetentionSession, RetentionTemporary, RetentionRetained, RetentionPermanent:
		return nil
	default:
		return errors.New("invalid retention class")
	}
}

type StorageState string

const (
	StoragePresent         StorageState = "PRESENT"
	StorageEvictionPending StorageState = "EVICTION_PENDING"
	StorageEvicted         StorageState = "EVICTED"
	StorageRestorePending  StorageState = "RESTORE_PENDING"
)

func (v StorageState) Validate() error {
	switch v {
	case StoragePresent, StorageEvictionPending, StorageEvicted, StorageRestorePending:
		return nil
	default:
		return errors.New("invalid storage state")
	}
}

type Recoverability string

const (
	RecoverabilityReupload       Recoverability = "REUPLOAD"
	RecoverabilityRegenerable    Recoverability = "REGENERABLE"
	RecoverabilityExternalSource Recoverability = "EXTERNAL_SOURCE"
	RecoverabilityNone           Recoverability = "NONE"
	RecoverabilityUnknown        Recoverability = "UNKNOWN"
)

func (v Recoverability) Validate() error {
	switch v {
	case RecoverabilityReupload, RecoverabilityRegenerable, RecoverabilityExternalSource, RecoverabilityNone, RecoverabilityUnknown:
		return nil
	default:
		return errors.New("invalid recoverability")
	}
}

type EvidenceAvailability string

const (
	EvidenceOnline               EvidenceAvailability = "ONLINE"
	EvidenceMetadataOnly         EvidenceAvailability = "METADATA_ONLY"
	EvidenceOfflineRehydratable  EvidenceAvailability = "OFFLINE_REHYDRATABLE"
	EvidenceOfflineUnrecoverable EvidenceAvailability = "OFFLINE_UNRECOVERABLE"
)

func (v EvidenceAvailability) Validate() error {
	switch v {
	case EvidenceOnline, EvidenceMetadataOnly, EvidenceOfflineRehydratable, EvidenceOfflineUnrecoverable:
		return nil
	default:
		return errors.New("invalid evidence availability")
	}
}

type Policy struct {
	CardMaxBytes          int64
	SessionGrace          time.Duration
	TemporaryMinimumAge   time.Duration
	TemporaryMaximumAge   time.Duration
	TemporaryQuotaBytes   int64
	RetainedMinimumAge    time.Duration
	RetainedQuotaBytes    int64
	PermanentAutoEviction bool
	BatchSize             int
	GCEnabled             bool
	ArtifactLeaseTTL      time.Duration
	ArtifactLeaseMaxLife  time.Duration
	Version               string
}

func DefaultPolicy() Policy {
	return Policy{CardMaxBytes: 1 << 20, SessionGrace: 72 * time.Hour, TemporaryMinimumAge: 7 * 24 * time.Hour,
		TemporaryMaximumAge: 90 * 24 * time.Hour, TemporaryQuotaBytes: 100 << 30,
		RetainedMinimumAge: 180 * 24 * time.Hour, RetainedQuotaBytes: 1 << 40, BatchSize: 100,
		GCEnabled: false, ArtifactLeaseTTL: 30 * time.Minute, ArtifactLeaseMaxLife: 24 * time.Hour, Version: "stage3.3.1-v1"}
}

func (p Policy) Validate() error {
	if p.CardMaxBytes <= 0 || p.CardMaxBytes > 2<<20 || p.SessionGrace < 0 || p.TemporaryMinimumAge < 0 ||
		p.TemporaryMaximumAge < p.TemporaryMinimumAge || p.TemporaryQuotaBytes < 0 || p.RetainedMinimumAge < 0 ||
		p.RetainedQuotaBytes < 0 || p.BatchSize < 1 || p.BatchSize > 1000 || p.ArtifactLeaseTTL <= 0 ||
		p.ArtifactLeaseTTL > 24*time.Hour || p.ArtifactLeaseMaxLife < p.ArtifactLeaseTTL ||
		p.ArtifactLeaseMaxLife > 24*time.Hour || p.Version == "" {
		return errors.New("RETENTION_POLICY_INVALID")
	}
	return nil
}

// Defaults returns server-owned role and retention assignments for promoted output slots.
func Defaults(outputRole string) (Role, RetentionClass, error) {
	switch outputRole {
	case "PRIMARY_RESULT", "SECONDARY_RESULT":
		return RoleWorkProduct, RetentionTemporary, nil
	case "FINAL_REPORT":
		return RoleFinalOutput, RetentionRetained, nil
	case "DIAGNOSTIC":
		return RoleWorkProduct, RetentionSession, nil
	default:
		return "", "", errors.New("unsupported output role")
	}
}
