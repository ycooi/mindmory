// Package command implements the lite operator CLI.
package command

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mindmory.local/core/internal/config"
	"mindmory.local/core/internal/lite"
)

// Run handles version, validation, read-only diagnostics, and narrow operator
// actions supported by the lite daemon.
func Run(name, version string, arguments []string) int {
	if len(arguments) > 0 && arguments[0] == "checkpoint-hook" {
		return runCheckpointHook(arguments[1:], os.Stdin)
	}
	if len(arguments) > 0 && arguments[0] == "verify" {
		return runIntegrityVerify(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "vectors" {
		return runVectorCommand(arguments[1:])
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	showVersion := flags.Bool("version", false, "print version")
	checkConfig := flags.Bool("check-config", false, "validate operator configuration")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("%s %s\n", name, version)
		return 0
	}
	if *checkConfig {
		if _, err := config.LoadCLI(os.LookupEnv); err != nil {
			fmt.Fprintln(os.Stderr, "configuration rejected:", err)
			return 2
		}
		fmt.Printf("%s configuration valid\n", name)
		return 0
	}

	if operation, ok := retrievalCommand(flags.Args()); ok {
		cfg, err := config.LoadBridge(os.LookupEnv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "configuration rejected:", err)
			return 2
		}
		request, err := operation.request(strings.TrimRight(cfg.Endpoint, "/"), string(cfg.Token))
		if err != nil {
			return 2
		}
		return send(request, 3*time.Minute)
	}

	method, path, ok := adminOperation(flags.Args())
	if !ok {
		fmt.Fprintln(os.Stderr, "unsupported operation")
		return 2
	}
	cfg, err := config.LoadCLI(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration rejected:", err)
		return 2
	}
	request, err := http.NewRequest(method, strings.TrimRight(cfg.Endpoint, "/")+path, nil)
	if err != nil {
		return 2
	}
	request.Header.Set("X-Admin-Token", cfg.Token)
	return send(request, 3*time.Minute)
}

// hookInput is the common subset emitted by Codex and Claude Code for a
// UserPromptSubmit lifecycle event. Unknown host-specific fields are ignored.
type hookInput struct {
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	Prompt               string `json:"prompt"`
	CWD                  string `json:"cwd"`
	HookEventName        string `json:"hook_event_name"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

type hookCheckpointRequest struct {
	ExternalSessionID string                  `json:"external_session_id"`
	ProjectKey        string                  `json:"project_key,omitempty"`
	Mode              string                  `json:"mode"`
	Messages          []hookCheckpointMessage `json:"messages"`
	ToolEvents        []any                   `json:"tool_events"`
}

type hookCheckpointMessage struct {
	ExternalMessageID string    `json:"external_message_id"`
	Role              string    `json:"role"`
	ContentType       string    `json:"content_type"`
	Content           string    `json:"content"`
	OccurredAt        time.Time `json:"occurred_at"`
	AssistantID       string    `json:"assistant_id,omitempty"`
	AssistantName     string    `json:"assistant_name,omitempty"`
}

// runCheckpointHook converts a host lifecycle event on stdin into a Mindmory
// checkpoint. It deliberately writes nothing on success: hook stdout can be
// injected into the model context by agent hosts.
func runCheckpointHook(arguments []string, input io.Reader) int {
	flags := flag.NewFlagSet("mindmoryctl checkpoint-hook", flag.ContinueOnError)
	host := flags.String("host", "generic", "agent host name")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	var event hookInput
	decoder := json.NewDecoder(io.LimitReader(input, 2<<20))
	if err := decoder.Decode(&event); err != nil {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: invalid hook input")
		return 1
	}
	cfg, err := config.LoadBridge(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: configuration rejected")
		return 1
	}
	hostName := strings.ToLower(strings.TrimSpace(*host))
	if hostName == "" {
		hostName = "generic"
	}
	eventName := strings.ToLower(strings.TrimSpace(event.HookEventName))
	role, content := "user", strings.TrimSpace(event.Prompt)
	assistantID, assistantName := "", ""
	if eventName == "stop" {
		role, content = "assistant", strings.TrimSpace(event.LastAssistantMessage)
		assistantID, assistantName = hostName, assistantDisplayName(hostName)
	} else if eventName != "" && eventName != "userpromptsubmit" {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: unsupported hook event")
		return 1
	}
	if content == "" || strings.TrimSpace(event.SessionID) == "" {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: empty conversation message")
		return 1
	}
	sequenceMarker := strings.TrimSpace(event.TurnID)
	if sequenceMarker == "" && strings.TrimSpace(event.TranscriptPath) != "" {
		if info, statErr := os.Stat(event.TranscriptPath); statErr == nil {
			sequenceMarker = fmt.Sprintf("%s:%d:%d", event.TranscriptPath, info.Size(), info.ModTime().UnixNano())
		}
	}
	identity := strings.Join([]string{hostName, event.SessionID, sequenceMarker, role, content}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	externalMessageID := fmt.Sprintf("%s-%s-%x", hostName, role, digest[:16])
	payload, err := json.Marshal(hookCheckpointRequest{
		// setup.sh creates and binds this stable continuity session. Every host
		// writes to it, so the stdio bridge can resolve the latest user message.
		ExternalSessionID: "mindmory-continuity",
		ProjectKey:        strings.TrimSpace(event.CWD),
		Mode:              "INCREMENTAL",
		Messages: []hookCheckpointMessage{{
			ExternalMessageID: externalMessageID,
			Role:              role,
			ContentType:       "text/plain",
			Content:           content,
			OccurredAt:        time.Now().UTC(),
			AssistantID:       assistantID,
			AssistantName:     assistantName,
		}},
		ToolEvents: []any{},
	})
	if err != nil {
		return 1
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.Endpoint, "/")+"/v1/checkpoints", bytes.NewReader(payload))
	if err != nil {
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+string(cfg.Token))
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: daemon unavailable")
		return 1
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, "Mindmory checkpoint skipped: daemon rejected the event")
		return 1
	}
	return 0
}

func assistantDisplayName(hostName string) string {
	switch hostName {
	case "codex":
		return "Codex"
	case "claude-code":
		return "Claude Code"
	default:
		return hostName
	}
}

func send(request *http.Request, timeout time.Duration) int {
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Mindmory daemon unavailable")
		return 1
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = os.Stderr.Write(body)
		return 1
	}
	_, _ = os.Stdout.Write(body)
	return 0
}

func runVectorCommand(arguments []string) int {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mindmoryctl vectors status | rebuild --incident-id ID --confirm")
		return 2
	}
	cfg, err := config.LoadCLI(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration rejected:", err)
		return 2
	}
	method, path := http.MethodGet, "/v1/system/status"
	var body io.Reader
	switch arguments[0] {
	case "status":
		if len(arguments) != 1 {
			return 2
		}
	case "rebuild":
		flags := flag.NewFlagSet("mindmoryctl vectors rebuild", flag.ContinueOnError)
		incidentID := flags.String("incident-id", "", "startup incident identifier")
		confirm := flags.Bool("confirm", false, "confirm rebuilding vectors with the configured provider")
		if err := flags.Parse(arguments[1:]); err != nil || len(flags.Args()) != 0 || *incidentID == "" || !*confirm {
			fmt.Fprintln(os.Stderr, "rebuild requires --incident-id ID --confirm")
			return 2
		}
		method, path = http.MethodPost, "/v1/admin/vectors/rebuild"
		payload, _ := json.Marshal(map[string]any{"incident_id": *incidentID, "confirm": true})
		body = bytes.NewReader(payload)
	default:
		fmt.Fprintln(os.Stderr, "usage: mindmoryctl vectors status | rebuild --incident-id ID --confirm")
		return 2
	}
	request, err := http.NewRequest(method, strings.TrimRight(cfg.Endpoint, "/")+path, body)
	if err != nil {
		return 2
	}
	request.Header.Set("X-Admin-Token", cfg.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return send(request, 31*time.Minute)
}

func runIntegrityVerify(arguments []string) int {
	flags := flag.NewFlagSet("mindmoryctl verify", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "var/data", "canonical data directory")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	report, err := lite.VerifyDataDir(*dataDir, []byte(os.Getenv("MINDMORY_CURSOR_SIGNING_KEY")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "integrity verification failed:", err)
		return 1
	}
	raw, _ := json.Marshal(report)
	fmt.Println(string(raw))
	return 0
}

func adminOperation(arguments []string) (string, string, bool) {
	if len(arguments) == 1 {
		switch arguments[0] {
		case "ops":
			return http.MethodGet, "/v1/admin/ops", true
		case "proposals":
			return http.MethodGet, "/v1/admin/proposals", true
		case "snapshot":
			return http.MethodPost, "/v1/admin/snapshot", true
		}
	}
	if len(arguments) == 2 && arguments[0] == "learner" && arguments[1] == "extract" {
		return http.MethodPost, "/v1/admin/learner/extract", true
	}
	if len(arguments) == 3 && arguments[0] == "proposal" {
		switch arguments[1] {
		case "approve", "reject":
			return http.MethodPost, "/v1/admin/proposals/" + url.PathEscape(arguments[2]) + "/" + arguments[1], true
		}
	}
	if len(arguments) == 3 && arguments[0] == "memory" && arguments[1] == "retire" {
		return http.MethodPost, "/v1/admin/memories/" + url.PathEscape(arguments[2]) + "/retire", true
	}
	return "", "", false
}
