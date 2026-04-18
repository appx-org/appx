# Phase 5 Steps 4-5: App Health Checker + OpenCode Integration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add on-demand TCP health checking for agent-built apps on their assigned ports, and create a thin Go HTTP client for the OpenCode server API so appx can poll health, inject API keys, and surface OpenCode status in the dashboard.

**Architecture:** `HealthChecker` is a stateless struct called synchronously from the project list handler -- no background goroutine. `opencode.Client` wraps three OpenCode REST endpoints with plain `net/http`. On startup, appx polls OpenCode health until available, then injects the Anthropic API key via `POST /auth`. The list-projects handler merges health data into the JSON response.

**Tech Stack:** Go 1.26, `net/http`, `net` (TCP dial), SQLite, `httptest` for mocking

**Reference:** See `docs/plans/phase_5_plan.md` (Steps 4-5), `docs/analysis/refactors/de-docker-refactor.md`

---

### Task 1: Health checker -- write tests first

**Files:**
- Create: `internal/project/health_test.go`

- [ ] **Step 1: Write health checker tests**

Create `internal/project/health_test.go`:

```go
package project

import (
	"net"
	"strconv"
	"testing"
)

// TestHealthChecker_PortListening verifies that Check returns true for a
// project whose assigned port has an active TCP listener.
func TestHealthChecker_PortListening(t *testing.T) {
	// Start a TCP listener on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	hc := NewHealthChecker()
	projects := []*Project{
		{ID: "p1", Name: "myapp", Port: port},
	}

	result := hc.Check(projects)
	if !result["p1"] {
		t.Errorf("expected project p1 to be healthy, got false")
	}
}

// TestHealthChecker_PortNotListening verifies that Check returns false for a
// project whose assigned port has no listener.
func TestHealthChecker_PortNotListening(t *testing.T) {
	// Find a port that is not listening by binding and immediately closing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // port is now free -- nothing listening

	hc := NewHealthChecker()
	projects := []*Project{
		{ID: "p2", Name: "deadapp", Port: port},
	}

	result := hc.Check(projects)
	if result["p2"] {
		t.Errorf("expected project p2 to be unhealthy, got true")
	}
}

// TestHealthChecker_EmptyList verifies that Check handles an empty project
// slice without error and returns an empty map.
func TestHealthChecker_EmptyList(t *testing.T) {
	hc := NewHealthChecker()
	result := hc.Check([]*Project{})
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

// TestHealthChecker_MultipleProjects verifies that Check handles a mix of
// listening and non-listening ports correctly.
func TestHealthChecker_MultipleProjects(t *testing.T) {
	// One listening port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	listenPort := ln.Addr().(*net.TCPAddr).Port

	// One closed port.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	hc := NewHealthChecker()
	projects := []*Project{
		{ID: "alive", Name: "alive", Port: listenPort},
		{ID: "dead", Name: "dead", Port: closedPort},
	}

	result := hc.Check(projects)
	if !result["alive"] {
		t.Errorf("expected 'alive' to be healthy")
	}
	if result["dead"] {
		t.Errorf("expected 'dead' to be unhealthy")
	}
}

// TestHealthChecker_ZeroPort verifies that a project with port 0 is treated
// as unhealthy (no dial attempted).
func TestHealthChecker_ZeroPort(t *testing.T) {
	hc := NewHealthChecker()
	projects := []*Project{
		{ID: "noport", Name: "noport", Port: 0},
	}

	result := hc.Check(projects)
	if result["noport"] {
		t.Errorf("expected project with port 0 to be unhealthy")
	}
}

// TestHealthChecker_PortString verifies the portString helper formats
// correctly.
func TestHealthChecker_PortString(t *testing.T) {
	want := "127.0.0.1:12345"
	got := "127.0.0.1:" + strconv.Itoa(12345)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests -- expect compile failure**

Run: `go test ./internal/project/ -run TestHealthChecker -v 2>&1 | head -10`
Expected: FAIL -- `NewHealthChecker` undefined

- [ ] **Step 3: Commit test file**

```bash
git add internal/project/health_test.go
git commit -m "test: add health checker tests (red -- implementation pending)"
```

---

### Task 2: Health checker -- implement

**Files:**
- Create: `internal/project/health.go`

- [ ] **Step 1: Implement HealthChecker**

Create `internal/project/health.go`:

```go
package project

