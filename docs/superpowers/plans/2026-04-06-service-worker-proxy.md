# Service Worker Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace subdomain routing with a Service Worker that intercepts OpenCode SPA fetch calls, enabling path-based routing (`/agent/:name/`) with no cert issues on any platform.

**Architecture:** OpenCode UI is served at `/agent/:name/` (same origin, no subdomain). On first load, a dynamically-generated Service Worker is installed; it intercepts all `fetch()` calls from the SPA and rewrites root-absolute paths (`/session` → `/api/agent/:name/session`, `/assets/chunk.js` → `/agent/:name/assets/chunk.js`). A small injected script wraps `window.WebSocket` to handle WS connections that SWs cannot intercept. HTML caching (server-side, per-project) provides graceful degradation when the container is stopped.

**Tech Stack:** Go 1.26 stdlib, Service Worker API, browser Fetch API interception

---

## File Map

**Create:**
- `internal/proxy/assets.go` — AssetCache (HTML + asset in-memory cache with RWMutex)
- `internal/proxy/assets_test.go` — Tests for AssetCache

**Modify:**
- `internal/proxy/proxy.go` — Add AgentUIHandler, AgentAPIHandler, rewriteHTML, generateSWScript, serveAgentHTML, serveAgentAsset; remove ContainerHandler, extractProjectName, ProjectNameKey
- `internal/proxy/proxy_test.go` — Add agent UI/API handler tests; remove ContainerHandler tests
- `internal/project/container.go` — Restore startHook field + SetStartHook method + call sites
- `internal/server/router.go` — Switch to path-based routing; remove subdomain dispatch, baseDomain param, agent-token route; add /agent/ and /api/agent/ routes
- `internal/server/middleware.go` — CSP based on path prefix instead of subdomain host; add worker-src; remove baseDomain param
- `internal/server/server.go` — Config: remove BaseDomain, add AssetCache
- `internal/server/auth_handlers.go` — Remove handleCreateAgentToken
- `internal/auth/auth.go` — Remove BaseDomain, agentTokens, all agent token methods; revert cookie to SameSite=Strict, no Domain
- `internal/server/router_test.go` — Update NewRouter/auth.New signatures, remove token/subdomain tests, update CSP tests
- `cmd/appx/main.go` — Restore assetCache + startHook wiring; remove baseDomain flag; update Config
- `web/src/components/ProjectCard.tsx` — Open button: simple navigation, remove createAgentToken
- `web/src/pages/Project.tsx` — Restore iframe for Agent tab
- `web/src/api/client.ts` — Remove createAgentToken function

---

## Task 1: Restore AssetCache and startHook

**Files:**
- Create: `internal/proxy/assets.go`
- Create: `internal/proxy/assets_test.go`
- Modify: `internal/project/container.go`
- Modify: `internal/project/manager_test.go`

- [ ] **Step 1: Write failing test for AssetCache**

Create `internal/proxy/assets_test.go`:

```go
package proxy

import "testing"

func TestAssetCache_HTMLRoundTrip(t *testing.T) {
	c := NewAssetCache()
	if _, ok := c.GetHTML("proj"); ok {
		t.Fatal("expected cache miss before set")
	}
	c.SetHTML("proj", []byte("<html>"))
	data, ok := c.GetHTML("proj")
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if string(data) != "<html>" {
		t.Errorf("got %q", data)
	}
}

func TestAssetCache_AssetRoundTrip(t *testing.T) {
	c := NewAssetCache()
	c.SetAsset("/assets/app.js", []byte("console.log('hi')"))
	data, ok := c.GetAsset("/assets/app.js")
	if !ok || string(data) != "console.log('hi')" {
		t.Errorf("asset round-trip failed: ok=%v data=%q", ok, data)
	}
}

func TestAssetCache_ClearWipesAll(t *testing.T) {
	c := NewAssetCache()
	c.SetHTML("a", []byte("html-a"))
	c.SetHTML("b", []byte("html-b"))
	c.SetAsset("/assets/x.js", []byte("js"))
	c.Clear("a") // name arg accepted but all entries cleared
	if _, ok := c.GetHTML("a"); ok {
		t.Error("expected html-a cleared")
	}
	if _, ok := c.GetHTML("b"); ok {
		t.Error("expected html-b cleared (conservative clear-all)")
	}
	if _, ok := c.GetAsset("/assets/x.js"); ok {
		t.Error("expected asset cleared")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/proxy/ -run TestAssetCache -v
```
Expected: FAIL — `NewAssetCache` undefined

- [ ] **Step 3: Create assets.go**

Create `internal/proxy/assets.go`:

