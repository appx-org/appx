# Phase 4 Plan: Reverse Proxy + AI Agent Web UI

**Date:** 2026-04-05  
**Updated:** 2026-04-06 (pitfalls + mitigations added)  
**Status:** Draft (updated after investigation spike)  
**Scope:** Reverse proxy for container web apps + OpenCode web UI integration

---

## Context

Phase 3 delivered a working in-browser terminal with persistent sessions. Users can run shell commands, and `opencode run "..."` works for AI tasks. However, we discovered that modern AI coding agents (Claude Code, OpenCode) have interactive TUI modes that cannot render through Docker exec PTY — they require native terminal emulators.

OpenCode has a `web` mode and a headless `serve` mode that start a standalone web server with a full coding agent UI and REST/SSE API. This bypasses the terminal emulation problem entirely. To make this accessible through appx, we need a reverse proxy.

The reverse proxy also serves the original Phase 4 purpose: making web apps built inside containers accessible at `/apps/:name/*`.

---

## Investigation Findings

Before finalising the architecture, we ran a spike to understand OpenCode's web mode behaviour. Key findings:

### opencode serve vs opencode web

Both commands start the **same HTTP server**. The server exposes:

- A REST/SSE API (80+ endpoints — sessions, files, providers, events, etc.)
- The OpenCode web UI as static assets

The only difference: `opencode web` also opens a browser tab automatically. `opencode serve` is the headless equivalent — correct for container use.

- Docs: https://opencode.ai/docs/web/ and https://opencode.ai/docs/server/
- OpenAPI spec available at runtime: `http://<host>:<port>/doc`

### No base-path support

`opencode serve --help` reveals the available flags: `--port`, `--hostname`, `--cors`, `--mdns`, `--log-level`, `--pure`. There is **no `--base-path` or `--prefix` flag**.

The HTML served by opencode uses root-absolute paths for all assets:

```html
<script type="module" src="/assets/index-Ca44lNAO.js"></script>
<link rel="stylesheet" href="/assets/index-BxQEOW35.css" />
```

This means naively proxying opencode at a path prefix (e.g. `/agent/my-project/`) breaks immediately — the browser requests `/assets/index.js` from the appx server root, which serves nothing.

### The SPA is designed to connect to any server

Inspecting the compiled JS bundle revealed that the OpenCode web UI is not hardwired to its local server. It has a built-in server switcher and reads the server URL from a well-known localStorage key before falling back to a default:

```js
// localStorage key for server URL override
const DEFAULT_SERVER_URL_KEY = "opencode.settings.dat:defaultServerUrl";

// Default resolution logic
const getDefaultServerUrl = () =>
  location.hostname.includes("opencode.ai")
    ? "http://localhost:4096" // opencode.ai hosted UI → talks to user's local server
    : location.origin; // all other deployments → talks to the same host

// Actual resolution: localStorage wins
const getServerUrl = () =>
  localStorage.getItem(DEFAULT_SERVER_URL_KEY) || getDefaultServerUrl();
```

This is how the `opencode.ai` website works: it serves the same UI and lets users point it at their local opencode server. The SPA also has a "Servers" dialog (visible as `command.server.switch` in the i18n strings) where users can manually add/remove/switch servers.

**This means we can tell the SPA exactly which server to use before it initialises**, by injecting a script that writes to localStorage.

### Static assets are not project-specific

The JS and CSS bundle is the same across all containers — it is the OpenCode web app at a fixed version. It does not contain any per-project data. This means:

- Assets do **not** need to be proxied per-container
- Appx can fetch and cache assets once (from any running container, or on startup) and serve them itself
- If no container is running, the UI still loads; it simply shows "could not reach server" until the container starts

### Real-time transport: SSE, not WebSocket

The main event stream (`/event`, `/global/event`) uses **Server-Sent Events**, not WebSocket. Go's `httputil.ReverseProxy` handles SSE correctly with no special configuration (unlike WebSocket which requires explicit `Upgrade` header handling).