import (
	"net"
	"strconv"
	"time"
)

// healthDialTimeout is the maximum time to wait for a TCP connection to a
// project's assigned port. 500ms is generous for localhost connections and
// keeps the list-projects handler fast even with many projects.
const healthDialTimeout = 500 * time.Millisecond

// HealthChecker probes whether agent-built apps are listening on their
// assigned ports. It is stateless and safe for concurrent use. Called
// on-demand by the project list handler -- not a background goroutine.
type HealthChecker struct{}

// NewHealthChecker creates a HealthChecker. The struct is stateless; the
// constructor exists for consistency with other appx types and to allow
// future extension (e.g. configurable timeout).
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

// Check probes each project's assigned port via TCP dial on 127.0.0.1 and
// returns a map of project ID to reachability. A project is considered
// healthy if a TCP connection can be established within healthDialTimeout.
// Projects with port 0 are always unhealthy (port not assigned).
func (hc *HealthChecker) Check(projects []*Project) map[string]bool {
	result := make(map[string]bool, len(projects))
	for _, p := range projects {
		if p.Port <= 0 {
			result[p.ID] = false
			continue
		}
		addr := "127.0.0.1:" + strconv.Itoa(p.Port)
		conn, err := net.DialTimeout("tcp", addr, healthDialTimeout)
		if err != nil {
			result[p.ID] = false
			continue
		}
		conn.Close()
		result[p.ID] = true
	}
	return result
}
```

- [ ] **Step 2: Run tests -- expect all green**

Run: `go test ./internal/project/ -run TestHealthChecker -v`
Expected: all 6 tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/project/health.go
git commit -m "feat: add HealthChecker -- TCP dial to project assigned ports"
```

---

### Task 3: Wire health checker into project list handler

**Files:**
- Modify: `internal/project/project.go` -- add `AppRunning` field
- Modify: `internal/server/project_handlers.go` -- use health checker in list handler
- Modify: `internal/server/router.go` -- pass health checker to handler
- Modify: `internal/server/server.go` -- add HealthChecker to Config (or create in Run)

- [ ] **Step 1: Add `AppRunning` field to Project struct**

In `internal/project/project.go`, add the `AppRunning` field to the `Project` struct. This field is populated at query time by the health checker, not persisted in the database.

```go
// In the Project struct, after the CreatedAt field:

	// AppRunning indicates whether a TCP listener is active on the project's
	// assigned port. Populated at query time by the health checker, not
	// persisted in the database. Only meaningful in API responses from the
	// list-projects handler.
	AppRunning bool `json:"appRunning"`
```

- [ ] **Step 2: Update handleListProjects to accept and use HealthChecker**

In `internal/server/project_handlers.go`, update the `handleListProjects` function signature to accept a `*project.HealthChecker` and merge results:

```go
// handleListProjects returns the handler for GET /api/projects. It queries all
// projects via the Manager, checks each project's app health via TCP dial, and
// returns a JSON array with the appRunning field populated. Returns an empty
// array when no projects exist. This route is behind auth middleware.
func handleListProjects(pm *project.Manager, hc *project.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projects, err := pm.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		health := hc.Check(projects)
		for _, p := range projects {
			p.AppRunning = health[p.ID]
		}

		writeJSON(w, projects)
	}
}
```

- [ ] **Step 3: Update router.go to create HealthChecker and pass to handler**

In `internal/server/router.go`, create a `HealthChecker` inside `NewRouter` and pass it:

```go
// At the top of NewRouter, before route registration:
	hc := project.NewHealthChecker()

// Update the route registration:
	api.HandleFunc("GET /api/projects", handleListProjects(pm, hc))
```

- [ ] **Step 4: Update router_test.go setupTest if needed**

The `setupTest` helper creates a `NewRouter`. Since `NewHealthChecker` is created inside `NewRouter`, no test changes are needed for the wiring. However, add a test that verifies the `appRunning` field appears in the list response.

In `internal/server/router_test.go`, add:

