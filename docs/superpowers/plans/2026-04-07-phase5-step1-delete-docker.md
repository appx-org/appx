# Phase 5 Step 1: Delete Docker Code

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove all Docker/container lifecycle code, SW proxy, and asset cache so appx compiles and tests pass with zero Docker dependencies.

**Architecture:** Delete `internal/proxy/`, container lifecycle in `internal/project/`, Docker imports, and all references. Stub out the project Manager to only do CRUD (no start/stop/reset). Update router, server config, main.go, and tests. The result is a compiling, test-passing codebase that serves the dashboard but cannot start containers.

**Tech Stack:** Go 1.26, SQLite, React (frontend changes minimal — just remove broken buttons)

**Reference:** See `docs/plans/phase_5_plan.md` (Step 1) and `docs/analysis/refactors/de-docker-refactor.md` (Q6) for context.

---

### Task 1: Delete the `internal/proxy/` package

**Files:**
- Delete: `internal/proxy/proxy.go`
- Delete: `internal/proxy/proxy_test.go`
- Delete: `internal/proxy/assets.go`
- Delete: `internal/proxy/assets_test.go`
- Delete: `internal/proxy/ws.go`

- [ ] **Step 1: Delete all files in internal/proxy/**

```bash
rm -rf internal/proxy/
```

- [ ] **Step 2: Verify deletion**

Run: `ls internal/proxy/ 2>&1`
Expected: `ls: internal/proxy/: No such file or directory`

- [ ] **Step 3: Commit**

```bash
git add -A internal/proxy/
git commit -m "refactor: delete internal/proxy/ package (SW proxy, asset cache, WS tunnel)"
```

---

### Task 2: Delete Docker container files

**Files:**
- Delete: `internal/project/container.go`
- Delete: `internal/project/Dockerfile.project`
- Delete: `internal/project/.tmux.conf`
- Delete: `internal/project/fake_docker_test.go`
- Delete: `internal/project/manager_test.go`

- [ ] **Step 1: Delete container lifecycle and Docker test files**

```bash
rm internal/project/container.go
rm internal/project/Dockerfile.project
rm internal/project/.tmux.conf
rm internal/project/fake_docker_test.go
rm internal/project/manager_test.go
```

- [ ] **Step 2: Verify deletion**

Run: `ls internal/project/`
Expected: only `project.go`, `store.go`, `store_test.go` remain

- [ ] **Step 3: Commit**

```bash
git add -A internal/project/
git commit -m "refactor: delete container.go, Dockerfile, tmux config, Docker tests"
```

---

### Task 3: Stub out the project Manager

The old `Manager` struct lived in `container.go` (now deleted). Create a minimal replacement that satisfies the compiler — CRUD only, no Docker.

**Files:**
- Create: `internal/project/manager.go`

- [ ] **Step 1: Write test for the stubbed Manager**

Create `internal/project/manager_test.go`:

```go
package project

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupManagerTest(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Create schema manually for test isolation
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		status TEXT DEFAULT 'stopped',
		internal_port INTEGER,
		container_id TEXT DEFAULT '',
		network_id TEXT DEFAULT '',
		image_name TEXT DEFAULT '',
		last_error TEXT DEFAULT '',
		resources TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		container_secret TEXT DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	mgr := NewManager(store)
	return mgr, db
}

func TestNewManager(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.Store == nil {
		t.Fatal("expected non-nil store")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/project/ -run TestNewManager -v`
Expected: FAIL — `NewManager` not defined

- [ ] **Step 3: Write the stubbed Manager**

Create `internal/project/manager.go`:

```go
package project

// Manager provides project lifecycle operations. In the current architecture
// (post Phase 5 de-Docker), it delegates to the Store for CRUD. Container
// lifecycle methods (Start, Stop, Reset) are removed — OpenCode runs as a
// separate systemd service and manages agent sessions natively.
type Manager struct {
	Store *Store
}

// NewManager creates a Manager backed by the given project store.
func NewManager(store *Store) *Manager {
	return &Manager{Store: store}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/project/ -run TestNewManager -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/project/manager.go internal/project/manager_test.go
git commit -m "refactor: add stubbed project Manager (CRUD only, no Docker)"
```

---

### Task 4: Update `cmd/appx/main.go` — remove Docker

**Files:**
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Remove Docker import, client init, findDockerHost, and all Docker-dependent wiring**

Replace the entire file with:

```go
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/db"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/server"
	"github.com/neuromaxer/appx/internal/terminal"
)

// webEmbed holds the built React frontend assets, embedded at compile time.
// The server serves these as the SPA for all non-API routes.
//
//go:embed web/dist/*
var webEmbed embed.FS

// main is the entry point for the appx server. It parses CLI flags, initializes
// the SQLite database and auth store, sets up the project manager, generates a
// password on first run, and starts the HTTPS server.
func main() {
	port := flag.Int("port", 443, "HTTPS port")
	dataDir := flag.String("data", "./data", "data directory for DB and TLS certs")
	host := flag.String("host", "", "additional hostname or IP for TLS cert SANs")
	domain := flag.String("domain", "", "domain for automatic Let's Encrypt TLS via Cloudflare DNS (requires CLOUDFLARE_API_TOKEN env var)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	database, err := db.Open(*dataDir)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	authStore := auth.NewStore(database)

	// If no password set, generate one and print it.
	set, err := authStore.IsPasswordSet()
	if err != nil {
		log.Fatalf("check password: %v", err)
	}
	if !set {
		pw, err := authStore.GeneratePassword()
		if err != nil {
			log.Fatalf("generate password: %v", err)
		}
		if err := authStore.SetPassword(pw); err != nil {
			log.Fatalf("set password: %v", err)
		}
		pwFile := filepath.Join(*dataDir, "initial_password")
		if err := os.WriteFile(pwFile, []byte(pw+"\n"), 0600); err != nil {
			log.Fatalf("write password file: %v", err)
		}
		fmt.Fprintf(os.Stderr, "Initial password: %s\n", pw)
		fmt.Fprintf(os.Stderr, "Also written to %s — delete this file after logging in.\n", pwFile)
	}

	projectStore := project.NewStore(database)
	pm := project.NewManager(projectStore)

	// Read terminal buffer size from settings (default 512KB).
	bufSizeKB := 512
	if val, err := authStore.GetSetting("terminal_buffer_size"); err == nil && val != "" {
		if n, err := strconv.Atoi(val); err == nil && n >= 64 && n <= 4096 {
			bufSizeKB = n
		}
	}
	_ = bufSizeKB // TODO: terminal will be rewired to OpenCode PTY in a later step

	webFS, err := fs.Sub(webEmbed, "web/dist")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}

	var hosts []string
	if *host != "" {
		hosts = []string{*host}
	}

	if err := server.Run(server.Config{
		Port:           *port,
		DataDir:        *dataDir,
		DB:             database,
		AuthStore:      authStore,
		ProjectManager: pm,
		WebFS:          webFS,
		TLSHosts:       hosts,
		Domain:         *domain,
		CloudflareToken: os.Getenv("CLOUDFLARE_API_TOKEN"),
	}); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 2: Verify it doesn't compile yet (router and server still reference old types)**

Run: `go build ./cmd/appx/ 2>&1 | head -20`
Expected: compile errors in `server` package referencing `terminal.Manager`, `proxy.AssetCache`, etc.

- [ ] **Step 3: Commit**

```bash
git add cmd/appx/main.go
git commit -m "refactor: remove Docker client, findDockerHost, container wiring from main.go"
```

---

### Task 5: Update `internal/server/` — remove proxy/terminal/asset references

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/router.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/project_handlers.go`

- [ ] **Step 1: Update `server.go` Config struct — remove TerminalManager and AssetCache**

In `internal/server/server.go`, replace the `Config` struct and `Run` function signature. Remove the `terminal` and `proxy` imports. Remove `tm` from `serve()` calls and the shutdown cleanup.

Replace the full file content:

```go
package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/project"
	appxtls "github.com/neuromaxer/appx/internal/tls"
)

// Config holds all dependencies needed to start the HTTPS server.
// It is constructed in main() and passed to Run().
type Config struct {
	Port            int
	DataDir         string
	DB              *sql.DB
	AuthStore       *auth.Store
	ProjectManager  *project.Manager
	WebFS           fs.FS
	TLSHosts        []string
	Domain          string
	CloudflareToken string
}

// Run starts the HTTPS server and blocks until it receives SIGINT/SIGTERM or
// encounters a fatal error. When Domain and CloudflareToken are set, it uses
// CertMagic for automatic Let's Encrypt certificates; otherwise it falls back
// to a self-signed certificate.
func Run(cfg Config) error {
	a := auth.New(cfg.AuthStore)
	a.Store.CleanExpiredSessions()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()
	go func() {
		for range cleanupTicker.C {
			a.Store.CleanExpiredSessions()
		}
	}()

	handler := NewRouter(a, cfg.ProjectManager, cfg.WebFS)

	if cfg.Domain != "" {
		if cfg.CloudflareToken == "" {
			return fmt.Errorf("--domain requires CLOUDFLARE_API_TOKEN to be set")
		}
		return runWithCertMagic(cfg, handler)
	}
	return runWithSelfSigned(cfg, handler)
}

func runWithCertMagic(cfg Config, handler http.Handler) error {
	storage := &certmagic.FileStorage{
		Path: filepath.Join(cfg.DataDir, "certmagic"),
	}

	magic := certmagic.NewDefault()
	magic.Storage = storage

	issuer := certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
		Agreed: true,
		Email:  "admin@" + cfg.Domain,
		DNS01Solver: &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{
				DNSProvider: &cloudflare.Provider{
					APIToken: cfg.CloudflareToken,
				},
			},
		},
	})
	magic.Issuers = []certmagic.Issuer{issuer}

	domains := []string{cfg.Domain, "*." + cfg.Domain}
	if err := magic.ManageSync(context.Background(), domains); err != nil {
		return fmt.Errorf("certmagic: %w", err)
	}

	tlsConfig := magic.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS12

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return serve(srv, cfg.Port, true)
}

func runWithSelfSigned(cfg Config, handler http.Handler) error {
	cert, err := appxtls.LoadOrGenerateSelfSigned(cfg.DataDir, cfg.TLSHosts...)
	if err != nil {
		return fmt.Errorf("tls setup: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return serve(srv, cfg.Port, false)
}

func serve(srv *http.Server, port int, autoTLS bool) error {
	errCh := make(chan error, 1)
	go func() {
		if autoTLS {
			log.Printf("Appx running with automatic TLS on https://localhost:%d", port)
		} else {
			log.Printf("Appx running on https://localhost:%d", port)
			log.Printf("To connect from another machine: https://<your-server-ip>:%d", port)
		}
		errCh <- srv.ListenAndServeTLS("", "")
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("Received %s, shutting down...", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Println("Server stopped")
	return nil
}
```

- [ ] **Step 2: Update `router.go` — remove proxy/terminal routes, simplify signature**

Replace the full file content of `internal/server/router.go`:

```go
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/project"
)

// NewRouter builds the top-level HTTP handler. All requests go through auth
// middleware (except POST /api/login which is public and rate-limited).
func NewRouter(a *auth.Auth, pm *project.Manager, webFS fs.FS) http.Handler {
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
	api.HandleFunc("GET /api/settings/api-key", handleGetAPIKeyStatus(a.Store, pm))
	api.HandleFunc("PUT /api/settings/api-key", handleSetAPIKey(a.Store, pm))
	api.HandleFunc("DELETE /api/settings/api-key", handleDeleteAPIKey(a.Store, pm))
	api.HandleFunc("GET /api/settings/terminal-buffer-size", handleGetTerminalBufferSize(a.Store))
	api.HandleFunc("PUT /api/settings/terminal-buffer-size", handleSetTerminalBufferSize(a.Store))
	api.HandleFunc("DELETE /api/session", handleLogout(a))
	mux.Handle("/api/", limitBody(a.Middleware(requireJSON(api))))

	// React SPA fallback
	fileServer := http.FileServerFS(webFS)
	mux.Handle("/", spaHandler(fileServer, webFS))

	return securityHeaders(mux)
}

// spaHandler wraps a file server to support single-page application routing.
// If the requested path matches a real file in webFS it is served directly;
// otherwise the request is rewritten to "/" so the React app handles client-side
// routing.
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
// appropriate Content-Type header. Used by all API handlers to send responses.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 3: Update `middleware.go` — remove agent CSP conditional**

In `internal/server/middleware.go`, replace the `securityHeaders` function body. Remove the `/agent/` and `/api/agent/` conditional — apply strict CSP everywhere:

```go
// securityHeaders wraps an HTTP handler to inject standard security headers on
// every response. A strict CSP is applied to all routes.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"connect-src 'self'")

		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Update `project_handlers.go` — remove start/stop/reset/terminal handlers**

In `internal/server/project_handlers.go`, delete the following handler functions entirely:
- `handleStartProject`
- `handleStopProject`
- `handleResetProject`
- `handleCreateSession` (terminal session — will be rewired to OpenCode PTY later)
- `handleListSessions` (terminal session)
- `handleDeleteSession` (terminal session)

Also update any handler that references `pm.Start`, `pm.Stop`, `pm.Reset`, `pm.Delete` (the container-aware versions). The `handleDeleteProject` should call `pm.Store.Delete(id)` directly.

Update handler function signatures that take `*terminal.Manager` — remove the `tm` parameter. Update `handleGetAPIKeyStatus`, `handleSetAPIKey`, `handleDeleteAPIKey` if they reference `pm.UpdateAnthropicKey` — stub or remove that call.

Review each remaining handler and ensure it only calls methods that exist on the new `Manager` struct (which only has a `Store` field).

- [ ] **Step 5: Verify compilation**

Run: `go build ./cmd/appx/ 2>&1`
Expected: may still have errors from handler references — fix any remaining references.

- [ ] **Step 6: Commit**

```bash
git add internal/server/
git commit -m "refactor: remove proxy/terminal/Docker references from server package"
```

---

### Task 6: Remove Docker dependencies from `go.mod`

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Remove Docker imports and tidy**

```bash
go mod tidy
```

This will automatically remove `github.com/moby/moby/client`, `github.com/moby/moby/api`, and all transitive Docker dependencies since nothing imports them anymore.

- [ ] **Step 2: Verify no Docker references remain in go.sum**

Run: `grep -c moby go.mod`
Expected: `0`

Run: `grep -c docker go.mod`
Expected: `0`

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "refactor: remove Docker SDK dependencies from go.mod"
```

---

### Task 7: Fix tests — update `router_test.go`

**Files:**
- Modify: `internal/server/router_test.go`

- [ ] **Step 1: Update `setupTest` helper**

The `setupTest` function in `router_test.go` creates a `project.NewManager(ps, nil, "test-key", "")` with Docker args. Update it to use the new signature `project.NewManager(ps)`. Remove `terminal.NewManager` creation and the `fakeExecer`. Remove `proxy.NewAssetCache()`. Update the `NewRouter` call to match the new signature `NewRouter(a, pm, webFS)`.

Find the `setupTest` function and update it. Also remove any test that references deleted routes (`/agent/`, `/api/agent/`, `/apps/`) or deleted handlers (start, stop, reset, terminal sessions).

Specifically delete these tests:
- `TestStartProject_*` tests
- `TestStopProject_*` tests
- `TestResetProject_*` tests
- `TestAgentRouteHasPermissiveCSP`
- `TestCreateSession_*`, `TestListSessions_*`, `TestDeleteSession_*` tests
- Any test referencing `/agent/` or `/apps/` routes

Keep these tests:
- `TestLogin_*`
- `TestLogout_*`
- `TestCreateProject_*`
- `TestGetProject_*`
- `TestListProjects_*`
- `TestDeleteProject_*`
- `TestUpdateProject_*`
- `TestAPIKey_*`
- `TestDashboardRouteHasStrictCSP` (update to check all routes have strict CSP)
- `TestSPAFallback_*`

- [ ] **Step 2: Run tests**

Run: `go test ./internal/server/ -v 2>&1 | tail -30`
Expected: remaining tests PASS

- [ ] **Step 3: Run full test suite**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/server/router_test.go
git commit -m "test: update router tests for de-Docker architecture"
```

---

### Task 8: Update frontend — remove broken buttons

**Files:**
- Modify: `web/src/components/ProjectCard.tsx`
- Modify: `web/src/pages/Project.tsx`

- [ ] **Step 1: Remove Agent button and Start/Stop/Reset from ProjectCard**

In `web/src/components/ProjectCard.tsx`, remove:
- The "Agent" button that navigates to `/agent/${project.name}/`
- The "Start" and "Stop" buttons (no container lifecycle)
- The "Reset" button

Keep:
- The "Term" button (will be rewired later but keep it pointing to the project page)
- The "Delete" button
- Project name, status display, port display

- [ ] **Step 2: Remove Agent tab and Open App link from Project.tsx**

In `web/src/pages/Project.tsx`, remove:
- The Agent tab and its iframe rendering
- The "Open App" link that points to `/apps/${projectName}/`
- Any references to `/agent/` routes

Keep:
- Terminal tab (will be rewired later)
- Basic project info display

- [ ] **Step 3: Build frontend**

Run: `task web`
Expected: builds cleanly with no errors

- [ ] **Step 4: Build full project**

Run: `task build`
Expected: compiles cleanly — Go binary + embedded frontend

- [ ] **Step 5: Commit**

```bash
git add web/src/
git commit -m "ui: remove Agent button, Start/Stop/Reset, and /agent/ references"
```

---

### Task 9: Final verification

- [ ] **Step 1: Run full test suite**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 2: Run linter**

Run: `task lint`
Expected: no errors

- [ ] **Step 3: Build and verify startup**

Run: `task build && ./appx -port 8443 2>&1 &`
Expected: server starts without Docker errors, no "Docker is required" fatal

- [ ] **Step 4: Verify dashboard loads**

Run: `curl -k https://localhost:8443/ -o /dev/null -w '%{http_code}'`
Expected: `200`

- [ ] **Step 5: Verify no Docker references in binary**

Run: `grep -r "moby\|docker" internal/ cmd/ --include="*.go" -l`
Expected: no files (or only comments/docs)

- [ ] **Step 6: Stop the test server**

```bash
kill %1
```

- [ ] **Step 7: Commit any final fixes**

```bash
git add -A
git commit -m "refactor: Phase 5 Step 1 complete — Docker code removed, clean compile"
```
