// Package agentserver is appx's client for the Pi agent-server's project
// lifecycle API. agent-server owns project identity, on-disk layout, and a
// durable registry; appx is a control plane that asks agent-server to
// create/remove projects and otherwise proxies session traffic to it.
//
// See agent-server's
// docs/architecture/project-lifecycle-and-workspace-layout.md.
package agentserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Project mirrors the agent-server ProjectInfo response shape.
type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ProjectDir string `json:"projectDir"`
	CreatedAt  string `json:"createdAt"`
}

// Client talks to a single agent-server instance over HTTP. It is safe for
// concurrent use.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client for the given agent-server base URL (e.g.
// "http://127.0.0.1:4001"). An empty token disables bearer auth.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// EnsureProject creates a project with the given name, or returns the existing
// one — the endpoint is idempotent on name, so this is safe to call on every
// create and to re-run after an agent-server restart.
func (c *Client) EnsureProject(ctx context.Context, name string) error {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return fmt.Errorf("marshal create-project body: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/projects", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call agent-server create-project: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.statusError("create project", resp)
	}
	// Drain the body so the connection can be reused; the response shape
	// (id/projectDir) is derivable on the appx side and not needed here.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// DeleteProject removes a project (runtime, metadata, and on-disk dirs) from
// agent-server. A 404 is treated as success so deletes are idempotent.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/projects/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call agent-server delete-project: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return c.statusError("delete project", resp)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build agent-server request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// statusError reads a bounded slice of the error body for context.
func (c *Client) statusError(action string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("agent-server %s failed: %s: %s", action, resp.Status, strings.TrimSpace(string(snippet)))
}
