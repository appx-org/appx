# Phase 4: Reverse Proxy + Agent UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a reverse proxy that routes `/apps/:name/*` to container user apps, `/api/agent/:name/*` to OpenCode's API, and `/agent/:name/` to a cached+modified copy of OpenCode's web UI — all behind the existing auth middleware.

**Architecture:** OpenCode runs headless (`opencode serve`) inside each container via a Dockerfile CMD change. The proxy fetches OpenCode's HTML once per container start, rewrites absolute asset paths, injects a localStorage script that tells the SPA which API endpoint to use, caches the result, and serves it. API calls from the SPA go through `/api/agent/:name/*` which the proxy strips-and-forwards to the container. Static assets (JS/CSS) are cached on first fetch and served directly by appx on subsequent requests.

**Tech Stack:** Go `net/http/httputil.ReverseProxy`, `net.Dial` for raw WebSocket tunnelling, `sync.RWMutex` for asset cache, `strings.ReplaceAll` for HTML rewriting, SQLite migration for `container_secret`, React + xterm.js for frontend tabs.

**Spec:** `docs/superpowers/specs/2026-04-06-phase4-design.md`

---

## File Map

**New files:**
- `internal/db/migrations/000003_proxy.up.sql` — add `container_secret` column
- `internal/db/migrations/000003_proxy.down.sql` — reverse the migration
- `internal/proxy/assets.go` — `AssetCache`: thread-safe in-memory cache for HTML + static assets
- `internal/proxy/ws.go` — `proxyWebSocket`: raw TCP tunnel for WebSocket upgrades
- `internal/proxy/proxy.go` — `ProxyHandler`, `AgentAPIHandler`, `AgentUIHandler`
- `internal/proxy/proxy_test.go` — handler tests using httptest servers

**Modified files:**
- `internal/project/project.go` — add `ContainerSecret string` field (json:"-")
- `internal/project/store.go` — update `projectColumns`, `scanInto`, `Create`; add `GetByName`, `UpdatePort`, `SetContainerSecret`
- `internal/project/container.go` — add `startHook` callback field, call it after container start; inject `OPENCODE_SERVER_PASSWORD` env var
- `internal/project/Dockerfile.project` — change CMD to start `opencode serve` in background
- `internal/server/middleware.go` — skip `X-Frame-Options: DENY` for `/agent/` paths
- `internal/server/router.go` — register `/agent/`, `/api/agent/`, `/apps/` routes; add `PATCH /api/projects/{id}`
- `internal/server/project_handlers.go` — add `handleUpdateProject`
- `internal/server/server.go` — add `AssetCache` to `Config`
- `cmd/appx/main.go` — create `AssetCache`, wire `SetStartHook`, pass to `Config`
- `web/src/api/client.ts` — add `updateProject`
- `web/src/components/ProjectCard.tsx` — Open button → `/agent/:name/`
- `web/src/pages/Project.tsx` — Agent/Terminal tabs + App link button
- `docs/architecture/arch_phase_4.md` — new architecture reference doc

---

## Task 1: Migration — add container_secret column

