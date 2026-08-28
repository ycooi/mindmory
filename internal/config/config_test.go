package config

import "testing"

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadCLIDefaultsToLitePort(t *testing.T) {
	config, err := LoadCLI(mapLookup(map[string]string{
		"MINDMORY_ADMIN_TOKEN": "admin-token-at-least-24-characters",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "http://127.0.0.1:58080" {
		t.Fatalf("endpoint=%q", config.Endpoint)
	}
}

func TestLoadBridgeUsesPublicEnvironmentNames(t *testing.T) {
	config, err := LoadBridge(mapLookup(map[string]string{
		"MINDMORY_ENDPOINT":  "http://127.0.0.1:58080",
		"MINDMORY_MCP_TOKEN": "client-token-at-least-24-characters",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "http://127.0.0.1:58080" || config.Token != MCPToken("client-token-at-least-24-characters") {
		t.Fatalf("config=%+v", config)
	}
}

func TestLoadMCPServerRequiresBoundSession(t *testing.T) {
	values := map[string]string{
		"MINDMORY_ENDPOINT":      "http://127.0.0.1:58080",
		"MINDMORY_MCP_TOKEN":     "client-token-at-least-24-characters",
		"MINDMORY_MCP_LOG_LEVEL": "warn",
	}
	if _, err := LoadMCPServer(mapLookup(values)); err == nil {
		t.Fatal("expected missing bound session to fail")
	}
	values["MINDMORY_BOUND_SESSION_ID"] = "session-1"
	config, err := LoadMCPServer(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.LogLevel != "WARN" || config.BoundMessageID != "" {
		t.Fatalf("config=%+v", config)
	}
}

func TestEndpointRejectsCredentialsQueryAndFragments(t *testing.T) {
	for _, endpoint := range []string{
		"http://user:pass@127.0.0.1:58080",
		"http://127.0.0.1:58080?token=secret",
		"http://127.0.0.1:58080#fragment",
	} {
		if validateEndpoint(endpoint) == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
}
