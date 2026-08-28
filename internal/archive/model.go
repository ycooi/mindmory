package archive

import (
	"errors"

	"mindmory.local/core/internal/artifact/policy"
)

// Role is the exact source role of an archived interaction.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Validate rejects archived roles outside exact user/assistant/system/tool history.
func (r Role) Validate() error {
	switch r {
	case RoleUser, RoleAssistant, RoleSystem, RoleTool:
		return nil
	default:
		return errors.New("invalid archive role")
	}
}

// MessageEvidence contains the minimum authoritative metadata needed by policy.
type MessageEvidence struct {
	MessageID       string
	ClientID        string
	SessionID       string
	Role            Role
	Content         string
	ContentHash     string
	CurrentUserTurn bool
	Retrieved       bool
	SecretLike      bool
	InstructionLike bool
	Sensitivity     policy.Sensitivity
}

// EpisodeSensitivity monotonically inherits sensitivity from included sources.
func EpisodeSensitivity(messages []MessageEvidence, included map[string]bool) policy.Sensitivity {
	result := policy.SensitivityNormal
	for _, message := range messages {
		if included[message.MessageID] {
			result = policy.InheritSensitivity(result, message.Sensitivity)
		}
	}
	return result
}
