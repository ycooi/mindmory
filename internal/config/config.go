package config

import (
	"errors"
	"net/url"
	"strings"
)

// LookupEnv is the environment lookup contract used by configuration loading.
type LookupEnv func(string) (string, bool)

type AdminClientConfig struct {
	Endpoint string
	Token    string
}

type MCPClientConfig struct {
	Endpoint string
	Token    MCPToken
}

type MCPServerConfig struct {
	Endpoint, Token, BoundSessionID, BoundMessageID, LogLevel string
}

// LoadMCPServer reads the configuration needed by the stdio MCP bridge.
func LoadMCPServer(lookup LookupEnv) (MCPServerConfig, error) {
	value := trimmedLookup(lookup)
	result := MCPServerConfig{
		Endpoint:       value("MINDMORY_ENDPOINT"),
		Token:          value("MINDMORY_MCP_TOKEN"),
		BoundSessionID: value("MINDMORY_BOUND_SESSION_ID"),
		BoundMessageID: value("MINDMORY_BOUND_MESSAGE_ID"),
		LogLevel:       strings.ToUpper(value("MINDMORY_MCP_LOG_LEVEL")),
	}
	if result.LogLevel == "" {
		result.LogLevel = "INFO"
	}
	if validateEndpointAndToken(result.Endpoint, result.Token) != nil || result.BoundSessionID == "" || !validLogLevel(result.LogLevel) {
		return MCPServerConfig{}, errors.New("MCP server requires endpoint, token, bound session, and valid log level")
	}
	return result, nil
}

// LoadCLI reads the operator endpoint and credential. The endpoint defaults to
// the lite daemon's loopback address.
func LoadCLI(lookup LookupEnv) (AdminClientConfig, error) {
	value := trimmedLookup(lookup)
	endpoint := value("MINDMORY_ADMIN_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:58080"
	}
	token := value("MINDMORY_ADMIN_TOKEN")
	if err := validateEndpointAndToken(endpoint, token); err != nil {
		return AdminClientConfig{}, err
	}
	return AdminClientConfig{Endpoint: endpoint, Token: token}, nil
}

// LoadBridge reads the model-facing endpoint and credential for diagnostic
// retrieval commands.
func LoadBridge(lookup LookupEnv) (MCPClientConfig, error) {
	value := trimmedLookup(lookup)
	endpoint, token := value("MINDMORY_ENDPOINT"), value("MINDMORY_MCP_TOKEN")
	if err := validateEndpointAndToken(endpoint, token); err != nil {
		return MCPClientConfig{}, err
	}
	return MCPClientConfig{Endpoint: endpoint, Token: MCPToken(token)}, nil
}

func trimmedLookup(lookup LookupEnv) func(string) string {
	return func(name string) string {
		value, _ := lookup(name)
		return strings.TrimSpace(value)
	}
}

func validateEndpointAndToken(endpoint, token string) error {
	if validateEndpoint(endpoint) != nil || weakToken(token) {
		return errors.New("client endpoint and non-default token are required")
	}
	return nil
}

func validateEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint must be a clean absolute HTTP URL without embedded credentials")
	}
	return nil
}

func weakToken(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return len(value) < 24 || strings.Contains(lower, "replace") || strings.Contains(lower, "default")
}

func validLogLevel(value string) bool {
	switch value {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return true
	default:
		return false
	}
}
