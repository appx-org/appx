# Phase 5 Step 2: HTTP Mode + Routing Foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--http` dev mode (plain HTTP locked to localhost), an `/api/opencode/*` reverse proxy route to the local OpenCode server, and subdomain-based routing for agent-built apps. This is the routing foundation that all subsequent steps build on.

**Architecture:** The `--http` flag enables plain HTTP on localhost (no TLS). It is mutually exclusive with `--domain`. The server config gains a `BaseDomain` field (`localhost` in HTTP mode, the `--domain` value in production). Cookie auth switches from `SameSite=Strict` (no Domain) to `SameSite=Lax` with `Domain=.<baseDomain>` for cross-subdomain sharing. A new top-level dispatcher inspects the Host header: base domain requests go to the existing dashboard mux, `<name>.<base>` requests go through auth middleware to a reverse proxy targeting the project's assigned port. The `/api/opencode/*` route is a simple reverse proxy to `localhost:4096` with prefix stripping.

**Tech Stack:** Go 1.26, stdlib `net/http`, `net/http/httputil`, SQLite, React (frontend unchanged)

**Reference:** See `docs/analysis/refactors/de-docker-refactor.md` (Q3, Q4, Q7) for design decisions.

**Prerequisite:** Step 1 (delete Docker code) must be completed first. This plan assumes the codebase state described in `docs/superpowers/plans/2026-04-07-phase5-step1-delete-docker.md` Task 9.

---

### Task 1: Add `--http` flag and `BaseDomain` / `HTTPMode` to server Config

**Files:**
- Modify: `cmd/appx/main.go`
- Modify: `internal/server/server.go`

- [ ] **Step 1: Write test for `--http` and `--domain` mutual exclusivity**

In `internal/server/server_test.go` (new file):

```go
package server

import (
	"testing"
)

// TestHTTPAndDomainMutuallyExclusive verifies that setting both HTTPMode and
// Domain on the Config produces an error from Run().
func TestHTTPAndDomainMutuallyExclusive(t *testing.T) {
	cfg := Config{
		Port:     8080,
		HTTPMode: true,
		Domain:   "example.com",
	}
	err := Run(cfg)
	if err == nil {
		t.Fatal("expected error when both HTTPMode and Domain are set")
	}
	want := "--http and --domain are mutually exclusive"
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/server/ -run TestHTTPAndDomainMutuallyExclusive -v`
Expected: FAIL -- `HTTPMode` field does not exist on Config

- [ ] **Step 3: Add `HTTPMode` and `BaseDomain` to Config, add validation to Run()**

In `internal/server/server.go`, add `HTTPMode bool` and `BaseDomain string` fields to the `Config` struct:

```go
// Config holds all dependencies needed to start the server.
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
	HTTPMode        bool   // true = plain HTTP, locked to localhost
	BaseDomain      string // "localhost" in HTTP mode, Domain value in production
}
```

At the top of `Run()`, before the existing `a := auth.New(cfg.AuthStore)` line, add:

```go
if cfg.HTTPMode && cfg.Domain != "" {
	return fmt.Errorf("--http and --domain are mutually exclusive")
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/server/ -run TestHTTPAndDomainMutuallyExclusive -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat: add HTTPMode and BaseDomain to server Config with mutual exclusivity check"
```

---

### Task 2: Add `runHTTP()` — plain HTTP server for dev mode

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write test for HTTP mode startup path**

Add to `internal/server/server_test.go`:

```go
// TestRunHTTPRequiresMinimalConfig verifies that HTTPMode=true with no Domain
// does not error on the mutual exclusivity check and proceeds past validation.
// We can't easily test the full server lifecycle in a unit test, so we test
// that the validation logic passes and the function attempts to start.
func TestRunHTTPModeValidation(t *testing.T) {
	cfg := Config{
		Port:     0,
		HTTPMode: true,
		// Domain intentionally empty — should pass validation
	}
	// Run will fail because AuthStore is nil, but it should get past
	// the HTTPMode/Domain check without error.
	err := Run(cfg)
	if err != nil && err.Error() == "--http and --domain are mutually exclusive" {
		t.Fatal("should not get mutual exclusivity error when only HTTPMode is set")
	}
}
```

- [ ] **Step 2: Run test, verify it passes (or fails for the right reason)**