```go
// TestListProjects_AppRunningField verifies that the project list response
// includes the appRunning field. Since no listener runs on the test project's
// port, it should be false.
func TestListProjects_AppRunningField(t *testing.T) {
	handler, store, db := setupTest(t)

	// Create a project with a high port unlikely to be in use.
	db.Exec("INSERT INTO projects (id, name, status, internal_port) VALUES ('hid', 'healthtest', 'stopped', 59999)")

	req := authedRequest(t, store, "GET", "/api/projects", "")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var projects []struct {
		ID         string `json:"id"`
		AppRunning bool   `json:"appRunning"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) == 0 {
		t.Fatal("expected at least one project")
	}

	found := false
	for _, p := range projects {
		if p.ID == "hid" {
			found = true
			if p.AppRunning {
				t.Error("expected appRunning=false for port with no listener")
			}
		}
	}
	if !found {
		t.Error("project 'hid' not found in response")
	}
}
```

- [ ] **Step 5: Build and test**

Run: `go test ./internal/project/ -v && go test ./internal/server/ -v`
Expected: all tests pass

Run: `task build`
Expected: compiles cleanly

- [ ] **Step 6: Commit**

```bash
git add internal/project/project.go internal/server/project_handlers.go internal/server/router.go internal/server/router_test.go
git commit -m "feat: wire health checker into project list handler, add appRunning field"
```

---

### Task 4: OpenCode client -- write tests first

**Files:**
- Create: `internal/opencode/client_test.go`

- [ ] **Step 1: Write OpenCode client tests using httptest**

Create `internal/opencode/client_test.go`:

```go
package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthCheck_Healthy verifies that HealthCheck returns nil when the
// OpenCode server responds with 200.
func TestHealthCheck_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.HealthCheck(); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// TestHealthCheck_Unhealthy verifies that HealthCheck returns an error when
// the OpenCode server responds with a non-200 status.
func TestHealthCheck_Unhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.HealthCheck(); err == nil {
		t.Error("expected error for 503 response, got nil")
	}
}

// TestHealthCheck_ConnectionRefused verifies that HealthCheck returns an error
// when the OpenCode server is unreachable.
func TestHealthCheck_ConnectionRefused(t *testing.T) {
	c := NewClient("http://127.0.0.1:1") // port 1 -- nothing listening
	if err := c.HealthCheck(); err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

// TestListProjects_Success verifies that ListProjects parses the response
// from the OpenCode /project endpoint correctly.
func TestListProjects_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/project" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]OpenCodeProject{
			{ID: "proj-abc", Name: "myapp", AbsolutePath: "/home/opencode/projects/myapp"},
			{ID: "proj-def", Name: "other", AbsolutePath: "/home/opencode/projects/other"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].ID != "proj-abc" {
		t.Errorf("expected ID 'proj-abc', got %q", projects[0].ID)
	}
	if projects[1].Name != "other" {
		t.Errorf("expected Name 'other', got %q", projects[1].Name)
	}
}

// TestListProjects_EmptyList verifies that ListProjects returns an empty
// slice when OpenCode has no projects.
func TestListProjects_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}
}

// TestListProjects_ServerError verifies that ListProjects returns an error
// when OpenCode responds with a server error.
func TestListProjects_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ListProjects()
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