```go
package proxy

import "sync"

// AssetCache holds cached OpenCode web UI assets in memory. Stores per-project
// HTML and shared static assets (JS, CSS, favicons). Cleared when any container
// starts to ensure assets stay in sync with the running OpenCode version.
type AssetCache struct {
	mu     sync.RWMutex
	html   map[string][]byte // key: project name
	assets map[string][]byte // key: URL path (e.g. "/assets/index.js")
}

// NewAssetCache creates an AssetCache with initialized storage maps. Called
// once at server startup and passed through to AgentUIHandler.
func NewAssetCache() *AssetCache {
	return &AssetCache{
		html:   make(map[string][]byte),
		assets: make(map[string][]byte),
	}
}

// GetHTML retrieves cached HTML for a project. Returns nil, false on miss.
// Safe for concurrent reads.
func (c *AssetCache) GetHTML(name string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.html[name]
	return v, ok
}

// SetHTML stores modified HTML for a project.
func (c *AssetCache) SetHTML(name string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.html[name] = data
}

// GetAsset retrieves a cached static asset by URL path.
func (c *AssetCache) GetAsset(path string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.assets[path]
	return v, ok
}

// SetAsset stores a static asset by URL path.
func (c *AssetCache) SetAsset(path string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.assets[path] = data
}

// Clear removes all cached HTML and assets. Called when any container starts
// to prevent stale content after a container rebuild or OpenCode update.
// The name parameter is accepted to satisfy the startHook signature but all
// entries are cleared regardless — conservative strategy that handles base
// image rebuilds where asset hashes change across all projects.
func (c *AssetCache) Clear(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.html {
		delete(c.html, k)
	}
	for k := range c.assets {
		delete(c.assets, k)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/proxy/ -run TestAssetCache -v
```
Expected: PASS (3 tests)

- [ ] **Step 5: Write failing test for startHook**

Add to `internal/project/manager_test.go`:

```go
func TestManager_StartHook_CalledOnStart(t *testing.T) {
	store := NewStore(setupTestDB(t))
	fd := newFakeDocker()
	fd.failImageInspect = fmt.Errorf("not found")
	m := NewManager(store, fd, "test-api-key", "")

	var hookedName string
	m.SetStartHook(func(name string) { hookedName = name })

	p, _ := m.Create("hook-test", 3000)
	m.Start(p.ID)
	waitForStatus(t, store, p.ID, StatusRunning)

	if hookedName != "hook-test" {
		t.Errorf("expected hook called with 'hook-test', got %q", hookedName)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

```bash
go test ./internal/project/ -run TestManager_StartHook -v
```
Expected: FAIL — `SetStartHook` undefined

- [ ] **Step 7: Restore startHook in container.go**

In `internal/project/container.go`, add `startHook` back to the `Manager` struct (after `tm terminalCloser`):

```go
type Manager struct {
	store       *Store
	docker      dockerer
	projectsDir string

	mu           sync.RWMutex
	anthropicKey string
	tm           terminalCloser
	startHook    func(projectName string) // called after a container starts successfully

	portCache   map[string]string
	portCacheMu sync.RWMutex
}
```

Add the `SetStartHook` method after `SetTerminalManager`:

```go
// SetStartHook registers a function to call whenever a container transitions
// to running. Used by the asset cache to clear cached HTML and assets for the
// project when the container restarts, so the next request fetches fresh content.
func (m *Manager) SetStartHook(f func(projectName string)) {
	m.startHook = f
}
```

In `tryReuseContainer`, add the hook call after `SetRunning` succeeds (find the `if err := m.store.SetRunning(...); err != nil {` block and add below it):

```go
	if err := m.store.SetRunning(proj.ID, proj.ContainerID, proj.NetworkID, proj.ImageName); err != nil {
		log.Printf("project %s: failed to update DB after restart: %v", proj.Name, err)
	}

	if m.startHook != nil {
		m.startHook(proj.Name)
	}
	return true
```

In `doFullCreate`, add the hook call after `SetRunning` succeeds (find the `if err := m.store.SetRunning(proj.ID, ctrRes.ID, netRes.ID, baseImageTag); err != nil {` block and add after the closing `}`):

```go
	if err := m.store.SetRunning(proj.ID, ctrRes.ID, netRes.ID, baseImageTag); err != nil {
		cleanup()
		log.Printf("project %s: failed to update DB after start (project deleted?): %v", name, err)
		return
	}

	if m.startHook != nil {
		m.startHook(name)
	}
```

- [ ] **Step 8: Run test to verify it passes**

```bash
go test ./internal/project/ -run TestManager_StartHook -v
```
Expected: PASS

- [ ] **Step 9: Run full test suite**

```bash
go test ./...
```
Expected: all pass

- [ ] **Step 10: Commit**

```bash
git add internal/proxy/assets.go internal/proxy/assets_test.go internal/project/container.go internal/project/manager_test.go
git commit -m "feat(proxy): restore AssetCache and startHook for service worker proxy"
```

---

## Task 2: Add AgentAPIHandler

**Files:**
- Modify: `internal/proxy/proxy.go` (add only — don't remove anything yet)
- Modify: `internal/proxy/proxy_test.go` (add tests only)

- [ ] **Step 1: Write failing tests for AgentAPIHandler**

Add to `internal/proxy/proxy_test.go`:

```go
// TestAgentAPIHandlerForwardsRequest verifies that AgentAPIHandler proxies requests
// to the container's opencode serve, strips the /api/agent/:name prefix, strips
// the session cookie, and injects Basic Auth when a ContainerSecret is set.
func TestAgentAPIHandlerForwardsRequest(t *testing.T) {
	var gotPath, gotAuth, gotCookie string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer backend.Close()

	resolver := &fakeResolver{
		proj: &project.Project{
			ID: "p1", Name: "my-agent", Status: project.StatusRunning,
			ContainerSecret: "testsecret",
		},
		addr: backend.Listener.Addr().String(),
	}

	h := AgentAPIHandler(resolver, 4096)
	req := httptest.NewRequest("GET", "/api/agent/my-agent/session", nil)
	req.Header.Set("Cookie", "appx_session=should-strip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rr.Code)
	}
	if gotPath != "/session" {
		t.Errorf("expected /session, got %q", gotPath)
	}
	if gotCookie != "" {
		t.Errorf("Cookie leaked: %q", gotCookie)
	}
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:testsecret"))
	if gotAuth != expected {
		t.Errorf("auth: got %q, want %q", gotAuth, expected)
	}
}

func TestAgentAPIHandlerProjectNotFound(t *testing.T) {
	h := AgentAPIHandler(&fakeResolver{}, 4096)
	req := httptest.NewRequest("GET", "/api/agent/ghost/session", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rr.Code)
	}
}

