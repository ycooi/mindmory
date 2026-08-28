// Package auth implements separated, constant-time credential domains.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/config"
)

type PrincipalType string

const (
	PrincipalMCP       PrincipalType = "MCP"
	PrincipalIngestion PrincipalType = "INGESTION"
	PrincipalAdmin     PrincipalType = "ADMIN"
	PrincipalWorker    PrincipalType = "INTERNAL_WORKER"
)

type Principal struct {
	Key                string
	Type               PrincipalType
	MCPCapabilities    []config.MCPClientCapability
	IngestCapabilities []config.IngestionCapability
}

type digest [sha256.Size]byte

type MCPAuthenticator struct{ credentials map[digest]Principal }
type IngestionAuthenticator struct{ credentials map[digest]Principal }
type AdminAuthenticator struct{ credential digest }
type WorkerAuthenticator struct{ credential digest }

func NewMCPAuthenticator(principals map[string]config.MCPPrincipalConfig) *MCPAuthenticator {
	result := &MCPAuthenticator{credentials: make(map[digest]Principal, len(principals))}
	for key, value := range principals {
		result.credentials[hash(string(value.Token))] = Principal{Key: key, Type: PrincipalMCP, MCPCapabilities: append([]config.MCPClientCapability(nil), value.Capabilities...)}
	}
	return result
}

func NewIngestionAuthenticator(principals map[string]config.IngestionPrincipalConfig) *IngestionAuthenticator {
	result := &IngestionAuthenticator{credentials: make(map[digest]Principal, len(principals))}
	for key, value := range principals {
		result.credentials[hash(string(value.Token))] = Principal{Key: key, Type: PrincipalIngestion, IngestCapabilities: append([]config.IngestionCapability(nil), value.Capabilities...)}
	}
	return result
}

func NewAdminAuthenticator(token string) *AdminAuthenticator {
	return &AdminAuthenticator{credential: hash(token)}
}
func NewWorkerAuthenticator(token string) *WorkerAuthenticator {
	return &WorkerAuthenticator{credential: hash(token)}
}

func (a *MCPAuthenticator) Authenticate(token string, required config.MCPClientCapability) (Principal, error) {
	principal, ok := match(a.credentials, token)
	if !ok {
		return Principal{}, apperror.New(apperror.AuthRequired, false, nil)
	}
	for _, capability := range principal.MCPCapabilities {
		if capability == required {
			return principal, nil
		}
	}
	return Principal{}, apperror.New(apperror.CapabilityDenied, false, nil)
}

func (a *IngestionAuthenticator) Authenticate(token string, required config.IngestionCapability) (Principal, error) {
	principal, ok := match(a.credentials, token)
	if !ok {
		return Principal{}, apperror.New(apperror.AuthRequired, false, nil)
	}
	for _, capability := range principal.IngestCapabilities {
		if capability == required {
			return principal, nil
		}
	}
	return Principal{}, apperror.New(apperror.CapabilityDenied, false, nil)
}

func (a *AdminAuthenticator) Authenticate(token string) error {
	return matchSingle(a.credential, token)
}
func (a *WorkerAuthenticator) Authenticate(token string) error {
	return matchSingle(a.credential, token)
}

func Bearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", apperror.New(apperror.AuthRequired, false, nil)
	}
	return parts[1], nil
}

func hash(value string) digest { return sha256.Sum256([]byte(value)) }

func match(credentials map[digest]Principal, token string) (Principal, bool) {
	incoming := hash(token)
	for expected, principal := range credentials {
		if subtle.ConstantTimeCompare(incoming[:], expected[:]) == 1 {
			return principal, true
		}
	}
	return Principal{}, false
}

func matchSingle(expected digest, token string) error {
	incoming := hash(token)
	if subtle.ConstantTimeCompare(incoming[:], expected[:]) != 1 {
		return apperror.New(apperror.AuthRequired, false, errors.New("credential rejected"))
	}
	return nil
}
