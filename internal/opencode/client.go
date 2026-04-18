package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const clientTimeout = 5 * time.Second
const maxResponseSize = 10 << 20

// OpenCodeProject represents a project as returned by GET /project.
type OpenCodeProject struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AbsolutePath string `json:"absolutePath"`
}

// Client is a thin HTTP client for the OpenCode server REST API.
// It communicates with the opencode process running inside a project container
// at its well-known port (4096 by default).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client targeting the given base URL. Trailing slashes are trimmed
// so callers don't need to worry about double-slash paths.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: clientTimeout},
	}
}

// HealthCheck calls GET /global/health. Returns nil on 200, error otherwise.
// Used by WaitForHealthy to poll readiness during container startup.
func (c *Client) HealthCheck() error {
	resp, err := c.httpClient.Get(c.baseURL + "/global/health")
	if err != nil {
		return fmt.Errorf("opencode health check: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode health check: status %d", resp.StatusCode)
	}
	return nil
}

// ListProjects calls GET /project and returns the discovered projects.
// Returns an empty slice (not nil) when the server returns an empty JSON array.
func (c *Client) ListProjects() ([]OpenCodeProject, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/project")
	if err != nil {
		return nil, fmt.Errorf("opencode list projects: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))
		return nil, fmt.Errorf("opencode list projects: status %d", resp.StatusCode)
	}
	var projects []OpenCodeProject
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&projects); err != nil {
		return nil, fmt.Errorf("opencode list projects: decode: %w", err)
	}
	return projects, nil
}

// SetAuth injects an API key for the given provider into the OpenCode server.
// It calls PUT /auth/:providerID with body {"type":"api","key":"<apiKey>"}.
// This matches the OpenCode server's auth endpoint schema (server.ts:99-129).
// Verified against OpenCode source: method=PUT, path param=providerID,
// body is a discriminated union — ApiAuth uses type="api" + key field.
func (c *Client) SetAuth(providerID, apiKey string) error {
	body := struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}{Type: "api", Key: apiKey}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("opencode set auth: marshal: %w", err)
	}

	url := c.baseURL + "/auth/" + providerID
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("opencode set auth: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opencode set auth: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode set auth: status %d", resp.StatusCode)
	}
	return nil
}
