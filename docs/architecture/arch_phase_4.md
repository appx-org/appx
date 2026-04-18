# Phase 4 Architecture: Reverse Proxy + AI Agent Web UI

## Table of Contents

- [Overview](#overview)
- [System Map](#system-map)
  - [Component Diagram](#component-diagram)
  - [Request Flow Diagrams](#request-flow-diagrams)
  - [API Endpoints](#api-endpoints)
  - [Database Changes](#database-changes)
- [Code Review Guide](#code-review-guide)
  - [Data Layer: Container Secret](#data-layer)
  - [Container Layer: Port Publishing + ContainerAddr](#container-layer)
  - [Proxy Package: Service Worker Approach](#proxy-package)
  - [Server Layer: Routing + Middleware + Auth](#server-layer)
  - [Security Hardening](#security-hardening)
  - [Frontend: Open Button + Agent Tab](#frontend)
- [Testing Guide](#testing-guide)
  - [Automated Coverage](#automated-coverage)
  - [Manual Verification Checklist](#manual-verification-checklist)
- [Architecture and Code Pitfalls](#architecture-and-code-pitfalls)
- [Fixed Pitfalls](#fixed-pitfalls)
- [TODOs and Future Improvements](#todos-and-future-improvements)

---

## Overview

Phase 4 adds a reverse proxy that routes browser traffic to services running inside Docker containers. It solves two problems:

1. **Users need to interact with the AI coding agent through its web UI.** OpenCode's TUI cannot render through Docker exec PTY. Its `serve` mode provides a full browser-based interface.

2. **Users need to access web apps they build inside containers** without exposing container ports directly on the host.

### The Design Problem: Making a Path-Prefixed SPA Work

The central challenge is that OpenCode's web UI is a Vite-built SPA that assumes it owns the origin root. It constructs API URLs from `location.origin`, Vite's build tooling generates asset preload helpers that prepend `/` to all paths, and dynamic imports use paths relative to the module's location. Serving the SPA at a path prefix like `/agent/my-project/` breaks all three.

**Three approaches were attempted, each building on lessons from the last:**

**Attempt 1 — Path prefix with server-side content rewriting:** Serve at `/agent/:name/`, rewrite HTML asset paths (`="/` → `="/agent/:name/`), patch the JS bundle to fix `location.origin` and Vite's preload helper. This worked but proved fragile: two separate JS patches were needed, both matched specific minified variable names that could silently change on any OpenCode update. Abandoned.

**Attempt 2 — Subdomain routing:** Serve each project at `<name>.localhost:<port>`. The SPA runs at the origin root, no rewriting needed. This worked architecturally but introduced a fatal UX problem: Chrome requires a separate certificate trust click-through for every new subdomain hostname, even with a `*.localhost` wildcard SAN in the self-signed cert. Users creating multiple projects would face repeated browser warnings with no streamlined path around them.

**Attempt 3 — Service Worker proxy (current):** Serve at `/agent/:name/` (path prefix, same origin, no cert issues), but use a browser-installed Service Worker to intercept all `fetch()` calls from the SPA at the network boundary and rewrite them before they go out. The SW doesn't touch the JS source — it intercepts compiled runtime calls. This is robust to OpenCode updates.

### How the Service Worker Approach Works

```
Page loads from /agent/test3/
  │
  ├── HTML has asset paths already rewritten (="/..." → ="/agent/test3/...")
  │   This handles the initial static load — no JS rewriting needed.
  │
  ├── SW registration script (injected into HTML) installs the SW
  │   First visit: brief "starting…" overlay while SW installs (~100-300ms)
  │   Subsequent visits: SW already active, no delay
  │
  └── SW intercepts ALL fetch() calls from the SPA:
       SPA calls: fetch('/session')         → SW rewrites → /api/agent/test3/session
       SPA calls: fetch('/assets/chunk.js') → SW rewrites → /agent/test3/assets/chunk.js
       SPA calls: fetch('https://ext.com')  → SW passes through (external origin)
```

WebSocket connections (`new WebSocket(...)`) are not interceptable by Service Workers (browser spec limitation). These are handled by a small injected script that wraps `window.WebSocket` to rewrite URLs at construction time.

The 401 session-expiry problem: when the user's session expires, `AgentAPIHandler` returns 401 with `X-Appx-Auth: required`. The SW detects this header and navigates all controlled windows to `/login`. Container-level 401s (wrong `OPENCODE_SERVER_PASSWORD`) don't include this header, so they don't mis-trigger a logout.

---

## System Map

### Component Diagram

```
Browser at localhost:8443 (appx dashboard)
  |
  |-- GET /                          --> React SPA (embedded in Go binary)
  |-- POST /api/login                --> rate-limited login handler
  |-- GET  /api/projects             --> project list
  |-- PATCH /api/projects/{id}       --> [NEW Phase 4] port editing
  |-- GET  /ws/term/{id}             --> terminal WebSocket (Phase 3)
  +-- GET  /apps/{name}/*            --> [NEW] ProxyHandler → container user-app port
  |
  |-- GET  /agent/{name}/            --> [NEW] AgentUIHandler
  |     |   serves rewritten HTML (="/... → ="/agent/:name/...)
  |     |   injects SW registration script + loading overlay
  |     |   injects WebSocket URL patcher
  |     |   caches HTML server-side (clears on container start)
  |     |
  |     +-- GET /agent/{name}/sw.js  --> [NEW] dynamically generated Service Worker
  |     |
  |     +-- GET /agent/{name}/assets/*  --> [NEW] proxied from container:4096
  |
  +-- * /api/agent/{name}/*          --> [NEW] AgentAPIHandler
        |   strips /api/agent/:name prefix
        |   strips appx_session cookie
        |   injects Authorization: Basic opencode:<secret>
        |   handles SSE (/global/event) and WebSocket (/pty/*/connect)
        |
        +-- container (appx-<name>)
              +-- opencode serve :4096 on 127.0.0.1 [NEW: was 0.0.0.0]
                    port published to 127.0.0.1:<random-host-port> [NEW]
```

**Host-based port access (why port publishing matters):**
```
Docker for Mac/Windows: containers run in a VM.
Bridge-network IPs (172.x.x.x) are unreachable from the host.

Solution: publish container ports to 127.0.0.1:<auto-assigned>
  container:4096  → 127.0.0.1:63404  (AgentAPIHandler + AgentUIHandler)
  container:PORT  → 127.0.0.1:52000  (ProxyHandler for user apps)
```

**Service Worker data flow:**
```
SPA fetch call                     Service Worker intercepts
─────────────────────────────────────────────────────────────
fetch('/session')              →   /api/agent/test3/session    (API)
fetch('/global/event')         →   /api/agent/test3/global/event (SSE)
fetch('/assets/chunk.js')      →   /agent/test3/assets/chunk.js (asset)
fetch('/favicon-v3.ico')       →   /agent/test3/favicon-v3.ico
fetch('/api/...')              →   pass through (already an API route)
fetch('/ws/...')               →   pass through (terminal WS)
fetch('https://extern.al')     →   pass through (external origin)

WebSocket (not interceptable by SW — patched at construction):
new WebSocket('wss://localhost:8443/pty/id') → /api/agent/test3/pty/id
```

### Request Flow Diagrams

**First page load (HTML + SW install):**

```
Browser: GET /agent/test3/
  → auth.Middleware: check appx_session cookie → pass
  → AgentUIHandler:
      cache miss → ContainerAddr(test3, 4096) → "127.0.0.1:63404"
      fetchClient.Do(GET http://127.0.0.1:63404/)
      rewriteHTML():
        - ="/... → ="/agent/test3/...
        - inject SW registration script + loading overlay
        - inject WebSocket URL patcher
      cache.SetHTML("test3", modified)
      → respond with modified HTML

Browser receives HTML, parses it:
  - Assets load from /agent/test3/assets/... → hit AgentUIHandler → proxied
  - SW registration script runs → installs /agent/test3/sw.js
  - Loading overlay shown until SW activates (~100-300ms first visit)
  - sw.js fetched → auth passes (same origin) → dynamic SW returned

SW activates (skipWaiting + clients.claim):
  - Loading overlay removed
  - SPA initializes, all subsequent fetches intercepted by SW
```

**Subsequent API call (through SW):**

```
SPA: fetch('/session')
  → SW intercepts, rewrites to /api/agent/test3/session
  → Browser: GET /api/agent/test3/session
  → auth.Middleware: session cookie → valid
  → AgentAPIHandler:
      strip Cookie header
      inject Authorization: Basic opencode:<secret>
      ContainerAddr(test3, 4096) → "127.0.0.1:63404" (from cache)
      → reverse proxy → http://127.0.0.1:63404/session
      → response forwarded to browser (CORS headers stripped)
```

**Session expiry recovery:**

```
SPA: fetch('/session')
  → SW rewrites → /api/agent/test3/session
  → auth.Middleware: cookie missing/expired → 401 + X-Appx-Auth: required
  → SW: detects 401 + X-Appx-Auth header
  → clients.matchAll({type:"window"}) → all windows navigate to /login
```

### API Endpoints

| Method | Path | Auth | Request | Response | Notes |
|--------|------|------|---------|----------|-------|
| PATCH | `/api/projects/{id}` | session | `{"port": 8080}` | Updated project JSON | 400/404/409 |
| GET | `/agent/{name}/` | session | — | Rewritten OpenCode HTML | 404/503/502 |
| GET | `/agent/{name}/sw.js` | session | — | Service Worker JS | dynamic, no-cache |
| GET | `/agent/{name}/*` | session | — | Asset proxied from container | 404/503 |
| ANY | `/api/agent/{name}/*` | session | any | Proxied to container:4096 | SSE + WS |
| ANY | `/apps/{name}/*` | session | any | Proxied to container:port | user apps |

All proxy routes bypass `limitBody` (streaming) and `requireJSON`, but request bodies are capped at 100MB by `http.MaxBytesReader`.

### Database Changes

**Migration 3** (`000003_proxy.up.sql`):

| Column | Type | Default | Purpose |
|--------|------|---------|---------|
| `container_secret` | TEXT NOT NULL | `''` | Random 32-byte hex; injected as `OPENCODE_SERVER_PASSWORD`; forwarded by proxy as Basic Auth |

---

## Code Review Guide

### Data Layer

**`internal/project/project.go`** — `ContainerSecret string` added with `json:"-"`. Never exposed in API responses. Generated at project creation via `generateSecret()` (32 bytes from `crypto/rand`), rotated on Reset.

**`internal/project/store.go`** — New methods:
- `GetByName(name)` — proxy handlers resolve project from URL path
- `UpdatePort(id, port)` — PATCH endpoint, validates 1-65535
- `SetContainerSecret(id, secret)` — used by Reset rotation

### Container Layer

**`internal/project/container.go`** — Three significant changes:

**Port publishing.** Both user-app port (`proj.Port`) and agent port (4096) are published to auto-assigned host ports bound to `127.0.0.1`. This makes containers reachable from the host on all platforms (Docker Desktop on macOS/Windows runs containers in a VM where bridge IPs are unreachable):

```go
portBindings := network.PortMap{
    appPort:   []network.PortBinding{{HostIP: localhost}},
    agentPort: []network.PortBinding{{HostIP: localhost}},
}
```

**`ContainerAddr(id, containerPort)`** replaces the old `ContainerIP`. Instead of returning a bridge-network IP (only routable on Linux), it inspects the container's port bindings and returns `"127.0.0.1:<hostPort>"`. Includes an in-memory cache (keyed by `containerID:containerPort`, invalidated on stop/delete) to avoid a Docker inspect call on every proxied request.

**`startHook`** — a `func(projectName string)` registered by `main.go` to call `AssetCache.Clear` when a container transitions to running. Ensures cached HTML is invalidated after a container rebuild.

**What to verify:**
- Port cache is invalidated in both `doStop` (via `portCacheMu.Lock()`) and `Delete`. What about the cleanup path in `doFullCreate` if `SetRunning` fails? The container is removed, so the cache key (based on `containerID`) becomes unreachable — no incorrect data is served, just a stale in-memory entry. Acceptable.
- Does `opencode serve` accept connections when bound to `127.0.0.1`? Yes — Docker's port publishing operates at the network layer before the container's loopback restriction.

### Proxy Package

**`internal/proxy/assets.go`** — `AssetCache` with `sync.RWMutex`. Two maps: per-project HTML and shared assets. `Clear(_ string)` ignores the project name and wipes everything — conservative but correct, because a base image rebuild changes asset hashes across all projects.

**`internal/proxy/proxy.go`** — The core of Phase 4. Five functions form the pipeline:

**`rewriteHTML(html, projectName)`** rewrites the HTML that OpenCode's container serves before caching it. Two transformations:
1. `strings.ReplaceAll(html, '="/', '="/agent/'+projectName+'/')` — rewrites all attribute values that start with `/` so static asset links work on first load, before the SW is installed
2. Injects two `<script>` tags before `</head>`:
   - SW registration with loading overlay (prevents unintercepted requests during first-visit SW installation)
   - WebSocket URL patcher (wraps `window.WebSocket` constructor to rewrite WS URLs through `/api/agent/:name/`)

Note: project names are validated slugs (`[a-z][a-z0-9-]+`) and cannot contain HTML/JS special chars, so direct string embedding in scripts is safe.

**`generateSWScript(projectName)`** returns the Service Worker JavaScript with the project name baked in as a JSON literal:
```javascript
const PROJECT="test3";
const SCOPE="/agent/test3/";
const API="/api/agent/test3";
self.addEventListener("install", e => e.waitUntil(skipWaiting()));  // take over immediately
self.addEventListener("activate", e => e.waitUntil(clients.claim())); // control existing pages
self.addEventListener("fetch", e => {
  // rewrite same-origin, not-already-prefixed requests
  // assets → /agent/test3/...
  // everything else → /api/agent/test3/...
  // 401 with X-Appx-Auth: required → navigate to /login
});
```

`skipWaiting()` ensures that when OpenCode or appx updates, the new SW version activates immediately rather than waiting for all tabs to close.

**`serveAgentHTML`** and **`serveAgentAsset`** — server-side fetchers that pull content from the container, cache it, and serve it. Both use `fetchClient` (15s timeout, shared transport) and `io.LimitReader(resp.Body, maxFetchSize)` (50MB cap) to prevent a malicious container from exhausting server memory.

**`AgentUIHandler`** — cache-first serving. Cached HTML is served even when the container is stopped (graceful degradation). Cache miss requires a running container. SW file (`/sw.js`) is always served fresh (`Cache-Control: no-cache`) so new SW versions activate promptly.

**`AgentAPIHandler`** — transparent reverse proxy for `/api/agent/:name/*`. Strips prefix, strips session cookie, injects Basic Auth, handles WebSocket upgrades via `proxyWebSocket`. Also the endpoint that Phase 6 native clients will call with bearer tokens.

**What to verify:**
- Does the cookie stripping happen before the WebSocket branch? Yes — `r.Header.Del("Cookie")` runs before the `if Upgrade == websocket` check in `AgentAPIHandler`. The cookie never reaches the container via WS either.
- `newReverseProxy`'s `ModifyResponse` strips `Access-Control-*` and `Vary` headers. The browser sees same-origin responses — these headers are irrelevant and caused browser caching confusion when forwarded.
- The SW's 401 handler checks `resp.headers.get("X-Appx-Auth") === "required"`. Only appx's `auth.Middleware` sets this header. Container-level 401s (wrong `OPENCODE_SERVER_PASSWORD`) don't include it, so they don't trigger a login redirect.

### Server Layer

**`internal/server/router.go`** — Flat `http.ServeMux` with path-based routing. No subdomain dispatch. Key routes:

```go
mux.Handle("/agent/",     a.Middleware(proxy.AgentUIHandler(pm, cache, proxy.AgentPort)))
mux.Handle("/api/agent/", a.Middleware(proxy.AgentAPIHandler(pm, proxy.AgentPort)))
mux.Handle("/apps/",      a.Middleware(proxy.ProxyHandler(pm)))
```

All three are behind auth but NOT behind `limitBody` (streaming) or `requireJSON` (not JSON-based). Request bodies are limited by `http.MaxBytesReader` in the handlers themselves (100MB for proxy, handled before any proxying occurs).

`/api/agent/` is registered on the outer mux, not the inner `api` submux, so it does NOT go through `requireJSON`. This is correct — OpenCode API calls don't set `Content-Type: application/json`.

**`internal/server/middleware.go`** — `securityHeaders` uses URL path prefix to choose CSP:

- `/agent/` and `/api/agent/` routes: permissive CSP with `'unsafe-inline'` (OpenCode uses inline scripts), `worker-src 'self'` (required for SW registration), `wss:`/`ws:` (SSE and WebSocket)
- All other routes: strict CSP + `X-Frame-Options: DENY`

**`internal/auth/auth.go`** — `AuthRequiredHeader = "X-Appx-Auth"` is exported so the router or future middleware can set it consistently. The `Middleware` function sets it on both 401 response paths (missing cookie + invalid session). The SW reads this header to distinguish appx auth failures from container auth failures.

`SetSessionCookie` uses `SameSite=Strict` with no `Domain` attribute (scoped to exact hostname). This is correct because all requests within appx (dashboard, agent UI, API) are same-origin — there are no subdomains in the current architecture.

### Security Hardening

Several security measures were added during Phase 4 alongside the proxy implementation:

**Auth (`internal/auth/store.go`):**
- `bcryptCost = 12` (was Go default 10, OWASP 2023 recommendation)
- `minPasswordLen = 12` — `SetPassword` rejects shorter passwords with a clear error

**Proxy (`internal/proxy/proxy.go`, `ws.go`):**
- `maxProxyBodyBytes = 100MB` — applied via `http.MaxBytesReader` in both `ContainerHandler` and `ProxyHandler`
- `wsIdleTimeout = 10 * time.Minute` — `deadlineConn` wrapper resets deadline on every Read/Write; abandoned WS tunnels are cleaned up

**Terminal (`internal/terminal/handler.go`):**
- `terminalIdleTimeout = 30 * time.Minute` (overridable in tests via `var`) — `conn.SetReadDeadline` reset on each message
- `maxReplayBytes = 64KB` — ring buffer replay capped on reconnect to prevent bandwidth amplification

**Logging:**
- Login success/failure, logout, API key changes logged with client IP via `log.Printf`
- Client IP extracted from `r.RemoteAddr` (not `X-Forwarded-For`) to prevent log spoofing

**Container:**
- `opencode serve --hostname 127.0.0.1` (was `0.0.0.0`) — loopback-only inside the container
- Ports published to `127.0.0.1` only — no network exposure

### Frontend

**`web/src/components/ProjectCard.tsx`** — "Open" button navigates to `/agent/${project.name}/` with `window.location.href`. Simple full-page navigation; no async token fetch needed because everything is same-origin.

**`web/src/pages/Project.tsx`** — Agent tab renders an `<iframe src="/agent/${project.name}/" />`. Same-origin iframe; the session cookie is sent automatically. The SW is registered within the iframe context, controlling requests from that iframe page.

---

## Testing Guide

### Automated Coverage

**`internal/proxy/proxy_test.go`**:

| Test | What it verifies |
|------|-----------------|
| `TestRewriteHTML_PrefixesPaths` | Asset paths rewritten; original `/` removed |
| `TestRewriteHTML_InjectsSWScript` | SW registration script present with correct path |
| `TestRewriteHTML_InjectsWSPatcher` | WebSocket patcher injected, contains API prefix |
| `TestGenerateSWScript_ContainsProjectName` | JSON-encoded name, skipWaiting, clients.claim, 401 redirect, API prefix |
| `TestAgentUIHandlerServesSWFile` | SW file served with `application/javascript` content type |
| `TestAgentUIHandlerServesHTMLFromCache` | Cache hit serves without hitting container |
| `TestAgentUIHandlerServesAssetFromCache` | Asset cache hit |
| `TestAgentUIHandlerProjectNotFound` | 404 for unknown project |
| `TestAgentAPIHandlerForwardsRequest` | Prefix stripped, cookie stripped, Basic Auth set |
| `TestAgentAPIHandlerProjectNotFound` | 404 |
| `TestAgentAPIHandlerProjectNotRunning` | 503 |

**`internal/proxy/assets_test.go`**: Cache round-trip, cache miss, `Clear` wipes all entries.

**`internal/project/manager_test.go`**:
- `TestContainerAddr_CachesResult` — second call uses cache, no Docker inspect
- `TestContainerAddr_CacheInvalidatedOnStop` — cache cleared after stop
- `TestManager_StartHook_CalledOnStart` — hook fires with correct project name

**`internal/auth/store_test.go`**:
- `TestSetPassword_TooShort` — passwords < 12 chars rejected
- `TestSetAndCheckPassword`, `TestSetPassword_Overwrite` — password round-trip with 12-char+ passwords

**`internal/server/router_test.go`**:
- `TestAgentRouteHasPermissiveCSP` — worker-src, unsafe-inline present
- `TestDashboardRouteHasStrictCSP` — X-Frame-Options: DENY, no unsafe-inline in script-src

**Gaps worth noting:**
- No test for the SW's 401 → login redirect (requires a browser; unit-testable only by verifying the SW script contains the pattern, which `TestGenerateSWScript_ContainsProjectName` does)
- No test for `proxyWebSocket` — would require a real WebSocket server. The function is short and well-understood.

### Manual Verification Checklist

```
[ ] 1. Delete old cert: rm -f data/cert.pem data/key.pem
[ ] 2. Build: task build — compiles cleanly
[ ] 3. Start server: ./appx -port 8443
        Verify in logs: "tls: generating self-signed cert with DNS SANs [localhost *.localhost]..."
[ ] 4. Open https://localhost:8443 in Chrome, accept the TLS cert
[ ] 5. Login with the auto-generated password
[ ] 6. Create a project (any name), start it
[ ] 7. Click "Open" on the running project
        Expected: navigates to https://localhost:8443/agent/<name>/
        Expected: brief "starting…" overlay appears on first visit only
        Expected: OpenCode UI loads within ~1-2 seconds
[ ] 8. DevTools → Application → Service Workers:
        Expected: SW registered for https://localhost:8443/agent/<name>/
        Expected: status "activated and running"
[ ] 9. DevTools → Network tab, make the OpenCode SPA perform an action:
        Expected: API calls appear as /api/agent/<name>/session etc.
        Expected: NOT as /session directly
[ ] 10. Reload the page — no "starting…" overlay (SW already active)
[ ] 11. Navigate back to dashboard, Open a second project:
         Expected: same origin, no new cert approval needed
         (Compare: subdomain routing required separate cert acceptance per project)
[ ] 12. DevTools → Application → Cookies:
         Expected: appx_session has SameSite=Strict, no Domain attribute
[ ] 13. Stop the project, try to load the agent page:
         Expected: serves cached HTML (if any), not 503
         Expected: SPA shows "not connected" state from its own error handling
[ ] 14. Restart the project; hard-reload the agent page:
         Expected: fresh HTML fetched from new container
[ ] 15. Project.tsx Agent tab:
         Expected: shows iframe with OpenCode UI embedded
[ ] 16. Test session expiry: in DevTools → Application → Cookies, delete
         the appx_session cookie, then perform an OpenCode action
         Expected: SW detects 401 + X-Appx-Auth, redirects to /login
[ ] 17. Verify terminal WebSocket still works (Agent tab → built-in PTY)
[ ] 18. Verify /apps/<name>/ proxy reaches the user app (if one is running)
[ ] 19. Check server logs: auth events appear on login/logout
```

---

## Architecture and Code Pitfalls

### P1: First page load has a brief visible "starting…" overlay

**Location:** `internal/proxy/proxy.go:rewriteHTML`  
**The problem:** On the very first visit to `/agent/:name/`, the browser must install the Service Worker before the SPA can safely make API calls. Until the SW is controlling the page, a loading overlay (`<div id="_appx_sw">`) is shown. Duration is typically 100–300ms on a local connection.  
**Severity:** Low — this is a one-time event per project per browser. Subsequent visits have no delay because the SW is already active. The overlay is intentional behavior, not a bug, but it can surprise users expecting instant load.  
**What a fix would look like:** None needed. The `skipWaiting` + `clients.claim` combination in the SW minimizes the window. Any further improvement would require the page to defer all app initialization until the SW is confirmed active, which is complex and not worth the effort.

### P2: `AssetCache.Clear` evicts all projects when any container starts

**Location:** `internal/proxy/assets.go:Clear`  
**The problem:** `Clear(_ string)` ignores the project name and wipes the entire cache. Starting project A clears project B's cached HTML.  
**Severity:** Low — causes unnecessary re-fetches for unrelated projects, but correctness is maintained. Acceptable for a tool with few projects.  
**What a fix would look like:** Per-project HTML entries could be cleared by name; shared assets (JS chunks with content-addressed filenames) could be kept across project restarts since their hashes are immutable within a given OpenCode version.

### P3: SW 401 detection could be confused by container-level auth failures

**Location:** `internal/proxy/proxy.go:generateSWScript`  
**The problem:** The SW redirects to `/login` when it receives a 401 with `X-Appx-Auth: required`. This header is set only by `auth.Middleware`, not by the container. However, if the container's `OPENCODE_SERVER_PASSWORD` somehow falls out of sync with the database (e.g., a container was started before a secret rotation applied), the container returns 401 without the header. The user sees OpenCode errors but is NOT redirected to login — which is correct behavior but may be confusing.  
**Severity:** Low — the header-based discrimination was specifically designed to prevent the worse outcome (redirect to login when session is still valid). Out-of-sync secrets should not occur in normal operation.

### P4: Server-side HTML cache has no TTL

**Location:** `internal/proxy/assets.go`  
**The problem:** Cached HTML is only cleared when a container starts (via `startHook`). If OpenCode is updated on a running container without a restart (not the normal workflow, but theoretically possible), the cached HTML is stale.  
**Severity:** Low — in normal operation, container restart triggers cache invalidation. This could only be an issue if someone manually updates the OpenCode binary inside a running container.

### P5: Container secrets visible via `docker inspect`

**Location:** `internal/project/container.go:doFullCreate`  
**The problem:** `ANTHROPIC_API_KEY` and `OPENCODE_SERVER_PASSWORD` are injected as Docker environment variables. Any OS user who can run `docker inspect` can read them.  
**Severity:** Medium on shared servers; Low on personal machines (same user).  
**Fix:** Deferred to Phase 5. See `docs/plans/phase_5_plan.md`.

---

## Fixed Pitfalls

> **Problem:** Docker for Mac/Windows runs containers in a VM — bridge-network IPs (`172.x.x.x`) are unreachable from the host process. The proxy timed out on every request.  
> **Fix:** Port publishing to `127.0.0.1:<auto-assigned>`. `ContainerAddr` reads the host-mapped port from `ContainerInspect` and returns `127.0.0.1:<hostPort>`, which works on all platforms.

> **Problem:** The original path-prefix proxy patched the OpenCode JS bundle server-side — matching specific minified variable names like `QO` (Vite's preload helper) and the `zM` API base URL function. These patches broke silently when OpenCode updated and changed its minification.  
> **Fix:** Service Worker approach. The SW intercepts at the network boundary, not the source level. OpenCode's internal variable names are irrelevant.

> **Problem:** Subdomain routing (`<name>.localhost`) required per-hostname certificate trust. Chrome stores TLS exceptions per hostname, not per cert — even with a `*.localhost` wildcard SAN, every new project name required a separate cert click-through.  
> **Fix:** Path-based routing (`/agent/:name/`). Everything is on `localhost` — one cert approval covers all projects forever.

> **Problem:** Session cookie had `Domain=localhost` + `SameSite=Lax` to enable cross-subdomain cookie sharing for the subdomain routing approach. `Lax` is weaker than `Strict` for CSRF.  
> **Fix:** With path-based routing, no subdomain sharing needed. Reverted to `SameSite=Strict`, no `Domain` attribute.

> **Problem:** `opencode serve` ran with `--hostname 0.0.0.0` — any process inside the container could reach the opencode API on `127.0.0.1:4096` without proxy authentication.  
> **Fix:** Changed to `--hostname 127.0.0.1`. Docker port publishing operates at the network layer before the container's loopback restriction.

> **Problem:** No size limit on proxied HTTP request bodies. An authenticated client could stream unlimited data to a container.  
> **Fix:** `http.MaxBytesReader(w, r.Body, 100MB)` applied in `AgentAPIHandler` and `ProxyHandler`.

> **Problem:** WebSocket tunnels (`proxyWebSocket`) had no idle timeout — abandoned connections held goroutines indefinitely.  
> **Fix:** `deadlineConn` wrapper resets TCP deadline on every Read/Write. 10-minute idle timeout.

> **Problem:** Terminal WebSocket sessions had no idle timeout. Abandoned sessions held exec processes inside containers. With the one-session-per-project cap, a leaked session blocked new sessions.  
> **Fix:** `terminalIdleTimeout = 30 * time.Minute` via `conn.SetReadDeadline`, reset on each message.

> **Problem:** Full ring buffer content (up to 4MB) was replayed on every WebSocket reconnect — bandwidth amplification via rapid reconnects.  
> **Fix:** Replay capped to 64KB (`maxReplayBytes`).

> **Problem:** bcrypt cost was Go's `DefaultCost` (10), below OWASP 2023 recommendation.  
> **Fix:** Raised to `bcryptCost = 12`. Backward-compatible — existing hashes still validate.

> **Problem:** `SetPassword` accepted passwords of any length, including single characters.  
> **Fix:** `minPasswordLen = 12` enforced at the start of `SetPassword`.

> **Problem:** `auth.Middleware` returned plain 401 responses. The Service Worker couldn't distinguish appx session expiry from container-level auth failures, causing incorrect login redirects.  
> **Fix:** `AuthRequiredHeader = "X-Appx-Auth"` set on all auth middleware 401s. SW checks for this header before redirecting.

---

## TODOs and Future Improvements

### Deferred to Phase 5

- **Egress logging** — containers have unrestricted outbound internet. Phase 5 adds visibility (DNS/iptables/eBPF logging). See `docs/plans/phase_5_plan.md`.
- **Dockerfile supply chain hardening** — `curl | bash` install; replace with pinned binary + SHA256 checksum.
- **Container secrets via `docker inspect`** — replace env var injection with bind-mounted secret files. Documented in `docs/plans/phase_5_plan.md`.
- **Initial password file** — delete `data/initial_password` after printing. Trivial fix, also in `docs/plans/phase_5_plan.md`.

### Deferred to Phase 6

- **Bearer token auth** — `AgentAPIHandler` currently requires a session cookie. Phase 6 adds `Authorization: Bearer` support for React Native clients. `Auth.Middleware` will check for the bearer header before falling back to the cookie. No architectural changes needed — the interface is ready.
- **React Native mobile client** — calls `AgentAPIHandler` with bearer tokens, renders its own native UI from OpenCode's REST/SSE API. See `docs/plans/phase_6_plan.md`.

### Known Limitations (Accepted Trade-offs)

- **`AssetCache.Clear` is all-or-nothing** — clears all projects when any container starts. Acceptable for few projects; a per-project strategy would require version-keyed asset entries.
- **SW `install` overlay** — first-visit loading screen (100–300ms). Unavoidable with the SW model; mitigated by `skipWaiting` + `clients.claim`.
- **No `sandbox` attribute on agent iframe** — OpenCode needs localStorage, fetch, and other APIs. A sandboxed iframe would require explicit `allow` attributes for each needed capability.
- **`build-essential` in container** — allows compiling native code; combined with unrestricted internet, increases container escape surface. Removal pending Phase 5 Dockerfile work.
