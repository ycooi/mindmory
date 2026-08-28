package retrieval

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

type CursorPayload struct {
	Version        int    `json:"version"`
	SessionID      string `json:"session_id"`
	ProjectKeyHash string `json:"project_key_hash"`
	Revision       int64  `json:"revision"`
}

func ProjectHash(project string) string {
	v := sha256.Sum256([]byte(project))
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func SignCursor(key []byte, p CursorPayload) (string, error) {
	if len(key) < 32 {
		return "", errors.New("cursor key too short")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), nil
}

// SignProjectCursor signs a v2 project-scoped continuity cursor. It carries no
// session identity, so a fresh session in the same project can diff "what
// changed since this earlier continuity state" across sessions. The project
// hash still isolates projects.
func SignProjectCursor(key []byte, projectKey string, revision int64) (string, error) {
	return SignCursor(key, CursorPayload{Version: 2, ProjectKeyHash: ProjectHash(projectKey), Revision: revision})
}

// VerifyCursor accepts v1 session-bound and v2 project-scoped cursors.
func VerifyCursor(key []byte, value string) (CursorPayload, error) {
	var p CursorPayload
	if len(key) < 32 {
		return p, errors.New("cursor key too short")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) <= sha256.Size {
		return p, errors.New("invalid cursor")
	}
	body, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return p, errors.New("invalid cursor")
	}
	if json.Unmarshal(body, &p) != nil || p.ProjectKeyHash == "" || p.Revision < 0 {
		return CursorPayload{}, errors.New("invalid cursor")
	}
	switch p.Version {
	case 1:
		if p.SessionID == "" {
			return CursorPayload{}, errors.New("invalid cursor")
		}
	case 2:
		// project-scoped; session identity deliberately absent
	default:
		return CursorPayload{}, errors.New("invalid cursor")
	}
	return p, nil
}