func TestAgentAPIHandlerProjectNotRunning(t *testing.T) {
	h := AgentAPIHandler(&fakeResolver{
		proj: &project.Project{ID: "p1", Name: "stopped", Status: project.StatusStopped},
		addr: "127.0.0.1:4096",
	}, 4096)
	req := httptest.NewRequest("GET", "/api/agent/stopped/session", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/proxy/ -run TestAgentAPIHandler -v
```
Expected: FAIL — `AgentAPIHandler` undefined

- [ ] **Step 3: Add AgentAPIHandler to proxy.go**

Add after `ProxyHandler` in `internal/proxy/proxy.go`:

```go
// AgentAPIHandler returns an http.Handler that proxies /api/agent/:name/* to
// the project's opencode serve instance. The /api/agent/:name prefix is stripped
// before forwarding. Handles HTTP (including SSE streams) and WebSocket upgrades.
// Cookie stripped, Basic Auth injected from ContainerSecret.
// agentBackendPort is the container port where opencode serve listens (4096 in production).
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

		addr, err := pm.ContainerAddr(proj.ID, agentBackendPort)
		if err != nil {
			log.Printf("proxy: agent API container %s unreachable: %v", name, err)
			http.Error(w, "container not reachable", http.StatusBadGateway)
			return
		}

		log.Printf("proxy: agent API %s %s → http://%s%s", r.Method, r.URL.Path, addr, rest)

		r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)
		r.Header.Del("Cookie")
		if a := agentBasicAuth(proj.ContainerSecret); a != "" {
			r.Header.Set("Authorization", a)
		}

		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			log.Printf("proxy: agent WS %s → %s%s", name, addr, rest)
			proxyWebSocket(w, r, addr, rest)
			return
		}

		target, _ := url.Parse(fmt.Sprintf("http://%s%s", addr, rest))
		target.RawQuery = r.URL.RawQuery
		rp := newReverseProxy(target)
		r2 := r.Clone(r.Context())
		r2.URL = target
		r2.Host = target.Host
		rp.ServeHTTP(w, r2)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/proxy/ -run TestAgentAPIHandler -v
```
Expected: all 3 PASS

- [ ] **Step 5: Run full test suite**

```bash
go test ./...
```
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): add AgentAPIHandler for path-based container API proxy"
```

---

## Task 3: Add AgentUIHandler with Service Worker support

**Files:**
- Modify: `internal/proxy/proxy.go` (add AgentUIHandler + helpers)
- Modify: `internal/proxy/proxy_test.go` (add tests)

- [ ] **Step 1: Write failing tests**

Add to `internal/proxy/proxy_test.go`:

```go
func TestRewriteHTML_PrefixesPaths(t *testing.T) {
	input := `<html><head><script src="/assets/index.js"></script><link href="/favicon.ico"></head></html>`
	got := rewriteHTML(input, "myproj")
	if strings.Contains(got, `src="/assets/`) {
		t.Error("unrewritten asset path found")
	}
	if !strings.Contains(got, `src="/agent/myproj/assets/index.js"`) {
		t.Errorf("expected rewritten src, got:\n%s", got)
	}
	if !strings.Contains(got, `href="/agent/myproj/favicon.ico"`) {
		t.Errorf("expected rewritten href, got:\n%s", got)
	}
}

func TestRewriteHTML_InjectsSWScript(t *testing.T) {
	input := `<html><head></head><body></body></html>`
	got := rewriteHTML(input, "myproj")
	if !strings.Contains(got, "serviceWorker") {
		t.Error("expected SW registration script injected")
	}
	if !strings.Contains(got, "/agent/myproj/sw.js") {
		t.Error("expected SW file path in script")
	}
}

func TestRewriteHTML_InjectsWSPatcher(t *testing.T) {
	input := `<html><head></head><body></body></html>`
	got := rewriteHTML(input, "myproj")
	if !strings.Contains(got, "window.WebSocket") {
		t.Error("expected WebSocket patcher injected")
	}
	if !strings.Contains(got, "/api/agent/myproj") {
		t.Error("expected project API prefix in WS patcher")
	}
}

func TestGenerateSWScript_ContainsProjectName(t *testing.T) {
	script := string(generateSWScript("my-app"))
	if !strings.Contains(script, `"my-app"`) {
		t.Error("expected JSON-encoded project name in SW script")
	}
	if !strings.Contains(script, "clients.claim") {
		t.Error("expected clients.claim() in SW")
	}
	if !strings.Contains(script, "/api/agent/") {
		t.Error("expected API prefix pattern in SW")
	}
}

