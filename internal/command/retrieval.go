package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

type retrievalOperation struct {
	method, path string
	body         []byte
}

func parseOptions(args []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) || len(args[i]) < 3 || args[i][:2] != "--" {
			return nil, errors.New("invalid options")
		}
		out[args[i][2:]] = args[i+1]
	}
	return out, nil
}

func retrievalCommand(args []string) (retrievalOperation, bool) {
	if len(args) < 2 {
		return retrievalOperation{}, false
	}
	kind, action := args[0], args[1]
	opts, err := parseOptions(args[2:])
	if err != nil || opts["session"] == "" {
		return retrievalOperation{}, false
	}
	session := opts["session"]
	post := func(path string, value any) (retrievalOperation, bool) {
		body, _ := json.Marshal(value)
		return retrievalOperation{http.MethodPost, path, body}, true
	}
	limit, _ := strconv.Atoi(opts["limit"])
	maxChars, _ := strconv.Atoi(opts["max-chars"])
	switch kind + " " + action {
	case "context search":
		if opts["query"] == "" {
			return retrievalOperation{}, false
		}
		return post("/v1/context/search", map[string]any{"session_id": session, "query": opts["query"], "limit": limit})
	case "context packet":
		return post("/v1/context/packet", map[string]any{"session_id": session, "query": opts["query"], "max_chars": maxChars})
	case "memory recall":
		if opts["id"] == "" {
			return retrievalOperation{}, false
		}
		return retrievalOperation{http.MethodGet, "/v1/context/memories/" + url.PathEscape(opts["id"]) + "?session_id=" + url.QueryEscape(session), nil}, true
	case "artifact search":
		if opts["query"] == "" {
			return retrievalOperation{}, false
		}
		return post("/v1/context/artifacts/search", map[string]any{"session_id": session, "query": opts["query"], "limit": limit})
	case "artifact read":
		if opts["id"] == "" {
			return retrievalOperation{}, false
		}
		return retrievalOperation{http.MethodGet, "/v1/context/artifacts/" + url.PathEscape(opts["id"]) + "/read?session_id=" + url.QueryEscape(session) + "&max_chars=" + strconv.Itoa(maxChars), nil}, true
	case "continuity diff":
		return post("/v1/context/diff", map[string]any{"session_id": session, "cursor": opts["cursor"], "limit": limit})
	case "memory feedback":
		if opts["id"] == "" || opts["outcome"] == "" {
			return retrievalOperation{}, false
		}
		if opts["outcome"] != "helped" && opts["outcome"] != "misled" {
			return retrievalOperation{}, false
		}
		return post("/v1/context/feedback", map[string]any{"session_id": session, "memory_id": opts["id"], "outcome": opts["outcome"], "note": opts["note"]})
	}
	return retrievalOperation{}, false
}

func (o retrievalOperation) request(endpoint, token string) (*http.Request, error) {
	r, err := http.NewRequest(o.method, endpoint+o.path, bytes.NewReader(o.body))
	if err == nil {
		r.Header.Set("Authorization", "Bearer "+token)
		if len(o.body) > 0 {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	return r, err
}
