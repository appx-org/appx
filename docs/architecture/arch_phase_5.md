# Phase 5 Architecture Reference: De-Docker Simplification

**Branch:** de-docker-refactor  
**Date:** 2026-04-08  
**Status:** Complete (with one pending task: PTY reconnect verification)

---

## Table of Contents

1. [Overview](#1-overview)
2. [System Map](#2-system-map)
   - [Component Diagram](#component-diagram)
   - [Request Flows](#request-flows)
   - [API Endpoint Table](#api-endpoint-table)
   - [Database Changes](#database-changes)
3. [Code Review Guide](#3-code-review-guide)
   - [Project Model](#project-model)
   - [OpenCode Client](#opencode-client)
   - [Egress Package](#egress-package)
   - [Server Routing](#server-routing)
   - [Handlers](#handlers)
   - [Frontend](#frontend)
4. [Testing Guide](#4-testing-guide)
5. [Architecture and Code Pitfalls](#5-architecture-and-code-pitfalls)
6. [Fixed Pitfalls](#6-fixed-pitfalls)
7. [TODOs and Future Improvements](#7-todos-and-future-improvements)

---

## 1. Overview

### The Problem: Per-Project Docker Complexity

Phases 1 through 4 ran a separate Docker container per project, each hosting its own OpenCode instance. Three concrete failure modes drove the redesign:

**Proxy complexity.** OpenCode's SPA calls `fetch('/session')` which resolves relative to the origin root. Phases 1–4 required three iterations to solve URL rewriting: server-side JS patching (fragile against minified variable names), subdomain routing (required per-hostname cert trust for self-signed certs on localhost), and a Service Worker proxy (robust but ~500 lines of complex SW lifecycle code).

**No Docker-in-Docker.** Agents inside containers cannot run `docker run`, blocking them from building containerized apps.

**Lifecycle overhead.** Container create/start/stop/delete/reset/recover-stale-states was the largest code surface in appx — and the one most likely to contain subtle race conditions.

### The Design Shift

Phase 5 replaces N Docker containers with a single `opencode serve` process that manages multiple projects natively. Appx becomes a thin management shell:

```
BEFORE (Phases 1-4):
  appx → N Docker containers → N OpenCode instances → Service Worker proxy

AFTER (Phase 5):
  appx (Go binary: auth + proxy + egress + UI)
    ↕ HTTP (localhost:4096)
  opencode serve (single process, all projects)
    ↕ HTTPS_PROXY
  appx egress proxy (Go CONNECT proxy: logging + allowlist, 127.0.0.1:9080)
```

### Key Decisions

**HTTPS_PROXY + iptables two-layer egress.** `HTTPS_PROXY` is cooperative — cooperative programs (npm, curl, pip, OpenCode itself) route through appx's Go CONNECT proxy for logging and allowlist enforcement. But a compromised agent can bypass it with raw sockets or `--noproxy`. iptables UID-based rules (set by the Phase 6 installer) block all direct outbound from the `opencode` OS user except to localhost, making proxy bypass impossible at the kernel level. The proxy layer provides visibility and user-configurable allowlist; iptables is the hard backstop.

**SDK-based agent UI.** OpenCode's web UI is never served to the browser. Instead, appx builds its own agent interaction components using the `@opencode-ai/sdk` TypeScript SDK. All SDK calls route through appx's `/api/opencode/*` reverse proxy to `localhost:4096`. Benefits: one cohesive UI, shared React components usable across web/mobile/desktop, full design control, and the SPA-at-root proxy problem disappears entirely.

**OS user separation.** `appx` and `opencode` run as separate OS users under systemd. `appx` (system user) owns `/opt/appx/` (binary) and `/var/lib/appx/` (database, TLS certs, config — mode 700). `opencode` (system user) owns `/home/opencode/` (project workspace — mode 700). Standard Unix file permissions prevent the OpenCode process (and any agent it runs) from reading or modifying appx's internals. The `deploy/setup.sh` script creates both users and their directories. `AmbientCapabilities=CAP_NET_BIND_SERVICE` lets appx bind to port 443 without root. Cross-project interference (one agent modifying another project's files) is accepted as low-risk for single-user servers with snapshot-based rollback.

**Appx-assigned ports.** Each project is assigned a unique port from the 10000–10999 range at creation time. The port is embedded in an `AGENTS.md` file scaffolded into the project directory. Agents read this file and start dev servers on the assigned port. Appx health-checks each port via TCP dial and proxies `<name>.<baseDomain>` requests to the running app.

### Trade-offs

**SameSite=Lax vs Strict.** The cookie was changed from `SameSite=Strict` to `SameSite=Lax` with an explicit `Domain=.<baseDomain>`. `Strict` would block the cookie on top-level navigation from a project subdomain to the dashboard. `Lax` allows this while still blocking cross-site POST. CSRF attacks against state-changing endpoints remain impractical because all state-changing endpoints require `Content-Type: application/json`, which triggers CORS preflight on cross-origin requests.

**Accepted cross-project risk.** Without per-project OS isolation, a buggy agent can modify files across projects. For the target deployment model (one dedicated server per user with daily snapshots), this is acceptable. Per-project isolation via OS users is deferred to a future phase.

**Terminal package deferred.** The `internal/terminal/` package (ring buffer, session manager, WebSocket handler) was retained rather than deleted during Phase 5. The Terminal component now connects directly to OpenCode's PTY endpoint (`/api/opencode/pty`), but whether OpenCode supports reconnect with output replay is unverified. The package remains as a fallback until that verification is complete.

---

## 2. System Map

### Component Diagram

```
Browser
  │
  ├─ https://localhost:443/ (or http://localhost:8080 in --http mode)
  │    │
  │    ▼
  │  appx Go binary (single process)
  │  ├── [NEW]  --http flag / --domain flag / self-signed HTTPS
  │  ├── [UPDATED] Auth middleware (SameSite=Lax, Domain=.<base>)
  │  ├── [NEW]  GET /api/config → baseDomain
  │  ├── [UPDATED] /api/* REST API (project CRUD, settings, egress)
  │  ├── [NEW]  GET /api/opencode/health → OpenCode health check
  │  ├── [NEW]  /api/opencode/* → reverse proxy to localhost:4096
  │  ├── [UPDATED] Subdomain dispatch (Host header → assigned port)
  │  ├── [NEW]  Egress CONNECT proxy (127.0.0.1:9080)
  │  └── / → React SPA (embedded)
  │
  ├─ https://<name>.localhost:443/ (or http://<name>.localhost:8080)
  │    │
  │    ▼ appx reverse proxy (from assigned_port in DB)
  │  Agent-built dev server (localhost:10000–10999)
  │
  └─ WebSocket (wss://localhost/api/opencode/pty/:id/connect)
       │
       ▼ appx /api/opencode/* proxy
     opencode serve (localhost:4096)
       │
       ├── Multi-project, multi-session PTY management
       ├── [NEW] HTTPS_PROXY=http://127.0.0.1:9080 (cooperative egress)
       └── [NEW] NO_PROXY=localhost,127.0.0.1 (prevents routing loop)

appx egress proxy [NEW]
  127.0.0.1:9080
  ├── CONNECT allowlist (default: api.anthropic.com:443, registry.npmjs.org:443, proxy.golang.org:443)
  ├── Logs every attempt to egress_log table (synchronous INSERT before response)
  └── DNS rebinding protection (post-dial IP check blocks resolved internal addresses)

Phase 6 (not yet applied):
  iptables UID rules → block all outbound from opencode user except lo
```

Labels: [NEW] = added in Phase 5, [UPDATED] = modified in Phase 5.

### Request Flows

**Dashboard load (`GET /`):**

1. Browser sends `GET / Host: localhost` with `appx_session` cookie.
2. Subdomain dispatcher: `stripPort("localhost") == baseDomain` → serve dashboard.
3. `securityHeaders` middleware injects HSTS, CSP, X-Frame-Options.
4. `auth.Middleware` validates session cookie.
5. `spaHandler` checks `webFS` for the path; falls back to `index.html`.
6. React SPA initializes; calls `GET /api/config` for `baseDomain`, `GET /api/projects` for project list.

**OpenCode API proxy (`GET /api/opencode/session`):**

1. Browser sends `GET /api/opencode/session Host: localhost`.
2. `mux` matches `/api/opencode/` pattern — more-specific `/api/opencode/health` pattern checked first.
3. `auth.Middleware` validates session cookie.
4. `openCodeProxyHandler` strips `/api/opencode` prefix, clears `Cookie` header, sets `FlushInterval=-1` for SSE.
5. `httputil.ReverseProxy` forwards to `http://localhost:4096/session`.

**App subdomain routing (`GET / Host: myapp.localhost:8080`):**

1. Subdomain dispatcher: `host != baseDomain`, checks suffix `.localhost`.
2. Extracts `projectName = "myapp"`, calls `pm.Store.GetByName("myapp")`.
3. `auth.Middleware` validates session cookie (cookie Domain=`.localhost` makes it available here).
4. Constructs `http://127.0.0.1:<assignedPort>`, creates `httputil.ReverseProxy` with `Cookie` header stripped and shared `subdomainTransport`.
5. Proxies request to agent-built dev server. No security headers injected (the app sets its own).

**Egress proxy chain (OpenCode calls `api.anthropic.com:443`):**

1. OpenCode (started with `HTTPS_PROXY=http://127.0.0.1:9080`) sends `CONNECT api.anthropic.com:443 HTTP/1.1` to proxy.
2. `Proxy.ServeHTTP`: splits host/port, calls `store.IsAllowed("api.anthropic.com", 443)` — O(1) in-memory map.
3. Calls `store.LogEntry("api.anthropic.com", 443, true)` — synchronous INSERT to `egress_log`.
4. Dials `api.anthropic.com:443` (TCP), checks resolved IP is not loopback/private (DNS rebinding protection).
5. Hijacks the client connection, writes `200 Connection Established`, pipes bidirectionally with 30-minute deadline.
6. For blocked requests: same flow but `store.LogEntry(..., false)`, returns 403, no dial.

### API Endpoint Table

| Method | Path | Auth | Request Body | Response |
|--------|------|------|-------------|----------|
| `POST` | `/api/login` | None (rate-limited) | `{"password":"..."}` | `{"status":"ok"}` + session cookie |
| `DELETE` | `/api/session` | Cookie | — | `{"status":"ok"}` + clears cookie |
| `GET` | `/api/projects` | Cookie | — | `[Project, ...]` with `appRunning`, `projectDir` |
| `POST` | `/api/projects` | Cookie | `{"name":"slug"}` | `Project` (201) |
| `GET` | `/api/projects/{id}` | Cookie | — | `Project` with `projectDir` |
| `DELETE` | `/api/projects/{id}` | Cookie | — | 204 |
| `GET` | `/api/config` | Cookie | — | `{"baseDomain":"..."}` |
| `PUT` | `/api/settings/password` | Cookie | `{"currentPassword":"...","newPassword":"..."}` | `{"status":"ok"}` + new cookie |
| `GET` | `/api/settings/api-key` | Cookie | — | `{"set":true/false}` |
| `PUT` | `/api/settings/api-key` | Cookie | `{"key":"sk-ant-..."}` | `{"status":"ok"}` |
| `DELETE` | `/api/settings/api-key` | Cookie | — | `{"status":"ok"}` |
| `GET` | `/api/settings/terminal-buffer-size` | Cookie | — | `{"value":N}` |
| `PUT` | `/api/settings/terminal-buffer-size` | Cookie | `{"value":N}` (64–4096) | `{"status":"ok"}` |
| `GET` | `/api/opencode/health` | Cookie | — | `{"healthy":true/false}` |
| `/api/opencode/*` | (any) | Cookie | Proxied as-is | Proxied from OpenCode |
| `GET` | `/api/egress/log` | Cookie | `?limit=50&offset=0` | `{"entries":[...],"total":N}` |
| `GET` | `/api/egress/allowlist` | Cookie | — | `{"entries":["host:port",...]}` |
| `PUT` | `/api/egress/allowlist` | Cookie | `{"entries":["host:port",...]}` | `{"status":"ok"}` |

Notes:
- All non-GET protected endpoints require `Content-Type: application/json` (enforced by `requireJSON` middleware).
- `/api/opencode/health` is registered on the outer `mux` (not behind `requireJSON`) so it is pattern-matched before the wildcard `/api/opencode/` proxy.
- The `Project` JSON includes: `id`, `name`, `status`, `assignedPort`, `appRunning`, `openCodeProjectId`, `lastError`, `createdAt`, `projectDir`. Fields `appRunning` and `projectDir` are computed at query time, not persisted.
- `PUT /api/egress/allowlist` validates that entries are `host:port` format and rejects loopback addresses (`localhost`, `*.localhost`, `127.x.x.x`, `::1`) and private/link-local IPs.

### Database Changes

**Migration 4** (`internal/db/migrations/000004_project_model.up.sql`):

```sql
ALTER TABLE projects ADD COLUMN assigned_port INTEGER;
ALTER TABLE projects ADD COLUMN opencode_project_id TEXT;
CREATE UNIQUE INDEX idx_assigned_port_unique ON projects(assigned_port) WHERE assigned_port IS NOT NULL;
```

The Docker-specific columns (`container_id`, `container_secret`, `network_id`, `image_name`, `resources`) are retained in the schema but ignored by all Phase 5 code. Dropping them via `ALTER TABLE DROP COLUMN` would be safe with modernc.org/sqlite but was deferred to avoid unnecessary migration risk.

`assigned_port`: auto-allocated from 10000–10999 at project creation. Unique non-null index prevents two projects sharing a port. The allocation runs inside a `BEGIN IMMEDIATE` transaction (`nextAvailablePortTx`) to prevent TOCTOU races under concurrent creates.

`opencode_project_id`: nullable TEXT. Populated after OpenCode discovers the project (the first session creation targeting the project directory causes OpenCode to generate a stable project ID from the git root commit hash). Updated via `store.SetOpenCodeProjectID`.

**Migration 5** (`internal/db/migrations/000005_egress_allowed.up.sql`):

```sql
ALTER TABLE egress_log ADD COLUMN allowed BOOLEAN NOT NULL DEFAULT 1;
```

Adds the `allowed` column to the `egress_log` table (which existed since Phase 1) to record whether each connection was permitted or blocked. The `DEFAULT 1` ensures old rows read as allowed.

---

## 3. Code Review Guide

Read files in this order to understand the dependency graph.

### Project Model

**`/Users/max/misc/pj/appx/internal/project/project.go`** — Data types and sentinel errors.

`Project` struct: `AssignedPort` (int, from DB) and `OpenCodeProjectID` (string, from DB) are the new Phase 5 fields. `AppRunning` (bool) and `ProjectDir` (string) are computed at query time by handlers, never persisted. The transitional Docker status constants (`StatusStarting`, `StatusStopping`, `StatusError`) are retained for backward compatibility with existing DB rows but are not set by any new code path.

`ErrNoPortAvailable` is new: returned when all 1000 ports in 10000–10999 are allocated. Mapped to HTTP 507 by `handleCreateProject`.

**`/Users/max/misc/pj/appx/internal/project/store.go`** — SQLite CRUD.

`Create` wraps port allocation and INSERT in a single transaction via `nextAvailablePortTx`. This is the fix for the TOCTOU race identified in the retrospective. The column list `projectColumns` is narrowed to Phase 5 fields only, ignoring legacy Docker columns.

`GetByName` is used by the subdomain dispatcher in `router.go` to look up projects from the `Host` header.

`SetOpenCodeProjectID` stores the OpenCode project ID mapping after discovery.

`nextAvailablePort` (non-transactional) remains for backward compatibility but is not called from `Create`. Only `nextAvailablePortTx` (runs inside the transaction opened by `Create`) is used.

**`/Users/max/misc/pj/appx/internal/project/manager.go`** — Filesystem operations.

`Create` inserts the DB record first, then calls `scaffoldProject`. On scaffold failure, calls `os.RemoveAll(projectDir)` before `m.Store.Delete` to prevent orphaned directories. The scaffold sequence: `os.MkdirAll`, write `AGENTS.md` (with `{{name}}`, `{{port}}`, `{{subdomain}}` placeholders replaced), `git init`, `git add .`, `git commit`. Git is required for OpenCode to discover the project via its root-commit-hash-based project ID mechanism.

`ProjectDir(name)` constructs the absolute path from `ProjectRoot + name`. Called by handlers to populate the `ProjectDir` response field.

`agentsTemplate` is the AGENTS.md content. It tells the agent which port to use for dev servers and what subdomain the app will be accessible at.

**`/Users/max/misc/pj/appx/internal/project/health.go`** — TCP port health checker.

`HealthChecker.Check(projects)` dials `127.0.0.1:<assignedPort>` with a 500ms timeout for each project. Returns a `map[string]bool` (project ID to reachability). Called synchronously by `handleListProjects` on every `GET /api/projects` request. The latency cost is bounded: N × 500ms worst case, but in practice each dial either succeeds quickly (TCP SYN/SYN-ACK) or fails fast (connection refused). Projects with port 0 are always reported unhealthy.

### OpenCode Client

**`/Users/max/misc/pj/appx/internal/opencode/client.go`** — HTTP client for OpenCode REST API.

Three methods:
- `HealthCheck()`: `GET /global/health` — returns nil on 200.
- `ListProjects()`: `GET /project` — returns `[]OpenCodeProject`. Used to discover OpenCode project IDs.
- `SetAuth(providerID, apiKey)`: `PUT /auth/:providerID` with body `{"type":"api","key":"<apiKey>"}`. The path parameter carries the provider ID (e.g., `"anthropic"`); the body is a discriminated union where `type="api"` selects the API key variant. This schema was verified against OpenCode's `server.ts:99-129` after the retrospective identified a mismatch in the original implementation.

All methods use a 5-second HTTP client timeout and discard response bodies with `io.LimitReader` (10 MB limit) to prevent memory exhaustion.

**`/Users/max/misc/pj/appx/internal/opencode/startup.go`** — Startup polling.

`WaitForHealthy(ctx, interval)` polls `HealthCheck()` at the given interval. Performs an immediate check before the first tick to minimize latency when OpenCode is already running.

`InjectAPIKey(ctx, pollInterval, apiKey)` combines wait + inject: waits for OpenCode to be healthy, then calls `SetAuth("anthropic", apiKey)`. `SetAuth` failures are logged but not fatal — the user can re-inject via the Settings page without restarting appx.

In `main.go`, this runs in a goroutine with a 2-minute context timeout. Server startup does not block on OpenCode being healthy.

### Egress Package

**`/Users/max/misc/pj/appx/internal/egress/store.go`** — Allowlist management and log persistence.

`Store` holds the allowlist as an in-memory `map[string]bool` protected by a `sync.RWMutex`. The map key format is `"host:port"`. `IsAllowed` is an O(1) read-lock operation — no DB hit on the hot path.

`DefaultAllowlist` permits `api.anthropic.com:443`, `registry.npmjs.org:443`, `proxy.golang.org:443`. Loaded from the `settings` table on `NewStore`; falls back to defaults if the key is absent or the JSON fails to parse.

`SetAllowlist` updates both the in-memory map (under write lock) and the `settings` table (`egress_allowlist` key, JSON-encoded `["host:port",...]`). Atomic from the caller's perspective — no partial state.

`LogEntry` writes a single row to `egress_log`. Called synchronously before the proxy sends any response, ensuring the log is complete even if the process is killed immediately after.

**`/Users/max/misc/pj/appx/internal/egress/proxy.go`** — HTTP CONNECT proxy.

`Proxy.ServeHTTP` accepts only `CONNECT` requests (returns 405 otherwise). Request flow:

1. Parse `host:port` from `r.Host`.
2. Check allowlist via `store.IsAllowed` (in-memory, no lock contention on reads).
3. Call `store.LogEntry` — synchronous, blocks until the INSERT commits.
4. If blocked: return 403.
5. If allowed: `net.DialTimeout("tcp", r.Host, 10s)`.
6. Post-dial IP check: if resolved IP is loopback/private/link-local, close connection and return 403. This is DNS rebinding protection — an allowlisted hostname can be made to resolve to an internal IP between allowlist-check time and dial time.
7. Hijack the HTTP connection, write `200 Connection Established`, pipe bidirectionally (`io.Copy` goroutines) with 30-minute tunnel deadline.

`Proxy.allowInternal` disables the post-dial IP check. Only set to true in tests where the echo server binds to `127.0.0.1`.

`ProxyAddr = "127.0.0.1:9080"` — the default address. Started in `main.go` in a goroutine.

### Server Routing

**`/Users/max/misc/pj/appx/internal/server/server.go`** — Server startup.

`Config` adds `OpenCodeClient *opencode.Client` and `EgressStore *egress.Store` as new fields. Both are passed to `NewRouter`.

`runHTTP` binds to `127.0.0.1` only (not `0.0.0.0`), preventing accidental exposure on public interfaces. Logs a warning. `--http` and `--domain` are mutually exclusive (validated in `Run`).

`runWithCertMagic` handles production HTTPS via Cloudflare DNS-01 challenge. Manages `*.domain` wildcard cert for subdomain routing.

**`/Users/max/misc/pj/appx/internal/server/router.go`** — Route registration.

`NewRouter` registers routes in two muxes: `mux` (outer, handles public routes and the SPA) and `api` (inner, protected by `auth.Middleware`). The subdomain dispatcher wraps the dashboard handler: requests to `baseDomain` go to the dashboard; requests to `<name>.<baseDomain>` are reverse-proxied to the project's assigned port.

Key routing decisions:
- `/api/opencode/health` is registered on `mux` (not `api`) so it bypasses `requireJSON`. It is registered before `/api/opencode/` so Go's `net/http` pattern matching (more-specific wins) routes health checks correctly.
- `/api/opencode/` is registered on `mux` (not `api`) directly with `auth.Middleware` wrapping it. This is because the proxy must handle SSE and WebSocket requests that cannot go through the `requireJSON` check (they don't have `Content-Type: application/json`).
- `openCodeProxyHandler` strips the `/api/opencode` prefix using `path.Clean(strings.TrimPrefix(...))` to prevent path traversal, clears `RawPath`, and sets `FlushInterval=-1` for streaming.
- Subdomain reverse proxies use a shared `subdomainTransport` (connection pooling) and strip the `Cookie` header before forwarding (prevents the appx session cookie from leaking to agent-built apps).

**`/Users/max/misc/pj/appx/internal/server/middleware.go`** — Security middleware.

`requireJSON` now documents `SameSite=Lax` correctly (fixed from the retrospective). The CSRF argument: HTML forms cannot set `Content-Type: application/json`, so state-changing endpoints require JS with explicit headers, which triggers CORS preflight on cross-origin requests. `Lax` was chosen over `Strict` to allow the session cookie on subdomain navigation.

CSP no longer includes `worker-src` (no Service Worker) and `script-src` does not allow `unsafe-inline` (was needed for the SW install overlay in Phase 4).

### Handlers

**`/Users/max/misc/pj/appx/internal/server/project_handlers.go`**

`handleListProjects`: creates a `HealthChecker`, calls `hc.Check(projects)` synchronously, merges `AppRunning` into each project, and populates `ProjectDir` from `pm.ProjectDir(p.Name)`. Returns an empty array (not null) when no projects exist.

`handleCreateProject`: delegates to `pm.Create(name)` which handles DB insert + filesystem scaffold. Returns 507 for `ErrNoPortAvailable`.

`handleGetProject`: populates `ProjectDir` on the response.

No start/stop/reset handlers exist in Phase 5. Container lifecycle is gone.

**`/Users/max/misc/pj/appx/internal/server/settings_handlers.go`**

`handleOpenCodeHealth`: lives in `settings_handlers.go` (minor organizational note). Returns `{"healthy":false}` when `oc == nil` (test case or unconfigured OpenCode).

`handleSetAPIKey`: stores key in settings table, then calls `oc.SetAuth("anthropic", key)` if `oc != nil`. Failures are logged but do not cause a non-200 response (non-fatal).

`handleGetConfig`: returns `{"baseDomain": baseDomain}`. Needed by the frontend to construct correct subdomain URLs in production deployments.

`handleSetTerminalBufferSize` / `handleGetTerminalBufferSize`: retain the setting in the DB (64–4096 KB range) but the setting has no effect on anything in Phase 5 since the terminal now uses OpenCode's PTY directly.

**`/Users/max/misc/pj/appx/internal/server/egress_handlers.go`**

`handleGetEgressLog`: paginates via `?limit` (default 50, max 200) and `?offset` query params. Responds with `{"entries":[...],"total":N}`.

`handleSetAllowlist`: validates each entry as `host:port` using `net.SplitHostPort`. Rejects loopback hostnames (`localhost`, `*.localhost`), loopback IPs, private IPs (`10.x`, `172.16-31.x`, `192.168.x`), and link-local addresses. This prevents the allowlist from becoming a proxy-bypass path to internal services.

### Frontend

**`/Users/max/misc/pj/appx/web/src/api/opencode.ts`**

Singleton OpenCode SDK client. `createOpencodeClient({ baseUrl: '/api/opencode' })` — all SDK calls go through appx's reverse proxy. The browser never connects directly to `localhost:4096`.

**`/Users/max/misc/pj/appx/web/src/api/client.ts`**

New functions added in Phase 5: `getServerConfig()`, `getOpenCodeHealth()`, `getEgressLog()`, `getEgressAllowlist()`, `setEgressAllowlist()`. The `Project` interface now includes `assignedPort`, `appRunning`, `openCodeProjectId`, `projectDir`.

Stale functions (`createSession`, `listSessions`, `deleteSession`) from Phase 3's terminal API were removed as part of the dead-code cleanup.

**`/Users/max/misc/pj/appx/web/src/components/agent/SessionList.tsx`**

Uses `opencode.session.list({ query: { directory: projectDir } })` to fetch sessions scoped to the project directory. New sessions are created with `opencode.session.create({ body: {} })`. The `onSelectSession` callback propagates the selected session ID to the parent (`Project.tsx`).

**`/Users/max/misc/pj/appx/web/src/components/agent/ChatPanel.tsx`**

Subscribes to the OpenCode event stream (`opencode.event.subscribe()`) in a `useEffect`. Filters `message.part.updated` events by `sessionId` and updates message state as text parts stream in. Uses `AbortController` for cleanup on unmount or session change. User messages are appended immediately on send for optimistic UI; assistant messages arrive via the event stream as `TextPart` objects with `messageID` for deduplication/update.

`projectDir` prop is retained in the signature for future use (it will be needed when creating directory-scoped sessions from the chat panel).

**`/Users/max/misc/pj/appx/web/src/components/Terminal.tsx`**

Connects to OpenCode's PTY endpoint:
1. `POST /api/opencode/pty` with `x-opencode-directory: projectDir` header → gets `ptyId`.
2. Opens WebSocket to `/api/opencode/pty/:id/connect`.
3. Sends resize messages as JSON `{"type":"resize","cols":N,"rows":N}`.
4. On non-intentional close: reconnects to the same `ptyId` with exponential backoff (1s base, 8s max, 5 retries).

Caveat: reconnect behavior assumes the PTY session persists across WebSocket disconnects. This is unverified against OpenCode's implementation. If OpenCode terminates the session on disconnect, reconnecting to the same `ptyId` will fail (task #43, pending).

**`/Users/max/misc/pj/appx/web/src/components/OpenCodeStatus.tsx`**

Polls `GET /api/opencode/health` every 10 seconds. Displays a colored dot: green (healthy), red (down), gray (initial load).

**`/Users/max/misc/pj/appx/web/src/components/ProjectCard.tsx`**

Displays project name, assigned port, `appRunning` status badge, and the subdomain link (shown only when `appRunning` is true). The subdomain URL is constructed using `window.location.protocol` and `window.location.port` combined with the project name. Note: the base domain is still hardcoded as `localhost` in `ProjectCard.tsx` — the `baseDomain` from `GET /api/config` is not yet threaded to this component.

**`/Users/max/misc/pj/appx/web/src/pages/Dashboard.tsx`**

Polls `GET /api/projects` every 10 seconds. Displays `OpenCodeStatus` in the header. Navigation includes an "Egress" link (new in Phase 5). No start/stop/reset buttons — projects are always available since OpenCode runs as systemd.

**`/Users/max/misc/pj/appx/web/src/pages/Project.tsx`**

Fetches `GET /api/projects/:id`. Computes `projectDir` from `project.projectDir` (now returned by the API). Renders two tabs: Agent (SessionList + ChatPanel) and Terminal. Subdomain URL is constructed from `window.location` — same limitation as `ProjectCard.tsx` with the hardcoded `localhost`.

**`/Users/max/misc/pj/appx/web/src/pages/Egress.tsx`**

New page. Fetches egress log (paginated, showing first 50 entries) and current allowlist on load. Allows adding and removing allowlist entries. Errors from `PUT /api/egress/allowlist` are displayed inline (the backend validates host/port format and rejects internal addresses).

**`/Users/max/misc/pj/appx/web/src/App.tsx`**

New route: `/egress` → `Egress`. Routes: `/login`, `/`, `/projects/:id`, `/settings`, `/egress`, catch-all redirects to `/`.

---

## 4. Testing Guide

### Coverage Per Package

**`internal/project/store_test.go`** — 14 tests. Covers: `Create` (valid/invalid/duplicate names, auto-port assignment, concurrent creates with distinct ports), `List`, `Get`, `Delete` (including port reuse after delete), `GetByName`, `SetOpenCodeProjectID`, `nextAvailablePort` (empty, gap-filling, sequential, range-exhausted). Notable: `TestCreate_ConcurrentCreates_AllGetDistinctPorts` fires 10 goroutines and asserts all get distinct ports — this is the regression test for the TOCTOU fix.

**`internal/project/manager_test.go`** — 8 tests. Covers: `Create` (directory creation, git init, AGENTS.md content, port in AGENTS.md), `Delete` (directory removal), invalid name propagation, `TestManagerCreate_CleansUpDirectoryOnScaffoldFailure` (read-only directory triggers scaffold failure; asserts no directory and no DB record remain). Also: `TestManagerProjectDir_ReturnsPath` verifies the path helper returns an absolute path ending with the project name.

**`internal/opencode/client_test.go`** — 8 tests. Covers: `HealthCheck` (healthy/unhealthy/connection refused), `ListProjects` (success/empty/server error), `SetAuth` (success — asserts method is PUT, providerID is in the path not body, body contains `type:"api"` and `key`), `NewClient` URL trimming. The `TestSetAuth_Success` test explicitly verifies the request shape that was wrong in the original implementation.

**`internal/opencode/startup_test.go`** — 4 tests. Covers: `WaitForHealthy` (immediate success, eventual success after 2 failures, context cancellation), `InjectAPIKey` (injects when healthy and key is non-empty, skips when key is empty).

**`internal/egress/store_test.go`** — 5 tests. Covers: `LogEntry` (allowed/blocked, ordering), `ListLog` (pagination), `GetAllowlist` (default entries), `SetAllowlist`, `IsAllowed` (port-exact matching).

**`internal/egress/proxy_test.go`** — 5 tests. Covers: allowed CONNECT (tunnels data through echo server), blocked CONNECT (returns 403), log entries (synchronous — no sleep needed), DNS rebinding protection (allowlisted host resolving to 127.0.0.1 is blocked post-dial), non-CONNECT method (returns 405).

**`internal/server/router_test.go`** — ~50 tests. Covers every API endpoint (auth, project CRUD, settings, egress, config), security headers (HSTS absent in HTTP mode, CSP lacks `worker-src` and `unsafe-inline` for scripts), cookie attributes (Domain=.localhost, SameSite=Lax, Secure=false in HTTP mode), subdomain dispatch (base domain → dashboard, unknown subdomain → 404, known subdomain without auth → 401, known subdomain with auth → proxied response), OpenCode proxy (prefix stripping, query string preservation, auth requirement), `GET /api/config` (returns baseDomain, requires auth), `projectDir` field in `GET /api/projects` and `GET /api/projects/:id`, allowlist loopback/private IP blocking.

### Manual Verification Checklist

```
[ ] task build — compiles cleanly
[ ] task test — all tests pass
[ ] ./appx --http --port 8080 starts, logs "WARNING: running in HTTP mode"
[ ] ./appx --http --domain foo.com fails with mutual exclusion error
[ ] http://localhost:8080 serves dashboard, login works
[ ] POST /api/projects {"name":"myapp"} → 201, assignedPort in 10000-10999, AGENTS.md in data/projects/myapp/
[ ] DELETE /api/projects/:id → 204, directory removed from disk
[ ] GET /api/projects → appRunning=false for project with no listener, appRunning=true after starting a server on assigned port
[ ] http://myapp.localhost:8080 without auth → 401
[ ] http://myapp.localhost:8080 with active session → proxied to assigned port
[ ] http://myapp.localhost:8080 on unknown project → 404
[ ] GET /api/config → {"baseDomain":"localhost"} in HTTP mode
[ ] GET /api/opencode/health → {"healthy":false} when OpenCode not running
[ ] PUT /api/settings/api-key {"key":"sk-ant-..."} → 200, key stored in DB
[ ] GET /api/egress/log → {"entries":[],"total":0} initially
[ ] GET /api/egress/allowlist → 3 default entries
[ ] PUT /api/egress/allowlist {"entries":["localhost:4096"]} → 400
[ ] PUT /api/egress/allowlist {"entries":["10.0.0.1:443"]} → 400
[ ] Egress page renders, allowlist editable, add/remove works
[ ] Project page: Agent tab shows session list, new session creates, chat sends
[ ] Project page: Terminal tab connects to OpenCode PTY
[ ] Cookie: Domain=.localhost, SameSite=Lax, HttpOnly=true
```

---

## 5. Architecture and Code Pitfalls

These are the issues identified in the Phase 5 retrospective that were NOT yet fixed at the time of writing. Read the "Fixed Pitfalls" section to see which were addressed on the branch.

### Pitfall: Terminal package orphaned (resolved — see Fixed Pitfalls)

The `internal/terminal/` package (ring buffer, session manager, WebSocket handler) was retained in the codebase after Phase 5 even though no routes use it. The `GET /ws/term/:id` endpoint no longer exists. Three stale exports in `client.ts` (`createSession`, `listSessions`, `deleteSession`) targeted endpoints that no longer exist. The `bufSizeKB` variable in `main.go` was computed but immediately discarded.

**Status:** Cleaned up on this branch (task #35 completed).

### Pitfall: PTY reconnect behavior unverified (pending)

`Terminal.tsx` reconnects to the same `ptyId` on WebSocket disconnect. This assumes OpenCode's PTY session persists across WebSocket disconnects and is reconnectable. If OpenCode terminates the session on disconnect (common behavior for PTY implementations), reconnect attempts will fail silently after 5 retries, showing "Connection lost" with no useful explanation.

The original `internal/terminal/` package handled this with a ring buffer that replayed the last N bytes on reconnect. That buffer is now effectively bypassed.

**Status:** Pending — task #43. Resolution requires checking OpenCode's PTY implementation (`packages/opencode/src/pty/`). If sessions do not survive disconnect, the reconnect logic must call `createPty()` to get a fresh session rather than reusing the old ID.

### Pitfall: projectDir and baseDomain exposed via API but not fully consumed

`GET /api/projects` and `GET /api/projects/:id` now return `projectDir`. `GET /api/config` returns `baseDomain`. Both were added to fix issues where the frontend hardcoded `/home/opencode/projects/` and `localhost` respectively. However:

- `ProjectCard.tsx` still constructs subdomain URLs with hardcoded `localhost` (uses `window.location.host` for port but hardcodes the base domain part).
- `Project.tsx` still constructs the subdomain URL with hardcoded `localhost`.

The `project.projectDir` field is consumed by `Project.tsx` for terminal and agent directory — this part is fixed. The `baseDomain` from `/api/config` is not yet threaded to components that construct subdomain URLs.

**Status:** The backend fixes (task #33 and #34) are complete. The frontend components still have hardcoded `localhost` for subdomain URL construction. This is a known remaining issue.

---

## 6. Fixed Pitfalls

These issues were identified in the Phase 5 retrospective and resolved on the `de-docker-refactor` branch.

### Fixed: SetAuth schema mismatch (task #38)

**Original problem:** `client.go` sent `{"providerId":"anthropic","apiKey":"..."}` as a flat object. The actual OpenCode API is `PUT /auth/:providerID` with body `{"type":"api","key":"..."}` (discriminated union, provider ID in path).

**Fix:** `SetAuth` now uses `http.MethodPut`, path `c.baseURL + "/auth/" + providerID`, body `{"type":"api","key":"..."}`. Verified against OpenCode source (`server.ts:99-129`).

**Test:** `TestSetAuth_Success` in `client_test.go` asserts the method is PUT, `gotProvider` is extracted from the path (not the body), and the body contains `type:"api"` and `key`.

### Fixed: Egress async log → synchronous (task #36)

**Original problem:** `store.LogEntry` was called in a goroutine (`go func() { ... }()`). For blocked requests, the 403 response was sent before the log write committed. Under load or on process kill, log entries could be lost.

**Fix:** `store.LogEntry` is now called synchronously before any response is sent. The goroutine was removed. `TestProxy_LogsEntries` no longer needs a `time.Sleep`.

### Fixed: Port allocator TOCTOU race (task #32)

**Original problem:** `nextAvailablePort` and `INSERT` were separate statements with no transaction. Concurrent creates could read the same available port and race to insert it; the loser would get a UNIQUE constraint error mapped to a misleading `ErrDuplicateName`.

**Fix:** `Store.Create` opens a transaction with `db.Begin()`, calls `nextAvailablePortTx(tx)` inside it, and commits after the INSERT. The UNIQUE constraint error on `assigned_port` is now distinguished from the one on `name` by checking for `"projects.name"` in the error string.

**Test:** `TestCreate_ConcurrentCreates_AllGetDistinctPorts` — 10 goroutines, all succeed, all get distinct ports.

### Fixed: Scaffold cleanup on failure (task #39)

**Original problem:** If `scaffoldProject` failed after `MkdirAll` (e.g., git not installed), the partial directory was left on disk. Subsequent creates with the same name would partially reinitialize.

**Fix:** `Manager.Create` calls `os.RemoveAll(projectDir)` before `m.Store.Delete(proj.ID)` in the error path.

**Test:** `TestManagerCreate_CleansUpDirectoryOnScaffoldFailure` — makes projectRoot read-only, asserts no directory and no DB record after failure.

### Fixed: Stale client.ts exports (task #35)

**Original problem:** `client.ts` exported `createSession`, `listSessions`, `deleteSession` targeting `/api/projects/:id/sessions` — endpoints that no longer exist. `bufSizeKB` in `main.go` was computed but unused.

**Fix:** Removed the three stale session functions from `client.ts`. Removed `bufSizeKB` dead code from `main.go`.

### Fixed: SameSite=Strict comment stale (task #37)

**Original problem:** `requireJSON` middleware comment referenced `SameSite=Strict` after Phase 5 changed cookies to `SameSite=Lax`. `auth/store.go` had the same stale reference.

**Fix:** Comments updated to reference `SameSite=Lax` and explain why CSRF is still mitigated under Lax (HTML forms cannot set `Content-Type: application/json`; POST from another origin triggers preflight).

### Fixed: Loopback allowlist validation (task #41)

**Original problem:** `handleSetAllowlist` did not validate that entries were not loopback addresses. A user could add `localhost:4096` to the allowlist, creating a proxy path to the OpenCode server from the agent's perspective.

**Fix:** `handleSetAllowlist` now calls `net.SplitHostPort` and rejects entries where host is `localhost`, any `.localhost` subdomain, or resolves to a loopback/private/link-local IP.

**Test:** `TestPutAllowlist_BlocksLoopback` and `TestPutAllowlist_BlocksPrivateIPs` in `router_test.go`.

### Fixed: GET /api/config and projectDir in API response (tasks #33, #34)

**Original problem:** Frontend hardcoded `localhost` for subdomain URLs and `/home/opencode/projects/<name>` for project directories. Both would break in non-localhost deployments.

**Fix:** Added `GET /api/config` endpoint returning `{"baseDomain":"..."}`. Added `projectDir` field to `Project` struct, populated by `pm.ProjectDir(p.Name)` in `handleListProjects` and `handleGetProject`.

**Tests:** `TestGetConfig_ReturnsDomain`, `TestGetConfig_RequiresAuth`, `TestGetProject_HasProjectDir`, `TestListProjects_HasProjectDir` in `router_test.go`. `Project.tsx` now uses `project.projectDir` for terminal/agent directory.

### Fixed: Phase 6 plan rewritten (task #40)

**Original problem:** `docs/plans/phase_6_plan.md` described the pre-Phase-5 Docker architecture (React Native proxying through `/api/agent/:name/*`, container lifecycle, `OPENCODE_SERVER_PASSWORD`).

**Fix:** Rewrote `phase_6_plan.md` to describe the correct Phase 6: installer (`install.sh`), OS user separation (appx/opencode), iptables egress enforcement, bearer token auth for native clients, rootless Docker setup.

---

## 7. TODOs and Future Improvements

### Pending: Terminal PTY reconnect verification (task #43)

`Terminal.tsx` reconnects to the same `ptyId` after disconnect. Needs verification that OpenCode's PTY sessions survive WebSocket disconnects. If they do not, the reconnect logic must call `createPty()` for a fresh session. Until this is verified, the terminal will silently fail to reconnect after a network blip.

Verification path: inspect `packages/opencode/src/pty/` in the OpenCode repository, or test empirically by disconnecting the WebSocket while a shell is active and observing whether the reconnect gets the same shell state.

### Phase 6 Prerequisites

Phase 6 (installer + security hardening) requires the following groundwork from Phase 5 to be in place (all complete):

- **Egress proxy running and logging** — done. The proxy listens on `127.0.0.1:9080`. Phase 6 adds iptables rules that make it the mandatory path for the `opencode` OS user. See `docs/architecture/egress_iptables.md` for the exact iptables rules.
- **`auth.Middleware` has a single entry point** — adding bearer token support (check `Authorization: Bearer` before falling back to the session cookie) is a small, clean change. Required for native clients (React Native, desktop) that cannot use cookies.
- **`baseDomain` exposed via `/api/config`** — the installer must configure the correct baseDomain at deploy time. Native clients use this to route SDK calls through the correct proxy URL.
- **`projectDir` in API response** — the installer sets `--data /var/lib/appx` which sets `projectRoot = /var/lib/appx/projects`. The frontend now uses `project.projectDir` from the API rather than a hardcoded path, so it will work correctly after the installer runs.

### Subdomain URL Construction (frontend debt)

`ProjectCard.tsx` and `Project.tsx` still construct subdomain URLs with hardcoded `localhost`. The correct approach is to call `GET /api/config` once on app load (or in a context), cache the `baseDomain`, and use it everywhere. This must be resolved before Phase 7 (hosted service with real domains like `myapp.username.appx.app`).

### Per-Project Allowlists (future)

The current egress allowlist is global — all projects share it. Phase 5 plan deferred per-project allowlists. The `egress_log` table has a `project_id` column (from the original Phase 1 schema) that was never populated. When per-project allowlists are implemented, the proxy would need to know which project a CONNECT request originates from — which requires either process tagging (via cgroups or SO_PASSCRED) or a per-session proxy credential.

### Terminal Buffer / Replay

The terminal buffer size setting (stored in DB, exposed via `GET/PUT /api/settings/terminal-buffer-size`) has no effect in Phase 5. If OpenCode's PTY is confirmed to not support reconnect replay, appx should either:
- Remove the setting and delete the buffer size handlers, or
- Implement a thin WebSocket proxy in `internal/terminal/` that wraps the OpenCode PTY connection and adds the ring buffer replay on top.