func TestAgentUIHandlerServesSWFile(t *testing.T) {
	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "sw-proj", Status: project.StatusRunning},
		addr: "127.0.0.1:4096",
	}
	cache := NewAssetCache()
	h := AgentUIHandler(resolver, cache, 4096)

	req := httptest.NewRequest("GET", "/agent/sw-proj/sw.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/javascript" {
		t.Errorf("expected application/javascript, got %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "sw-proj") {
		t.Error("expected project name in SW file")
	}
}

func TestAgentUIHandlerServesHTMLFromCache(t *testing.T) {
	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "cached-proj", Status: project.StatusRunning},
		addr: "127.0.0.1:4096",
	}
	cache := NewAssetCache()
	cache.SetHTML("cached-proj", []byte("<html>cached</html>"))
	h := AgentUIHandler(resolver, cache, 4096)

	req := httptest.NewRequest("GET", "/agent/cached-proj/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "cached") {
		t.Errorf("expected cached HTML, got %q", rr.Body.String())
	}
}

func TestAgentUIHandlerServesAssetFromCache(t *testing.T) {
	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "asset-proj", Status: project.StatusRunning},
		addr: "127.0.0.1:4096",
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

func TestAgentUIHandlerProjectNotFound(t *testing.T) {
	h := AgentUIHandler(&fakeResolver{}, NewAssetCache(), 4096)
	req := httptest.NewRequest("GET", "/agent/ghost/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/proxy/ -run "TestRewriteHTML|TestGenerateSW|TestAgentUIHandler" -v
```
Expected: FAIL — functions undefined

- [ ] **Step 3: Add required imports to proxy.go**

At the top of `internal/proxy/proxy.go`, update the imports block:

```go
import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/project"
)
```

- [ ] **Step 4: Add constants and fetchClient**

Add after the existing `maxProxyBodyBytes` constant in `proxy.go`:

```go
// maxFetchSize is the maximum response body size when fetching HTML or assets
// from containers server-side. Prevents a container from causing unbounded
// memory growth via the server-side asset cache.
const maxFetchSize = 50 << 20 // 50 MB

// fetchClient fetches HTML and assets from containers. Separate from
// proxyTransport's reverse-proxy path: this is used for the server-side
// asset cache population, not for live request proxying.
var fetchClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: proxyTransport,
}
```

- [ ] **Step 5: Add rewriteHTML**

Add after `fetchClient` in `proxy.go`:

```go
// rewriteHTML rewrites all root-absolute paths (="/...) in the HTML to
// project-prefixed paths (="/agent/:name/...), injects a Service Worker
// registration script with a loading overlay (prevents the SPA from making
// unintercepted requests during first-visit SW installation), and injects a
// WebSocket URL patcher (SWs cannot intercept WebSocket connections, so the
// patcher wraps window.WebSocket to rewrite WS URLs through the API proxy).
func rewriteHTML(html, projectName string) string {
	// Rewrite root-absolute asset paths to be project-prefixed.
	html = strings.ReplaceAll(html, `="/`, `="/agent/`+projectName+`/`)

	// JSON-encode the project name for safe embedding in JavaScript string literals.
	encodedName, _ := json.Marshal(projectName)
	name := string(encodedName) // e.g. "test3" (with quotes)

	// SW registration with loading overlay. Shows a dark screen until the SW
	// is controlling the page. On first visit this takes ~100-300ms while the
	// SW installs and claims clients. On subsequent visits the SW is already
	// active and the overlay is removed immediately.
	swScript := `<script>(function(){` +
		`var el=document.createElement('div');` +
		`el.id='_appx_sw';` +
		`el.style.cssText='position:fixed;inset:0;background:#0d0d0d;z-index:9999;` +
		`display:flex;align-items:center;justify-content:center;` +
		`color:#6b7280;font-family:monospace;font-size:.85rem';` +
		`el.textContent='starting\u2026';` +
		`document.documentElement.appendChild(el);` +
		`if(!('serviceWorker' in navigator)){el.remove();return;}` +
		`if(navigator.serviceWorker.controller){el.remove();return;}` +
		`navigator.serviceWorker.register('/agent/` + projectName + `/sw.js',` +
		`{scope:'/agent/` + projectName + `/'});` +
		`navigator.serviceWorker.addEventListener('controllerchange',function(){el.remove();});` +
		`})();</script>`

	// WebSocket URL patcher. SWs cannot intercept new WebSocket() calls.
	// This wraps window.WebSocket so OpenCode's PTY connections are routed
	// through /api/agent/:name/ instead of hitting the root origin directly.
	wsScript := `<script>(function(){` +
		`var _WS=window.WebSocket;` +
		`var pfx='/api/agent/` + projectName + `';` +
		`window.WebSocket=function(url,p){` +
		`if(typeof url==='string')` +
		`{url=url.replace(/^(wss?:\/\/[^\/]+)\//,'$1'+pfx+'/');}` +
		`return p?new _WS(url,p):new _WS(url);};` +
		`window.WebSocket.prototype=_WS.prototype;` +
		`window.WebSocket.CONNECTING=0;window.WebSocket.OPEN=1;` +
		`window.WebSocket.CLOSING=2;window.WebSocket.CLOSED=3;` +
		`})();</script>`

	html = strings.Replace(html, `</head>`, swScript+wsScript+`</head>`, 1)
	return html
}
```

- [ ] **Step 6: Add generateSWScript**

```go
// generateSWScript returns the JavaScript for the project-specific Service
// Worker. The project name is baked in as a JSON literal. The SW:
//   - Calls clients.claim() on activate so it takes control of the current
//     page without requiring a reload after first installation.
//   - Intercepts same-origin fetch calls and rewrites root-absolute paths:
//     asset paths (/assets/*, /favicon*, etc.) → /agent/:name/...
//     all other paths (/session, /global/event, etc.) → /api/agent/:name/...
//   - Passes through external origins and already-prefixed paths unchanged.
func generateSWScript(projectName string) []byte {
	encodedName, _ := json.Marshal(projectName)
	name := string(encodedName) // JSON-encoded, safe to embed

	script := `const PROJECT=` + name + `;
const SCOPE="/agent/"+PROJECT+"/";
const API="/api/agent/"+PROJECT;
self.addEventListener("activate",e=>e.waitUntil(clients.claim()));
self.addEventListener("fetch",e=>{
  const u=new URL(e.request.url);
  if(u.origin!==self.location.origin)return;
  if(u.pathname.startsWith(SCOPE))return;
  if(u.pathname.startsWith("/api/"))return;
  if(u.pathname.startsWith("/ws/"))return;
  const isAsset=u.pathname.startsWith("/assets/")||
    /^\/(favicon|apple-touch|site\.webmanifest|social-share)/.test(u.pathname);
  const rewritten=isAsset
    ?SCOPE.slice(0,-1)+u.pathname+u.search
    :API+u.pathname+u.search;
  const init={method:e.request.method,headers:e.request.headers,
    credentials:e.request.credentials};
  if(e.request.method!=="GET"&&e.request.method!=="HEAD")
    init.body=e.request.body;
  e.respondWith(fetch(new Request(rewritten,init)));
});
`
	return []byte(script)
}
```

- [ ] **Step 7: Add serveAgentHTML and serveAgentAsset**

```go
// serveAgentHTML fetches HTML from the container, rewrites it via rewriteHTML
// (path-prefix + SW scripts injection), caches the result, and writes it to
// the response. authHeader is "Basic ..." or empty.
func serveAgentHTML(w http.ResponseWriter, r *http.Request, cache *AssetCache, name, backendBase, authHeader string) {
	req, _ := http.NewRequest("GET", backendBase+"/", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		log.Printf("proxy: failed to fetch HTML for %s: %v", name, err)
		http.Error(w, "cannot reach opencode server", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchSize))
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}
	modified := rewriteHTML(string(raw), name)
	cache.SetHTML(name, []byte(modified))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(modified))
}

