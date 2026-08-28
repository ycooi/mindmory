package memory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"time"
)

type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "PENDING"
	ProposalApplied  ProposalStatus = "APPLIED"
	ProposalRejected ProposalStatus = "REJECTED"
)

type MessageQuote struct {
	Hash      string
	StartByte int
	EndByte   int
}

type ProposalIdentity struct {
	ClientKey, SessionID, MessageID string
	Mutation                        MutationKind
	TargetMemoryID                  string
	ProposedKind                    Kind
	Scope                           ScopeType
	ProjectKey                      string
	Subject                         string
	Replacement                     string
	RequestEvidenceHash             string
	EvidenceContentHash             string
	Evidence                        *MessageQuote
}

type Proposal struct {
	ID, RequestHash, ReasonCode, AppliedMemoryID string
	GateClass                                    GateClass
	Identity                                     ProposalIdentity
	Status                                       ProposalStatus
	CreatedAt                                    time.Time
	ResolvedAt                                   *time.Time
}

func (p ProposalStatus) Validate() error {
	if p != ProposalPending && p != ProposalApplied && p != ProposalRejected {
		return errors.New("invalid proposal status")
	}
	return nil
}

// RequestHash is a canonical length-prefixed digest of server-hydrated
// proposal identity. Length framing prevents tuple ambiguity.
func RequestHash(identity ProposalIdentity) string {
	digest := sha256.New()
	for _, value := range []string{
		identity.ClientKey, identity.SessionID, identity.MessageID, string(identity.Mutation),
		identity.TargetMemoryID, string(identity.ProposedKind), string(identity.Scope), identity.ProjectKey,
		identity.Subject, identity.Replacement, identity.RequestEvidenceHash,
		identity.EvidenceContentHash,
	} {
		writeHashField(digest, value)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeHashField(destination hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}