**Files:**
- Create: `internal/db/migrations/000003_proxy.up.sql`
- Create: `internal/db/migrations/000003_proxy.down.sql`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/db/db_test.go` (inside `TestMigrations` or as a new test):

```go
func TestMigration3ContainerSecret(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Verify container_secret column exists by inserting and reading back.
	_, err = db.Exec(`INSERT INTO projects (id, name, status, internal_port, container_secret)
		VALUES ('test-id', 'test-proj', 'stopped', 3000, 'mysecret')`)
	if err != nil {
		t.Fatalf("insert with container_secret: %v", err)
	}

	var secret string
	err = db.QueryRow(`SELECT container_secret FROM projects WHERE id = 'test-id'`).Scan(&secret)
	if err != nil {
		t.Fatalf("select container_secret: %v", err)
	}
	if secret != "mysecret" {
		t.Errorf("got %q, want %q", secret, "mysecret")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
task test 2>&1 | grep -A3 "TestMigration3"
```
Expected: FAIL — `no such column: container_secret`

- [ ] **Step 3: Create the migration files**

`internal/db/migrations/000003_proxy.up.sql`:
```sql
ALTER TABLE projects ADD COLUMN container_secret TEXT NOT NULL DEFAULT '';
```

`internal/db/migrations/000003_proxy.down.sql`:
```sql
-- SQLite does not support DROP COLUMN in older versions; recreate is not worth the complexity.
-- This migration is intentionally not reversible in place.
SELECT 1;
```

- [ ] **Step 4: Run test to verify it passes**

```bash
task test 2>&1 | grep -A3 "TestMigration3"
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/000003_proxy.up.sql internal/db/migrations/000003_proxy.down.sql internal/db/db_test.go
git commit -m "feat(db): add container_secret column (migration 3)"
```

---

## Task 2: Project model + store updates

**Files:**
- Modify: `internal/project/project.go`
- Modify: `internal/project/store.go`
- Modify: `internal/project/store_test.go` (or create if it doesn't exist alongside existing tests)

- [ ] **Step 1: Write the failing tests**

Add to the existing project store test file:

```go
func TestStoreGetByName(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	_, err := s.Create("my-app", 3000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	p, err := s.GetByName("my-app")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if p.Name != "my-app" {
		t.Errorf("got name %q, want %q", p.Name, "my-app")
	}

	_, err = s.GetByName("nonexistent")
	if err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestStoreUpdatePort(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	p, _ := s.Create("port-app", 3000)

	err := s.UpdatePort(p.ID, 8080)
	if err != nil {
		t.Fatalf("UpdatePort: %v", err)
	}

	updated, _ := s.Get(p.ID)
	if updated.Port != 8080 {
		t.Errorf("got port %d, want 8080", updated.Port)
	}

	err = s.UpdatePort(p.ID, 0)
	if err != ErrInvalidPort {
		t.Errorf("want ErrInvalidPort for port 0, got %v", err)
	}

	err = s.UpdatePort(p.ID, 70000)
	if err != ErrInvalidPort {
		t.Errorf("want ErrInvalidPort for port 70000, got %v", err)
	}
}

func TestStoreCreateSetsContainerSecret(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	p, err := s.Create("secret-app", 3000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(p.ContainerSecret) != 64 {
		t.Errorf("expected 64-char hex secret, got len=%d: %q", len(p.ContainerSecret), p.ContainerSecret)
	}

	// Get round-trips the secret.
	got, _ := s.Get(p.ID)
	if got.ContainerSecret != p.ContainerSecret {
		t.Errorf("secret not persisted: got %q, want %q", got.ContainerSecret, p.ContainerSecret)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test 2>&1 | grep -E "FAIL|undefined"
```
Expected: compile errors for missing methods, or FAIL for existing tests.

- [ ] **Step 3: Add ContainerSecret field to Project struct**

In `internal/project/project.go`, add to the `Project` struct after `CreatedAt`:

```go
// ContainerSecret is a random 32-byte hex token set at project creation and
// rotated on reset. It is injected into the container as OPENCODE_SERVER_PASSWORD
// and forwarded by the proxy on all requests to the opencode serve endpoint.
// Not exposed in JSON responses.
ContainerSecret string `json:"-"`
```

- [ ] **Step 4: Update projectColumns, scanInto, and Create in store.go**

In `internal/project/store.go`:

Replace the `projectColumns` constant:
```go
const projectColumns = `id, name, status, internal_port, container_id, network_id, image_name, last_error, resources, created_at, container_secret`
```

Replace `scanInto`:
```go
// scanInto reads a project row into a Project struct. Nullable string columns
// are handled with sql.NullString. The resources column is parsed from JSON.
// The container_secret column is always present after migration 3.
func scanInto(sc scanner) (*Project, error) {
	var p Project
	var containerID, networkID, imageName, lastError, resources, containerSecret sql.NullString
	err := sc.Scan(
		&p.ID, &p.Name, &p.Status, &p.Port,
		&containerID, &networkID, &imageName, &lastError,
		&resources, &p.CreatedAt, &containerSecret,
	)
	if err != nil {
		return nil, err
	}
	p.ContainerID = containerID.String
	p.NetworkID = networkID.String
	p.ImageName = imageName.String
	p.LastError = lastError.String
	p.ContainerSecret = containerSecret.String
	if resources.Valid && resources.String != "" {
		var r Resources
		if err := json.Unmarshal([]byte(resources.String), &r); err == nil {
			p.Resources = &r
		}
	}
	return &p, nil
}
```

Add `generateSecret` helper at the bottom of store.go (before `isUniqueViolation`):
```go
// generateSecret returns a cryptographically random 32-byte value encoded as
// a 64-character lowercase hex string. Used for per-container OPENCODE_SERVER_PASSWORD.
func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
```

Add `"crypto/rand"` and `"encoding/hex"` to the import block.

Replace the `Create` method's INSERT statement:
```go
// Create inserts a new project with the given name and port. It validates the
// name and port, generates a UUID, generates a random container_secret for
// OPENCODE_SERVER_PASSWORD, and sets the initial status to "stopped".
// Returns ErrInvalidName, ErrInvalidPort, or ErrDuplicateName on validation failure.
func (s *Store) Create(name string, port int) (*Project, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, ErrInvalidPort
	}

	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	_, err = s.db.Exec(
		"INSERT INTO projects (id, name, status, internal_port, container_secret) VALUES (?, ?, ?, ?, ?)",
		id, name, StatusStopped, port, secret,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}

	return s.Get(id)
}
```

Add `GetByName`, `UpdatePort`, and `SetContainerSecret` methods after `Get`:

```go
// GetByName returns the project with the given slug name. Returns ErrNotFound
// if no project has that name. Used by the reverse proxy to look up projects
// from URL path segments.
func (s *Store) GetByName(name string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT `+projectColumns+` FROM projects WHERE name = ?`, name,
	)
	p, err := scanInto(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project by name: %w", err)
	}
	return p, nil
}

// UpdatePort changes the user-app proxy port for a project. The new port must
// be in 1–65535. Returns ErrInvalidPort or ErrNotFound on validation failure.
func (s *Store) UpdatePort(id string, port int) error {
	if port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	res, err := s.db.Exec("UPDATE projects SET internal_port = ? WHERE id = ?", port, id)
	if err != nil {
		return fmt.Errorf("update port: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update port rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetContainerSecret rotates the OPENCODE_SERVER_PASSWORD secret for a project.
// Called by Manager.Reset after destroying and recreating a container.
func (s *Store) SetContainerSecret(id, secret string) error {
	_, err := s.db.Exec("UPDATE projects SET container_secret = ? WHERE id = ?", secret, id)
	if err != nil {
		return fmt.Errorf("set container secret: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Add Manager delegates in container.go**

At the end of the `Manager` method group in `container.go`, add:

```go
// GetByName returns the project with the given slug name. Used by the proxy
// to look up projects from URL path segments.
func (m *Manager) GetByName(name string) (*Project, error) {
	return m.store.GetByName(name)
}

// UpdatePort changes the user-app proxy port for a project. Returns ErrInvalidPort
// or ErrNotFound on failure.
func (m *Manager) UpdatePort(id string, port int) error {
	return m.store.UpdatePort(id, port)
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/project/project.go internal/project/store.go internal/project/container.go internal/db/db_test.go
git commit -m "feat(project): add ContainerSecret field, GetByName, UpdatePort store methods"
```

---

## Task 3: Container startup changes

**Files:**
- Modify: `internal/project/container.go`
- Modify: `internal/project/Dockerfile.project`

- [ ] **Step 1: Change the Dockerfile CMD**

In `internal/project/Dockerfile.project`, replace the last line:

```dockerfile
# Before:
CMD ["sleep", "infinity"]

# After:
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 0.0.0.0 & sleep infinity"]
```

The `&` sends `opencode serve` to the background. `sleep infinity` stays as PID 1 so the container remains alive if opencode crashes. OpenCode inherits `ANTHROPIC_API_KEY` and `OPENCODE_SERVER_PASSWORD` from the container's env, which are injected at create time.

- [ ] **Step 2: Add OPENCODE_SERVER_PASSWORD to container env and add startHook**

In `internal/project/container.go`, add a `startHook` field to `Manager`:

```go
type Manager struct {
	store       *Store
	docker      dockerer
	projectsDir string

	mu           sync.RWMutex
	anthropicKey string
	tm           terminalCloser
	startHook    func(projectName string) // called after a container starts successfully
}
```

Add a setter (mirrors the `SetTerminalManager` pattern):

```go
// SetStartHook registers a function to call whenever a container transitions
// to running. Used by the proxy to clear the cached HTML and assets for the
// project, so the next request fetches fresh content from the new container.
func (m *Manager) SetStartHook(f func(projectName string)) {
	m.startHook = f
}
```

In `doFullCreate`, find the section that builds `env` (around the comment "// 4. Create container.") and add the password injection:

```go
var env []string
if key := m.getAnthropicKey(); key != "" {
	env = append(env, "ANTHROPIC_API_KEY="+key)
}
// Inject the per-container opencode server password. The proxy forwards this
// as Authorization: Basic on all requests to container:4096.
if proj.ContainerSecret != "" {
	env = append(env, "OPENCODE_SERVER_PASSWORD="+proj.ContainerSecret)
}
```

At the end of `doFullCreate`, after the successful `SetRunning` call, call the hook:

```go
if err := m.store.SetRunning(proj.ID, ctrRes.ID, createdNetworkID, baseImageTag); err != nil {
	log.Printf("project %s: failed to update DB after start: %v", proj.Name, err)
}
// Notify proxy to clear cached assets — new container may run a different
// opencode version after a base image rebuild.
if m.startHook != nil {
	m.startHook(proj.Name)
}
```

Do the same in `tryReuseContainer` after the `SetRunning` call:

```go
if err := m.store.SetRunning(proj.ID, proj.ContainerID, proj.NetworkID, proj.ImageName); err != nil {
	log.Printf("project %s: failed to update DB after restart: %v", proj.Name, err)
}
if m.startHook != nil {
	m.startHook(proj.Name)
}
```

Also update `Reset` to rotate the container secret. Find the `Reset` method and add after the old container is removed and before the new one is created (or at the start of the goroutine after the project is fetched):

```go
// Rotate the container secret on reset so the old secret cannot be replayed.
newSecret, err := generateSecret()
if err == nil {
	m.store.SetContainerSecret(proj.ID, newSecret)
}
```

- [ ] **Step 3: Rebuild the base image to verify the Dockerfile change**

```bash
docker rmi appx-base:latest 2>/dev/null || true
task build
./appx -port 8443 &
sleep 2
# In another terminal or via curl: create and start a project, then verify
# opencode is listening on port 4096 in the container
docker exec appx-<yourprojectname> curl -s http://localhost:4096/global/health
# Expected: {"healthy":true,"version":"..."}
kill %1
```

- [ ] **Step 4: Commit**

```bash
git add internal/project/container.go internal/project/Dockerfile.project
git commit -m "feat(container): start opencode serve via CMD, inject OPENCODE_SERVER_PASSWORD, add startHook"
```

---

## Task 4: Asset cache

**Files:**
- Create: `internal/proxy/assets.go`
- Create: `internal/proxy/assets_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/assets_test.go`:

```go
package proxy

import (
	"bytes"
	"testing"
)

func TestAssetCacheHTMLRoundTrip(t *testing.T) {
	c := NewAssetCache()

	c.SetHTML("my-project", []byte("<html>hello</html>"))

	got, ok := c.GetHTML("my-project")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !bytes.Equal(got, []byte("<html>hello</html>")) {
		t.Errorf("got %q", got)
	}

	_, ok = c.GetHTML("other-project")
	if ok {
		t.Error("expected cache miss for unknown project")
	}
}

func TestAssetCacheAssetRoundTrip(t *testing.T) {
	c := NewAssetCache()
	c.SetAsset("/assets/index.js", []byte("console.log('hi')"))

	got, ok := c.GetAsset("/assets/index.js")
	if !ok {
		t.Fatal("expected asset cache hit")
	}
	if string(got) != "console.log('hi')" {
		t.Errorf("got %q", got)
	}
}

func TestAssetCacheClearRemovesProjectAndAssets(t *testing.T) {
	c := NewAssetCache()
	c.SetHTML("proj-a", []byte("html-a"))
	c.SetHTML("proj-b", []byte("html-b"))
	c.SetAsset("/assets/index.js", []byte("js"))

	c.Clear("proj-a")

	if _, ok := c.GetHTML("proj-a"); ok {
		t.Error("proj-a HTML should be cleared")
	}
	if _, ok := c.GetHTML("proj-b"); ok {
		t.Error("proj-b HTML should also be cleared (shared asset cache reset)")
	}
	if _, ok := c.GetAsset("/assets/index.js"); ok {
		t.Error("assets should be cleared")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test 2>&1 | grep "proxy"
```
Expected: compile error — package does not exist yet.

- [ ] **Step 3: Implement AssetCache**

Create `internal/proxy/assets.go`:

```go
// Package proxy implements the reverse proxy handlers for appx. It routes
// /apps/:name/* to container user apps, /api/agent/:name/* to the opencode
// serve API, and /agent/:name/ to a cached+modified copy of the opencode
// web UI. All handlers require auth (enforced by the router).
package proxy

import "sync"

// AssetCache holds the OpenCode web UI assets in memory. It stores per-project
// modified HTML (which contains the injected server URL and rewritten asset
// paths) and the shared static assets (JS, CSS, favicons) that are identical
// across all containers running the same opencode version.
//
// The cache is cleared on every container start: Clear(projectName) removes
// the HTML for that project and all shared assets, so the next request fetches
// fresh content from the new container.
type AssetCache struct {
	mu     sync.RWMutex
	html   map[string][]byte // key: project name, value: modified HTML
	assets map[string][]byte // key: URL path (e.g. "/assets/index-xxx.js"), value: body
}

// NewAssetCache returns an empty AssetCache ready for use.
func NewAssetCache() *AssetCache {
	return &AssetCache{
		html:   make(map[string][]byte),
		assets: make(map[string][]byte),
	}
}

// GetHTML returns the cached modified HTML for a project, or (nil, false) on
// a cache miss.
func (c *AssetCache) GetHTML(name string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.html[name]
	return v, ok
}

// SetHTML stores the modified HTML for a project.
func (c *AssetCache) SetHTML(name string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.html[name] = data
}

// GetAsset returns a cached static asset by its URL path (e.g.
// "/assets/index-Ca44lNAO.js"), or (nil, false) on a cache miss.
func (c *AssetCache) GetAsset(path string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.assets[path]
	return v, ok
}

// SetAsset stores a static asset indexed by its URL path.
func (c *AssetCache) SetAsset(path string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.assets[path] = data
}

// Clear removes the cached HTML for a project and all shared static assets.
// Called by Manager.startHook when a container starts, so the next request
// fetches fresh content from the new container (handles opencode version
// changes after a base image rebuild).
func (c *AssetCache) Clear(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.html, name)
	for k := range c.assets {
		delete(c.assets, k)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
task test ./internal/proxy/...
```
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/assets.go internal/proxy/assets_test.go
git commit -m "feat(proxy): add AssetCache for opencode web UI assets"
```

---

## Task 5: WebSocket proxy helper

**Files:**
- Create: `internal/proxy/ws.go`
- Create: (tests are in `proxy_test.go` in Task 6 — WS is tested end-to-end there)

- [ ] **Step 1: Implement proxyWebSocket**

Create `internal/proxy/ws.go`:

```go
package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// proxyWebSocket tunnels a WebSocket upgrade request to a backend TCP address.
// It hijacks the client connection, dials the backend directly, forwards the
// HTTP upgrade handshake, then copies frames bidirectionally until either side
// closes. Used for /api/agent/:name/pty/* endpoints inside the opencode server.
//
// backendHost is "ip:port" (e.g. "172.17.0.2:4096").
// backendPath is the path+query to forward (e.g. "/pty/abc123/connect").
func proxyWebSocket(w http.ResponseWriter, r *http.Request, backendHost, backendPath string) {
	// Dial the backend.
	backendConn, err := net.DialTimeout("tcp", backendHost, 10*time.Second)
	if err != nil {
		http.Error(w, "backend unavailable", http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	// Build a forwarded request pointed at the backend.
	backendReq := r.Clone(r.Context())
	backendReq.URL = &url.URL{
		Scheme:   "http",
		Host:     backendHost,
		Path:     backendPath,
		RawQuery: r.URL.RawQuery,
	}
	backendReq.Host = backendHost
	backendReq.RequestURI = ""

	// Send the upgrade request to the backend.
	if err := backendReq.Write(backendConn); err != nil {
		http.Error(w, "backend write failed", http.StatusBadGateway)
		return
	}

	// Read the backend's 101 Switching Protocols response.
	br := bufio.NewReader(backendConn)
	backendResp, err := http.ReadResponse(br, backendReq)
	if err != nil {
		http.Error(w, "backend read failed", http.StatusBadGateway)
		return
	}
	if backendResp.StatusCode != http.StatusSwitchingProtocols {
		http.Error(w, "backend did not upgrade", http.StatusBadGateway)
		return
	}

	// Hijack the client connection.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()

	// Forward the 101 response to the client.
	if err := backendResp.Write(clientBuf); err != nil {
		return
	}
	if err := clientBuf.Flush(); err != nil {
		return
	}

	// Copy any bytes the backend buffered before we hijacked.
	if n := br.Buffered(); n > 0 {
		buf := make([]byte, n)
		br.Read(buf)
		backendConn.Write(buf)
	}

	// Bidirectional copy until either side closes.
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(backendConn, clientConn)
	}()
	io.Copy(clientConn, backendConn)
	<-done
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/proxy/ws.go
git commit -m "feat(proxy): add WebSocket tunnel helper"
```

---

## Task 6: User app proxy handler

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/proxy_test.go`:

```go
package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neuromaxer/appx/internal/project"
)

// fakeResolver is a test double for the containerResolver interface.
type fakeResolver struct {
	proj *project.Project
	ip   string
	ipErr error
}

func (f *fakeResolver) GetByName(name string) (*project.Project, error) {
	if f.proj != nil && f.proj.Name == name {
		return f.proj, nil
	}
	return nil, project.ErrNotFound
}

func (f *fakeResolver) ContainerIP(id string) (string, error) {
	if f.ipErr != nil {
		return "", f.ipErr
	}
	return f.ip, nil
}

func TestProxyHandlerForwardsRequest(t *testing.T) {
	// Backend: simulates the user's app inside the container.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Errorf("expected backend to receive /hello, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	}))
	defer backend.Close()

	// The backend listener is at 127.0.0.1:<port>.
	// Extract host (no scheme).
	backendHost := backend.Listener.Addr().String()

	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "my-app", Status: project.StatusRunning, Port: 9999},
		ip:   "127.0.0.1",
	}

	// Override the port lookup: proxy.go looks up project.Port; we need it
	// to match the actual httptest backend port. We patch by pointing resolver
	// to a project whose Port matches backend's port.
	port := backend.Listener.Addr().(*net.TCPAddr).Port
	resolver.proj.Port = port
	// backendHost is already ip:port — resolver.ip alone isn't enough; use
	// an httptest-aware helper by pointing the resolver's ip to backendHost's
	// split.
	resolverIP, _, _ := net.SplitHostPort(backendHost)
	resolver.ip = resolverIP

	h := ProxyHandler(resolver)

	req := httptest.NewRequest("GET", "/apps/my-app/hello", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rr.Code)
	}
	if rr.Body.String() != "pong" {
		t.Errorf("got body %q, want %q", rr.Body.String(), "pong")
	}
}

func TestProxyHandlerProjectNotFound(t *testing.T) {
	resolver := &fakeResolver{}
	h := ProxyHandler(resolver)

	req := httptest.NewRequest("GET", "/apps/no-such-project/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", rr.Code)
	}
}

func TestProxyHandlerProjectNotRunning(t *testing.T) {
	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "stopped-app", Status: project.StatusStopped, Port: 3000},
		ip:   "127.0.0.1",
	}
	h := ProxyHandler(resolver)

	req := httptest.NewRequest("GET", "/apps/stopped-app/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got status %d, want 503", rr.Code)
	}
}
```

Add the import `"net"` to the test file import block.

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test ./internal/proxy/... 2>&1 | head -20
```
Expected: compile error — `ProxyHandler` not defined.

- [ ] **Step 3: Implement ProxyHandler**

Create `internal/proxy/proxy.go`:

```go
package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/project"
)

// containerResolver is the subset of project.Manager used by proxy handlers.
// Declared as an interface so handlers can be tested without a real Manager.
type containerResolver interface {
	GetByName(name string) (*project.Project, error)
	ContainerIP(id string) (string, error)
}

// parsePath splits a URL path on the first segment after a prefix.
// parsePath("/apps/my-project/some/path", "/apps/") returns ("my-project", "/some/path").
// parsePath("/apps/my-project/", "/apps/") returns ("my-project", "/").
func parsePath(path, prefix string) (name, rest string) {
	trimmed := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], trimmed[i:]
	}
	return trimmed, "/"
}

// backendURL builds http://ip:port/rest from the resolved container IP, port,
// and stripped request path.
func backendURL(ip string, port int, rest string) string {
	return fmt.Sprintf("http://%s:%d%s", ip, port, rest)
}

// ProxyHandler returns an http.Handler that routes /apps/:name/* requests to
// the project's configured user-app port in the container. The /apps/:name
// prefix is stripped before forwarding. Returns 404 if the project is not
// found, 503 if it is not running.
func ProxyHandler(pm containerResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest := parsePath(r.URL.Path, "/apps/")
		if name == "" {
			http.Error(w, "project name required", http.StatusBadRequest)
			return
		}

		proj, err := pm.GetByName(name)
		if err == project.ErrNotFound {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if proj.Status != project.StatusRunning {
			http.Error(w, "project not running", http.StatusServiceUnavailable)
			return
		}

		ip, err := pm.ContainerIP(proj.ID)
		if err != nil {
			http.Error(w, "container not reachable", http.StatusBadGateway)
			return
		}

		target, _ := url.Parse(backendURL(ip, proj.Port, rest))
		rp := newReverseProxy(target)
		// Rewrite the request path to the stripped path.
		r2 := r.Clone(r.Context())
		r2.URL = target
		r2.Host = target.Host
		rp.ServeHTTP(w, r2)
	})
}

// newReverseProxy returns an httputil.ReverseProxy configured for proxying to
// the given target. FlushInterval is set to -1 so SSE streams are flushed
// immediately rather than buffered.
func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		FlushInterval: -1, // flush immediately — required for SSE streams
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	return rp
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
task test ./internal/proxy/...
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): add ProxyHandler for /apps/:name/* user app routing"
```

---

## Task 7: Agent API proxy handler

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `proxy_test.go`:

```go
func TestAgentAPIHandlerForwardsSSE(t *testing.T) {
	// Backend: simulates opencode serve SSE endpoint.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/event" {
			t.Errorf("expected /event, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: hello\n\n"))
	}))
	defer backend.Close()

	port := backend.Listener.Addr().(*net.TCPAddr).Port
	resolverIP, _, _ := net.SplitHostPort(backend.Listener.Addr().String())

	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "my-agent", Status: project.StatusRunning, Port: 3000},
		ip:   resolverIP,
	}
	// AgentAPIHandler always uses port 4096; override the port via a custom
	// resolver that returns the httptest backend port.
	_ = port // port is used through resolver.ip + agentPort in handler

	// For this test, wrap backend at a path our handler will strip correctly.
	// We test path stripping: /api/agent/my-agent/event → /event.
	h := AgentAPIHandler(resolver, port) // pass port for testing; production uses 4096

	req := httptest.NewRequest("GET", "/api/agent/my-agent/event", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "data: hello") {
		t.Errorf("unexpected body: %q", rr.Body.String())
	}
}

func TestAgentAPIHandlerProjectNotFound(t *testing.T) {
	resolver := &fakeResolver{}
	h := AgentAPIHandler(resolver, 4096)

	req := httptest.NewRequest("GET", "/api/agent/missing/event", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
task test ./internal/proxy/... 2>&1 | head -10
```
Expected: compile error — `AgentAPIHandler` undefined.

- [ ] **Step 3: Implement AgentAPIHandler**

Add to `internal/proxy/proxy.go`:

```go
const agentPort = 4096 // opencode serve port inside every container

// AgentAPIHandler returns an http.Handler that proxies /api/agent/:name/*
// requests to the project's opencode serve instance. The /api/agent/:name
// prefix is stripped before forwarding. Handles both regular HTTP requests
// (including SSE event streams) and WebSocket upgrade requests.
//
// agentBackendPort is injected to allow tests to override the hardcoded 4096.
// Call with agentPort in production.
func AgentAPIHandler(pm containerResolver, agentBackendPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest := parsePath(r.URL.Path, "/api/agent/")
		if name == "" {
			http.Error(w, "project name required", http.StatusBadRequest)
			return
		}

		proj, err := pm.GetByName(name)
		if err == project.ErrNotFound {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if proj.Status != project.StatusRunning {
			http.Error(w, "project not running", http.StatusServiceUnavailable)
			return
		}

		ip, err := pm.ContainerIP(proj.ID)
		if err != nil {
			http.Error(w, "container not reachable", http.StatusBadGateway)
			return
		}

		backendHost := fmt.Sprintf("%s:%d", ip, agentBackendPort)

		// WebSocket upgrade requests need a raw TCP tunnel; httputil.ReverseProxy
		// does not handle the Upgrade header.
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			proxyWebSocket(w, r, backendHost, rest)
			return
		}

		target, _ := url.Parse(fmt.Sprintf("http://%s%s", backendHost, rest))
		rp := newReverseProxy(target)
		r2 := r.Clone(r.Context())
		r2.URL = target
		r2.Host = target.Host
		rp.ServeHTTP(w, r2)
	})
}
```

Also update `NewRouter` call signature note: in production `AgentAPIHandler(pm, agentPort)`.

- [ ] **Step 4: Run tests**

```bash
task test ./internal/proxy/...
```
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): add AgentAPIHandler for /api/agent/:name/* with SSE and WebSocket support"
```

---

## Task 8: Agent UI handler

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `proxy_test.go`:

```go
func TestAgentUIHandlerServesModifiedHTML(t *testing.T) {
	// Backend: serves minimal opencode-like HTML.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><script src="/assets/app.js"></script></head><body></body></html>`))
		case "/assets/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write([]byte(`console.log("app")`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	port := backend.Listener.Addr().(*net.TCPAddr).Port
	resolverIP, _, _ := net.SplitHostPort(backend.Listener.Addr().String())

	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "ui-proj", Status: project.StatusRunning},
		ip:   resolverIP,
	}
	cache := NewAssetCache()
	h := AgentUIHandler(resolver, cache, port)

	// First request: HTML should be fetched, modified, and cached.
	req := httptest.NewRequest("GET", "/agent/ui-proj/", nil)
	req.Host = "myappx.local"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	// Asset path must be rewritten.
	if strings.Contains(body, `src="/assets/`) {
		t.Error("root-absolute asset path not rewritten")
	}
	if !strings.Contains(body, `src="/agent/ui-proj/assets/app.js"`) {
		t.Errorf("expected rewritten asset path, got:\n%s", body)
	}

	// localStorage injection must be present.
	if !strings.Contains(body, "opencode.settings.dat:defaultServerUrl") {
		t.Error("localStorage injection not found in HTML")
	}
	if !strings.Contains(body, "/api/agent/ui-proj") {
		t.Error("agent API URL not injected")
	}

	// Second request: should be served from cache (backend can be stopped).
	backend.Close()
	req2 := httptest.NewRequest("GET", "/agent/ui-proj/", nil)
	req2.Host = "myappx.local"
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("cache miss after first fetch: got %d", rr2.Code)
	}
}

func TestAgentUIHandlerServesAssetFromCache(t *testing.T) {
	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "asset-proj", Status: project.StatusRunning},
		ip:   "127.0.0.1",
	}
	cache := NewAssetCache()
	cache.SetAsset("/assets/main.css", []byte("body{color:red}"))
	h := AgentUIHandler(resolver, cache, 4096)

	req := httptest.NewRequest("GET", "/agent/asset-proj/assets/main.css", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rr.Code)
	}
	if rr.Body.String() != "body{color:red}" {
		t.Errorf("unexpected body: %q", rr.Body.String())
	}
}

func TestRewriteHTML(t *testing.T) {
	input := `<html><head>
<link href="/favicon.ico">
<script src="/assets/index.js"></script>
<link href="/assets/index.css">
</head></html>`

	got := rewriteHTML(input, "my-project", "https://appx.host")
	if strings.Contains(got, `href="/favicon.ico"`) {
		t.Error("favicon href not rewritten")
	}
	if !strings.Contains(got, `href="/agent/my-project/favicon.ico"`) {
		t.Error("favicon href not rewritten correctly")
	}
	if strings.Contains(got, `src="/assets/index.js"`) {
		t.Error("script src not rewritten")
	}
	if !strings.Contains(got, "opencode.settings.dat:defaultServerUrl") {
		t.Error("localStorage injection missing")
	}
	if !strings.Contains(got, "https://appx.host/api/agent/my-project") {
		t.Error("server URL not injected")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test ./internal/proxy/... 2>&1 | head -10
```
Expected: compile error.

- [ ] **Step 3: Implement AgentUIHandler and rewriteHTML**

Add to `internal/proxy/proxy.go`:

```go
import (
	// add these to existing import block:
	"io"
	"mime"
	"net/http"
	"path/filepath"
)
```

Add functions:

```go
// rewriteHTML modifies opencode's index.html for use behind the appx proxy.
// It rewrites all root-absolute attribute paths (="/ ...) to be project-prefixed
// (="/agent/:name/...) so the browser requests assets from the correct appx
// route. It also injects a script tag that writes the project's API proxy URL
// into localStorage before the SPA initialises, so the SPA connects to the
// right opencode serve instance.
func rewriteHTML(html, projectName, hostURL string) string {
	// Rewrite all root-absolute paths in HTML attributes: src="/ href="/ content="/
	prefixed := `="/agent/` + projectName + `/`
	html = strings.ReplaceAll(html, `="/`, prefixed)

	// Inject the server URL before </head> so localStorage is set before the
	// SPA's module script executes.
	serverURL := hostURL + "/api/agent/" + projectName
	script := `<script>localStorage.setItem("opencode.settings.dat:defaultServerUrl","` + serverURL + `")</script>`
	html = strings.Replace(html, `</head>`, script+`</head>`, 1)

	return html
}

// requestHostURL returns "https://host" (or "http://host") derived from the
// incoming request, used to build the absolute server URL injected into the SPA.
func requestHostURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// AgentUIHandler returns an http.Handler that serves the OpenCode web UI for
// a project. For the index route (/agent/:name/ or /agent/:name), it fetches
// and caches the HTML from the container, rewrites asset paths, and injects the
// API server URL. For asset paths (/agent/:name/assets/*, /agent/:name/favicon*,
// etc.) it serves from the in-memory asset cache, fetching from the container
// on the first request.
//
// agentBackendPort is injected to allow tests to override 4096.
func AgentUIHandler(pm containerResolver, cache *AssetCache, agentBackendPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest := parsePath(r.URL.Path, "/agent/")
		if name == "" {
			http.Error(w, "project name required", http.StatusBadRequest)
			return
		}

		// Validate project exists (prevents serving stale cache for deleted projects).
		if _, err := pm.GetByName(name); err == project.ErrNotFound {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}

		// Index route: check cache FIRST — serves gracefully even when container
		// is stopped. Only hit ContainerIP on a cache miss.
		if rest == "/" || rest == "" {
			if cached, ok := cache.GetHTML(name); ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(cached)
				return
			}
			// Cache miss — need a running container to fetch HTML.
			proj, _ := pm.GetByName(name)
			ip, err := pm.ContainerIP(proj.ID)
			if err != nil {
				http.Error(w, "container not reachable — start the project first", http.StatusServiceUnavailable)
				return
			}
			serveAgentHTML(w, r, cache, name, fmt.Sprintf("http://%s:%d", ip, agentBackendPort))
			return
		}

		// Asset routes: check cache FIRST — same graceful degradation.
		if cached, ok := cache.GetAsset(rest); ok {
			ct := mime.TypeByExtension(filepath.Ext(rest))
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Write(cached)
			return
		}
		// Cache miss — need the container.
		proj, _ := pm.GetByName(name)
		ip, err := pm.ContainerIP(proj.ID)
		if err != nil {
			http.Error(w, "container not reachable — start the project first", http.StatusServiceUnavailable)
			return
		}
		serveAgentAsset(w, r, cache, rest, fmt.Sprintf("http://%s:%d", ip, agentBackendPort))
	})
}

// serveAgentHTML fetches index.html from the container, rewrites it, caches it,
// and writes it to the response. Only called on a cache miss — the cache check
// is in AgentUIHandler.
func serveAgentHTML(w http.ResponseWriter, r *http.Request, cache *AssetCache, name, backendBase string) {
	resp, err := http.Get(backendBase + "/")
	if err != nil {
		http.Error(w, "cannot reach opencode server", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}

	modified := rewriteHTML(string(raw), name, requestHostURL(r))
	cache.SetHTML(name, []byte(modified))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(modified))
}

// serveAgentAsset fetches a static asset from the container, caches it, and
// writes it to the response. Only called on a cache miss — the cache check is
// in AgentUIHandler. The assetPath is relative to the container root
// (e.g. "/assets/index-Ca44lNAO.js").
func serveAgentAsset(w http.ResponseWriter, r *http.Request, cache *AssetCache, assetPath, backendBase string) {
	resp, err := http.Get(backendBase + assetPath)
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}

	cache.SetAsset(assetPath, data)

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(assetPath))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}
```

- [ ] **Step 4: Run tests**

```bash
task test ./internal/proxy/...
```
Expected: all pass.

- [ ] **Step 5: Add HTML rewrite regression test**

Ensure `TestRewriteHTML` catches future opencode HTML structure changes. In CI, this test will fail if a version bump introduces new root-absolute paths that aren't caught. This is the early-warning system referenced in the spec.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): add AgentUIHandler with HTML rewriting and asset cache"
```

---

## Task 9: Security headers + router wiring

**Files:**
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/router.go`
- Modify: `internal/server/server.go`
- Modify: `cmd/appx/main.go`
- Modify: `internal/server/router_test.go`

- [ ] **Step 1: Write failing router tests**

Add to `internal/server/router_test.go`:

```go
func TestProxyRoutesRequireAuth(t *testing.T) {
	handler, _, _ := setupTest(t)

	for _, path := range []string{
		"/apps/my-project/",
		"/agent/my-project/",
		"/api/agent/my-project/event",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		// Without a session cookie, all proxy routes must redirect or return 401.
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusFound {
			t.Errorf("path %s: got %d, want 401 or 302", path, rr.Code)
		}
	}
}

func TestAgentRouteNoXFrameOptions(t *testing.T) {
	handler, _, _ := setupTest(t)

	// We only test the header behaviour — not the proxy destination.
	// Even a 404/503 from the proxy is fine; what matters is the absence of
	// X-Frame-Options on /agent/ routes.
	req := httptest.NewRequest("GET", "/agent/any-project/", nil)
	req = withSession(t, handler, req) // authenticate the request
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if v := rr.Header().Get("X-Frame-Options"); v == "DENY" {
		t.Error("X-Frame-Options: DENY must not be set on /agent/ routes (iframe would break)")
	}
}

func TestNonAgentRoutesHaveXFrameOptions(t *testing.T) {
	handler, _, _ := setupTest(t)

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("X-Frame-Options: DENY must be set on non-agent routes")
	}
}
```

Add helper `withSession` if it doesn't exist:

```go
// withSession authenticates req by first logging in and extracting the session cookie.
func withSession(t *testing.T, handler http.Handler, req *http.Request) *http.Request {
	t.Helper()
	// POST /api/login with the test password to get a session cookie.
	body := `{"password":"` + testPassword + `"}`
	loginReq := httptest.NewRequest("POST", "/api/login",
		strings.NewReader(body))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	for _, c := range loginRR.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}
```

(Check whether `testPassword` and `setupTest` are already in router_test.go and adapt accordingly.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test ./internal/server/... 2>&1 | grep -E "FAIL|undefined"
```

- [ ] **Step 3: Fix securityHeaders to skip X-Frame-Options for /agent/ routes**

In `internal/server/middleware.go`, replace `securityHeaders`:

```go
// securityHeaders wraps an HTTP handler to inject standard security headers on
// every response. X-Frame-Options: DENY is omitted for /agent/ paths because
// those routes serve the OpenCode web UI, which is embedded in an iframe on the
// appx Project page. All other routes receive the full set of headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(r.URL.Path, "/agent/") {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; connect-src 'self'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
```

`strings` is already imported.

- [ ] **Step 4: Add AssetCache to server.Config and NewRouter**

In `internal/server/server.go`, add to the `Config` struct:

```go
AssetCache *proxy.AssetCache // cache for opencode web UI assets; wired by main
```

Add import `"github.com/neuromaxer/appx/internal/proxy"` to server.go.

Update `NewRouter` signature in `router.go`:

```go
func NewRouter(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS, cache *proxy.AssetCache) http.Handler {
```

Add `"github.com/neuromaxer/appx/internal/proxy"` to router.go imports.

- [ ] **Step 5: Register proxy routes in NewRouter**

In `NewRouter`, add after the WebSocket route registration (before the SPA handler):

```go
// Proxy routes — behind auth, outside limitBody (streaming and large payloads).
// Do NOT add limitBody or requireJSON to these routes.
//
// NOTE: Do not add response-buffering middleware to these routes. SSE streams
// (e.g. /api/agent/:name/event) require the ResponseWriter chain to implement
// http.Flusher. Any middleware that wraps ResponseWriter without forwarding
// Flusher will break SSE.
mux.Handle("/agent/", a.Middleware(proxy.AgentUIHandler(pm, cache, proxy.AgentPort)))
mux.Handle("/api/agent/", a.Middleware(proxy.AgentAPIHandler(pm, proxy.AgentPort)))
mux.Handle("/apps/", a.Middleware(proxy.ProxyHandler(pm)))
```

Export `AgentPort` in proxy.go:

```go
// AgentPort is the port opencode serve binds to inside every project container.
const AgentPort = agentPort
```

- [ ] **Step 6: Wire AssetCache in main.go**

In `cmd/appx/main.go`, after the project manager is created:

```go
// Asset cache for the opencode web UI. Cleared whenever a container starts
// so stale HTML (from a prior opencode version) is never served.
assetCache := proxy.NewAssetCache()
pm.SetStartHook(assetCache.Clear)
```

Add `"github.com/neuromaxer/appx/internal/proxy"` to the imports.

Pass it to the server config:

```go
server.Run(server.Config{
    // ... existing fields ...
    AssetCache: assetCache,
})
```

Pass it into `NewRouter` wherever it's called inside `server.go`.

- [ ] **Step 7: Run full test suite**

```bash
task test
```
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/server/middleware.go internal/server/router.go internal/server/server.go cmd/appx/main.go internal/server/router_test.go internal/proxy/proxy.go
git commit -m "feat(server): register proxy routes, fix X-Frame-Options for /agent/, wire AssetCache"
```

---

## Task 10: Project port edit endpoint

**Files:**
- Modify: `internal/server/project_handlers.go`
- Modify: `internal/server/router.go`
- Modify: `internal/server/router_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/server/router_test.go`:

```go
func TestUpdateProjectPort(t *testing.T) {
	handler, store, _ := setupTest(t)

	// Create a project first.
	p, err := store.Create("port-test", 3000)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	cookie := loginAndGetCookie(t, handler)

	// Update the port.
	body := `{"port":8080}`
	req := httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var updated project.Project
	json.NewDecoder(rr.Body).Decode(&updated)
	if updated.Port != 8080 {
		t.Errorf("got port %d, want 8080", updated.Port)
	}

	// Verify persistence.
	stored, _ := store.Get(p.ID)
	if stored.Port != 8080 {
		t.Errorf("port not persisted: got %d", stored.Port)
	}
}

func TestUpdateProjectPortInvalid(t *testing.T) {
	handler, store, _ := setupTest(t)
	p, _ := store.Create("port-invalid", 3000)
	cookie := loginAndGetCookie(t, handler)

	for _, body := range []string{`{"port":0}`, `{"port":99999}`, `{}`} {
		req := httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s: got %d, want 4xx", body, rr.Code)
		}
	}
}
```

Add `loginAndGetCookie` helper if not already present in router_test.go:

```go
// loginAndGetCookie logs in with the test password and returns the session cookie.
func loginAndGetCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"password":"` + testPassword + `"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "appx_session" {
			return c
		}
	}
	t.Fatal("no session cookie in login response")
	return nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
task test ./internal/server/... 2>&1 | grep "FAIL"
```

- [ ] **Step 3: Add handleUpdateProject to project_handlers.go**

Add to `internal/server/project_handlers.go`:

```go
// handleUpdateProject handles PATCH /api/projects/{id}. Accepts a JSON body
// with an optional "port" field (1–65535) and updates the project's user-app
// proxy port. Returns the updated project. Requires authentication.
func handleUpdateProject(pm *project.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var body struct {
			Port int `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if body.Port < 1 || body.Port > 65535 {
			http.Error(w, "port must be 1–65535", http.StatusBadRequest)
			return
		}

		if err := pm.UpdatePort(id, body.Port); err == project.ErrNotFound {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		} else if err == project.ErrInvalidPort {
			http.Error(w, "port must be 1–65535", http.StatusBadRequest)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		p, err := pm.Get(id)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, p)
	}
}
```

- [ ] **Step 4: Register the route in router.go**

Inside the `api` submux registrations block, add:

```go
api.HandleFunc("PATCH /api/projects/{id}", handleUpdateProject(pm))
```

- [ ] **Step 5: Run tests**

```bash
task test
```
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/server/project_handlers.go internal/server/router.go internal/server/router_test.go
git commit -m "feat(api): add PATCH /api/projects/:id for port editing"
```

---

## Task 11: Frontend — API client

**Files:**
- Modify: `web/src/api/client.ts`

- [ ] **Step 1: Add updateProject to client.ts**

In `web/src/api/client.ts`, add after the existing project functions:

```typescript
/**
 * Updates the user-app proxy port for a project.
 * PATCH /api/projects/:id
 * @param id Project UUID
 * @param port New port number (1–65535)
 * @returns Updated Project
 */
export async function updateProject(id: string, port: number): Promise<Project> {
  return request<Project>(`/api/projects/${id}`, {
    method: 'PATCH',
    body: JSON.stringify({ port }),
  });
}
```

- [ ] **Step 2: Build frontend to verify no TS errors**

```bash
task web
```
Expected: builds cleanly.

- [ ] **Step 3: Commit**

```bash
git add web/src/api/client.ts
git commit -m "feat(frontend): add updateProject API client function"
```

---

## Task 12: Frontend — ProjectCard Open button

**Files:**
- Modify: `web/src/components/ProjectCard.tsx`

- [ ] **Step 1: Read the current ProjectCard to understand the Open button**

```bash
cat web/src/components/ProjectCard.tsx
```

Find the button or link that currently navigates to `/projects/:id` or similar on "Open".

- [ ] **Step 2: Update the Open button to navigate to /agent/:name/**

The "Open" action should only be available when the project is running. Change the destination from the internal project route to `/agent/:name/`. Example — find the open/navigate call and replace:

```typescript
// Before (approximate — adapt to actual code):
onClick={() => navigate(`/projects/${project.id}`)}

// After:
onClick={() => window.location.href = `/agent/${project.name}/`}
// or if using react-router:
onClick={() => navigate(`/agent/${project.name}/`)}
```

If the Open button opens in the same tab, keep it as-is. The OpenCode UI is a full-page experience; opening in the same tab is fine and expected.

- [ ] **Step 3: Build and manually verify**

```bash
task build && ./appx -port 8443
```

1. Open `https://localhost:8443` in a browser
2. Create and start a project
3. Click "Open" on the project card
4. Verify browser navigates to `https://localhost:8443/agent/:name/`
5. (The OpenCode UI may not load yet if the container doesn't have `opencode serve` bound — that's expected until the base image is rebuilt. A 502/service unavailable is correct.)

- [ ] **Step 4: Commit**

```bash
git add web/src/components/ProjectCard.tsx
git commit -m "feat(frontend): Open button navigates to /agent/:name/ (OpenCode UI)"
```

---

## Task 13: Frontend — Project page tabs

**Files:**
- Modify: `web/src/pages/Project.tsx`

- [ ] **Step 1: Read the current Project.tsx**

```bash
cat web/src/pages/Project.tsx
```

Understand the current layout — it likely shows the xterm.js terminal full-page.

- [ ] **Step 2: Add Agent/Terminal tabs and App button**

Replace the single-view layout with a tabbed view. Use the existing CSS variable system (never hardcode colours):

```typescript
// Add to state:
const [activeTab, setActiveTab] = useState<'agent' | 'terminal'>('agent');

// Tab bar styles (use CSS variables from index.css):
const styles: Record<string, React.CSSProperties> = {
  // ... existing styles ...
  tabBar: {
    display: 'flex',
    gap: '4px',
    padding: '8px 16px',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg-secondary)',
    alignItems: 'center',
  },
  tab: {
    padding: '6px 16px',
    cursor: 'pointer',
    border: '1px solid transparent',
    borderRadius: '4px',
    fontSize: '13px',
    color: 'var(--text-secondary)',
    background: 'transparent',
  },
  tabActive: {
    padding: '6px 16px',
    cursor: 'pointer',
    border: '1px solid var(--border)',
    borderRadius: '4px',
    fontSize: '13px',
    color: 'var(--text-primary)',
    background: 'var(--bg-tertiary)',
  },
  iframe: {
    flex: 1,
    width: '100%',
    border: 'none',
    display: 'block',
  },
  appLink: {
    marginLeft: 'auto',
    padding: '4px 12px',
    fontSize: '12px',
    color: 'var(--accent)',
    textDecoration: 'none',
    border: '1px solid var(--accent)',
    borderRadius: '4px',
  },
};

// Tab bar JSX (insert above the terminal/iframe content area):
<div style={styles.tabBar}>
  <button
    style={activeTab === 'agent' ? styles.tabActive : styles.tab}
    onClick={() => setActiveTab('agent')}
  >
    Agent
  </button>
  <button
    style={activeTab === 'terminal' ? styles.tabActive : styles.tab}
    onClick={() => setActiveTab('terminal')}
  >
    Terminal
  </button>
  {project?.status === 'running' && project.port && (
    <a
      href={`/apps/${project.name}/`}
      target="_blank"
      rel="noopener noreferrer"
      style={styles.appLink}
    >
      Open App ↗
    </a>
  )}
</div>

// Content area — show iframe or terminal based on activeTab:
{activeTab === 'agent' ? (
  <iframe
    src={`/agent/${project?.name}/`}
    style={styles.iframe}
    title="OpenCode Agent UI"
  />
) : (
  // existing Terminal component here
  <Terminal ... />
)}
```

Ensure the content area uses `flex: 1` and `overflow: hidden` so the iframe fills the available height.

- [ ] **Step 3: Build and manually verify**

```bash
task build && ./appx -port 8443
```

1. Open `https://localhost:8443`
2. Start a project
3. Open it — should land on the Project page with Agent tab active
4. Click Terminal tab — xterm.js terminal should appear
5. If project port is set, "Open App ↗" link should appear

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/Project.tsx
git commit -m "feat(frontend): add Agent/Terminal tabs and App link to Project page"
```

---

## Task 14: End-to-end verification and architecture doc

**Files:**
- Create: `docs/architecture/arch_phase_4.md`

- [ ] **Step 1: Full integration test**

Rebuild the base image (which now has the new CMD), then verify the full flow:

```bash
# Rebuild base image
docker rmi appx-base:latest 2>/dev/null || true
task build
./appx -port 8443 &
sleep 2

# Verify opencode serve starts in container
# (replace <name> with an actual project name you've started)
docker exec appx-<name> curl -s http://localhost:4096/global/health
# Expected: {"healthy":true,"version":"..."}

# Verify proxy routes work
curl -sk -b "appx_session=<your_session>" https://localhost:8443/api/agent/<name>/global/health
# Expected: {"healthy":true,...}

# Verify HTML rewriting (check no root-absolute /assets/ remain)
curl -sk -b "appx_session=<your_session>" https://localhost:8443/agent/<name>/ | grep '="/assets/'
# Expected: no output (all paths rewritten)

kill %1
```

- [ ] **Step 2: Run full test suite**

```bash
task test && task lint
```
Expected: all pass, no lint errors.

- [ ] **Step 3: Write architecture doc**

Create `docs/architecture/arch_phase_4.md` with the following content:

```markdown
# Phase 4: Reverse Proxy + AI Agent Web UI

## Overview

Phase 4 adds reverse proxy capabilities for two types of traffic:
1. `/apps/:name/*` → container's user app port (transparent proxy, path stripped)
2. `/agent/:name/` + `/api/agent/:name/*` → OpenCode's `opencode serve` API inside the container

## How the OpenCode proxy works

OpenCode's web UI (`opencode web` / `opencode serve`) does not support `--base-path`.
Its HTML uses root-absolute asset paths (`/assets/index.js`) which break when
proxied at a sub-path. The proxy solves this by:

1. Fetching the HTML from the container once per container start
2. Rewriting all `="/` attribute occurrences to `="/agent/:name/`
3. Injecting a `<script>` that sets `localStorage["opencode.settings.dat:defaultServerUrl"]`
   to `https://<host>/api/agent/:name` before the SPA initialises
4. Caching the result (and all static assets) in memory

The SPA reads the localStorage key at startup and uses it as the API base URL.
All API calls (SSE, REST, WebSocket) go to `/api/agent/:name/*` on the appx server,
which strips the prefix and forwards to `container:4096`.

## Key implementation files

| File | Responsibility |
|---|---|
| `internal/proxy/proxy.go` | Three HTTP handlers: ProxyHandler, AgentUIHandler, AgentAPIHandler |
| `internal/proxy/assets.go` | Thread-safe in-memory cache for HTML + static assets |
| `internal/proxy/ws.go` | Raw TCP tunnel for WebSocket upgrade requests |
| `internal/project/Dockerfile.project` | CMD starts `opencode serve` on container start |
| `internal/server/middleware.go` | X-Frame-Options skipped for /agent/ routes (iframe support) |

## Startup sequence

1. Container starts → CMD runs `opencode serve --port 4096 --hostname 0.0.0.0 &`
2. appx Manager's `startHook` is called → `AssetCache.Clear(projectName)`
3. First browser request to `/agent/:name/`:
   - Proxy fetches HTML from `container:4096/`
   - Rewrites paths + injects server URL
   - Caches and returns modified HTML
4. Browser requests `/agent/:name/assets/index-xxx.js` → served from asset cache
5. SPA initialises, reads server URL from localStorage
6. SPA calls `/api/agent/:name/event` (SSE) → proxy strips prefix → `container:4096/event`

## Security

- All proxy routes are behind auth middleware (session cookie required)
- `OPENCODE_SERVER_PASSWORD` is generated per-container at creation, stored in `container_secret` DB column
- The proxy forwards `Authorization: Basic` on all requests to `container:4096`
- `X-Frame-Options: DENY` is omitted only for `/agent/` routes
```

- [ ] **Step 4: Final commit**

```bash
git add docs/architecture/arch_phase_4.md
git commit -m "docs: add Phase 4 architecture reference doc"
```

---

## Summary

| Task | What it builds |
|---|---|
| 1 | DB migration: `container_secret` column |
| 2 | Project model + store: `ContainerSecret`, `GetByName`, `UpdatePort` |
| 3 | Container: `startHook`, `OPENCODE_SERVER_PASSWORD` env, Dockerfile CMD |
| 4 | `AssetCache`: thread-safe HTML + asset cache |
| 5 | `proxyWebSocket`: raw TCP WebSocket tunnel |
| 6 | `ProxyHandler`: `/apps/:name/*` → container user app |
| 7 | `AgentAPIHandler`: `/api/agent/:name/*` → opencode serve (HTTP + WS) |
| 8 | `AgentUIHandler`: `/agent/:name/` → cached + modified opencode HTML |
| 9 | Router + middleware wiring + `X-Frame-Options` fix |
| 10 | `PATCH /api/projects/:id` for port editing |
| 11 | `updateProject` API client function |
| 12 | ProjectCard "Open" → `/agent/:name/` |
| 13 | Project page Agent/Terminal tabs + App link |
| 14 | Integration test + architecture doc |