// serveAgentAsset fetches an asset from the container, caches it, and writes
// it to the response. authHeader is "Basic ..." or empty.
func serveAgentAsset(w http.ResponseWriter, r *http.Request, cache *AssetCache, assetPath, backendBase, authHeader string) {
	req, _ := http.NewRequest("GET", backendBase+assetPath, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := fetchClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if err != nil {
			log.Printf("proxy: failed to fetch asset %s: %v", assetPath, err)
		}
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchSize))
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

- [ ] **Step 8: Add AgentUIHandler**

```go
// AgentUIHandler serves the OpenCode web UI at /agent/:name/. Implements
// cache-first serving for HTML and assets — cached content is served even when
// the container is stopped, enabling graceful degradation. On cache miss the
// handler fetches from the container, rewrites HTML (path-prefix + SW injection),
// and caches the result. The SW is served at /agent/:name/sw.js dynamically.
// agentBackendPort is the opencode serve port inside the container (4096).
func AgentUIHandler(pm containerResolver, cache *AssetCache, agentBackendPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest := parsePath(r.URL.Path, "/agent/")
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

		// Serve the dynamically-generated Service Worker file.
		if rest == "/sw.js" {
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Cache-Control", "no-cache") // always fetch fresh SW
			w.Write(generateSWScript(name))
			return
		}

		// Index HTML: cache-first (serve cached even if container stopped).
		if rest == "/" || rest == "" {
			if cached, ok := cache.GetHTML(name); ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(cached)
				return
			}
			if proj.Status != project.StatusRunning {
				http.Error(w, "project not running — start it first", http.StatusServiceUnavailable)
				return
			}
			addr, err := pm.ContainerAddr(proj.ID, agentBackendPort)
			if err != nil {
				log.Printf("proxy: agent UI container %s unreachable: %v", name, err)
				http.Error(w, "container not reachable", http.StatusServiceUnavailable)
				return
			}
			serveAgentHTML(w, r, cache, name, "http://"+addr, agentBasicAuth(proj.ContainerSecret))
			return
		}

		// Assets: cache-first.
		if cached, ok := cache.GetAsset(rest); ok {
			ct := mime.TypeByExtension(filepath.Ext(rest))
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Write(cached)
			return
		}
		if proj.Status != project.StatusRunning {
			http.Error(w, "project not running", http.StatusServiceUnavailable)
			return
		}
		addr, err := pm.ContainerAddr(proj.ID, agentBackendPort)
		if err != nil {
			log.Printf("proxy: agent UI asset %s container %s unreachable: %v", rest, name, err)
			http.Error(w, "container not reachable", http.StatusServiceUnavailable)
			return
		}
		serveAgentAsset(w, r, cache, rest, "http://"+addr, agentBasicAuth(proj.ContainerSecret))
	})
}
```

