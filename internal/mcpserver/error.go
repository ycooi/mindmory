package mcpserver

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrorEnvelope is the stable public failure contract. Message and details must be content-safe.
type ErrorEnvelope struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	TraceID   string         `json:"trace_id"`
	Details   map[string]any `json:"details,omitempty"`
}

var safeErrorToken = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,256}$`)

// Validate ensures the public error contract contains bounded diagnostic metadata, not source content.
func (e ErrorEnvelope) Validate() error {
	if !safeErrorToken.MatchString(e.Code) || !safeErrorToken.MatchString(e.TraceID) ||
		strings.TrimSpace(e.Message) == "" || len(e.Message) > 256 || strings.ContainsAny(e.Message, "\r\n") {
		return errors.New("invalid public error envelope")
	}
	if len(e.Details) > 16 {
		return errors.New("public error details are too large")
	}
	for key, value := range e.Details {
		if !safeErrorToken.MatchString(key) {
			return errors.New("unsafe public error detail key")
		}
		switch typed := value.(type) {
		case string:
			if !safeErrorToken.MatchString(typed) {
				return errors.New("unsafe public error detail value")
			}
		case bool, int, int32, int64, uint, uint32, uint64, float64:
		default:
			return fmt.Errorf("unsupported public error detail type %T", value)
		}
	}
	return nil
}
