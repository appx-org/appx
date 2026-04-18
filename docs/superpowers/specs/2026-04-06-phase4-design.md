# Phase 4 Design Spec: Reverse Proxy + AI Agent Web UI

**Date:** 2026-04-06  
**Status:** Approved  
**Author:** neuromaxer  
**References:** [`docs/plans/phase_4_plan.md`](../../plans/phase_4_plan.md)

---

## Problem

Users need two things beyond the terminal (Phase 3):

1. **An AI coding agent UI** — a full interactive interface to OpenCode, not just `opencode run` one-shots through the terminal. OpenCode's TUI cannot render through Docker exec PTY; its web mode is the right answer.
2. **Access to apps they build** — when a user builds a web app inside a container, they need to reach it through appx without exposing container ports directly.

Both require a reverse proxy.

---

## Key Finding: OpenCode Web Mode Behaviour

Before designing, we investigated `opencode web` / `opencode serve` hands-on (OpenCode v1.3.15). Critical findings that shaped the architecture:

- **`opencode serve` and `opencode web` run the same HTTP server.** The only difference is `web` opens a browser. Use `serve` for headless container operation.
- **No `--base-path` flag exists.** The served HTML uses root-absolute asset paths (`src="/assets/index-xxx.js"`). Naively proxying at a path prefix (e.g. `/agent/my-project/`) breaks immediately — the browser requests assets from the appx root, finds nothing.
- **The SPA is designed to connect to any server.** The JS bundle reads the server URL from a localStorage key (`opencode.settings.dat:defaultServerUrl`) before falling back to `location.origin`. We can inject this key before the page initialises. Source: inspected bundle + https://opencode.ai/docs/web/
- **Static assets are not project-specific.** The JS/CSS bundle is the same across all containers (same opencode version). Appx can cache assets once and serve them directly — no per-request container involvement for static files.
- **Real-time transport is SSE, not WebSocket.** The main event stream uses Server-Sent Events. Go's `httputil.ReverseProxy` handles SSE without special configuration. The `/pty/{id}/connect` endpoint is WebSocket and needs explicit upgrade handling.
- **SDK available.** OpenCode publishes a typed TS SDK (`createOpencodeClient({ baseUrl })`) over the same REST/SSE API. Useful for a future native UI. Docs: https://opencode.ai/docs/sdk/

---

## Architecture

### Core Insight

Split the OpenCode integration into two independent concerns:

```
/agent/:name/         ← web UI  (HTML served by appx, assets cached by appx)
/api/agent/:name/*    ← API     (live proxy to container:4096)
```

Appx fetches the OpenCode HTML from the container **once**, makes minimal modifications, and serves it. All subsequent asset requests are served from appx's own cache. Only live API traffic (`/api/agent/:name/*`) is proxied to the container on each request.

### Request flow

```
1. Browser → GET /agent/my-project/
   appx fetches HTML from container:4096/ (cached per container start)
   modifies HTML:
     a. Rewrite src="/assets/ → src="/agent/my-project/assets/
        (browser will request assets from appx, not root)
     b. Inject before </head>:
        <script>localStorage.setItem(
          "opencode.settings.dat:defaultServerUrl",
          "https://<host>/api/agent/my-project"
        )</script>
        (SPA connects to the right container via proxy)
   return modified HTML

2. Browser → GET /agent/my-project/assets/index-xxx.js
   appx serves from in-memory asset cache (no container call)

3. SPA → GET /api/agent/my-project/event   (SSE)
   appx strips /api/agent/my-project prefix
   proxies → container:4096/event

4. SPA → GET /api/agent/my-project/session/abc
   appx strips prefix → container:4096/session/abc

5. Browser → GET /apps/my-project/some/path
   appx strips /apps/my-project prefix
   proxies → container:project.Port/some/path
```

### Why this works

- **No CORS**: everything is same-origin from the browser's perspective. The browser never contacts the container directly.
- **Multiple projects**: each has its own `/agent/:name/` + `/api/agent/:name/` namespace.
- **Graceful degradation**: if the container is stopped, cached assets still load. The SPA shows "server unreachable" — no 404.
- **HTML rewriting is minimal**: ~2.4 KB HTML, 3–4 targeted string replacements + one script injection.

---

## Components

### `internal/proxy/` (new package)

**`proxy.go`** — three handlers:

```go
// ProxyHandler proxies /apps/:name/* to the project's declared port.
// Strips the /apps/:name prefix before forwarding.
func ProxyHandler(pm *project.Manager) http.Handler

// AgentUIHandler serves the OpenCode web UI for a project.
// Fetches and caches HTML from the container on first load after each
// container start. Rewrites asset paths and injects the server URL
// into localStorage. Serves static assets from the in-memory cache.
func AgentUIHandler(pm *project.Manager, cache *AssetCache) http.Handler

// AgentAPIHandler proxies /api/agent/:name/* to the project's opencode
// serve instance. Strips the prefix. Handles SSE (unbuffered) and
// WebSocket upgrades (dedicated copy loop — httputil.ReverseProxy does
// not handle WS).
func AgentAPIHandler(pm *project.Manager) http.Handler
```

**`assets.go`** — in-memory asset cache:
- Keyed by project name (cleared on container start, since we clear per start)
- Populated lazily on first `/agent/:name/` request after container start
- Stores JS, CSS, favicon, manifest fetched from the container
- All containers run the same opencode version, so one cache entry serves all projects between restarts

### Container startup changes

Change `Dockerfile.project` CMD from `sleep infinity` to a startup script that runs `opencode serve` as a background process alongside `sleep infinity` (PID 1). This ensures opencode serve restarts with the container regardless of how the container was started.

```dockerfile
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 0.0.0.0 & sleep infinity"]
```

`ANTHROPIC_API_KEY` and `OPENCODE_SERVER_PASSWORD` are injected as env vars by `Manager.Start()` at container creation time.

### DB changes

New column `container_secret TEXT` on the `projects` table (migration). A random 32-byte hex secret is generated on `ContainerCreate`, stored here, injected as `OPENCODE_SERVER_PASSWORD`, and used by the proxy for `Authorization: Basic` on all requests to `container:4096`. Rotated on container reset.

### Project port — editable

`project.Port` (already stored) is the user app proxy target for `/apps/:name/*`. New:
- `PATCH /api/projects/:id` endpoint accepting `{ "port": N }`
- Port field in the Project settings UI

### Router

```go
mux.Handle("/agent/",     a.Middleware(proxy.AgentUIHandler(pm, assetCache)))
mux.Handle("/api/agent/", a.Middleware(proxy.AgentAPIHandler(pm)))
mux.Handle("/apps/",      a.Middleware(proxy.ProxyHandler(pm)))
```

All three: behind auth, outside `limitBody` (streaming/large responses).

### Frontend — Project page and navigation

**Project card (`ProjectCard.tsx`):** the "Open" button currently navigates to the appx Project page (terminal view). In Phase 4 it should navigate to `/agent/:name/` — the OpenCode web UI — directly. The appx Project page becomes the fallback/settings view, not the primary entry point.

**Project page (`Project.tsx`):** add tabbed view:
- **Agent tab** (default): `<iframe src="/agent/:name/" />` filling available space
- **Terminal tab**: existing xterm.js terminal
- **App button**: visible when project is running, links to `/apps/:name/`

---

## Decisions

| Question | Decision |
|---|---|
| Asset cache invalidation | Clear on every container start (simple; starts are infrequent) |
| opencode startup in container | Dockerfile CMD (reliable across independent restarts) |
| User app port | Declared `project.Port`, made editable via `PATCH /api/projects/:id` |
| opencode auth | `OPENCODE_SERVER_PASSWORD` per-container secret, stored in DB, optional for local dev |
| SSE proxy | `httputil.ReverseProxy` (handles SSE natively when chain supports `http.Flusher`) |
| WebSocket proxy | Dedicated upgrade + bidirectional copy loop (reuse Phase 3 pattern) |
| Agent UI embedding | iframe — preserves OpenCode's maintained UI; SDK-based native UI is future option |

---

## Key Constraints

- **Do not apply `X-Frame-Options` or `frame-ancestors` CSP to `/agent/*` routes** — these would silently block the iframe. Audit `middleware.go`.
- **Do not wrap proxy `ResponseWriter` with buffering middleware** — breaks SSE. Add comment at proxy route registration in `router.go`.
- **Pin opencode version in `Dockerfile.project`** — the HTML rewriting assumes a stable structure. Write a test that applies all rewrites and asserts zero root-absolute `/assets/` paths remain.

---

## Out of Scope for Phase 4

- Custom native agent UI via the OpenCode SDK (future phase)
- Subdomain routing (would require wildcard DNS + cert)
- Auto-detection of listening ports in containers
- Multi-user isolation beyond existing container model
