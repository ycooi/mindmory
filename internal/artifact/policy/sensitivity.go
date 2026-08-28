package policy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mindmory.local/core/internal/debugnode"
)

// Sensitivity is ordered from ordinary private content to maximally restricted content.
type Sensitivity uint8

const (
	SensitivityNormal Sensitivity = iota
	SensitivitySensitive
	SensitivitySecret
	SensitivityRestricted
)

// Validate rejects values outside the frozen sensitivity ordering.
func (s Sensitivity) Validate() error {
	if s > SensitivityRestricted {
		return errors.New("invalid sensitivity")
	}
	return nil
}

func (s Sensitivity) String() string {
	switch s {
	case SensitivityNormal:
		return "NORMAL"
	case SensitivitySensitive:
		return "SENSITIVE"
	case SensitivitySecret:
		return "SECRET"
	case SensitivityRestricted:
		return "RESTRICTED"
	default:
		return "INVALID"
	}
}

// MarshalJSON keeps the wire contract descriptive instead of exposing numeric ordering.
func (s Sensitivity) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s.String())
}

// UnmarshalJSON accepts only the frozen symbolic sensitivity values.
func (s *Sensitivity) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch value {
	case "NORMAL":
		*s = SensitivityNormal
	case "SENSITIVE":
		*s = SensitivitySensitive
	case "SECRET":
		*s = SensitivitySecret
	case "RESTRICTED":
		*s = SensitivityRestricted
	default:
		return errors.New("invalid sensitivity")
	}
	return nil
}

// InheritSensitivity returns the stricter source or processor classification.
func InheritSensitivity(source, detected Sensitivity) Sensitivity {
	if detected > source {
		return detected
	}
	return source
}

// DowngradeRequest is an explicit administrative sensitivity reduction.
type DowngradeRequest struct {
	ResourceID string
	Current    Sensitivity
	Requested  Sensitivity
	Reason     string
	Confirmed  bool
	TraceID    string
}

// DowngradeResult tells persistence to audit, recalculate eligibility, and rebuild projections.
type DowngradeResult struct {
	Sensitivity                  Sensitivity
	AuditRequired                bool
	RecalculateRetrieval         bool
	InvalidateSemanticProjection bool
}

// AuthorizeDowngrade validates an admin-only downgrade contract.
func AuthorizeDowngrade(ctx context.Context, request DowngradeRequest, observer debugnode.Observer) (DowngradeResult, error) {
	if request.Current > SensitivityRestricted || request.Requested > SensitivityRestricted {
		return DowngradeResult{}, errors.New("invalid sensitivity")
	}
	if request.Requested >= request.Current {
		return DowngradeResult{}, errors.New("requested sensitivity is not a downgrade")
	}
	if strings.TrimSpace(request.ResourceID) == "" || strings.TrimSpace(request.Reason) == "" || !request.Confirmed {
		return DowngradeResult{}, errors.New("resource, reason, and confirmation are required")
	}
	if observer == nil {
		observer = debugnode.NopObserver{}
	}
	observer.Observe(ctx, debugnode.Event{Node: debugnode.SensitivityDowngrade, TraceID: request.TraceID,
		ResourceID: request.ResourceID, Status: "authorized", ReasonCode: request.Requested.String()})
	return DowngradeResult{Sensitivity: request.Requested, AuditRequired: true,
		RecalculateRetrieval: true, InvalidateSemanticProjection: true}, nil
}