Run: `go test ./internal/server/ -run TestRunHTTPModeValidation -v`
Expected: The test should pass (Run panics on nil AuthStore, not on the mutual exclusivity check). If it panics, wrap the Run call in a recover.

Update the test to handle the panic:

```go
func TestRunHTTPModeValidation(t *testing.T) {
	defer func() {
		// Run will panic or error on nil AuthStore — that's fine,
		// we're only testing that it gets past the flag validation.
		recover()
	}()

	cfg := Config{
		Port:     0,
		HTTPMode: true,
	}
	err := Run(cfg)
	if err != nil && err.Error() == "--http and --domain are mutually exclusive" {
		t.Fatal("should not get mutual exclusivity error when only HTTPMode is set")
	}
}
```

- [ ] **Step 3: Add `runHTTP()` function in `server.go`**

Add after the existing `runWithSelfSigned` function:

```go
// runHTTP starts a plain HTTP server on 127.0.0.1 only. Used in --http dev mode
// where TLS is unnecessary (localhost traffic never leaves the machine). Logs a
// warning that this mode is for local development only. Binding to 127.0.0.1
// prevents accidental exposure on public interfaces.
func runHTTP(cfg Config, handler http.Handler) error {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	log.Printf("WARNING: running in HTTP mode -- for local development only")
	log.Printf("Appx running on http://localhost:%d", cfg.Port)

	return serveHTTP(srv, cfg.Port)
}

// serveHTTP starts an HTTP (non-TLS) server and blocks until shutdown signal.
func serveHTTP(srv *http.Server, port int) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
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

- [ ] **Step 4: Wire `runHTTP` into `Run()`**

In the `Run()` function, after the mutual exclusivity check and after building the handler, add the HTTP mode branch. The updated `Run()` should look like:

```go
func Run(cfg Config) error {
	if cfg.HTTPMode && cfg.Domain != "" {
		return fmt.Errorf("--http and --domain are mutually exclusive")
	}

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

	if cfg.HTTPMode {
		return runHTTP(cfg, handler)
	}

	if cfg.Domain != "" {
		if cfg.CloudflareToken == "" {
			return fmt.Errorf("--domain requires CLOUDFLARE_API_TOKEN to be set")
		}
		return runWithCertMagic(cfg, handler)
	}
	return runWithSelfSigned(cfg, handler)
}
```

- [ ] **Step 5: Verify compilation**

Run: `go build ./cmd/appx/`
Expected: compiles cleanly (main.go doesn't set HTTPMode yet, so no changes needed there)

- [ ] **Step 6: Run all tests**

Run: `task test`
Expected: all pass

- [ ] **Step 7: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat: add runHTTP() for plain HTTP dev mode on localhost"
```

---

### Task 3: Wire `--http` flag in `cmd/appx/main.go`

**Files:**
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Add `--http` flag, default port change, and BaseDomain computation**

In `cmd/appx/main.go`, add the `--http` flag alongside the existing flags. When `--http` is set, default the port to 8080 instead of 443. Compute `BaseDomain` from the flags.

Update the flag parsing section:

```go
port := flag.Int("port", 0, "listen port (default: 443 for HTTPS, 8080 for --http)")
dataDir := flag.String("data", "./data", "data directory for DB and TLS certs")
host := flag.String("host", "", "additional hostname or IP for TLS cert SANs")
domain := flag.String("domain", "", "domain for automatic Let's Encrypt TLS via Cloudflare DNS (requires CLOUDFLARE_API_TOKEN env var)")
httpMode := flag.Bool("http", false, "run in plain HTTP mode (localhost only, for local development)")
flag.Parse()

// Default port depends on mode.
if *port == 0 {
	if *httpMode {
		*port = 8080
	} else {
		*port = 443
	}
}
```

Compute `BaseDomain` and pass it through Config:

```go
// Compute base domain for cookie scoping and subdomain routing.
baseDomain := "localhost"
if *domain != "" {
	baseDomain = *domain
}
```

Update the `server.Run` call to include the new fields:

