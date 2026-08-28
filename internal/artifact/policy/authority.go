package policy

import (
	"context"
	"errors"
	"strings"

	"mindmory.local/core/internal/artifact/ingest"
	"mindmory.local/core/internal/debugnode"
)

// OriginClass records immutable artifact provenance.
type OriginClass string

const (
	OriginUserProvided   OriginClass = "USER_PROVIDED"
	OriginUserAuthored   OriginClass = "USER_AUTHORED"
	OriginAgentGenerated OriginClass = "AGENT_GENERATED"
	OriginExternalSource OriginClass = "EXTERNAL_SOURCE"
	OriginTrustedTool    OriginClass = "TRUSTED_TOOL_OUTPUT"
	OriginUnknown        OriginClass = "UNKNOWN"
)

// ApprovalState records governed user acceptance independently of origin.
type ApprovalState string

const (
	ApprovalUnreviewed   ApprovalState = "UNREVIEWED"
	ApprovalUserApproved ApprovalState = "USER_APPROVED"
	ApprovalUserRejected ApprovalState = "USER_REJECTED"
)

// Authority keeps origin private so ordinary code cannot mutate it.
type Authority struct {
	origin   OriginClass
	approval ApprovalState
}

// NewAuthority assigns origin from a validated authenticated ingestion channel.
func NewAuthority(channel ingest.Channel) (Authority, error) {
	origin, err := OriginForChannel(channel)
	if err != nil {
		return Authority{}, err
	}
	return Authority{origin: origin, approval: ApprovalUnreviewed}, nil
}

// Origin returns immutable provenance.
func (a Authority) Origin() OriginClass { return a.origin }

// Approval returns the current governed approval state.
func (a Authority) Approval() ApprovalState { return a.approval }

// OriginForChannel is the only origin assignment map and accepts no override.
func OriginForChannel(channel ingest.Channel) (OriginClass, error) {
	if err := channel.Validate(); err != nil {
		return "", err
	}
	switch channel {
	case ingest.ChannelHostUserAttachment:
		return OriginUserProvided, nil
	case ingest.ChannelAdminAuthored:
		return OriginUserAuthored, nil
	case ingest.ChannelGeneratedArtifact:
		return OriginAgentGenerated, nil
	case ingest.ChannelExternalImport:
		return OriginExternalSource, nil
	case ingest.ChannelTrustedToolCapture:
		return OriginTrustedTool, nil
	default:
		return OriginUnknown, nil
	}
}

// Validate rejects origin values outside the frozen provenance dimension.
func (o OriginClass) Validate() error {
	switch o {
	case OriginUserProvided, OriginUserAuthored, OriginAgentGenerated, OriginExternalSource, OriginTrustedTool, OriginUnknown:
		return nil
	default:
		return errors.New("invalid origin class")
	}
}

// Validate rejects approval values outside the governed acceptance dimension.
func (a ApprovalState) Validate() error {
	switch a {
	case ApprovalUnreviewed, ApprovalUserApproved, ApprovalUserRejected:
		return nil
	default:
		return errors.New("invalid approval state")
	}
}

// ApprovalChange is an explicit administrator decision.
type ApprovalChange struct {
	ArtifactID string
	State      ApprovalState
	Reason     string
	Confirmed  bool
	TraceID    string
}

// ChangeApproval validates an administrative transition while preserving origin.
func (a Authority) ChangeApproval(ctx context.Context, change ApprovalChange, observer debugnode.Observer) (Authority, error) {
	if err := a.origin.Validate(); err != nil {
		return Authority{}, err
	}
	if err := a.approval.Validate(); err != nil {
		return Authority{}, err
	}
	if err := change.State.Validate(); err != nil {
		return Authority{}, err
	}
	if strings.TrimSpace(change.ArtifactID) == "" || strings.TrimSpace(change.Reason) == "" || !change.Confirmed {
		return Authority{}, errors.New("artifact, reason, and confirmation are required")
	}
	if observer == nil {
		observer = debugnode.NopObserver{}
	}
	updated := Authority{origin: a.origin, approval: change.State}
	observer.Observe(ctx, debugnode.Event{Node: debugnode.ArtifactApprovalChange, TraceID: change.TraceID,
		ResourceID: change.ArtifactID, Status: "complete", ReasonCode: string(change.State)})
	return updated, nil
}