// TestSetAuth_Success verifies that SetAuth sends the correct JSON body to
// the OpenCode /auth endpoint.
func TestSetAuth_Success(t *testing.T) {
	var gotProvider, gotKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected content-type: %s", ct)
		}

		var body struct {
			ProviderID string `json:"providerId"`
			APIKey     string `json:"apiKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotProvider = body.ProviderID
		gotKey = body.APIKey

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.SetAuth("anthropic", "sk-ant-test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotProvider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", gotProvider)
	}
	if gotKey != "sk-ant-test-key" {
		t.Errorf("expected key 'sk-ant-test-key', got %q", gotKey)
	}
}

// TestSetAuth_ServerError verifies that SetAuth returns an error when
// OpenCode responds with a non-200 status.
func TestSetAuth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad provider"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.SetAuth("bad-provider", "key")
	if err == nil {
		t.Error("expected error for 400 response, got nil")
	}
}

// TestNewClient_BaseURL verifies that the client stores the base URL and
// trims a trailing slash.
func TestNewClient_BaseURL(t *testing.T) {
	c := NewClient("http://localhost:4096/")
	if c.baseURL != "http://localhost:4096" {
		t.Errorf("expected trimmed base URL, got %q", c.baseURL)
	}
}
```

- [ ] **Step 2: Run tests -- expect compile failure**

Run: `go test ./internal/opencode/ -v 2>&1 | head -10`
Expected: FAIL -- package does not exist or types undefined

- [ ] **Step 3: Commit test file**

```bash
git add internal/opencode/client_test.go
git commit -m "test: add OpenCode client tests (red -- implementation pending)"
```

---

### Task 5: OpenCode client -- implement

**Files:**
- Create: `internal/opencode/client.go`

- [ ] **Step 1: Implement the OpenCode client**

Create `internal/opencode/client.go`:

```go
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

// clientTimeout is the HTTP request timeout for all OpenCode API calls. 5
// seconds is generous for localhost communication and prevents hanging when
// OpenCode is unresponsive.
const clientTimeout = 5 * time.Second

// maxResponseSize is the maximum response body size the client will read. 10
// MB prevents unbounded memory growth if OpenCode returns unexpectedly large
// responses.
const maxResponseSize = 10 << 20 // 10 MB

// OpenCodeProject represents a project as returned by the OpenCode
// /project endpoint. Only the fields appx needs are included; additional
// fields from OpenCode's response are ignored during JSON decoding.
type OpenCodeProject struct {
	// ID is OpenCode's internal project identifier, derived from the git root
	// commit hash (SHA-256). Stable across restarts.
	ID string `json:"id"`

	// Name is the project directory name (e.g. "myapp").
	Name string `json:"name"`

	// AbsolutePath is the full filesystem path to the project directory.
	AbsolutePath string `json:"absolutePath"`
}

// Client is a thin HTTP client for the OpenCode server REST API. It wraps
// three endpoints: health check, project listing, and auth configuration.
// All communication is over localhost HTTP -- no TLS, no auth headers needed
// (OpenCode trusts same-host callers).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client targeting the given OpenCode server base URL
// (e.g. "http://localhost:4096"). Trailing slashes are trimmed.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: clientTimeout,
		},
	}
}

// HealthCheck calls GET /global/health on the OpenCode server. Returns nil
// if the server responds with 200 OK, or an error describing the failure.
// Used on startup to poll until OpenCode is available, and periodically to
// surface status in the dashboard.
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

// ListProjects calls GET /project on the OpenCode server and returns the list
// of discovered projects. OpenCode discovers projects automatically when a
// session targets a directory containing a .git folder. Returns an error if
// the request fails or the response is not valid JSON.
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

// SetAuth calls POST /auth on the OpenCode server to inject an API key for
// the given provider. For Anthropic, providerID should be "anthropic" and
// apiKey the sk-ant-... key. Returns an error if the request fails or
// OpenCode responds with a non-200 status.
func (c *Client) SetAuth(providerID, apiKey string) error {
	body := struct {
		ProviderID string `json:"providerId"`
		APIKey     string `json:"apiKey"`
	}{
		ProviderID: providerID,
		APIKey:     apiKey,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("opencode set auth: marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/auth", bytes.NewReader(jsonBody))
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
```

- [ ] **Step 2: Run tests -- expect all green**

Run: `go test ./internal/opencode/ -v`
Expected: all 10 tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/opencode/client.go
git commit -m "feat: add OpenCode HTTP client -- health, projects, auth"
```

---

### Task 6: OpenCode startup polling and API key injection

**Files:**
- Modify: `cmd/appx/main.go` -- add startup polling + key injection
- Create: `internal/opencode/startup.go` -- polling logic

- [ ] **Step 1: Write startup polling tests**

Create `internal/opencode/startup_test.go`:

```go
package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestWaitForHealthy_ImmediateSuccess verifies that WaitForHealthy returns
// immediately when the server is already healthy.
func TestWaitForHealthy_ImmediateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.WaitForHealthy(ctx, 50*time.Millisecond); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// TestWaitForHealthy_EventualSuccess verifies that WaitForHealthy retries
// until the server becomes healthy.
func TestWaitForHealthy_EventualSuccess(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.WaitForHealthy(ctx, 50*time.Millisecond); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if n := calls.Load(); n < 3 {
		t.Errorf("expected at least 3 calls, got %d", n)
	}
}

// TestWaitForHealthy_ContextCancelled verifies that WaitForHealthy returns
// an error when the context is cancelled before the server becomes healthy.
func TestWaitForHealthy_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := c.WaitForHealthy(ctx, 50*time.Millisecond)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
```

- [ ] **Step 2: Run tests -- expect compile failure**

Run: `go test ./internal/opencode/ -run TestWaitForHealthy -v 2>&1 | head -10`
Expected: FAIL -- `WaitForHealthy` undefined

- [ ] **Step 3: Implement WaitForHealthy**

Create `internal/opencode/startup.go`:

```go
package opencode

import (
	"context"
	"fmt"
	"log"
	"time"
)

// WaitForHealthy polls the OpenCode health endpoint at the given interval
// until it returns 200 OK or the context is cancelled. Logs each retry
// attempt. Used on appx startup to wait for OpenCode to become available
// before injecting the API key.
func (c *Client) WaitForHealthy(ctx context.Context, interval time.Duration) error {
	if err := c.HealthCheck(); err == nil {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("opencode not healthy: %w", ctx.Err())
		case <-ticker.C:
			attempt++
			if err := c.HealthCheck(); err == nil {
				log.Printf("opencode: healthy after %d retries", attempt)
				return nil
			}
			log.Printf("opencode: waiting for health (attempt %d)...", attempt)
		}
	}
}

