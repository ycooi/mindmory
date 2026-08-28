package memory

import "errors"

// ScopeType is the server-governed logical memory boundary.
type ScopeType string

const (
	ScopeGlobal  ScopeType = "GLOBAL"
	ScopeProject ScopeType = "PROJECT"
)

func (s ScopeType) Validate() error {
	if s != ScopeGlobal && s != ScopeProject {
		return errors.New("invalid memory scope")
	}
	return nil
}
