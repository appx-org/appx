# Subdomain Proxy Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace fragile path-prefix proxy routing (`/agent/:name/`) with subdomain-based routing (`<name>.localhost:8443`) so the opencode SPA runs at the origin root — eliminating all HTML/JS content rewriting.

**Architecture:** Each project's opencode UI is served at `<project-name>.localhost:<port>` instead of `localhost:<port>/agent/<name>/`. The Go server dispatches on the `Host` header: requests to `<name>.localhost` are proxied to the container; requests to `localhost` serve the appx dashboard. Self-signed TLS certs include `*.localhost` as a SAN. The session cookie is set with `Domain=localhost` so it's shared across all subdomains. All HTML/JS rewriting code, the asset cache, and the `AgentUIHandler` are deleted.

**Tech Stack:** Go 1.26 stdlib `net/http` (Host-based routing), self-signed TLS with wildcard SAN, React frontend

---

## File Map

**Create:**
- (none — all changes are modifications or deletions)

**Modify:**
- `internal/tls/selfsigned.go` — Add `*.localhost` to SANs
- `internal/auth/auth.go` — Set `Domain: "localhost"` on session cookie
- `internal/server/router.go` — Replace path-prefix proxy routes with subdomain dispatch
- `internal/server/middleware.go` — CSP based on subdomain instead of path prefix
- `internal/server/server.go` — Remove `AssetCache` from Config; pass baseDomain to router
- `internal/proxy/proxy.go` — Simplify to transparent reverse proxy (no rewriting)
- `cmd/appx/main.go` — Remove AssetCache creation and start hook wiring
- `web/src/components/ProjectCard.tsx` — "Open" navigates to subdomain URL
- `web/src/api/client.ts` — Derive API base URL from current hostname

**Delete:**
- `internal/proxy/assets.go` — AssetCache no longer needed
- `internal/proxy/assets_test.go` — Tests for deleted cache

**Test files to modify:**
- `internal/tls/selfsigned_test.go` — Test wildcard SAN
- `internal/server/router_test.go` — Test subdomain dispatch
- `internal/proxy/proxy_test.go` — Test simplified proxy (no rewriting)

---

## Important Context