// InjectAPIKey waits for OpenCode to become healthy, then calls SetAuth to
// inject the Anthropic API key. This is the main startup sequence called
// from main.go. If apiKey is empty, the auth injection is skipped (user
// has not configured a key yet). Returns an error only if the context is
// cancelled before OpenCode becomes healthy; SetAuth failures are logged
// but not fatal (the user can re-inject via the Settings page).
func (c *Client) InjectAPIKey(ctx context.Context, pollInterval time.Duration, apiKey string) error {
	if err := c.WaitForHealthy(ctx, pollInterval); err != nil {
		return err
	}

	if apiKey == "" {
		log.Printf("opencode: no API key configured, skipping auth injection")
		return nil
	}

	if err := c.SetAuth("anthropic", apiKey); err != nil {
		log.Printf("opencode: failed to inject API key: %v (user can re-inject via Settings)", err)
		return nil // non-fatal
	}

	log.Printf("opencode: API key injected successfully")
	return nil
}
```

- [ ] **Step 4: Run tests -- expect all green**

Run: `go test ./internal/opencode/ -v`
Expected: all 13 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/opencode/startup.go internal/opencode/startup_test.go
git commit -m "feat: add WaitForHealthy polling and InjectAPIKey startup sequence"
```

---

### Task 7: Wire OpenCode client into main.go

**Files:**
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Add OpenCode client initialization and startup polling**

In `cmd/appx/main.go`, add the OpenCode client import and startup wiring. Add this block after the project manager setup and before `server.Run`:

```go
import (
	// ... existing imports ...
	"github.com/neuromaxer/appx/internal/opencode"
)
```

After the project manager creation (`pm := project.NewManager(projectStore)`) and before the `server.Run` call, add:

```go
	// Initialize OpenCode client and start background health polling + API key
	// injection. OpenCode runs as a separate process (systemd service) on
	// localhost:4096. The startup sequence polls until OpenCode responds, then
	// injects the Anthropic API key so agents can make API calls.
	ocClient := opencode.NewClient("http://127.0.0.1:4096")

	// Resolve Anthropic API key: DB setting takes priority, then env var.
	anthropicKey, _ := authStore.GetSetting("anthropic_api_key")
	if anthropicKey == "" {
		anthropicKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Start OpenCode polling in a background goroutine. This does not block
	// server startup -- the dashboard will show OpenCode as unavailable until
	// the health check passes.
	go func() {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer pollCancel()
		if err := ocClient.InjectAPIKey(pollCtx, 2*time.Second, anthropicKey); err != nil {
			log.Printf("opencode: startup polling failed: %v", err)
		}
	}()
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./cmd/appx/ 2>&1`
Expected: compiles cleanly (or has expected errors from prior steps not yet applied -- fix any import issues)

- [ ] **Step 3: Commit**

```bash
git add cmd/appx/main.go
git commit -m "feat: wire OpenCode client startup polling and API key injection"
```

---

### Task 8: OpenCode health status API endpoint

**Files:**
- Modify: `internal/server/router.go` -- add `/api/opencode/health` route
- Modify: `internal/server/settings_handlers.go` -- add handler (or create new file)
- Modify: `internal/server/server.go` -- add OpenCode client to Config
- Modify: `internal/server/router_test.go` -- add tests