```go
if err := server.Run(server.Config{
	Port:            *port,
	DataDir:         *dataDir,
	DB:              database,
	AuthStore:       authStore,
	ProjectManager:  pm,
	WebFS:           webFS,
	TLSHosts:        hosts,
	Domain:          *domain,
	CloudflareToken: os.Getenv("CLOUDFLARE_API_TOKEN"),
	HTTPMode:        *httpMode,
	BaseDomain:      baseDomain,
}); err != nil {
	log.Fatal(err)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./cmd/appx/`
Expected: compiles cleanly

- [ ] **Step 3: Verify `--help` output shows new flag**

Run: `go run ./cmd/appx/ --help 2>&1 | grep -A1 http`
Expected: shows `-http` flag with description

- [ ] **Step 4: Run all tests**

Run: `task test`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add cmd/appx/main.go
git commit -m "feat: add --http CLI flag with default port 8080 and BaseDomain computation"
```

---

### Task 4: Update `securityHeaders` -- no HSTS in HTTP mode

**Files:**
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/router.go`
- Modify: `internal/server/router_test.go`

The `securityHeaders` function currently always sets HSTS. In HTTP mode, HSTS must be omitted (browsers would reject HTTP connections if HSTS is set). We need to pass the HTTPMode flag through to the middleware.

- [ ] **Step 1: Write test for no HSTS in HTTP mode**

Add to `internal/server/router_test.go`:

```go
func TestSecurityHeaders_NoHSTS_HTTPMode(t *testing.T) {
	handler, _, _ := setupTestWithHTTPMode(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if hsts := w.Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("expected no HSTS header in HTTP mode, got %q", hsts)
	}
	// Other security headers should still be present.
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", got)
	}
}
```

Also add the `setupTestWithHTTPMode` helper:

```go
// setupTestWithHTTPMode creates the same test setup as setupTest but with
// HTTPMode enabled, which affects security headers (no HSTS).
func setupTestWithHTTPMode(t *testing.T) (http.Handler, *auth.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'stopped',
			container_id TEXT,
			internal_port INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			network_id TEXT,
			image_name TEXT,
			last_error TEXT,
			resources TEXT,
			container_secret TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	store := auth.NewStore(db)
	store.SetPassword("testpassword1")
	a := auth.New(store)

	ps := project.NewStore(db)
	pm := project.NewManager(ps)

	webFS := fstest.MapFS{
		"index.html":          {Data: []byte("<html>app</html>")},
		"assets/index-abc.js": {Data: []byte("console.log('hi')")},
	}

	return NewRouter(a, pm, webFS, RouterConfig{HTTPMode: true, BaseDomain: "localhost"}), store, db
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/server/ -run TestSecurityHeaders_NoHSTS_HTTPMode -v`
Expected: FAIL -- `RouterConfig` type does not exist, `NewRouter` signature mismatch

- [ ] **Step 3: Add `RouterConfig` and update `NewRouter` signature**

In `internal/server/router.go`, add a config struct and update the signature:

```go
// RouterConfig holds runtime configuration that affects routing behavior.
// Passed to NewRouter so middleware can adapt to the deployment mode.
type RouterConfig struct {
	HTTPMode   bool   // true = plain HTTP dev mode, affects security headers
	BaseDomain string // base domain for subdomain routing (e.g. "localhost", "user.appx.app")
}

// NewRouter builds the top-level HTTP handler. All requests go through auth
// middleware (except POST /api/login which is public and rate-limited).
func NewRouter(a *auth.Auth, pm *project.Manager, webFS fs.FS, rcfg RouterConfig) http.Handler {
```

Pass `rcfg.HTTPMode` to `securityHeaders`:

```go
return securityHeaders(mux, rcfg.HTTPMode)
```

- [ ] **Step 4: Update `securityHeaders` to accept `httpMode` parameter**

In `internal/server/middleware.go`:

```go
// securityHeaders wraps an HTTP handler to inject standard security headers on
// every response. HSTS is omitted in HTTP mode since it would cause browsers to
// reject the plain HTTP connection.
func securityHeaders(next http.Handler, httpMode bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !httpMode {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
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

- [ ] **Step 5: Update `server.go` to pass `RouterConfig` to `NewRouter`**

In `internal/server/server.go`, update the `NewRouter` call in `Run()`:

```go
handler := NewRouter(a, cfg.ProjectManager, cfg.WebFS, RouterConfig{
	HTTPMode:   cfg.HTTPMode,
	BaseDomain: cfg.BaseDomain,
})
```

- [ ] **Step 6: Update existing `setupTest` in `router_test.go`**

Update the existing `setupTest` function to pass `RouterConfig{}` (zero value -- HTTPS mode, empty BaseDomain):

```go
return NewRouter(a, pm, webFS, RouterConfig{}), store, db
```

- [ ] **Step 7: Run all tests**

Run: `task test`
Expected: all pass, including the new HSTS test

- [ ] **Step 8: Commit**

```bash
git add internal/server/router.go internal/server/middleware.go internal/server/server.go internal/server/router_test.go
git commit -m "feat: skip HSTS header in HTTP mode, add RouterConfig to NewRouter"
```

---

### Task 5: Update cookie scoping -- `SameSite=Lax` with `Domain=.<baseDomain>`

**Files:**
- Modify: `internal/auth/auth.go`
- Modify: `internal/server/auth_handlers.go`
- Modify: `internal/server/router_test.go`

The cookie currently uses `SameSite=Strict` with no Domain attribute. For subdomain routing, the cookie must be `SameSite=Lax` with `Domain=.<baseDomain>` so it is sent on requests to `<name>.localhost` subdomains. In HTTP mode, `Secure` must be `false`.

- [ ] **Step 1: Write test for cookie attributes**

Add to `internal/server/router_test.go`:

```go
func TestLogin_CookieHasDomainAttribute(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{
		BaseDomain: "localhost",
	})

	body := strings.NewReader(`{"password":"testpassword1"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "appx_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected appx_session cookie")
	}
	if sessionCookie.Domain != ".localhost" {
		t.Errorf("expected Domain=.localhost, got %q", sessionCookie.Domain)
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", sessionCookie.SameSite)
	}
}

func TestLogin_CookieSecureInHTTPS(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{
		BaseDomain: "example.com",
		HTTPMode:   false,
	})

	body := strings.NewReader(`{"password":"testpassword1"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == "appx_session" {
			if !c.Secure {
				t.Error("expected Secure=true in HTTPS mode")
			}
			return
		}
	}
	t.Fatal("expected appx_session cookie")
}