- [ ] **Step 9: Run all new tests**

```bash
go test ./internal/proxy/ -run "TestRewriteHTML|TestGenerateSW|TestAgentUIHandler" -v
```
Expected: all PASS

- [ ] **Step 10: Run full test suite**

```bash
go test ./...
```
Expected: all pass

- [ ] **Step 11: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): add AgentUIHandler with service worker support"
```

---

## Task 4: Update router, middleware, server, and main

**Files:**
- Modify: `internal/server/router.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/auth_handlers.go`
- Modify: `internal/server/router_test.go`
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Rewrite router.go**

Replace `internal/server/router.go` with:

```go
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/proxy"
	"github.com/neuromaxer/appx/internal/terminal"
)

// NewRouter builds the top-level HTTP handler. OpenCode UI requests at
// /agent/:name/ and /api/agent/:name/* are served by dedicated handlers.
// All other requests go to the dashboard mux (API + terminal + SPA).
func NewRouter(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS, cache *proxy.AssetCache) http.Handler {
	mux := http.NewServeMux()

	// Public API routes (no auth) — rate limited
	loginLimiter := newRateLimiter(5*time.Minute, 10)
	mux.Handle("POST /api/login", limitBody(requireJSON(http.HandlerFunc(loginLimiter.middleware(handleLogin(a))))))

	// Protected API routes
	api := http.NewServeMux()
	api.HandleFunc("GET /api/projects", handleListProjects(pm))
	api.HandleFunc("POST /api/projects", handleCreateProject(pm))
	api.HandleFunc("GET /api/projects/{id}", handleGetProject(pm))
	api.HandleFunc("PATCH /api/projects/{id}", handleUpdateProject(pm))
	api.HandleFunc("DELETE /api/projects/{id}", handleDeleteProject(pm))
	api.HandleFunc("POST /api/projects/{id}/start", handleStartProject(pm))
	api.HandleFunc("POST /api/projects/{id}/stop", handleStopProject(pm))
	api.HandleFunc("POST /api/projects/{id}/reset", handleResetProject(pm))
	api.HandleFunc("GET /api/settings/api-key", handleGetAPIKeyStatus(a.Store, pm))
	api.HandleFunc("PUT /api/settings/api-key", handleSetAPIKey(a.Store, pm))
	api.HandleFunc("DELETE /api/settings/api-key", handleDeleteAPIKey(a.Store, pm))
	api.HandleFunc("GET /api/settings/terminal-buffer-size", handleGetTerminalBufferSize(a.Store))
	api.HandleFunc("PUT /api/settings/terminal-buffer-size", handleSetTerminalBufferSize(a.Store))
	api.HandleFunc("POST /api/projects/{id}/sessions", handleCreateSession(pm, tm))
	api.HandleFunc("GET /api/projects/{id}/sessions", handleListSessions(pm, tm))
	api.HandleFunc("DELETE /api/projects/{id}/sessions/{sid}", handleDeleteSession(tm))
	api.HandleFunc("DELETE /api/session", handleLogout(a))
	mux.Handle("/api/", limitBody(a.Middleware(requireJSON(api))))

	// WebSocket route for terminal sessions
	mux.Handle("/ws/", a.Middleware(http.HandlerFunc(terminal.HandleTerminalWS(tm))))

	// OpenCode agent UI — path-based, no subdomains needed
	// /agent/:name/sw.js served without auth (SW must be fetchable before auth cookie is set)
	// /agent/:name/* requires auth
	mux.Handle("/agent/", a.Middleware(proxy.AgentUIHandler(pm, cache, proxy.AgentPort)))

	// OpenCode agent API — all methods, SSE and WebSocket
	mux.Handle("/api/agent/", a.Middleware(proxy.AgentAPIHandler(pm, proxy.AgentPort)))

	// User app proxy
	mux.Handle("/apps/", a.Middleware(proxy.ProxyHandler(pm)))

	// React SPA fallback
	fileServer := http.FileServerFS(webFS)
	mux.Handle("/", spaHandler(fileServer, webFS))

	return securityHeaders(mux)
}