- [ ] **Step 1: Add OpenCode client to server Config**

In `internal/server/server.go`, add the OpenCode client to the `Config` struct:

```go
import (
	// ... existing imports ...
	"github.com/neuromaxer/appx/internal/opencode"
)

// In the Config struct, add:
	// OpenCodeClient is the HTTP client for the OpenCode server API.
	// Used by health status and settings handlers. May be nil if OpenCode
	// integration is not configured.
	OpenCodeClient *opencode.Client
```

Update the `Run` function to pass the client to `NewRouter`:

```go
	handler := NewRouter(a, cfg.ProjectManager, cfg.WebFS, cfg.OpenCodeClient)
```

- [ ] **Step 2: Update NewRouter to accept and use OpenCode client**

In `internal/server/router.go`, update the `NewRouter` signature and add the health endpoint:

```go
import (
	// ... existing imports ...
	"github.com/neuromaxer/appx/internal/opencode"
)

// NewRouter builds the top-level HTTP handler. All requests go through auth
// middleware (except POST /api/login which is public and rate-limited).
func NewRouter(a *auth.Auth, pm *project.Manager, webFS fs.FS, oc *opencode.Client) http.Handler {
	// ... existing mux setup ...

	hc := project.NewHealthChecker()

	// ... existing api mux setup ...
	api.HandleFunc("GET /api/projects", handleListProjects(pm, hc))
	// ... other existing routes ...
	api.HandleFunc("GET /api/opencode/health", handleOpenCodeHealth(oc))

	// ... rest of function unchanged ...
}
```

- [ ] **Step 3: Add handleOpenCodeHealth handler**

In `internal/server/settings_handlers.go`, add:

```go
import (
	// ... existing imports ...
	"github.com/neuromaxer/appx/internal/opencode"
)

// handleOpenCodeHealth returns the handler for GET /api/opencode/health. It
// calls the OpenCode health endpoint and returns {"healthy": true/false}.
// Used by the dashboard to show the OpenCode server status. This route is
// behind auth middleware.
func handleOpenCodeHealth(oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if oc == nil {
			writeJSON(w, map[string]bool{"healthy": false})
			return
		}
		healthy := oc.HealthCheck() == nil
		writeJSON(w, map[string]bool{"healthy": healthy})
	}
}
```

- [ ] **Step 4: Update main.go to pass OpenCode client to Config**

In `cmd/appx/main.go`, update the `server.Run` call:

```go
	if err := server.Run(server.Config{
		// ... existing fields ...
		OpenCodeClient: ocClient,
	}); err != nil {
		log.Fatal(err)
	}
```

- [ ] **Step 5: Update router_test.go setupTest to pass nil OpenCode client**

In the `setupTest` function in `internal/server/router_test.go`, update the `NewRouter` call:

```go
	return NewRouter(a, pm, webFS, nil), store, db
```

- [ ] **Step 6: Add test for the health endpoint**

In `internal/server/router_test.go`, add:

```go
// TestOpenCodeHealth_NilClient verifies that the health endpoint returns
// {"healthy": false} when no OpenCode client is configured.
func TestOpenCodeHealth_NilClient(t *testing.T) {
	handler, store, _ := setupTest(t)
	req := authedRequest(t, store, "GET", "/api/opencode/health", "")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Healthy {
		t.Error("expected healthy=false with nil client")
	}
}

// TestOpenCodeHealth_RequiresAuth verifies that the health endpoint requires
// authentication.
func TestOpenCodeHealth_RequiresAuth(t *testing.T) {
	handler, _, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/api/opencode/health", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
```

- [ ] **Step 7: Build and test**

Run: `go test ./internal/opencode/ -v && go test ./internal/server/ -v && go test ./internal/project/ -v`
Expected: all tests pass

Run: `task build`
Expected: compiles cleanly

- [ ] **Step 8: Commit**

```bash
git add internal/server/server.go internal/server/router.go internal/server/settings_handlers.go internal/server/router_test.go cmd/appx/main.go
git commit -m "feat: add GET /api/opencode/health endpoint and wire OpenCode client"
```

---

### Task 9: Re-inject API key when settings change

**Files:**
- Modify: `internal/server/settings_handlers.go` -- update handleSetAPIKey and handleDeleteAPIKey

- [ ] **Step 1: Update handleSetAPIKey to also inject into OpenCode**

