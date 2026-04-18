# Appx System Documentation

Comprehensive technical reference for the Appx system. This document provides all the context a human or AI developer needs to understand, maintain, and extend the codebase.

---

## Table of Contents

1. [What is Appx](#1-what-is-appx)
2. [High-Level Architecture](#2-high-level-architecture)
3. [Request Lifecycle](#3-request-lifecycle)
4. [Project Layout](#4-project-layout)
5. [Build Pipeline](#5-build-pipeline)
6. [Backend Deep Dive](#6-backend-deep-dive)
   - 6.1 [Entry Point (main.go)](#61-entry-point-maingo)
   - 6.2 [Server & Lifecycle (server.go)](#62-server--lifecycle-servergo)
   - 6.3 [Routing (router.go)](#63-routing-routergo)
   - 6.4 [Authentication System](#64-authentication-system)
   - 6.5 [Database & Migrations](#65-database--migrations)
   - 6.6 [TLS Certificate Management](#66-tls-certificate-management)
   - 6.7 [Rate Limiting](#67-rate-limiting)
   - 6.8 [Security Headers](#68-security-headers)
7. [Frontend Deep Dive](#7-frontend-deep-dive)
8. [Data Flow Diagrams](#8-data-flow-diagrams)
9. [Database Schema](#9-database-schema)
10. [Security Model](#10-security-model)
11. [Testing Architecture](#11-testing-architecture)
12. [Error Handling Patterns](#12-error-handling-patterns)
13. [Future Phases](#13-future-phases)
14. [Key Decisions and Trade-offs](#14-key-decisions-and-trade-offs)
15. [Phase 1 Validation Checklist](#15-phase-1-validation-checklist)

---

## 1. What is Appx

Appx (Agentic Application Proxy) is a self-hostable tool that lets a single user build and host personal applications using AI agents (Claude Code running in sandboxed Docker containers). It solves three problems:

1. **Safe agent execution** -- Agents work best in autonomous mode, but you don't want that on your local machine. Appx runs them in isolated Docker containers on a remote server.
2. **Instant hosting** -- There's a gap between "an agent built me an app" and "I can use it from my phone." Appx bridges that by reverse-proxying container apps on a single port.
3. **Simple operations** -- Hosting is hard for most people. Appx is a single binary with zero-config TLS, embedded UI, and SQLite storage.

The current implementation is **Phase 1** (foundation): HTTPS server, React dashboard, password auth, and the scaffolding that later phases build on.

---

## 2. High-Level Architecture

```
                          Internet / LAN
                               |
                               | HTTPS (single port, default 443)
                               v
                    +---------------------+
                    |     Go Binary        |
                    |      (appx)          |
                    |                      |
                    |  +---------------+   |
                    |  | TLS Termination|   |    Self-signed ECDSA P-256
                    |  +-------+-------+   |    cert with auto-detected SANs
                    |          |           |
                    |  +-------v-------+   |
                    |  |Security Headers|   |    HSTS, CSP, X-Frame-Options
                    |  +-------+-------+   |
                    |          |           |
                    |  +-------v-------+   |
                    |  |   Router       |   |    stdlib http.ServeMux
                    |  +--+----+----+--+   |
                    |     |    |    |      |
                    |     v    v    v      |
                    |   /api  /api  /      |
                    |  login  /*   SPA     |
                    |  (pub) (auth) (embed)|
                    |     |    |    |      |
                    |     v    v    v      |
                    |  Rate  Auth  File    |
                    |  Limit Middleware Server |
                    +--------|----+--------+
                             |    |
                    +--------v-+  |
                    |  SQLite   |  |   ./data/appx.db
                    |  (WAL)    |  |   WAL mode, foreign keys
                    +-----------+  |
                                   |
                    +--------------v---+
                    | Embedded React   |   go:embed web/dist/*
                    | SPA (index.html) |   Vite-built, served as static files
                    +------------------+

Future (Phase 2+):
                    +------------------+
                    | Docker Containers|   Per-project isolation
                    |  proj-a :3001    |   Not exposed to network
                    |  proj-b :3002    |   Accessed via /apps/:name/*
                    +------------------+   reverse proxy
```

Key properties:
- **Single binary**: Go compiles everything (including the React frontend) into one executable
- **Single port**: All traffic (UI, API, future app proxying) goes through one HTTPS port
- **Zero external dependencies at runtime**: No nginx, no Redis, no external DB server
- **Single-user**: One password, one session cookie, designed for personal VPS use

---

## 3. Request Lifecycle

Every HTTP request passes through the same pipeline. Understanding this pipeline is essential for debugging and extending the system.

```
Client Request
     |
     v
[TLS Termination] ---- Go's crypto/tls with in-memory certificate
     |
     v
[securityHeaders] ---- Adds HSTS, CSP, X-Frame-Options, etc. to EVERY response
     |
     v
[http.ServeMux] ------ Pattern matching on method + path
     |
     +-- "POST /api/login" -------> [rateLimiter] --> [handleLogin]
     |                                                      |
     |                                               Check bcrypt hash
     |                                               Create session (SHA-256 token in DB)
     |                                               Set appx_session cookie
     |
     +-- "/api/" (all other) ----> [auth.Middleware] --> [inner mux]
     |                                   |                   |
     |                            Read cookie            +-- "GET /api/projects" -> [handleListProjects]
     |                            Validate token         +-- "DELETE /api/session" -> [handleLogout]
     |                            401 if invalid
     |
     +-- "/" (everything else) --> [spaHandler]
                                       |
                                  File exists in webFS? --> Serve static file
                                  File doesn't exist?   --> Serve index.html (SPA fallback)
```

### Important details:

1. **Route registration order matters.** Go 1.22+ ServeMux uses most-specific-match, so `POST /api/login` takes priority over `/api/` for POST login requests. The `/api/` catch-all sends everything else through auth middleware.

2. **The outer mux vs inner mux pattern.** Public routes go on `mux` (the outer ServeMux). Protected routes go on `api` (the inner ServeMux), which is wrapped with `a.Middleware(api)` and mounted at `/api/`. This means the auth check happens before the inner mux even does routing.

3. **SPA fallback.** The `spaHandler` checks if the requested path exists as a real file (JS, CSS, favicon, etc). If yes, serve it. If no, serve `index.html` so React Router can handle client-side routes like `/login` or `/projects`.

---

## 4. Project Layout

```
appx/
+-- cmd/appx/
|   +-- main.go                  Entry point. Parses flags, wires deps, starts server.
|   +-- web/dist/                Build artifact: frontend assets copied here for go:embed.
|                                GITIGNORED. Created by `task web`.
|
+-- internal/                    All Go packages. Not importable by external code.
|   +-- auth/
|   |   +-- auth.go              Auth struct: middleware + cookie helpers
|   |   +-- store.go             Store struct: password + session CRUD against SQLite
|   |   +-- store_test.go        Unit tests for Store (password, sessions, cleanup)
|   |
|   +-- db/
|   |   +-- db.go                SQLite connection, WAL config, versioned migration runner
|   |   +-- db_test.go           Migration tests (fresh, idempotent, data preservation)
|   |
|   +-- server/
|   |   +-- server.go            Config struct, Run() function, TLS setup, graceful shutdown
|   |   +-- router.go            NewRouter(), spaHandler(), writeJSON()
|   |   +-- auth_handlers.go     handleLogin, handleLogout
|   |   +-- project_handlers.go  handleListProjects
|   |   +-- middleware.go        securityHeaders middleware
|   |   +-- ratelimit.go         In-memory IP rate limiter
|   |   +-- router_test.go       Integration tests: full request/response through router
|   |
|   +-- tls/
|       +-- selfsigned.go        Self-signed cert generation, SAN detection, PEM I/O
|
+-- web/                         React frontend (separate npm project)
|   +-- src/
|   |   +-- main.tsx             React entry point
|   |   +-- App.tsx              Root component with react-router-dom
|   |   +-- index.css            Global CSS reset (dark theme)
|   |   +-- api/client.ts        Typed HTTP client for all API calls
|   |   +-- pages/
|   |       +-- Login.tsx         Password form, redirects to / on success
|   |       +-- Dashboard.tsx     Project list, redirects to /login on 401
|   +-- package.json             React 19, Vite 8, TypeScript 5.9
|   +-- vite.config.ts           Vite config (just React plugin)
|   +-- tsconfig*.json           TypeScript configs
|
+-- data/                        GITIGNORED. Created at runtime.
|   +-- appx.db                  SQLite database (WAL mode)
|   +-- cert.pem                 Self-signed TLS certificate
|   +-- key.pem                  ECDSA P-256 private key
|
+-- docs/
|   +-- SYSTEM.md                This file
|   +-- plans/                   Design docs and phase plans
|   +-- brainstorm/              Original idea exploration
|
+-- Taskfile.yml                 Build system (replaces Makefile)
+-- CLAUDE.md                    AI assistant instructions and project conventions
+-- go.mod / go.sum              Go module definition
+-- .gitignore
```

---

## 5. Build Pipeline

```
                     task build
                         |
                         v
           +------ task web (cached) ------+
           |                               |
           v                               |
   cd web && npm run build                 |
   (tsc -b && vite build)                  |
           |                               |
           v                               |
   web/dist/                               |
   +-- index.html                          |
   +-- assets/index-*.js                   |
   +-- assets/index-*.css                  |
   +-- favicon.svg                         |
   +-- icons.svg                           |
           |                               |
           v                               |
   cp -r web/dist --> cmd/appx/web/dist    |
                         |                 |
                         v                 |
               go build -o appx ./cmd/appx |
                         |                 |
                         v                 |
              //go:embed web/dist/*        |
              bundles frontend into binary |
                         |                 |
                         v                 |
                    ./appx binary          |
                    (single file,          |
                     ~15MB,                |
                     includes everything)  |
```

The `web` task uses Taskfile's `sources`/`generates` for file-based caching -- it skips the npm build if no frontend source files have changed since the last build.

### Commands

| Command | What it does |
|---------|-------------|
| `task build` | Full build: frontend + Go binary |
| `task web` | Frontend only (with caching) |
| `task dev` | Vite dev server on localhost:5173 (hot reload) |
| `task test` | `go test ./internal/...` |
| `task lint` | `npm run lint` in web/ |
| `task clean` | Remove all build artifacts |

---

## 6. Backend Deep Dive

### 6.1 Entry Point (main.go)

**File:** `cmd/appx/main.go`

The main function performs a linear startup sequence with no goroutines or async initialization:

```
Parse CLI flags
       |
       v
Create data directory (./data/ by default, mode 0700)
       |
       v
Open SQLite database (runs migrations automatically)
       |
       v
Create auth store (wraps *sql.DB)
       |
       v
Check if password exists in DB
       |--- No:  Generate random 32-char hex password
       |         Hash with bcrypt, store in settings table
       |         Print to stdout (only way user gets the password)
       |
       |--- Yes: Continue silently
       |
       v
Extract embedded filesystem (web/dist -> fs.FS)
       |
       v
Call server.Run(config) -- blocks until shutdown
```

**CLI flags:**
- `-port` (default 443): HTTPS port. Use 8443 for non-root development.
- `-data` (default `./data`): Directory for SQLite DB and TLS certificates.
- `-host` (default empty): Additional hostname/IP for TLS certificate SANs.

**Critical detail:** The embedded filesystem path. The `//go:embed web/dist/*` directive in main.go embeds files relative to the file's location (`cmd/appx/`). The build process copies `web/dist` to `cmd/appx/web/dist`, so the embed path is `web/dist`. At runtime, `fs.Sub(webEmbed, "web/dist")` strips that prefix so files are served from their natural paths (`index.html`, `assets/...`).

### 6.2 Server & Lifecycle (server.go)

**File:** `internal/server/server.go`

The `Run()` function owns the server lifecycle:

```
Load/generate TLS certificate
       |
       v
Create Auth instance, clean expired sessions
       |
       v
Start hourly session cleanup goroutine
       |
       v
Build router (NewRouter)
       |
       v
Configure TLS (min TLS 1.2, in-memory cert)
       |
       v
Start HTTP server in goroutine
       |
       v
Block on select:
  +-- Server error channel --> return error
  +-- SIGINT/SIGTERM       --> graceful shutdown (10s timeout)
       |
       v
srv.Shutdown(ctx) -- drains in-flight requests
       |
       v
Return nil
```

**Graceful shutdown** is important because future phases will manage Docker containers that need proper cleanup. The 10-second timeout ensures the process doesn't hang indefinitely if a connection is stuck.

**Session cleanup** runs as a background goroutine on a 1-hour ticker. It deletes rows from the `sessions` table where `expires_at < now()`. Without this, the sessions table would grow unboundedly (one row per login, 30-day expiry).

**TLS configuration:** The certificate is loaded into memory and passed to `tls.Config.Certificates`. The server calls `ListenAndServeTLS("", "")` with empty file paths because the cert is already in the TLS config -- Go uses the in-memory cert when file paths are empty.

### 6.3 Routing (router.go)

**File:** `internal/server/router.go`

The routing architecture uses a two-mux pattern to cleanly separate public and authenticated routes:

```
                    securityHeaders (outermost wrapper)
                           |
                           v
                     mux (outer ServeMux)
                    /      |            \
                   /       |             \
    POST /api/login    /api/* catch-all    / catch-all
         |                |                    |
    rateLimiter      auth.Middleware       spaHandler
         |                |                    |
    handleLogin      api (inner ServeMux)  fileServer
                    /              \        or index.html
        GET /api/projects    DELETE /api/session
              |                     |
    handleListProjects        handleLogout
```

**Why two muxes?** If auth middleware were applied to the outer mux, it would block the login endpoint too. By mounting the inner mux at `/api/` with auth middleware wrapping it, and registering login on the outer mux with a more specific pattern (`POST /api/login`), Go's ServeMux routes login requests to the outer handler (more specific match) and everything else under `/api/` to the auth-wrapped inner mux.

**SPA handler logic (`spaHandler`):** This is a common pattern for serving single-page applications from a Go file server. The handler:
1. Checks if the URL path corresponds to a real file in the embedded filesystem
2. If yes (e.g., `/assets/index-abc.js`), serves the file directly
3. If no (e.g., `/login`, `/settings`), rewrites the path to `/` and serves `index.html`

This lets React Router handle client-side navigation. Without it, refreshing the browser on `/login` would return a 404 because there's no `login` file in the dist directory.

### 6.4 Authentication System

Authentication spans two files with distinct responsibilities:

```
+------------------+          +------------------+
|   auth/auth.go   |          |  auth/store.go   |
|                  |          |                  |
|  HTTP concerns:  |  uses    |  Data concerns:  |
|  - Middleware     +--------->  - Password CRUD  |
|  - Cookie mgmt   |          |  - Session CRUD  |
|                  |          |  - Token hashing  |
+------------------+          +--------+---------+
                                       |
                                       v
                                   SQLite DB
                              (settings + sessions)
```

#### Password Flow

```
First Run:
  main.go --> store.IsPasswordSet()
                 |
                 v
          SELECT value FROM settings WHERE key = 'password_hash'
                 |
            No rows --> store.GeneratePassword() --> 16 random bytes --> hex encode (32 chars)
                 |
                 v
          store.SetPassword(pw)
                 |
                 v
          bcrypt.GenerateFromPassword(pw, DefaultCost)
                 |
                 v
          INSERT INTO settings (key='password_hash', value=<bcrypt hash>)
                 |
                 v
          Print password to stdout (only time it's shown)

Login:
  POST /api/login {"password": "..."}
         |
         v
  store.CheckPassword(pw)
         |
         v
  SELECT value FROM settings WHERE key = 'password_hash'
         |
         v
  bcrypt.CompareHashAndPassword(hash, pw)
         |
    Match --> Create session
    No match --> 401
```

#### Session Flow

```
Session Creation (on successful login):
  store.CreateSession()
         |
         v
  Generate 32 random bytes --> hex encode --> 64-char token (raw)
         |
         v
  SHA-256(raw token) --> token hash (stored in DB)
         |
         v
  INSERT INTO sessions (token=<hash>, expires_at=now+30days)
         |
         v
  Return raw token to caller
         |
         v
  auth.SetSessionCookie(w, rawToken)
         |
         v
  Set-Cookie: appx_session=<raw token>; Path=/; HttpOnly; Secure; SameSite=Strict; MaxAge=2592000

Session Validation (on every protected request):
  Request with Cookie: appx_session=<raw token>
         |
         v
  auth.Middleware reads cookie
         |
         v
  store.ValidSession(rawToken)
         |
         v
  SHA-256(rawToken) --> look up hash in sessions table
         |
         v
  SELECT expires_at WHERE token = <hash>
         |
    Found + not expired --> Allow request through
    Not found or expired --> 401 Unauthorized

Session Deletion (logout):
  DELETE /api/session
         |
         v
  Read cookie value
         |
         v
  store.DeleteSession(rawToken) --> DELETE FROM sessions WHERE token = SHA-256(rawToken)
         |
         v
  Clear cookie (MaxAge=-1)
```

**Why hash session tokens?** If an attacker gains read access to the SQLite database (file system access, SQL injection in a future feature, backup leak), they would see token hashes, not raw tokens. They cannot forge a valid cookie from a hash because SHA-256 is a one-way function.

### 6.5 Database & Migrations

**File:** `internal/db/db.go`

#### Connection Configuration

```go
sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
```

- **WAL (Write-Ahead Logging):** Allows concurrent readers while writing. Essential for a web server where multiple requests may read simultaneously.
- **Foreign keys:** Enabled explicitly because SQLite disables them by default. The `egress_log.project_id` references `projects.id`.
- **Busy timeout:** 5 seconds. If the database is locked (rare with WAL, but possible during migrations), wait up to 5s instead of immediately failing.

#### Migration System

Migrations are managed by [golang-migrate](https://github.com/golang-migrate/migrate). SQL files live in `internal/db/migrations/` and are embedded into the binary at compile time via `go:embed`, keeping the single-binary guarantee. Applied migrations are tracked in the `schema_migrations` table.

```
On startup:
  db.Open(dataDir)
       |
       v
  runMigrations(db)
       |
       v
  golang-migrate reads embedded *.sql files via iofs source driver
       |
       v
  SELECT version FROM schema_migrations  -->  already-applied versions
       |
       v
  For each pending migration (up.sql):
       |
       v
  Execute SQL inside a transaction
       |
       v
  Record version in schema_migrations + COMMIT
       |
  If error --> ROLLBACK, return error, server refuses to start
```

**Adding a new migration:**
1. Create `internal/db/migrations/000002_your_title.up.sql` with the DDL changes
2. Create `internal/db/migrations/000002_your_title.down.sql` to reverse them
3. The files are picked up automatically on next startup — no Go code changes needed

**Current schema (000001_initial):**

```sql
-- 000001_initial.up.sql

CREATE TABLE projects (
    id TEXT PRIMARY KEY,              -- UUID
    name TEXT UNIQUE NOT NULL,        -- human-readable, used in URL paths
    status TEXT DEFAULT 'stopped',    -- stopped | running | error
    container_id TEXT,                -- Docker container ID (Phase 2)
    internal_port INTEGER,            -- Container's exposed port (Phase 2)
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    token TEXT PRIMARY KEY,           -- SHA-256 hash of raw session token
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME               -- now + 30 days at creation
);

CREATE TABLE egress_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT REFERENCES projects(id),  -- FK to projects
    destination TEXT,                 -- hostname or IP
    port INTEGER,                     -- destination port
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,             -- e.g. 'password_hash'
    value TEXT                        -- arbitrary string value
);
```

### 6.6 TLS Certificate Management

**File:** `internal/tls/selfsigned.go`

The TLS subsystem provides zero-config HTTPS:

```
LoadOrGenerateSelfSigned(dataDir, hosts...)
       |
       v
  Try loading cert.pem + key.pem from dataDir
       |
  Success --> Parse cert --> Check expiry
       |                         |
       |                   Expires in > 7 days? --> Return cert (use existing)
       |                   Expires in <= 7 days? --> Fall through to generate
       |
  Failure (no files) --> Fall through to generate
       |
       v
  generateAndSave(certPath, keyPath, hosts)
       |
       v
  Generate ECDSA P-256 private key
       |
       v
  collectSANs(hosts)
       |
       v
  +-- Always: 127.0.0.1, localhost
  +-- From --host flag: additional IP or DNS name
  +-- Auto-detected: all non-loopback IPs from network interfaces
       |                (e.g., 192.168.1.50, 10.0.0.5)
       |
       v
  Create X.509 certificate:
    Subject:     "Appx Self-Signed"
    Valid:       now --> now + 365 days
    Key usage:   Digital signature, Server auth
    SANs:        All collected IPs and DNS names
       |
       v
  Write cert.pem (0600) and key.pem (0600) to dataDir
       |
       v
  Return parsed tls.Certificate
```

**Why auto-detect IPs?** When Appx runs on a VPS, users access it via the server's public IP (e.g., `https://203.0.113.50:443`). If the cert only has `localhost`/`127.0.0.1` in SANs, browsers show a certificate mismatch warning even after trusting the self-signed cert. By detecting all local IPs, the cert is valid for however the user accesses the server.

**Cert renewal:** The 7-day-before-expiry check means the cert auto-renews on the next server restart when it's close to expiring. There's no background renewal -- the server must be restarted.

### 6.7 Rate Limiting

**File:** `internal/server/ratelimit.go`

The rate limiter protects the login endpoint from brute-force attacks:

```
rateLimiter
  +-- window: 15 minutes
  +-- max: 10 attempts per IP
  +-- attempts: map[IP] -> []timestamp (in-memory)

On each request:
  Extract IP from r.RemoteAddr
       |
       v
  Lock mutex (concurrent-safe)
       |
       v
  Prune timestamps older than (now - window)
       |
       v
  Count remaining timestamps for this IP
       |
  Count >= max --> 429 Too Many Requests
  Count < max  --> Record timestamp, allow request through
```

**Trade-offs:**
- In-memory only: resets on server restart. Acceptable for single-user personal use.
- No cleanup goroutine: stale entries for IPs that stop requesting stay in memory. Not a concern for low-traffic personal server.
- IP-based: works behind direct connections. If behind a reverse proxy (Caddy, nginx), would need X-Forwarded-For handling (not implemented yet).

### 6.8 Security Headers

**File:** `internal/server/middleware.go`

Applied to every response via the outermost middleware wrapper:

| Header | Value | Purpose |
|--------|-------|---------|
| `X-Content-Type-Options` | `nosniff` | Prevents MIME-type sniffing |
| `X-Frame-Options` | `DENY` | Prevents clickjacking via iframes |
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains` | Forces HTTPS for 2 years |
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'` | Restricts resource loading to same origin |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | Limits referrer leakage |

The CSP allows `'unsafe-inline'` for styles because the React frontend uses inline style objects. All scripts must come from same origin (`'self'`).

---

## 7. Frontend Deep Dive

### Stack

- **React 19** with functional components and hooks
- **Vite 8** as build tool (fast HMR in dev, optimized production builds)
- **TypeScript 5.9** with strict config
- **react-router-dom 7** for client-side routing

### Component Tree

```
main.tsx
  |
  StrictMode
    |
    App.tsx (BrowserRouter)
      |
      Routes
        +-- /login  -->  Login.tsx
        +-- /       -->  Dashboard.tsx
        +-- *       -->  Navigate to / (catch-all redirect)
```

### API Client (`web/src/api/client.ts`)

All API calls go through one shared `request<T>()` function:

```
request<T>(path, opts?)
     |
     v
  fetch(`/api${path}`, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...opts?.headers }
  })
     |
     v
  Response OK? --> return res.json() as T
  Response not OK? --> throw new Error(responseBody)
```

Exported functions:
- `login(password)` -- `POST /api/login`
- `logout()` -- `DELETE /api/session`
- `getProjects()` -- `GET /api/projects`

### Page Components

**Login.tsx:**
- Renders a centered card with password input and submit button
- On submit: calls `login()`, navigates to `/` on success, shows error on failure
- No auth check on mount -- this is the public entry point

**Dashboard.tsx:**
- On mount: calls `getProjects()`. If the call fails (401), redirects to `/login`
- Renders a header with logout button and a project grid
- Empty state shown when no projects exist (current state)
- Logout: calls `logout()`, then `window.location.href = '/login'` (full page reload to clear state)

### Styling

The frontend uses a consistent dark theme with inline styles:

```
Background (page):  #0a0a0a
Background (card):  #1a1a1a
Text (primary):     #e0e0e0
Text (secondary):   #888
Text (hint):        #555
Borders:            #333
Error:              #ef4444
Running status:     #22c55e
Button (primary):   #fff text on #000 (inverted)
Font:               system-ui, -apple-system, sans-serif
```

Styles are defined as `Record<string, React.CSSProperties>` objects at the bottom of each component file. No CSS modules, no Tailwind, no CSS-in-JS library.

---

## 8. Data Flow Diagrams

### First Run

```
User                    Server                     SQLite              Filesystem
  |                        |                          |                     |
  |   (server starts)      |                          |                     |
  |                        +------ Open DB ---------->|                     |
  |                        |       Run migrations     |                     |
  |                        |                          |                     |
  |                        +------ Check password --->|                     |
  |                        |       (no rows)          |                     |
  |                        |                          |                     |
  |                        +------ Generate + store ->|                     |
  |                        |       password hash      |                     |
  |                        |                          |                     |
  |  <-- Print password ---+                          |                     |
  |      to stdout         |                          |                     |
  |                        +------ Load/gen cert -----|-------------------->|
  |                        |       (no cert.pem)      |     Write cert.pem |
  |                        |                          |     Write key.pem  |
  |                        |                          |                     |
  |                        +-- Server listening on :443                     |
```

### Login Flow

```
Browser                  Server                      SQLite
  |                         |                           |
  | GET /login              |                           |
  |------------------------>|                           |
  | <-- 200 index.html -----|                           |
  |                         |                           |
  | (React renders Login)   |                           |
  |                         |                           |
  | POST /api/login         |                           |
  | {"password": "abc123"}  |                           |
  |------------------------>|                           |
  |                         +--- Rate limit check       |
  |                         |    (in-memory, allow)     |
  |                         |                           |
  |                         +--- CheckPassword -------->|
  |                         |    bcrypt compare         |
  |                         |                           |
  |                         +--- CreateSession -------->|
  |                         |    INSERT token hash      |
  |                         |                           |
  | <-- 200 {"status":"ok"} |                           |
  |     Set-Cookie: appx_session=<raw token>            |
  |                         |                           |
  | (React navigates to /)  |                           |
  |                         |                           |
  | GET /api/projects       |                           |
  | Cookie: appx_session=.. |                           |
  |------------------------>|                           |
  |                         +--- ValidSession --------->|
  |                         |    SHA-256, check expiry  |
  |                         |                           |
  |                         +--- Query projects ------->|
  |                         |    SELECT * FROM projects |
  |                         |                           |
  | <-- 200 []              |                           |
```

---

## 9. Database Schema

### Entity Relationship Diagram

```
+------------------+       +----------------------+
|    settings      |       |   schema_migrations  |
+------------------+       +----------------------+
| key  TEXT PK     |       | version BIGINT PK    |
| value TEXT       |       | dirty BOOLEAN        |
+------------------+       +----------------------+
  Contains:                  Managed by golang-migrate
  - password_hash
  Contains:
  - password_hash

+------------------+       +-------------------+
|    projects      |       |    egress_log     |
+------------------+       +-------------------+
| id TEXT PK       |<------| id INT PK AUTO    |
| name TEXT UNIQUE |       | project_id TEXT FK |
| status TEXT      |       | destination TEXT   |
| container_id TEXT|       | port INT           |
| internal_port INT|       | timestamp DATETIME |
| created_at DT    |       +-------------------+
+------------------+

+------------------+
|    sessions      |
+------------------+
| token TEXT PK    |   <-- SHA-256 hash, not raw token
| created_at DT    |
| expires_at DT    |
+------------------+
```

### Table Usage by Component

| Table | Read by | Written by |
|-------|---------|-----------|
| `settings` | `auth.Store.IsPasswordSet`, `CheckPassword` | `auth.Store.SetPassword` |
| `sessions` | `auth.Store.ValidSession` | `auth.Store.CreateSession`, `DeleteSession`, `CleanExpiredSessions` |
| `projects` | `server.handleListProjects` | (Phase 2: project CRUD) |
| `egress_log` | (Phase 5: egress viewer) | (Phase 5: egress logger) |
| `schema_migrations` | golang-migrate | golang-migrate |

---

## 10. Security Model

### Threat Model

Appx is designed for a **single user on a personal VPS**. The threat model assumes:
- The server is directly exposed to the internet on one port
- The attacker does not have filesystem access to the server
- The user accesses from trusted devices over untrusted networks

### Defense Layers

```
Layer 1: TLS Encryption
  +-- All traffic encrypted (HSTS enforced)
  +-- Self-signed ECDSA P-256, minimum TLS 1.2
  +-- User must trust cert on first use

Layer 2: Authentication
  +-- Single password, bcrypt hashed (cost 10)
  +-- Session tokens: 256-bit random, SHA-256 hashed in DB
  +-- Cookies: HttpOnly + Secure + SameSite=Strict
  +-- 30-day expiry, server-side validation on every request

Layer 3: Rate Limiting
  +-- Login: 10 attempts per 15 minutes per IP
  +-- Prevents online brute-force attacks

Layer 4: Security Headers
  +-- CSP prevents XSS and resource injection
  +-- X-Frame-Options prevents clickjacking
  +-- Strict-Transport-Security prevents downgrade attacks

Layer 5: Input Validation
  +-- Request body size limited to 1MB (MaxBytesReader)
  +-- JSON decoding with type validation
  +-- Parameterized SQL queries (no string concatenation)
```

### Cookie Properties

| Property | Value | Why |
|----------|-------|-----|
| `HttpOnly` | true | JavaScript cannot read the session token (XSS protection) |
| `Secure` | true | Cookie only sent over HTTPS |
| `SameSite` | Strict | Cookie not sent on cross-site requests (CSRF protection) |
| `Path` | `/` | Cookie sent for all paths |
| `MaxAge` | 2592000 (30 days) | Client-side expiry matches server-side |

---

## 11. Testing Architecture

### Test Files and Coverage

| Test File | What it Tests | Test Count |
|-----------|--------------|------------|
| `internal/auth/store_test.go` | Password CRUD, session CRUD, expiry, cleanup | 7 |
| `internal/db/db_test.go` | Migrations: fresh, idempotent, data preservation | 3 |
| `internal/server/router_test.go` | Full HTTP integration: login, auth, SPA, cookies | 8 |

### Testing Strategy

All tests use **in-memory SQLite** (`:memory:`) for speed and isolation:

```go
db, _ := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
```

Each test creates its own database, so tests are fully independent and parallelizable.

**Router tests** use the `setupTest(t)` helper which:
1. Creates an in-memory SQLite database
2. Manually creates the required tables (not via runMigrations(), to keep tests independent of migration ordering)
3. Creates an auth store with password "testpass"
4. Creates a mock filesystem (`fstest.MapFS`) with `index.html` and a JS file
5. Returns the fully-wired router and the auth store (for creating test sessions)

Tests then exercise the full HTTP stack using `httptest.NewRequest` and `httptest.NewRecorder`:

```go
// Example: testing authenticated access
handler, store := setupTest(t)
token, _ := store.CreateSession()

req := httptest.NewRequest("GET", "/api/projects", nil)
req.AddCookie(&http.Cookie{Name: "appx_session", Value: token})
w := httptest.NewRecorder()

handler.ServeHTTP(w, req)
// Assert on w.Code, w.Body, w.Result().Cookies(), etc.
```

### What's Tested

**Auth store tests (`store_test.go`):**
- Empty DB reports no password set
- Set and check password (correct + wrong)
- Password overwrite (old password fails, new works)
- Generated passwords are 32 chars and unique
- Create session returns 64-char token, validates correctly
- Expired sessions fail validation
- CleanExpiredSessions removes expired but keeps valid

**Migration tests (`db_test.go`):**
- Fresh migration creates all tables (verified by inserting into each)
- Running migrations twice is idempotent (no error, no duplicate application)
- Existing data survives re-migration

**Router tests (`router_test.go`):**
- Login with correct password: 200, sets HttpOnly+Secure session cookie
- Login with wrong password: 401
- Login with invalid JSON: 400
- Protected route without cookie: 401
- Protected route with valid session: 200, returns JSON project array
- Protected route with invalid session token: 401
- SPA serves index.html for `/`
- SPA serves actual static assets for `/assets/...`
- SPA falls back to index.html for unknown routes (e.g., `/login`)

---

## 12. Error Handling Patterns

### Go Backend

Errors are wrapped with context at each level using `fmt.Errorf("context: %w", err)`:

```
Top level (main.go):     log.Fatalf("open db: %v", err)
                              |
Middle level (db.go):    return fmt.Errorf("migrate: %w", err)
                              |
Low level (db.go):       return fmt.Errorf("begin migration %d: %w", i+1, err)

Result: "open db: migrate: begin migration 1: <sqlite error>"
```

**HTTP handlers** return generic error messages to clients (never internal details):
- `"bad request"` (400) -- malformed input
- `"unauthorized"` (401) -- missing/invalid auth
- `"internal error"` (500) -- unexpected server-side failure
- `"too many requests"` (429) -- rate limit exceeded

### Frontend

API errors are caught at the component level:
- **Login page:** catches login() rejection, shows "Invalid password"
- **Dashboard:** catches getProjects() rejection (any error, including 401), redirects to `/login`

No global error boundary exists yet. The frontend assumes that API failures on protected routes mean the session is invalid and redirects to login.

---

## 13. Future Phases

The current implementation is Phase 1 of a 6-phase roadmap (see `docs/plans/appx_plan_v0.md`):

```
Phase 1: Foundation          <-- COMPLETE (current state)
  Go server, React frontend, TLS, password auth, graceful shutdown

Phase 2: Project Management + Docker
  CRUD projects, start/stop Docker containers, Docker SDK integration
  New packages: internal/project/project.go, internal/project/container.go
  New endpoints: POST/DELETE /api/projects, POST /api/projects/:id/start|stop

Phase 3: Terminal (Claude Code in browser)
  xterm.js + WebSocket, docker exec bridge
  New packages: internal/project/terminal.go
  New endpoint: /ws/term/:id (WebSocket upgrade)
  New component: web/src/components/Terminal.tsx

Phase 4: Reverse Proxy (access apps)
  httputil.ReverseProxy, path prefix stripping
  /apps/:name/* routes to container internal ports
  New packages: internal/proxy/proxy.go, internal/proxy/rewrite.go

Phase 5: Egress Logging
  Monitor outbound connections from containers
  Log to egress_log table, display in dashboard

Phase 6: Installer + Polish
  curl | sh installer, systemd service, Docker image
```

### Extension Points

The architecture is designed to accommodate these phases:

| Future Need | Current Preparation |
|-------------|-------------------|
| Docker container management | `projects` table has `container_id`, `internal_port`, `status` columns |
| Egress logging | `egress_log` table exists with project FK |
| WebSocket terminal | Graceful shutdown handles draining connections |
| Reverse proxy | Single-port architecture with path-based routing ready |
| More API endpoints | Handler file pattern (`*_handlers.go`), inner mux for auth routes |
| Schema changes | golang-migrate: add `000002_title.up.sql` + `000002_title.down.sql` |

---

## 14. Key Decisions and Trade-offs

### Single binary with embedded frontend

**Decision:** Embed the React build output into the Go binary via `go:embed`.

**Trade-off:** Requires a two-step build (npm build then go build). But eliminates deployment complexity -- copy one file to a server and run it. No web server config, no file serving setup, no path issues.

### SQLite over PostgreSQL

**Decision:** Use SQLite with WAL mode as the only database.

**Trade-off:** Cannot scale to multiple server instances. But Appx is single-user on a single VPS, so this is fine. SQLite gives zero-ops (no database server to install/manage), single-file backups, and the `data/` directory contains everything.

### Self-signed TLS over Let's Encrypt

**Decision:** Generate self-signed certificates by default.

**Trade-off:** Users must trust the cert on first use (browser warning). But this provides zero-config encrypted traffic without needing a domain name. Let's Encrypt/Caddy integration is planned as an opt-in upgrade when domain support is added.

### Session cookies over JWT

**Decision:** Server-side sessions with opaque tokens over stateless JWTs.

**Trade-off:** Requires a database lookup on every authenticated request. But sessions can be immediately revoked (logout, cleanup), there's no token size bloat, and the database is local SQLite so the lookup is sub-millisecond.

### stdlib net/http over a framework

**Decision:** Use Go's standard library HTTP server and mux, no gorilla/mux, no chi, no gin.

**Trade-off:** More manual work for path parameters (needed in Phase 2+). But Go 1.22+ ServeMux supports method-based routing (`POST /api/login`), which covers current needs. Avoids a dependency and keeps the codebase idiomatic.

### In-memory rate limiter over distributed

**Decision:** Track rate limits in a Go map protected by a mutex.

**Trade-off:** Resets on server restart, doesn't work across multiple instances. But Appx is a single process and the rate limiter is a defense-in-depth measure (the primary protection is bcrypt's slowness). The simplicity is worth it.

### Inline styles over CSS framework

**Decision:** Use React inline style objects instead of Tailwind, CSS modules, or styled-components.

**Trade-off:** No hover states (would need onMouseEnter/onMouseLeave), no media queries (would need JS), no pseudo-elements. But the UI is simple enough that this isn't a problem yet, and it avoids build tool configuration and extra dependencies.

---

## 15. Phase 1 Validation Checklist

Use these steps to verify a Phase 1 deployment is working correctly end-to-end.

```bash
export APPX_HOST=<your-server-ip-or-domain>
```

### Authentication

```bash
# 1. Unauthenticated API access → 401
curl -k https://$APPX_HOST/api/projects

# 2. Wrong password → 401
curl -k -X POST https://$APPX_HOST/api/login \
  -H "Content-Type: application/json" -d '{"password":"wrong"}'

# 3. Bad JSON → 400
curl -k -X POST https://$APPX_HOST/api/login \
  -H "Content-Type: application/json" -d 'not json'

# 4. Correct password → 200 + sets session cookie
curl -k -c cookies.txt -X POST https://$APPX_HOST/api/login \
  -H "Content-Type: application/json" -d '{"password":"<your-password>"}'

# 5. Authenticated access → 200 + empty array
curl -k -b cookies.txt https://$APPX_HOST/api/projects

# 6. Logout clears session (DELETE /api/session)
curl -k -c cookies.txt -b cookies.txt -X DELETE https://$APPX_HOST/api/session
curl -k -b cookies.txt https://$APPX_HOST/api/projects  # → 401
```

### SPA Routing

```bash
# Client-side routes → 200 + index.html (SPA fallback)
curl -k https://$APPX_HOST/login
curl -k https://$APPX_HOST/dashboard

# Static asset → 200 + asset content (not index.html)
curl -k https://$APPX_HOST/assets/index-*.js

# API routes are not caught by SPA handler
curl -k https://$APPX_HOST/api/projects  # → 401, not index.html
```

### Security Headers

```bash
curl -kI https://$APPX_HOST/
# Expect all of:
#   strict-transport-security: max-age=63072000; includeSubDomains
#   x-frame-options: DENY
#   x-content-type-options: nosniff
#   content-security-policy: default-src 'self'; ...
#   referrer-policy: strict-origin-when-cross-origin
```

### Session Persistence

```bash
# 1. Log in and save cookie
curl -k -c cookies.txt -X POST https://$APPX_HOST/api/login \
  -H "Content-Type: application/json" -d '{"password":"<your-password>"}'

# 2. Restart the server process

# 3. Session from step 1 should still be valid
curl -k -b cookies.txt https://$APPX_HOST/api/projects  # → 200, not 401
```

### Rate Limiting

```bash
# 7 rapid failed logins — last ones should return 429
for i in {1..7}; do
  curl -k -s -o /dev/null -w "%{http_code}\n" -X POST \
    https://$APPX_HOST/api/login \
    -H "Content-Type: application/json" -d '{"password":"wrong"}'
done
# Expected: 401 401 401 401 401 401 429 (or similar)
```

### Browser (Chrome / Firefox)

- [ ] Login page renders at `/login`
- [ ] Wrong password shows error message
- [ ] Correct password redirects to dashboard at `/`
- [ ] Dashboard shows empty projects list
- [ ] Logout button redirects back to `/login`
- [ ] Navigating directly to `/dashboard` redirects to `/login` if not logged in

### Graceful Shutdown

On the server, run `./appx` then press `Ctrl+C`. The process should log a shutdown message and exit cleanly (exit code 0) without leaving orphaned goroutines or a locked database file.
