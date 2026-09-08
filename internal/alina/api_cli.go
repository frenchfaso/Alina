package alina

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This is the discoverable map of the existing local API, not another command
// tree. Less frequent operations stay here and in Alina's native tools.
var apiCatalog = []map[string]any{
	{"method": "GET", "path": "/v1/status"},
	{"method": "POST", "path": "/v1/jobs", "body": map[string]any{"session": "local", "message": "Your request", "request_id": "unique-id", "interactive": true}},
	{"method": "GET", "path": "/v1/jobs/{id}"},
	{"method": "POST", "path": "/v1/jobs/{id}/steer", "body": map[string]string{"message": "Correction", "request_id": "unique-id"}},
	{"method": "POST", "path": "/v1/jobs/{id}/cancel"},
	{"method": "POST", "path": "/v1/jobs/{id}/resume"},
	{"method": "POST", "path": "/v1/jobs/{id}/approve", "body": map[string]string{"approval_id": "id-from-job", "scope": "once"}, "values": map[string]any{"scope": []string{"once", "restart", "always", "deny"}}},
	{"method": "GET", "path": "/v1/grants"},
	{"method": "DELETE", "path": "/v1/grants/{id}"},
	{"method": "GET", "path": "/v1/intentions"},
	{"method": "GET", "path": "/v1/memory/read?q=focus&offset=0", "values": map[string]any{"q": []string{"focus", "recent", "archive", "soul", "YYYY-MM-DD", "ID"}}},
	{"method": "GET", "path": "/v1/memory/search?q=query"},
	{"method": "POST", "path": "/v1/memory/focus", "body": map[string]any{"id": "memory-id", "pinned": true}},
	{"method": "POST", "path": "/v1/memory/jobs", "body": map[string]string{"kind": "dream"}, "values": map[string]any{"kind": []string{"dream", "reindex"}}},
	{"method": "GET", "path": "/v1/tasks"},
	{"method": "POST", "path": "/v1/tasks", "body": map[string]any{"name": "Task", "cron": "0 9 * * *", "prompt": "Request", "catch_up": true}, "note": "For a one-shot, replace cron with at (RFC3339)."},
	{"method": "POST", "path": "/v1/tasks/{id}", "body": map[string]string{"action": "pause"}, "values": map[string]any{"action": []string{"pause", "resume", "remove"}}},
}

func apiCLI(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return printJSON(out, map[string]any{"schema": 1, "endpoints": apiCatalog, "body": "JSON argument or - to read stdin; URL-encode query values"})
	}
	if len(args) < 2 || len(args) > 3 {
		return errors.New("usage: alina api METHOD /v1/path [JSON|-]")
	}
	method := strings.ToUpper(args[0])
	if method != "GET" && method != "POST" && method != "DELETE" {
		return errors.New("method must be GET, POST or DELETE")
	}
	u, err := url.ParseRequestURI(args[1])
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/v1/") {
		return errors.New("API path must start with /v1/ and stay on the local socket")
	}
	var body any
	if len(args) == 3 {
		data := []byte(args[2])
		if args[2] == "-" {
			data, err = io.ReadAll(io.LimitReader(in, 40<<10+1))
			if err != nil {
				return err
			}
		}
		if len(data) > 40<<10 {
			return errors.New("API body exceeds 40 KiB")
		}
		if err = json.Unmarshal(data, &body); err != nil {
			return errors.New("API body must be valid JSON")
		}
	}
	return apiRequest(ctx, dir, method, u.RequestURI(), body, out)
}

func apiRequest(ctx context.Context, dir, method, path string, body any, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://alina"+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := LocalClient(dir).Do(req)
	if err != nil {
		return errors.New("Alina is not reachable; run alina doctor")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("API response exceeds 8 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &localAPIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data)), RequestID: resp.Header.Get("X-Alina-Request-ID")}
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return errors.New("local API returned invalid JSON")
	}
	return printJSON(out, value)
}

type localAPIError struct {
	RequestID string
	Status    int
	Message   string
}

func (e *localAPIError) Error() string { return e.Message }