// spaHandler wraps a file server to support single-page application routing.
func spaHandler(fileServer http.Handler, webFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = path[1:]
		}
		if _, err := fs.Stat(webFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 2: Rewrite middleware.go**

Replace `securityHeaders` in `internal/server/middleware.go` — remove `baseDomain` param, switch to path-prefix, add `worker-src`:

```go
// securityHeaders wraps an HTTP handler to inject standard security headers on
// every response. For /agent/ routes (OpenCode UI), a permissive CSP is applied
// to support inline scripts, Service Workers, and WebSocket connections.
// For all other routes (dashboard), a strict CSP with X-Frame-Options: DENY
// is applied.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		if strings.HasPrefix(r.URL.Path, "/agent/") || strings.HasPrefix(r.URL.Path, "/api/agent/") {
			// OpenCode proxy routes: permissive CSP required for the SPA.
			// 'unsafe-inline' for OpenCode's inline scripts; worker-src 'self'
			// for the Service Worker; wss: for WebSocket connections.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self' 'unsafe-inline'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"font-src 'self' data:; "+
					"img-src 'self' data: blob:; "+
					"connect-src 'self' wss: ws:; "+
					"worker-src 'self'; "+
					"frame-ancestors 'self'")
		} else {
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self'; "+
					"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
					"font-src 'self' https://fonts.gstatic.com; "+
					"connect-src 'self'")
		}

		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 3: Update server.go Config**

In `internal/server/server.go`, replace the `Config` struct:

```go
type Config struct {
	Port            int
	DataDir         string
	DB              *sql.DB
	AuthStore       *auth.Store
	ProjectManager  *project.Manager
	TerminalManager *terminal.Manager
	WebFS           fs.FS
	TLSHosts        []string
	Domain          string
	CloudflareToken string
	AssetCache      *proxy.AssetCache
}
```

Update `Run()` to pass the cache and use `auth.New` without `baseDomain`:

```go
func Run(cfg Config) error {
	a := auth.New(cfg.AuthStore)
	// ... rest unchanged ...
	handler := NewRouter(a, cfg.ProjectManager, cfg.TerminalManager, cfg.WebFS, cfg.AssetCache)
	// ... rest unchanged ...
}
```

- [ ] **Step 4: Remove handleCreateAgentToken from auth_handlers.go**

Delete the `handleCreateAgentToken` function from `internal/server/auth_handlers.go`. It's the function that starts with:
```go
// handleCreateAgentToken handles POST /api/projects/{id}/agent-token...
func handleCreateAgentToken(a *auth.Auth) http.HandlerFunc {
```

- [ ] **Step 5: Update main.go**

In `cmd/appx/main.go`:

1. Remove the `--base-domain` flag and all `baseDomain` variable logic
2. Restore asset cache:
```go
assetCache := proxy.NewAssetCache()
pm.SetStartHook(assetCache.Clear)
```
3. Update `server.Config`:
```go
server.Config{
    // ...
    AssetCache: assetCache,
    // Remove: BaseDomain: baseDomain,
}
```

- [ ] **Step 6: Fix router_test.go**

In `internal/server/router_test.go`, make these changes:

a) Update `setupTest` helper — change `auth.New(store, "localhost")` to `auth.New(store)`, and update `NewRouter` to pass a cache:
```go
func setupTest(t *testing.T) (http.Handler, *auth.Store, *sql.DB) {
    // ... existing DB setup unchanged ...
    store := auth.NewStore(db)
    a := auth.New(store)  // remove "localhost" arg
    pm := project.NewManager(projectStore, nil, "", "")
    tm := terminal.NewManager(nil, 512*1024)
    cache := proxy.NewAssetCache()
    return server.NewRouter(a, pm, tm, webFS, cache), store, db
}
```

b) Remove these tests (they tested the now-deleted subdomain features):
- `TestSubdomainRouteHasFrameAncestorsCSP`
- `TestDashboardRouteNoFrameAncestors`
- `TestExtractSubdomain`
- `TestSubdomainDispatch_InjectsProjectNameInContext`
- `TestAgentTokenHandoff`
- `TestLoginSetsCookieDomainLocalhost`

c) Add replacement CSP tests:
```go
func TestAgentRouteHasPermissiveCSP(t *testing.T) {
	h, _, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/agent/my-proj/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "worker-src") {
		t.Errorf("expected worker-src in CSP for /agent/, got: %q", csp)
	}
	if !strings.Contains(csp, "unsafe-inline") {
		t.Errorf("expected unsafe-inline in CSP for /agent/, got: %q", csp)
	}
}

func TestDashboardRouteHasStrictCSP(t *testing.T) {
	h, _, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/api/projects", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if xfo := rr.Header().Get("X-Frame-Options"); xfo != "DENY" {
		t.Errorf("expected X-Frame-Options: DENY, got %q", xfo)
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("unexpected unsafe-inline in dashboard CSP: %q", csp)
	}
}
```

- [ ] **Step 7: Build and run all tests**

```bash
go build ./... && go test ./...
```
Expected: clean build, all tests pass

- [ ] **Step 8: Commit**

```bash
git add internal/server/router.go internal/server/middleware.go internal/server/server.go internal/server/auth_handlers.go internal/server/router_test.go cmd/appx/main.go
git commit -m "feat(server): switch to path-based routing with service worker support"
```

---

## Task 5: Revert auth.go — remove subdomain token machinery

**Files:**
- Modify: `internal/auth/auth.go`
- Modify: `internal/auth/store_test.go` (if it has domain-related tests)

- [ ] **Step 1: Rewrite auth.go**

Replace `internal/auth/auth.go` with:

```go
package auth

import "net/http"

// Auth provides HTTP authentication middleware and cookie management.
// It wraps a Store to validate session tokens from incoming requests.
type Auth struct {
	Store *Store
}

// New creates an Auth instance backed by the given session/password store.
func New(store *Store) *Auth {
	return &Auth{Store: store}
}