### How `*.localhost` works
Modern browsers (Chrome 112+, Firefox 84+, Safari 15+) resolve `*.localhost` to `127.0.0.1` per [RFC 6761](https://tools.ietf.org/html/rfc6761). No DNS setup needed. The Go TLS cert needs `*.localhost` as a DNS SAN — self-signed X.509 certs support wildcard SANs natively.

### Session cookie sharing
Setting `Domain: "localhost"` on the cookie makes it available to `test3.localhost`, `my-app.localhost`, etc. Without this, each subdomain would require a separate login. `SameSite: Lax` (changed from `Strict`) is needed because navigating from `localhost` to `test3.localhost` is a cross-site navigation — `Strict` would not send the cookie on that first navigation.

### What gets deleted
The entire content-rewriting pipeline: `rewriteHTML`, `rewriteJS`, `agentLoadingPage`, `requestHostURL`, `AssetCache`, `serveAgentHTML`, `serveAgentAsset`, `htmlFetchClient`, the `AgentUIHandler`. The `AgentAPIHandler` becomes unnecessary too — the subdomain proxy handles everything (HTML, assets, API, WebSocket) in one handler.

### Port in subdomain URLs
The "Open" button must include the port in the URL: `https://test3.localhost:8443/`. The frontend needs to know the current port. It can read it from `window.location.port`.

---

### Task 1: Add wildcard `*.localhost` to self-signed TLS certificate

**Files:**
- Modify: `internal/tls/selfsigned.go:98-113`
- Modify: `internal/tls/selfsigned_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/tls/selfsigned_test.go`:

```go
func TestCollectSANs_IncludesWildcardLocalhost(t *testing.T) {
	_, dnsNames := collectSANs(nil)
	found := false
	for _, name := range dnsNames {
		if name == "*.localhost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected *.localhost in DNS SANs, got %v", dnsNames)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tls/ -run TestCollectSANs_IncludesWildcardLocalhost -v`
Expected: FAIL — `*.localhost` not in SANs

- [ ] **Step 3: Add `*.localhost` to default SANs**

In `internal/tls/selfsigned.go`, modify the `dnsSet` initialization in `collectSANs`:

```go
dnsSet := map[string]bool{
	"localhost":   true,
	"*.localhost": true,
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tls/ -run TestCollectSANs_IncludesWildcardLocalhost -v`
Expected: PASS

- [ ] **Step 5: Delete existing cert so it regenerates on next startup**

Note for manual verification: `rm data/cert.pem data/key.pem` (if they exist). The cert auto-regenerates on next server start.

- [ ] **Step 6: Commit**

```bash
git add internal/tls/selfsigned.go internal/tls/selfsigned_test.go
git commit -m "tls: add *.localhost wildcard SAN for subdomain routing"
```

---

### Task 2: Update session cookie for cross-subdomain sharing

**Files:**
- Modify: `internal/auth/auth.go:39-51`
- Modify: `internal/auth/store_test.go` (or `internal/server/router_test.go` for integration)

- [ ] **Step 1: Write the failing test**

Add to `internal/server/router_test.go` (where `setupTest` exists for integration tests):

```go
func TestLoginSetsCookieDomainLocalhost(t *testing.T) {
	h, _, _ := setupTest(t)

	body := `{"password":"admin"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:8443"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("login failed: %d", rr.Code)
	}

	cookies := rr.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == "appx_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no appx_session cookie set")
	}
	if session.Domain != "localhost" {
		t.Errorf("expected cookie Domain 'localhost', got %q", session.Domain)
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite Lax, got %v", session.SameSite)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestLoginSetsCookieDomainLocalhost -v`
Expected: FAIL — Domain is empty, SameSite is Strict

- [ ] **Step 3: Update cookie attributes**

In `internal/auth/auth.go`, modify `SetSessionCookie`:

```go
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "appx_session",
		Value:    token,
		Path:     "/",
		Domain:   "localhost",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}
```

**Why Lax instead of Strict:** When the user clicks "Open" on the dashboard (`localhost:8443`), the browser navigates to `test3.localhost:8443`. This is a cross-site navigation. `SameSite=Strict` would not send the cookie, forcing a re-login. `Lax` sends cookies on top-level navigations (clicks), which is the exact use case.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestLoginSetsCookieDomainLocalhost -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/auth.go internal/server/router_test.go
git commit -m "auth: set cookie Domain=localhost and SameSite=Lax for subdomain sharing"
```

---

### Task 3: Add subdomain-aware routing to the server

This is the core change. The router dispatches on the `Host` header: if the hostname has a subdomain (e.g., `test3.localhost`), all requests are proxied to the container. Otherwise, the existing appx dashboard routes are used.

**Files:**
- Modify: `internal/server/router.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/server.go` (remove AssetCache from Config, add BaseDomain)

- [ ] **Step 1: Add `BaseDomain` to server Config and remove `AssetCache`**

In `internal/server/server.go`, update the Config struct:

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
	BaseDomain      string // e.g. "localhost" — subdomains route to containers
}
```

- [ ] **Step 2: Update `NewRouter` signature and add subdomain dispatch**

Replace `internal/server/router.go` with:

```go
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/proxy"
	"github.com/neuromaxer/appx/internal/terminal"
)

// NewRouter builds the top-level HTTP handler with all routes registered.
// Requests to <project>.baseDomain are proxied to the project's container.
// Requests to baseDomain (no subdomain) serve the appx dashboard and API.
func NewRouter(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS, baseDomain string) http.Handler {
	dashboard := dashboardHandler(a, pm, tm, webFS)
	containerProxy := a.Middleware(proxy.ContainerHandler(pm))

	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectName := extractSubdomain(r.Host, baseDomain)
		if projectName != "" {
			containerProxy.ServeHTTP(w, r)
			return
		}
		dashboard.ServeHTTP(w, r)
	}))
}

// extractSubdomain returns the project name from a subdomain request.
// For "test3.localhost:8443" with baseDomain "localhost", returns "test3".
// For "localhost:8443", returns "" (no subdomain — dashboard request).
func extractSubdomain(host, baseDomain string) string {
	// Strip port
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	// Check if host is a subdomain of baseDomain
	if h == baseDomain {
		return ""
	}
	suffix := "." + baseDomain
	if strings.HasSuffix(h, suffix) {
		sub := strings.TrimSuffix(h, suffix)
		if sub != "" && !strings.Contains(sub, ".") {
			return sub
		}
	}
	return ""
}

// dashboardHandler builds the appx dashboard mux with all API routes,
// terminal WebSocket, and the React SPA.
func dashboardHandler(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS) http.Handler {
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

	// User app proxy — /apps/:name/* routes to the container's user-app port
	mux.Handle("/apps/", a.Middleware(proxy.ProxyHandler(pm)))

	// Serve React frontend for all other routes
	fileServer := http.FileServerFS(webFS)
	mux.Handle("/", spaHandler(fileServer, webFS))

	return mux
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

// writeJSON encodes v as JSON and writes it to the response with the
// appropriate Content-Type header.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 3: Update `securityHeaders` for subdomain routing**

In `internal/server/middleware.go`, replace the path-prefix CSP logic with subdomain-aware CSP. When requests come to a project subdomain, they're proxied container content and need the permissive CSP:

```go
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Subdomain requests serve proxied container content (opencode SPA)
		// which uses inline scripts, data: fonts, blob: URLs, and WebSocket.
		// Dashboard requests (no subdomain) use the strict default CSP.
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		if host != "localhost" && strings.HasSuffix(host, ".localhost") {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self' 'unsafe-inline'; "+
					"script-src 'self' 'unsafe-inline'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"font-src 'self' data:; "+
					"img-src 'self' data: blob:; "+
					"connect-src 'self' wss: ws:; "+
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

- [ ] **Step 4: Update server.go to pass baseDomain instead of AssetCache**

In `Run()`, change the `NewRouter` call:

```go
handler := NewRouter(a, cfg.ProjectManager, cfg.TerminalManager, cfg.WebFS, cfg.BaseDomain)
```

- [ ] **Step 5: Commit**

```bash
git add internal/server/router.go internal/server/middleware.go internal/server/server.go
git commit -m "server: subdomain-based routing dispatch replacing path-prefix proxy"
```

---

### Task 4: Simplify proxy to transparent reverse proxy (no rewriting)

Replace the entire content-rewriting proxy with a single `ContainerHandler` that transparently proxies all requests to the container's opencode serve.

**Files:**
- Rewrite: `internal/proxy/proxy.go`
- Delete: `internal/proxy/assets.go`
- Delete: `internal/proxy/assets_test.go`
- Modify: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write the test for the new `ContainerHandler`**

Replace `internal/proxy/proxy_test.go` tests for agent handling with:

```go
func TestContainerHandlerProxiesRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Errorf("expected /session, got %s", r.URL.Path)
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("Cookie header should be stripped")
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("Authorization header should be set")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer backend.Close()

	resolver := &fakeResolver{
		proj: &project.Project{
			ID:              "p1",
			Name:            "my-app",
			Status:          project.StatusRunning,
			Port:            3000,
			ContainerSecret: "testsecret",
		},
		addr: backend.Listener.Addr().String(),
	}

	h := ContainerHandler(resolver)
	req := httptest.NewRequest("GET", "/session", nil)
	req.Host = "my-app.localhost:8443"
	req.Header.Set("Cookie", "appx_session=secret-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rr.Code)
	}
}

func TestContainerHandlerServesHTML(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>Hello</body></html>`))
	}))
	defer backend.Close()

	resolver := &fakeResolver{
		proj: &project.Project{ID: "p1", Name: "ui-app", Status: project.StatusRunning},
		addr: backend.Listener.Addr().String(),
	}

	h := ContainerHandler(resolver)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ui-app.localhost:8443"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Hello") {
		t.Error("expected HTML body to be proxied through")
	}
}
```

- [ ] **Step 2: Rewrite `internal/proxy/proxy.go`**

Strip out all rewriting code. The new proxy.go is much simpler:

```go
package proxy

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/project"
)

