package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mindmory.local/core/internal/apperror"
	"mindmory.local/core/internal/debugnode"
)

const maximumToolJSON = 64 << 10

type Client struct {
	Endpoint, Token string
	HTTP            *http.Client
	Observer        debugnode.Observer
}
type PublicError struct{ Envelope ErrorEnvelope }

type SystemIncident struct {
	Code                 string   `json:"code"`
	IncidentID           string   `json:"incident_id"`
	Severity             string   `json:"severity"`
	AffectedCapabilities []string `json:"affected_capabilities"`
	DataSafety           string   `json:"data_safety"`
	OperatorMessage      string   `json:"operator_message"`
	CopyPasteCommands    []string `json:"copy_paste_commands"`
	AgentInstruction     string   `json:"agent_instruction"`
}
type ModelIdentity struct {
	Provider              string `json:"provider,omitempty"`
	ModelName             string `json:"model_name,omitempty"`
	ModelDigest           string `json:"model_digest,omitempty"`
	Dimensions            int    `json:"dimensions,omitempty"`
	EmbeddingInputVersion int    `json:"embedding_input_version,omitempty"`
	NormalizationVersion  int    `json:"normalization_version,omitempty"`
}
type SystemStatus struct {
	State           string           `json:"state"`
	InitializedAt   string           `json:"initialized_at"`
	Canonical       string           `json:"canonical"`
	Lexical         string           `json:"lexical"`
	Embeddings      string           `json:"embeddings"`
	MCP             string           `json:"mcp"`
	ActiveModel     ModelIdentity    `json:"active_model"`
	ConfiguredModel ModelIdentity    `json:"configured_model"`
	Incidents       []SystemIncident `json:"incidents"`
	Configuration   map[string]any   `json:"configuration"`
	Statistics      map[string]any   `json:"statistics"`
}

type ActionRequiredError struct{ Incident SystemIncident }

func (e *ActionRequiredError) Error() string { return formatIncident(e.Incident) }

func (e *PublicError) Error() string {
	return fmt.Sprintf("%s retryable=%t trace_id=%s", e.Envelope.Code, e.Envelope.Retryable, e.Envelope.TraceID)
}
func (c Client) Do(ctx context.Context, method, path string, input any) (result map[string]any, resultErr error) {
	if path != "/v1/system/status" {
		if status, statusErr := c.SystemStatus(ctx); statusErr == nil && (status.State == "ACTION_REQUIRED" || status.State == "BUILDING") && len(status.Incidents) > 0 {
			return nil, &ActionRequiredError{Incident: status.Incidents[0]}
		}
	}
	return c.do(ctx, method, path, input)
}

func (c Client) SystemStatus(ctx context.Context) (SystemStatus, error) {
	result, err := c.do(ctx, http.MethodGet, "/v1/system/status", nil)
	if err != nil {
		return SystemStatus{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return SystemStatus{}, err
	}
	var status SystemStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return SystemStatus{}, err
	}
	return status, nil
}

func formatIncident(incident SystemIncident) string {
	var b strings.Builder
	b.WriteString("MINDMORY REQUIRES ATTENTION\n\n")
	b.WriteString("Code: ")
	b.WriteString(incident.Code)
	b.WriteString("\nIncident: ")
	b.WriteString(incident.IncidentID)
	b.WriteString("\n\n")
	b.WriteString(incident.OperatorMessage)
	b.WriteString("\n\n")
	b.WriteString(incident.DataSafety)
	if len(incident.CopyPasteCommands) > 0 {
		b.WriteString("\n\nCopy and run:\n")
		for _, command := range incident.CopyPasteCommands {
			b.WriteString("  ")
			b.WriteString(command)
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n")
	b.WriteString(incident.AgentInstruction)
	return strings.TrimSpace(b.String())
}

func (c Client) do(ctx context.Context, method, path string, input any) (result map[string]any, resultErr error) {
	started := time.Now()
	observer := c.Observer
	if observer == nil {
		observer = debugnode.NopObserver{}
	}
	safePath := strings.SplitN(path, "?", 2)[0]
	observer.Observe(ctx, debugnode.Event{Node: debugnode.MCPToolCall, Status: "start", Tool: safePath})
	defer func() {
		node, status, reason := debugnode.MCPToolSuccess, "complete", ""
		if resultErr != nil {
			node, status, reason = debugnode.MCPToolError, "error", apperror.Code(resultErr)
			var public *PublicError
			if errors.As(resultErr, &public) {
				reason = public.Envelope.Code
			}
		}
		observer.Observe(ctx, debugnode.Event{Node: node, Status: status, Tool: safePath, ReasonCode: reason, Duration: time.Since(started)})
	}()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		if len(raw) > maximumToolJSON {
			return nil, errors.New("request exceeds MCP tool limit")
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("mindmory HTTP unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumToolJSON+1))
	if err != nil || len(raw) > maximumToolJSON {
		return nil, errors.New("response exceeds MCP tool limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope ErrorEnvelope
		if json.Unmarshal(raw, &envelope) != nil || envelope.Validate() != nil {
			return nil, errors.New("mindmory request failed")
		}
		return nil, &PublicError{Envelope: envelope}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, errors.New("invalid Mindmory response")
	}
	return out, nil
}
func queryPath(path string, values map[string]string) string {
	q := url.Values{}
	for k, v := range values {
		q.Set(k, v)
	}
	return path + "?" + q.Encode()
}