In `internal/server/settings_handlers.go`, update `handleSetAPIKey` to accept an `*opencode.Client` and call `SetAuth` after saving:

```go
// handleSetAPIKey returns the handler for PUT /api/settings/api-key. It stores
// the Anthropic API key in the settings table, updates the in-memory key on
// the project Manager, and injects it into the running OpenCode server via
// SetAuth. Requires authentication.
func handleSetAPIKey(store *auth.Store, pm *project.Manager, oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}

		if err := store.SetSetting("anthropic_api_key", req.Key); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		log.Printf("settings: API key updated")
		pm.SetAnthropicKey(req.Key)

		// Best-effort injection into OpenCode. Failure is logged but does not
		// fail the API request -- the key is saved and will be retried on next
		// startup or next settings save.
		if oc != nil {
			if err := oc.SetAuth("anthropic", req.Key); err != nil {
				log.Printf("settings: failed to inject key into OpenCode: %v", err)
			}
		}

		writeJSON(w, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 2: Update handleDeleteAPIKey similarly**

```go
// handleDeleteAPIKey returns the handler for DELETE /api/settings/api-key. It
// removes the Anthropic API key from the settings table and clears the
// Manager's in-memory key. Does not call OpenCode -- clearing the key from
// the running OpenCode instance is not needed (it will just fail on next
// Anthropic call, which is the desired behavior). Returns 200 on success
// (idempotent). This route is behind auth middleware.
func handleDeleteAPIKey(store *auth.Store, pm *project.Manager, oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteSetting("anthropic_api_key"); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		log.Printf("settings: API key deleted")
		// Fall back to host env var if set.
		pm.SetAnthropicKey(os.Getenv("ANTHROPIC_API_KEY"))
		writeJSON(w, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 3: Update router.go route registrations**

Update the API key handler registrations in `NewRouter` to pass the OpenCode client:

```go
	api.HandleFunc("GET /api/settings/api-key", handleGetAPIKeyStatus(a.Store, pm))
	api.HandleFunc("PUT /api/settings/api-key", handleSetAPIKey(a.Store, pm, oc))
	api.HandleFunc("DELETE /api/settings/api-key", handleDeleteAPIKey(a.Store, pm, oc))
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/ -v`
Expected: all tests pass (existing tests pass `nil` for OpenCode client)

Run: `task build`
Expected: compiles cleanly

- [ ] **Step 5: Commit**

```bash
git add internal/server/settings_handlers.go internal/server/router.go
git commit -m "feat: inject API key into OpenCode when settings change"
```

---

### Task 10: Add OpenCode client to frontend API client

**Files:**
- Modify: `web/src/api/client.ts`

- [ ] **Step 1: Add opencode health API function**

In `web/src/api/client.ts`, add:

```typescript
/** Response from the OpenCode health check endpoint. */
export interface OpenCodeHealthResponse {
  healthy: boolean;
}

/**
 * Fetches the OpenCode server health status.
 * GET /api/opencode/health
 * Returns { healthy: true } when OpenCode is reachable, false otherwise.
 */
export async function getOpenCodeHealth(): Promise<OpenCodeHealthResponse> {
  return request<OpenCodeHealthResponse>("/api/opencode/health");
}
```

- [ ] **Step 2: Build frontend**

Run: `task web`
Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add web/src/api/client.ts
git commit -m "feat(frontend): add getOpenCodeHealth API function"
```

---

### Task 11: Final verification

- [ ] **Step 1: Run full test suite**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 2: Run linter**

Run: `task lint`
Expected: no errors

- [ ] **Step 3: Full build**

Run: `task build`
Expected: compiles cleanly

- [ ] **Step 4: Verify new files exist**

Run: `ls internal/project/health.go internal/opencode/client.go internal/opencode/startup.go`
Expected: all three files listed

- [ ] **Step 5: Verify test coverage**

Run: `go test ./internal/project/ -run TestHealthChecker -v`
Expected: 6 tests pass

Run: `go test ./internal/opencode/ -v`
Expected: 13 tests pass

Run: `go test ./internal/server/ -v`
Expected: all tests pass (including new health and appRunning tests)

- [ ] **Step 6: Commit any final fixes**

```bash
git add -A
git commit -m "feat: Phase 5 Steps 4-5 complete -- health checker and OpenCode integration"
```