// AgentPort is the fixed port where opencode serve listens inside every container.
const AgentPort = 4096

var proxyTransport = &http.Transport{
	ResponseHeaderTimeout: 30 * time.Second,
	MaxIdleConns:          50,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
}

// containerResolver is the subset of project.Manager methods used by proxy handlers.
type containerResolver interface {
	GetByName(name string) (*project.Project, error)
	ContainerAddr(id string, containerPort int) (string, error)
}

// agentBasicAuth returns the base64-encoded Basic Auth value for opencode serve
// proxy requests.
func agentBasicAuth(secret string) string {
	if secret == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+secret))
}

// ContainerHandler returns an http.Handler that transparently proxies all
// requests to the project's opencode serve instance. The project name is
// extracted from the request context (set by the router's subdomain dispatch).
// This replaces the old AgentUIHandler + AgentAPIHandler + rewriting pipeline.
func ContainerHandler(pm containerResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract project name from subdomain (e.g. "test3" from "test3.localhost:8443")
		name := extractProjectName(r.Host)
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

		addr, err := pm.ContainerAddr(proj.ID, AgentPort)
		if err != nil {
			log.Printf("proxy: container %s unreachable: %v", name, err)
			http.Error(w, "container not reachable", http.StatusBadGateway)
			return
		}

		log.Printf("proxy: %s %s %s → http://%s%s", name, r.Method, r.URL.Path, addr, r.URL.Path)

		// Strip session cookie — must not reach the container.
		r.Header.Del("Cookie")

		// Set Basic Auth header if the project has a ContainerSecret.
		if auth := agentBasicAuth(proj.ContainerSecret); auth != "" {
			r.Header.Set("Authorization", auth)
		}

		// WebSocket upgrade requests use a raw TCP tunnel.
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			proxyWebSocket(w, r, addr, r.URL.Path)
			return
		}

		target, _ := url.Parse(fmt.Sprintf("http://%s%s", addr, r.URL.Path))
		target.RawQuery = r.URL.RawQuery
		rp := newReverseProxy(target)
		r2 := r.Clone(r.Context())
		r2.URL = target
		r2.Host = target.Host
		rp.ServeHTTP(w, r2)
	})
}

