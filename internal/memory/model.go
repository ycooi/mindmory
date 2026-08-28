package memory

import "errors"

// Kind is an authority-specific cognitive-memory category.
type Kind string

const (
	KindUserPreference       Kind = "USER_PREFERENCE"
	KindPersonalGoal         Kind = "PERSONAL_GOAL"
	KindPersonalConstraint   Kind = "PERSONAL_CONSTRAINT"
	KindProjectDecision      Kind = "PROJECT_DECISION"
	KindDocumentFact         Kind = "DOCUMENT_FACT"
	KindContractTerm         Kind = "CONTRACT_TERM"
	KindDatasetFact          Kind = "DATASET_FACT"
	KindCodeArchitectureFact Kind = "CODE_ARCHITECTURE_FACT"
	KindEntityRelation       Kind = "ENTITY_RELATION"
	KindCorrection           Kind = "CORRECTION"
	KindLesson               Kind = "LESSON"
)

// Validate rejects cognitive-memory kinds outside the frozen V1 vocabulary.
func (k Kind) Validate() error {
	switch k {
	case KindUserPreference, KindPersonalGoal, KindPersonalConstraint, KindProjectDecision,
		KindDocumentFact, KindContractTerm, KindDatasetFact, KindCodeArchitectureFact,
		KindEntityRelation, KindCorrection, KindLesson:
		return nil
	default:
		return errors.New("invalid memory kind")
	}
}

// Lifecycle is the non-destructive cognitive-memory state machine.
type Lifecycle string

const (
	LifecycleActive      Lifecycle = "ACTIVE"
	LifecycleSuperseded  Lifecycle = "SUPERSEDED"
	LifecycleForgotten   Lifecycle = "FORGOTTEN"
	LifecycleInvalidated Lifecycle = "INVALIDATED"
)

// Validate rejects lifecycle values outside the non-destructive state model.
func (l Lifecycle) Validate() error {
	switch l {
	case LifecycleActive, LifecycleSuperseded, LifecycleForgotten, LifecycleInvalidated:
		return nil
	default:
		return errors.New("invalid memory lifecycle")
	}
}

// EpistemicStatus states what a derived cognitive claim is permitted to assert.
type EpistemicStatus string

const (
	EpistemicSourceAssertion    EpistemicStatus = "SOURCE_ASSERTION"
	EpistemicUserAccepted       EpistemicStatus = "USER_ACCEPTED"
	EpistemicVerifiedDerivation EpistemicStatus = "VERIFIED_DERIVATION"
)

// Validate rejects epistemic values outside the cognitive-claim dimension.
func (e EpistemicStatus) Validate() error {
	switch e {
	case EpistemicSourceAssertion, EpistemicUserAccepted, EpistemicVerifiedDerivation:
		return nil
	default:
		return errors.New("invalid epistemic status")
	}
}