// Middleware returns an HTTP middleware that enforces authentication.
// It reads the "appx_session" cookie, validates it against the sessions table,
// and returns 401 Unauthorized if the session is missing or invalid.
// Public routes (e.g. POST /api/login) must be registered outside this middleware.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("appx_session")
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if !a.Store.ValidSession(cookie.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie writes a secure, HttpOnly "appx_session" cookie to the
// response. Called after successful login to establish the user's session.
// SameSite=Strict prevents cross-site request forgery. No Domain attribute
// is set — the cookie is scoped to the exact hostname of the server.
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "appx_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   true,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}
```

- [ ] **Step 2: Remove createAgentToken from client.ts**

Delete the `createAgentToken` export from `web/src/api/client.ts`. It's the function at the bottom that calls `/projects/${projectId}/agent-token`.

- [ ] **Step 3: Build and run tests**

```bash
go build ./... && go test ./...
```
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add internal/auth/auth.go web/src/api/client.ts
git commit -m "refactor(auth): remove subdomain token machinery, revert cookie to SameSite=Strict"
```

---

## Task 6: Remove dead code from proxy.go

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

The subdomain routing infrastructure in proxy.go is no longer needed: `ContainerHandler`, `extractProjectName`, and `ProjectNameKey`.

- [ ] **Step 1: Delete ContainerHandler, extractProjectName, ProjectNameKey from proxy.go**

Remove these three items from `internal/proxy/proxy.go`:

1. The `ProjectNameKey` type declaration (the `type ProjectNameKey struct{}` line and its doc comment)
2. The `extractProjectName` function and its doc comment
3. The `ContainerHandler` function and its doc comment

- [ ] **Step 2: Remove ContainerHandler tests from proxy_test.go**

Remove these tests from `internal/proxy/proxy_test.go` (they tested the deleted ContainerHandler):
- `TestContainerHandlerProxiesRequest`
- `TestContainerHandlerServesHTML`
- `TestContainerHandlerProjectNotFound`
- `TestContainerHandlerProjectNotRunning`

- [ ] **Step 3: Build and run tests**

```bash
go build ./... && go test ./...
```
Expected: clean compile, all pass

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "refactor(proxy): remove ContainerHandler and subdomain extraction (replaced by AgentUIHandler+AgentAPIHandler)"
```

---

## Task 7: Frontend changes

**Files:**
- Modify: `web/src/components/ProjectCard.tsx`
- Modify: `web/src/pages/Project.tsx`

- [ ] **Step 1: Restore simple Open button in ProjectCard.tsx**

In `web/src/components/ProjectCard.tsx`, change the Open button `onClick` from the async token-fetch version back to simple navigation:

```tsx
onClick={() => { window.location.href = `/agent/${project.name}/`; }}
```

Also remove the `createAgentToken` import from the import statement at the top.

- [ ] **Step 2: Restore iframe in Project.tsx**

In `web/src/pages/Project.tsx`, find the agent tab content (around line 373). Replace the current "Open Agent UI" link panel with the iframe:

```tsx
if (activeTab === 'agent') {
  return (
    <iframe
      src={`/agent/${project.name}/`}
      style={styles.iframe}
      title="Agent Interface"
    />
  );
}
```

- [ ] **Step 3: Build frontend**

```bash
task web
```
Expected: clean build

- [ ] **Step 4: Build everything and run all tests**

```bash
task build && go test ./...
```
Expected: clean build, all tests pass

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ProjectCard.tsx web/src/pages/Project.tsx
git commit -m "feat(frontend): restore iframe for agent tab, simple Open navigation"
```

---

## Task 8: Manual end-to-end verification

- [ ] **Step 1: Delete old cert and restart**

```bash
rm -f data/cert.pem data/key.pem
./appx -port 8443
```

Accept the self-signed cert for `localhost:8443` in Chrome (once, covers all `/agent/` paths — no subdomains anymore).

- [ ] **Step 2: Verify basic flow**

1. Login at `https://localhost:8443`
2. Create a project, start it
3. Click "Open" → navigates to `https://localhost:8443/agent/:name/`
4. Brief "starting…" overlay appears (~100–300ms on first visit)
5. OpenCode UI loads

- [ ] **Step 3: Verify Service Worker in DevTools**

Open DevTools → Application → Service Workers:
- Service worker for `https://localhost:8443/agent/:name/` should be listed as "activated and running"

- [ ] **Step 4: Verify API calls in Network tab**

In DevTools → Network, make the OpenCode SPA perform an action:
- API calls should show as `/api/agent/:name/session`, `/api/agent/:name/global/event` etc.
- NOT as `/session` or `localhost:8443/session` directly

- [ ] **Step 5: Verify second visit is instant**

Reload the page. The "starting…" overlay should not appear — SW is already active.

- [ ] **Step 6: Verify no cert issues for additional projects**

Create a second project, start it, click "Open". It opens at `/agent/second-project/` on the same origin — no new cert approval needed.

- [ ] **Step 7: Verify Project.tsx Agent tab**

Navigate to a project page (`/projects/:id`). The Agent tab should show the OpenCode UI in an iframe, not a link.

- [ ] **Step 8: Verify cookie**

DevTools → Application → Cookies:
- `appx_session` has `SameSite: Strict`, no `Domain` attribute

- [ ] **Step 9: Update docs**

Update `CLAUDE.md` Current State section to reflect the service worker approach. Remove all references to subdomain routing and the token handoff.