// extractProjectName returns the project name from a subdomain host header.
// "test3.localhost:8443" returns "test3". "localhost:8443" returns "".
func extractProjectName(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	if !strings.HasSuffix(h, ".localhost") {
		return ""
	}
	return strings.TrimSuffix(h, ".localhost")
}

// newReverseProxy returns a configured httputil.ReverseProxy for container requests.
// FlushInterval -1 enables SSE streaming. CORS headers from the container are
// stripped since the browser sees a same-origin response.
func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.Header.Del("Cookie")
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Access-Control-Allow-Origin")
			resp.Header.Del("Access-Control-Allow-Methods")
			resp.Header.Del("Access-Control-Allow-Headers")
			resp.Header.Del("Access-Control-Allow-Credentials")
			resp.Header.Del("Vary")
			return nil
		},
		FlushInterval: -1,
		Transport:     proxyTransport,
	}
}

// ProxyHandler routes /apps/:name/* to the container's user-app port.
// (Unchanged from current implementation — still uses path-prefix routing
// on the dashboard domain for user-facing app proxying.)
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

		addr, err := pm.ContainerAddr(proj.ID, proj.Port)
		if err != nil {
			log.Printf("proxy: container %s (%s) unreachable: %v", name, proj.ID, err)
			http.Error(w, "container not reachable", http.StatusBadGateway)
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

// parsePath splits a URL path into the project name and the rest of the path.
func parsePath(path, prefix string) (name, rest string) {
	trimmed := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], trimmed[i:]
	}
	return trimmed, "/"
}
```

- [ ] **Step 3: Delete old files**

```bash
rm internal/proxy/assets.go internal/proxy/assets_test.go
```

- [ ] **Step 4: Update proxy_test.go fakeResolver**

Keep the `fakeResolver` and existing `ProxyHandler` tests. Remove all `AgentUIHandler`, `AgentAPIHandler`, `rewriteHTML`, `rewriteJS` tests. Add the `ContainerHandler` tests from Step 1.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/proxy/ -v`
Expected: All new and remaining tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/
git commit -m "proxy: replace content-rewriting pipeline with transparent subdomain proxy"
```

---

### Task 5: Wire up main.go and update server startup

**Files:**
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Remove AssetCache and add BaseDomain**

In `cmd/appx/main.go`:
1. Remove the `assetCache := proxy.NewAssetCache()` line
2. Remove `pm.SetStartHook(assetCache.Clear)`
3. Add `BaseDomain: "localhost"` to the `server.Config` struct
4. Remove `AssetCache: assetCache` from the config

The project manager's `SetStartHook` can be removed entirely (or set to a no-op if the proxy cache clearing is the only hook user). Check if `startHook` is used for anything else.

- [ ] **Step 2: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: Clean compile, all tests pass.

- [ ] **Step 3: Commit**

```bash
git add cmd/appx/main.go
git commit -m "main: wire subdomain routing, remove asset cache"
```

---

### Task 6: Update frontend "Open" button to use subdomain URL

**Files:**
- Modify: `web/src/components/ProjectCard.tsx:116`

- [ ] **Step 1: Update the Open button onClick handler**

Change line 116 in `web/src/components/ProjectCard.tsx` from:

```tsx
onClick={() => { window.location.href = `/agent/${project.name}/`; }}
```

To:

```tsx
onClick={() => {
  const port = window.location.port ? `:${window.location.port}` : '';
  window.location.href = `${window.location.protocol}//${project.name}.localhost${port}/`;
}}
```

- [ ] **Step 2: Build frontend**

Run: `task web`
Expected: Clean build

- [ ] **Step 3: Commit**

```bash
git add web/src/components/ProjectCard.tsx
git commit -m "ui: Open button navigates to project subdomain URL"
```

---

### Task 7: Update Dockerfile and clean up dead code

**Files:**
- Modify: `internal/project/Dockerfile.project` — revert `--cors` flag addition (no longer needed)
- Modify: `internal/project/container.go` — remove `startHook` field and `SetStartHook` if only used for asset cache

- [ ] **Step 1: Revert Dockerfile cors change**

In `internal/project/Dockerfile.project`, change:

```dockerfile
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 0.0.0.0 --cors https://localhost https://localhost:8443 & sleep infinity"]
```

Back to:

```dockerfile
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 0.0.0.0 & sleep infinity"]
```

- [ ] **Step 2: Remove `startHook` from project Manager if unused**

Check if `SetStartHook` / `startHook` is used by anything other than the deleted asset cache. If not, remove the `startHook` field from `Manager`, the `SetStartHook` method, and the `startHook` call sites in `doFullCreate` and `tryReuseContainer`.

- [ ] **Step 3: Build and test everything**

Run: `task build && task test`
Expected: Full clean build and all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/project/Dockerfile.project internal/project/container.go
git commit -m "cleanup: remove cors flag and unused startHook"
```