func TestLogin_CookieNotSecureInHTTP(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{
		BaseDomain: "localhost",
		HTTPMode:   true,
	})

	body := strings.NewReader(`{"password":"testpassword1"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == "appx_session" {
			if c.Secure {
				t.Error("expected Secure=false in HTTP mode")
			}
			return
		}
	}
	t.Fatal("expected appx_session cookie")
}
```

Also add the `setupTestWithConfig` helper:

```go
// setupTestWithConfig creates a test setup with the given RouterConfig,
// allowing tests to control HTTPMode and BaseDomain.
func setupTestWithConfig(t *testing.T, rcfg RouterConfig) (http.Handler, *auth.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'stopped',
			container_id TEXT,
			internal_port INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			network_id TEXT,
			image_name TEXT,
			last_error TEXT,
			resources TEXT,
			container_secret TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	store := auth.NewStore(db)
	store.SetPassword("testpassword1")
	a := auth.New(store)

	ps := project.NewStore(db)
	pm := project.NewManager(ps)

	webFS := fstest.MapFS{
		"index.html":          {Data: []byte("<html>app</html>")},
		"assets/index-abc.js": {Data: []byte("console.log('hi')")},
	}

	return NewRouter(a, pm, webFS, rcfg), store, db
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/server/ -run TestLogin_Cookie -v`
Expected: FAIL -- cookie has wrong Domain and SameSite values

- [ ] **Step 3: Update `Auth` to accept cookie configuration**

In `internal/auth/auth.go`, add a `CookieConfig` struct and update `SetSessionCookie`:

```go
// CookieConfig controls the attributes of the appx_session cookie. These
// vary by deployment mode: HTTP dev mode uses Secure=false, production uses
// Secure=true. Domain is set to ".<baseDomain>" for cross-subdomain sharing.
type CookieConfig struct {
	Domain string // e.g. ".localhost" or ".user.appx.app" (leading dot)
	Secure bool   // false in --http mode, true otherwise
}

// Auth provides HTTP authentication middleware and cookie management.
// It wraps a Store to validate session tokens from incoming requests.
type Auth struct {
	Store  *Store
	Cookie CookieConfig
}

// New creates an Auth instance backed by the given session/password store.
// Cookie config defaults are safe for HTTPS (Secure=true, no Domain).
func New(store *Store) *Auth {
	return &Auth{
		Store: store,
		Cookie: CookieConfig{
			Secure: true,
		},
	}
}

// SetSessionCookie writes a secure, HttpOnly "appx_session" cookie to the
// response. Called after successful login to establish the user's session.
// SameSite=Lax allows the cookie to be sent on top-level navigations to
// subdomains (needed for agent-built app routing). The Domain attribute is
// set to ".<baseDomain>" so the cookie is shared across all subdomains.
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "appx_session",
		Value:    token,
		Path:     "/",
		Domain:   a.Cookie.Domain,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Cookie.Secure,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}
```

- [ ] **Step 4: Wire CookieConfig from RouterConfig in `server.go`**

In `internal/server/server.go`, after creating `Auth`, configure its cookie:

```go
a := auth.New(cfg.AuthStore)
if cfg.BaseDomain != "" {
	a.Cookie.Domain = "." + cfg.BaseDomain
}
a.Cookie.Secure = !cfg.HTTPMode
a.Store.CleanExpiredSessions()
```

- [ ] **Step 5: Update `handleLogout` cookie clearing to match new attributes**

In `internal/server/auth_handlers.go`, update the logout cookie to include the Domain:

```go
// handleLogout returns the handler for DELETE /api/session. It deletes the
// session from the database and clears the appx_session cookie. The Domain
// attribute must match the login cookie for browsers to clear it correctly.
func handleLogout(a *auth.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("auth: logout from %s", clientIP(r))
		cookie, err := r.Cookie("appx_session")
		if err == nil {
			a.Store.DeleteSession(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "appx_session",
			Value:    "",
			Path:     "/",
			Domain:   a.Cookie.Domain,
			HttpOnly: true,
			Secure:   a.Cookie.Secure,
			MaxAge:   -1,
		})
		writeJSON(w, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 6: Update existing TestLogin_Success to not check Secure (it depends on config now)**

In the existing `TestLogin_Success` test, the Secure check should account for the default `setupTest` using `RouterConfig{}` which has `HTTPMode: false`. The default `Auth` sets `Secure: true` but `setupTest` doesn't go through `server.Run()` so it doesn't configure the cookie. Update `setupTest` to configure Auth's cookie:

The `setupTest` function creates `Auth` directly. After `a := auth.New(store)`, there is no cookie config set, so `Secure` defaults to `true` and `Domain` defaults to `""`. This is fine for the existing `TestLogin_Success` test. The new tests use `setupTestWithConfig` which should configure the Auth cookie. Update `setupTestWithConfig` to set the cookie config:

After `a := auth.New(store)`, add:

```go
if rcfg.BaseDomain != "" {
	a.Cookie.Domain = "." + rcfg.BaseDomain
}
a.Cookie.Secure = !rcfg.HTTPMode
```

- [ ] **Step 7: Run all tests**

Run: `task test`
Expected: all pass

- [ ] **Step 8: Commit**

```bash
git add internal/auth/auth.go internal/server/auth_handlers.go internal/server/server.go internal/server/router_test.go
git commit -m "feat: cookie SameSite=Lax with Domain=.<baseDomain> for cross-subdomain auth"
```

---

### Task 6: Add `/api/opencode/*` reverse proxy route

**Files:**
- Modify: `internal/server/router.go`
- Modify: `internal/server/router_test.go`

This route strips the `/api/opencode` prefix and forwards to `localhost:4096` (the OpenCode server). It is behind auth middleware.

- [ ] **Step 1: Write test for `/api/opencode/*` proxy**

Add to `internal/server/router_test.go`:

```go
func TestOpenCodeProxy_RequiresAuth(t *testing.T) {
	handler, _, _ := setupTest(t)

	req := httptest.NewRequest("GET", "/api/opencode/session", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestOpenCodeProxy_Authed_ForwardsRequest(t *testing.T) {
	// Start a fake OpenCode backend.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the prefix was stripped.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"path":   r.URL.Path,
			"method": r.Method,
		})
	}))
	defer backend.Close()

	handler, store, _ := setupTestWithOpenCodeBackend(t, backend.URL)

	req := authedRequest(t, store, "GET", "/api/opencode/session", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["path"] != "/session" {
		t.Errorf("expected path /session after prefix strip, got %q", resp["path"])
	}
}

func TestOpenCodeProxy_Authed_PreservesQueryString(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"path":  r.URL.Path,
			"query": r.URL.RawQuery,
		})
	}))
	defer backend.Close()

	handler, store, _ := setupTestWithOpenCodeBackend(t, backend.URL)

	req := authedRequest(t, store, "GET", "/api/opencode/session?projectID=abc&limit=10", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["query"] != "projectID=abc&limit=10" {
		t.Errorf("expected query string preserved, got %q", resp["query"])
	}
}
```

Also add the `setupTestWithOpenCodeBackend` helper:

```go
// setupTestWithOpenCodeBackend creates a test router configured with a custom
// OpenCode backend URL (instead of the default localhost:4096). This allows
// tests to point the /api/opencode/* proxy at a test server.
func setupTestWithOpenCodeBackend(t *testing.T, openCodeURL string) (http.Handler, *auth.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'stopped',
			container_id TEXT,
			internal_port INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			network_id TEXT,
			image_name TEXT,
			last_error TEXT,
			resources TEXT,
			container_secret TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	store := auth.NewStore(db)
	store.SetPassword("testpassword1")
	a := auth.New(store)

	ps := project.NewStore(db)
	pm := project.NewManager(ps)

	webFS := fstest.MapFS{
		"index.html":          {Data: []byte("<html>app</html>")},
		"assets/index-abc.js": {Data: []byte("console.log('hi')")},
	}

	return NewRouter(a, pm, webFS, RouterConfig{
		OpenCodeURL: openCodeURL,
	}), store, db
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/server/ -run TestOpenCodeProxy -v`
Expected: FAIL -- `OpenCodeURL` field does not exist on `RouterConfig`

- [ ] **Step 3: Add `OpenCodeURL` to `RouterConfig` and implement the proxy handler**

In `internal/server/router.go`, add `OpenCodeURL` to `RouterConfig`:

```go
type RouterConfig struct {
	HTTPMode    bool   // true = plain HTTP dev mode, affects security headers
	BaseDomain  string // base domain for subdomain routing
	OpenCodeURL string // URL of the OpenCode server (default "http://localhost:4096")
}
```

Add a helper function that creates the OpenCode reverse proxy handler:

```go
// openCodeProxyHandler returns an http.Handler that reverse-proxies requests to
// the OpenCode server. The /api/opencode prefix is stripped before forwarding.
// For example, /api/opencode/session becomes /session on the backend.
// The Cookie header is stripped to prevent the appx session cookie from reaching
// OpenCode. FlushInterval=-1 enables streaming for SSE responses.
func openCodeProxyHandler(backendURL string) http.Handler {
	target, err := url.Parse(backendURL)
	if err != nil {
		log.Fatalf("invalid OpenCode URL %q: %v", backendURL, err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip /api/opencode prefix.
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/opencode")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.Scheme
				req.URL.Host = target.Host
				req.Host = target.Host
				req.Header.Del("Cookie")
			},
			FlushInterval: -1,
		}

		proxy.ServeHTTP(w, r)
	})
}
```

Add the required imports to `router.go`:

```go
import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/project"
)
```

In `NewRouter`, add the route (after the existing protected API routes, before the SPA fallback):

```go
// OpenCode API proxy — strips /api/opencode prefix, forwards to OpenCode server.
ocURL := rcfg.OpenCodeURL
if ocURL == "" {
	ocURL = "http://localhost:4096"
}
mux.Handle("/api/opencode/", a.Middleware(openCodeProxyHandler(ocURL)))
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/ -run TestOpenCodeProxy -v`
Expected: all three tests PASS

- [ ] **Step 5: Run full test suite**

Run: `task test`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/server/router.go internal/server/router_test.go
git commit -m "feat: add /api/opencode/* reverse proxy route to OpenCode server"
```

---

### Task 7: Add subdomain dispatcher for agent-built apps

**Files:**
- Modify: `internal/server/router.go`
- Modify: `internal/server/router_test.go`

The top-level handler must inspect the Host header. If the host matches `<base>`, serve the dashboard mux. If it matches `<name>.<base>`, reverse-proxy to the project's assigned port. Unknown subdomains return 404.

- [ ] **Step 1: Write tests for subdomain dispatch**

Add to `internal/server/router_test.go`:

```go
func TestSubdomainDispatch_BaseDomain_ServesDashboard(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for base domain, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<html>app</html>") {
		t.Error("expected SPA content for base domain")
	}
}

func TestSubdomainDispatch_BaseDomainWithPort_ServesDashboard(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestSubdomainDispatch_UnknownProject_Returns404(t *testing.T) {
	handler, _, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "nonexistent.localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubdomainDispatch_ExistingProject_RequiresAuth(t *testing.T) {
	handler, store, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	// Create a project.
	req := authedRequest(t, store, "POST", "/api/projects", `{"name":"myapp","port":3000}`)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Access subdomain without auth.
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "myapp.localhost"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestSubdomainDispatch_ExistingProject_ProxiesToPort(t *testing.T) {
	// Start a fake app backend.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from app")
	}))
	defer backend.Close()

	// Extract port from backend URL.
	backendPort := strings.TrimPrefix(backend.URL, "http://127.0.0.1:")
	port, _ := strconv.Atoi(backendPort)

	handler, store, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	// Create project with the backend's port.
	body := fmt.Sprintf(`{"name":"myapp","port":%d}`, port)
	req := authedRequest(t, store, "POST", "/api/projects", body)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Access subdomain with auth.
	req = authedRequest(t, store, "GET", "http://myapp.localhost/", "")
	req.Host = "myapp.localhost"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello from app") {
		t.Errorf("expected proxied content, got %q", w.Body.String())
	}
}
```

Add the required imports at the top of the test file (if not already present):

```go
import (
	"fmt"
	"strconv"
)
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/server/ -run TestSubdomainDispatch -v`
Expected: FAIL -- subdomain dispatch not implemented, base domain host serves SPA for all hosts

- [ ] **Step 3: Implement the subdomain dispatcher**

In `internal/server/router.go`, the `NewRouter` function currently returns `securityHeaders(mux, ...)`. Wrap that in a subdomain dispatcher that checks the Host header.

Replace the last line of `NewRouter` (the `return securityHeaders(mux, rcfg.HTTPMode)`) with a subdomain-aware dispatcher:

```go
// Build the dashboard handler (base domain requests).
dashboard := securityHeaders(mux, rcfg.HTTPMode)

// If no BaseDomain configured, skip subdomain dispatch — serve everything
// through the dashboard handler.
if rcfg.BaseDomain == "" {
	return dashboard
}

// Subdomain dispatcher: inspect Host header to route requests.
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)

	// Base domain — serve dashboard.
	if host == rcfg.BaseDomain {
		dashboard.ServeHTTP(w, r)
		return
	}

	// Check for subdomain: <name>.<baseDomain>
	suffix := "." + rcfg.BaseDomain
	if !strings.HasSuffix(host, suffix) {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}
	projectName := strings.TrimSuffix(host, suffix)
	if projectName == "" {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	// Look up the project.
	proj, err := pm.Store.GetByName(projectName)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	// Auth middleware for subdomain requests.
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reverse proxy to the project's assigned port on localhost.
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proj.Port))
		target.Path = r.URL.Path
		target.RawQuery = r.URL.RawQuery

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.Scheme
				req.URL.Host = target.Host
				req.Host = target.Host
				req.Header.Del("Cookie")
			},
			FlushInterval: -1,
		}

		proxy.ServeHTTP(w, r)
	})).ServeHTTP(w, r)
})
```

Add the `stripPort` helper:

```go
// stripPort removes the port from a host:port string. If there is no port,
// the host is returned unchanged. Used by the subdomain dispatcher to compare
// the hostname against BaseDomain.
func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}
```

Add `"fmt"` to the imports in `router.go` if not already present.

- [ ] **Step 4: Run subdomain dispatch tests**

Run: `go test ./internal/server/ -run TestSubdomainDispatch -v`
Expected: all pass

- [ ] **Step 5: Run full test suite**

Run: `task test`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/server/router.go internal/server/router_test.go
git commit -m "feat: add subdomain dispatcher for agent-built app routing"
```

---

### Task 8: Update CSP `connect-src` for subdomain WebSocket/fetch

**Files:**
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/router_test.go`

The strict CSP currently only allows `connect-src 'self'`. For the dashboard to make API calls that the browser considers same-origin, and for subdomains to work with WebSocket, the CSP needs to allow `ws:` and `wss:` on subdomain routes. For the dashboard, `connect-src 'self'` is sufficient. No changes needed for the dashboard CSP. But for subdomain-proxied apps, the CSP from the app itself should pass through. The dashboard CSP is fine as-is.

Actually, since subdomain requests are reverse-proxied directly to the app, the app's own headers pass through. The `securityHeaders` middleware only wraps the dashboard mux, not the subdomain proxy. Verify this is the case.

- [ ] **Step 1: Write test to verify subdomain responses don't get appx CSP headers**

Add to `internal/server/router_test.go`:

```go
func TestSubdomainDispatch_NoAppxSecurityHeaders(t *testing.T) {
	// Subdomain-proxied responses should carry the app's own headers,
	// not appx's strict CSP. Verify appx doesn't inject its CSP.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>app</html>")
	}))
	defer backend.Close()

	backendPort := strings.TrimPrefix(backend.URL, "http://127.0.0.1:")
	port, _ := strconv.Atoi(backendPort)

	handler, store, _ := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})

	body := fmt.Sprintf(`{"name":"csptest","port":%d}`, port)
	req := authedRequest(t, store, "POST", "/api/projects", body)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", w.Code)
	}

	req = authedRequest(t, store, "GET", "http://csptest.localhost/", "")
	req.Host = "csptest.localhost"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "script-src 'self'") {
		t.Errorf("subdomain response should not have appx strict CSP, got: %q", csp)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/server/ -run TestSubdomainDispatch_NoAppxSecurityHeaders -v`
Expected: PASS (subdomain routes bypass the securityHeaders middleware)

If it fails, ensure the subdomain dispatch path does not wrap responses in `securityHeaders`. The current architecture in Task 7 has the dashboard going through `securityHeaders(mux, ...)` while subdomain routes bypass it.

- [ ] **Step 3: Commit (if any fixes were needed)**

```bash
git add internal/server/
git commit -m "test: verify subdomain routes bypass appx security headers"
```

---

### Task 9: Final verification

- [ ] **Step 1: Run full test suite**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 2: Run linter**

Run: `task lint`
Expected: no errors

- [ ] **Step 3: Build**

Run: `task build`
Expected: compiles cleanly

- [ ] **Step 4: Verify `--http` mode starts**

Run: `./appx --http --port 8443 &`
Expected output includes:
```
WARNING: running in HTTP mode -- for local development only
Appx running on http://localhost:8443
```

- [ ] **Step 5: Verify dashboard loads over HTTP**

Run: `curl http://localhost:8443/ -o /dev/null -w '%{http_code}'`
Expected: `200`

- [ ] **Step 6: Verify no HSTS header in HTTP mode**

Run: `curl -sI http://localhost:8443/ | grep -i strict`
Expected: no output (HSTS not present)

- [ ] **Step 7: Verify login sets correct cookie attributes**

Run: `curl -s -X POST http://localhost:8443/api/login -H 'Content-Type: application/json' -d '{"password":"<initial-password>"}' -D - -o /dev/null | grep -i set-cookie`
Expected: cookie contains `Domain=.localhost`, `SameSite=Lax`, no `Secure` flag

- [ ] **Step 8: Verify /api/opencode/ returns 502 (no OpenCode running)**

Run: `curl -s -o /dev/null -w '%{http_code}' --cookie 'appx_session=<token>' http://localhost:8443/api/opencode/session`
Expected: `502` (connection refused to localhost:4096)

- [ ] **Step 9: Verify subdomain returns 404 for nonexistent project**

Run: `curl -s -o /dev/null -w '%{http_code}' http://nonexistent.localhost:8443/`
Expected: `404`

- [ ] **Step 10: Verify `--http` and `--domain` together fails**

Run: `./appx --http --domain example.com 2>&1`
Expected: fatal error containing "mutually exclusive"

- [ ] **Step 11: Stop the test server**

```bash
kill %1
```

- [ ] **Step 12: Run full suite one final time**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 13: Commit any final fixes**

```bash
git add -A
git commit -m "feat: Phase 5 Step 2 complete -- HTTP mode, /api/opencode proxy, subdomain routing"
```