There is a `/pty/{id}/connect` endpoint which is likely WebSocket (for OpenCode's built-in terminal). This will need the standard WebSocket upgrade proxy treatment.

### Authentication

`OPENCODE_SERVER_PASSWORD` / `OPENCODE_SERVER_USERNAME` env vars enable HTTP Basic Auth on the opencode server. The container-to-appx channel is internal (Docker network), so this is optional, but good practice. Appx can inject the password as an env var on container start and forward credentials when proxying API requests.

- Docs: https://opencode.ai/docs/server/

### SDK

OpenCode publishes a typed JS/TS SDK (`@opencode-ai/sdk` or similar) that wraps the REST/SSE API with full TypeScript types, generated from the OpenAPI spec. This is a clean way to build custom UI components that talk to an opencode server without embedding the full SPA.

- Docs: https://opencode.ai/docs/sdk/

---

## Architecture

### How it works (simple version)

The key insight: instead of trying to proxy the entire opencode web server at a path prefix (which breaks due to hardcoded asset paths), we split responsibilities:

```
/agent/:name/         ← serves the OpenCode web UI (HTML + cached assets)
/api/agent/:name/*    ← proxies API calls to the right container
```

Appx fetches the HTML from the container once, makes a small modification (rewrites 3–4 asset paths and injects one script tag), then serves it. The static JS/CSS assets are cached by appx and served directly — no per-request container involvement. Only API traffic (`/api/agent/:name/*`) is proxied live to the container.

### Detailed flow

```
Browser → GET /agent/my-project/
    appx fetches HTML from container:4096/ (once, cached)
    modifies the HTML:
      1. Rewrite src="/assets/ → src="/agent/my-project/assets/
         (so browser requests assets from the right appx route)
      2. Inject before </head>:
         <script>localStorage.setItem(
           "opencode.settings.dat:defaultServerUrl",
           "https://myappx.host/api/agent/my-project"
         )</script>
         (so the SPA connects to the right container via the proxy)
    returns modified HTML to browser

Browser → GET /agent/my-project/assets/index-Ca44lNAO.js
    appx serves from its own asset cache (no container needed)

SPA → GET /api/agent/my-project/event  (SSE stream)
    appx strips /api/agent/my-project prefix
    proxies → container:4096/event

SPA → GET /api/agent/my-project/session/abc123
    appx strips prefix
    proxies → container:4096/session/abc123
```

### Why this works

- **No CORS issues**: from the browser's perspective, everything is same-origin (`https://myappx.host`). The browser never directly contacts the container.
- **Multiple projects**: each project has its own `/agent/:name/` and `/api/agent/:name/` namespace — they don't interfere.
- **Asset caching**: since all containers run the same opencode version, assets are fetched once and reused. If the opencode version in the base image changes, the cache is invalidated on next container start.
- **Graceful degradation**: if the container is stopped, the UI still loads from the cached assets and shows a "server unreachable" message. It does not 404.
- **HTML modification is minimal**: the HTML is ~2.4 KB and has a fixed structure (3–4 `src="/assets/...` and `href="/assets/...` occurrences). This is targeted string replacement, not HTML parsing.

### Two proxy targets per container

```
container
  ├─ port 4096: opencode serve (AI agent API + serves web UI HTML/assets on first fetch)
  └─ port N:    user's web app (npm start, vite, etc.) — accessed via /apps/:name/*
```

Routing:

- `/agent/:name/*` — OpenCode web UI and its API
- `/apps/:name/*` — user's web app on the project's configured port (path prefix stripped)

### Container networking

The appx server (running on the host) reaches containers via their internal Docker bridge IP. `Manager.ContainerIP(id)` already implements this — it inspects the container and returns its IP on the `appx-{name}-net` network. The proxy uses `http://{containerIP}:{port}` as the backend.

Container IPs are stable for the lifetime of a container and change only on restart. Caching the IP after `ContainerStart` and invalidating on `ContainerStop` is sufficient.

---

## Components

### 1. `internal/proxy/proxy.go`

Reverse proxy using Go's `httputil.ReverseProxy`. Handles:

- **`/apps/:name/*`** — look up project by name, get container IP + project port, strip prefix, forward request. Transparent HTTP proxy; no modification.
- **`/api/agent/:name/*`** — look up project by name, get container IP + opencode port (4096), strip `/api/agent/:name` prefix, forward request. Must handle SSE (flush, no buffering) and WebSocket upgrade.
- **`/agent/:name/`** (index only) — fetch HTML from container (with cache), apply modifications (asset path rewriting + localStorage injection), serve to browser.
- **`/agent/:name/assets/*`** — serve from appx-side asset cache.

```go
// ProxyHandler returns an http.Handler that routes /apps/:name/* requests
// to the project's user app container port, stripping the path prefix.
func ProxyHandler(pm *project.Manager) http.Handler

// AgentUIHandler returns an http.Handler that serves the OpenCode web UI
// for a given project. It fetches and caches the HTML from the container,
// injects the correct server URL into localStorage, rewrites asset paths,
// and serves static assets from appx's own cache.
func AgentUIHandler(pm *project.Manager, cache *AssetCache) http.Handler

// AgentAPIHandler returns an http.Handler that proxies /api/agent/:name/*
// requests to the project's opencode serve instance, stripping the prefix.
// Handles both regular HTTP (including SSE) and WebSocket upgrades.
func AgentAPIHandler(pm *project.Manager) http.Handler
```

### 2. Asset cache (`internal/proxy/assets.go`)

Holds the OpenCode static assets (JS, CSS, favicon, manifest) fetched from a container. Since all containers run the same opencode version, one cache serves all projects.

- Populated on first request (lazy) or on container start (eager)
- Invalidated when the base image is rebuilt
- Stored in memory; size is small (~1–2 MB for the JS bundle)

### 3. Container startup changes

When a container starts, appx launches `opencode serve` inside it:

```go
// After ContainerStart succeeds:
execOpenCodeServe(ctx, containerID, types.ExecConfig{
    Cmd: []string{
        "opencode", "serve",
        "--port", "4096",
        "--hostname", "0.0.0.0",
    },
    Env: []string{
        "ANTHROPIC_API_KEY=" + apiKey,
        "OPENCODE_SERVER_PASSWORD=" + containerSecret,
    },
    Detach: true,
})
```

**Option A: Docker exec on start** — `Manager.Start()` runs the exec after `ContainerStart`. Simple, no Dockerfile changes needed.

**Option B: Change container CMD** — Replace `CMD ["sleep", "infinity"]` with a startup script. More robust (survives container restart without appx involvement), but requires Dockerfile change and base image rebuild.

Option B is preferred for reliability: if the container restarts independently of appx (e.g. after a host reboot), opencode serve starts automatically.

### 4. Router registration

```go
// In NewRouter():

// OpenCode web UI — serve cached HTML and assets
mux.Handle("/agent/", a.Middleware(proxy.AgentUIHandler(pm, assetCache)))

// OpenCode API proxy — forward to container's opencode serve
mux.Handle("/api/agent/", a.Middleware(proxy.AgentAPIHandler(pm)))

// User app proxy — forward to container's user app port
mux.Handle("/apps/", a.Middleware(proxy.ProxyHandler(pm)))
```

All three behind auth middleware. None behind `limitBody` (proxy requests can be large or streaming).

### 5. Frontend: Project page integration

The Project page (`Project.tsx`) currently shows the terminal. Phase 4 adds:

- **Agent tab**: embeds the OpenCode web UI via `<iframe src="/agent/:name/" />`
- **Terminal tab**: the existing xterm.js terminal
- **App link**: when the user's app is running, a button linking to `/apps/:name/`

Default view: Agent tab.

---

## Open Questions

All open questions resolved:

1. **Asset cache invalidation strategy** — **Decision:** clear the cache on every container start. Simple and correct; container starts are infrequent for a personal tool.

2. **opencode serve startup timing** — there is a brief window after `ContainerStart` where the container is running but opencode serve has not yet bound its port. The proxy should retry with backoff on connection refused rather than returning 502 immediately.

3. **`OPENCODE_SERVER_PASSWORD` handling** — appx generates a random secret per container, injects it as env var, and includes it in proxy requests (`Authorization: Basic ...`). This prevents direct access to the opencode server from within the container network. Needs a new column in the `projects` table to store the per-container secret.

4. **WebSocket in `/pty/{id}/connect`** — OpenCode's built-in terminal uses WebSocket. `httputil.ReverseProxy` does not handle WebSocket by default; explicit upgrade forwarding is required (same pattern as the existing terminal handler in Phase 3).

5. **User app port discovery** — **Decision:** use `project.Port` (declared at creation) as the proxy target, and make it editable via a new `PATCH /api/projects/:id` endpoint + port field in the project settings UI. This is deterministic and handles the common case; the edit endpoint covers the case where the user changes their app's port after creation.

---

## Future Path: Custom UI via SDK

The OpenCode SDK (`@opencode-ai/sdk`) provides a fully-typed TypeScript client for the opencode REST/SSE API, generated from the server's OpenAPI spec. Connection is as simple as:

```ts
const client = createOpencodeClient({ baseUrl: "/api/agent/my-project" })
const session = await client.session.create({ body: { title: "New session" } })
for await (const event of client.event.stream()) { ... }
```

This opens a future path: instead of (or alongside) embedding the full OpenCode SPA in an iframe, appx could build its own native agent UI — integrated with the existing appx design system — that talks to the same opencode server. This would give full control over the UX, eliminate the iframe sandboxing constraints, and allow tighter integration with project management (e.g. showing agent activity inline on the dashboard).

This is out of scope for Phase 4 but worth keeping in mind when designing the proxy API surface.

- SDK docs: https://opencode.ai/docs/sdk/
- Server docs: https://opencode.ai/docs/server/

---

## Implementation Order

1. **Basic HTTP reverse proxy** — `/apps/:name/*` → container:userPort, HTTP only
2. **WebSocket proxy support** — handle Upgrade headers (needed for `/pty` and terminal)
3. **SSE proxy support** — verify flush behaviour for `/api/agent/:name/event`
4. **Auto-start opencode serve** — modify container CMD or exec on start
5. **Asset cache** — fetch assets from container, serve from appx
6. **Agent UI route** — `/agent/:name/` serves modified HTML with localStorage injection
7. **Agent API proxy** — `/api/agent/:name/*` strips prefix and forwards
8. **Project page integration** — Agent/Terminal tabs, App link button
9. **Testing + verification**

---

## Risks and Pitfalls

### HTML rewriting is fragile

**Problem:** The asset path rewriting (`src="/assets/` → `src="/agent/:name/assets/`) is string replacement against a ~2.4 KB HTML file with a fixed structure today. If opencode adds a web worker, a service worker, a `<link rel="modulepreload">`, or a manifest reference that uses root-absolute paths, the rewriting silently misses it and the UI breaks.

**Mitigations:**

- Pin the opencode version in `Dockerfile.project`. Never auto-update.
- Write a unit test that fetches the HTML, applies all rewrites, and asserts that zero root-absolute `/assets/` references remain. This test will catch breakage immediately on any version bump.
- On a version bump, review the new HTML diff before updating the pin.

---

### iframe CSP / X-Frame-Options

**Problem:** The appx server sends security headers. If `X-Frame-Options: DENY` or `Content-Security-Policy: frame-ancestors 'none'` is applied to `/agent/` routes, the iframe will be blocked by the browser — silently in some cases.

**Mitigation:** Ensure the security headers middleware does **not** apply `X-Frame-Options` or a restrictive `frame-ancestors` CSP to `/agent/*` routes. These headers should only apply to appx's own pages. Audit `middleware.go` before shipping.

---

### SSE response buffering

**Problem:** Go's `httputil.ReverseProxy` handles SSE correctly only if the full response writer chain supports `http.Flusher`. Any middleware that buffers the response body (e.g. a body-capturing logger, a compression layer, or `limitBody`) will break SSE by holding chunks until the buffer fills.

**Mitigation:** Proxy routes (`/api/agent/`, `/apps/`, `/agent/`) are already excluded from `limitBody`. Ensure no future middleware is added to these routes that wraps the `ResponseWriter` without forwarding `http.Flusher`. Add a comment in `router.go` at the proxy route registration explaining this constraint.

---

### Per-container auth secret is underspecified

**Problem:** Open question 3 (the `OPENCODE_SERVER_PASSWORD` per-container secret) touches the DB schema, secret generation timing, secret rotation, and proxy header injection. These are non-trivial and affect the security model. The plan currently defers this decision.

**Decision (resolved):** For Phase 4, implement the secret as follows:

- Add a `container_secret TEXT` column to the `projects` table (new migration).
- Generate a random 32-byte hex secret on `ContainerCreate` and store it.
- Inject `OPENCODE_SERVER_PASSWORD=<secret>` into the container startup env.
- The proxy injects `Authorization: Basic base64(opencode:<secret>)` on every request to `container:4096`.
- The secret is rotated on container reset (new secret generated, old container destroyed).
- No user-facing exposure of the secret.

This is optional for initial development but must be in place before any public-facing deployment.

---

### opencode serve startup latency

**Problem:** After `ContainerStart`, there is a window (typically 1–3 seconds) where the container is running but `opencode serve` has not yet bound port 4096. Proxy requests during this window get `connection refused`, which surfaces as a 502 to the browser.

**Mitigation:** The proxy's `Transport` should implement a retry loop with exponential backoff on `connection refused` errors, up to ~10 seconds. Surface a loading state in the iframe (e.g. the appx Project page shows a spinner while polling `GET /api/agent/:name/health` before rendering the iframe).

---

### Asset cache cold start

**Problem:** If appx restarts and no container is running, the in-memory asset cache is empty. The first user to hit `/agent/:name/` after a container starts incurs the full asset fetch latency before the HTML is returned.

**Mitigation:** This is acceptable for a personal tool — container starts are infrequent. Document the behaviour: first load after a container start may be slightly slower. Do not attempt to persist the cache to disk in Phase 4.

---

### iframe UX tradeoffs (accepted)

**Decision (resolved, not a blocker):** The iframe approach embeds OpenCode's own SPA with its own design language inside appx. This breaks visual coherence but is the right call because:

- OpenCode's UI is actively maintained and will improve over time for free.
- Building a native agent UI from scratch using the SDK would mean chasing opencode's feature set indefinitely.
- The iframe is contained to one tab on the Project page; the rest of appx remains fully native.
- The SDK-based native UI remains a future option once the proxy infrastructure is stable.

The only acceptable mitigation is ensuring the iframe fills the available space well and the tab switching (Agent / Terminal) is clearly designed.

---

### WebSocket proxy edge cases

**Problem:** `httputil.ReverseProxy` does not handle WebSocket upgrades. The `/pty/{id}/connect` endpoint (OpenCode's built-in terminal) uses WebSocket. Ping/pong frames, connection timeouts, and half-close semantics require careful handling.

**Mitigation:** Reuse the Phase 3 WebSocket proxy pattern from `internal/terminal/handler.go` as a reference implementation. Do not attempt to use `httputil.ReverseProxy` for WebSocket — detect the `Upgrade: websocket` header and handle it with a dedicated bidirectional copy loop.

---

### opencode serve stability

**Problem:** `opencode serve` is a relatively new feature. It may have bugs in edge cases: long-running sessions, container resource limits, or unexpected process exits.

**Mitigation:** The xterm.js terminal tab remains available as a fallback for all projects. If opencode serve crashes, the user can still open a terminal and restart it manually. Consider adding a health-check poll in the Project page that detects when the agent is unreachable and shows an appropriate message with a restart option.