---

### Task 8: Full end-to-end verification

- [ ] **Step 1: Delete existing certs and data**

```bash
rm -f data/cert.pem data/key.pem
```

- [ ] **Step 2: Start the server**

```bash
./appx -port 8443
```

Verify in logs:
- New cert generated with `*.localhost` SAN
- Server starts on https://localhost:8443

- [ ] **Step 3: Login and create a project**

1. Navigate to `https://localhost:8443`
2. Login with password
3. Create project "test-app"
4. Start it

- [ ] **Step 4: Click Open and verify opencode loads**

1. Click "Open" on the project card
2. Browser should navigate to `https://test-app.localhost:8443/`
3. The opencode UI should load fully — no JS rewriting, no content modification
4. Verify in browser DevTools:
   - No module script MIME type errors
   - No 401 errors (except possibly webmanifest — cosmetic)
   - API calls go to `https://test-app.localhost:8443/session` etc. (same origin)
   - SSE events stream correctly

- [ ] **Step 5: Verify session cookie works across subdomains**

1. After login on `localhost:8443`, navigate to `test-app.localhost:8443`
2. Should NOT be prompted to login again
3. Check DevTools → Application → Cookies: `appx_session` should have `Domain: localhost`

- [ ] **Step 6: Update docs**

Update `CLAUDE.md` and architecture docs to reflect the subdomain routing change:
- Remove references to `/agent/:name/`, `AgentUIHandler`, `AgentAPIHandler`, HTML/JS rewriting
- Document the subdomain routing model
- Update the route table in the Architecture section

---

## What This Deletes

For reference, the following code is **entirely removed** by this refactor:

| Code | Purpose | Why deleted |
|------|---------|-------------|
| `rewriteHTML()` | Prefix HTML paths with `/agent/:name/` | Subdomain = origin root, paths work natively |
| `rewriteJS()` | Patch Vite preloader and API base URL | No longer needed — same origin |
| `AgentUIHandler()` | Serve cached+rewritten opencode HTML/assets | `ContainerHandler` proxies everything transparently |
| `AgentAPIHandler()` | Proxy `/api/agent/:name/*` to container API | `ContainerHandler` handles all paths |
| `serveAgentHTML()` | Fetch+rewrite+cache HTML from container | Deleted |
| `serveAgentAsset()` | Fetch+rewrite+cache assets from container | Deleted |
| `agentLoadingPage()` | Auto-refresh loading screen | Container serves its own loading state |
| `requestHostURL()` | Derive scheme://host for injection | No injection needed |
| `htmlFetchClient` | Short-timeout client for HTML fetching | Deleted |
| `AssetCache` | In-memory cache for rewritten content | No rewriting = no cache needed |
| `assets.go` / `assets_test.go` | AssetCache implementation | Deleted entirely |
