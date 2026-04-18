# Service Worker Proxy Design

**Date:** 2026-04-06  
**Status:** Approved  
**Replaces:** Subdomain routing (Phase 4 refactor, `d3b2bbd`)

---

## Problem

The subdomain routing approach (`<name>.localhost:<port>`) requires browsers to trust the self-signed TLS certificate per hostname. Chrome stores certificate exceptions per hostname, not per cert — so every new project name requires a new "Proceed anyway" click. This is not acceptable for end users and doesn't work on mobile without installing the cert as a system root CA.

The previous path-based approach (`/agent/:name/`) solved the cert problem but required brittle server-side rewriting of the OpenCode JS bundle (patching minified variable names), which broke silently on OpenCode updates.

---

## Solution

Serve the OpenCode UI at `/agent/:name/` (path-based, same origin) with a **Service Worker** that intercepts all `fetch()` calls from the SPA and rewrites root-absolute paths before they hit the network. The SW operates at the network boundary — it patches runtime behaviour without touching source code.

This combines the best of both previous approaches:
- Same origin = no cert issues (path-based)
- No JS bundle rewriting = robust across OpenCode updates (SW)

---

## Architecture

```
Browser (localhost:8443)
  │
  │  GET /agent/test3/           → AgentUIHandler [RESTORED]
  │    serves rewritten HTML + injects SW scripts
  │    caches HTML server-side for graceful degradation
  │
  │  GET /agent/test3/sw.js      → AgentUIHandler
  │    serves dynamically-generated SW file with project name
  │
  │  GET /agent/test3/assets/*   → AgentUIHandler
  │    proxies from container:4096/assets/*
  │
  │  * /api/agent/test3/*        → AgentAPIHandler [RESTORED]
  │    proxies all methods to container:4096, handles SSE + WS
  │
  └── Service Worker (scope: /agent/test3/)
        intercepts fetch() from OpenCode SPA:
        /session           → /api/agent/test3/session
        /global/event      → /api/agent/test3/global/event
        /assets/chunk.js   → /agent/test3/assets/chunk.js
        /favicon-v3.ico    → /agent/test3/favicon-v3.ico
        (external URLs)    → pass through unchanged
```

---

## Server Changes

### Routes (on dashboard mux, same origin)

```
GET  /agent/          → a.Middleware(AgentUIHandler)
*    /api/agent/      → a.Middleware(AgentAPIHandler)
```

Subdomain dispatch in `NewRouter` is removed. `ContainerHandler` is removed.

### AgentUIHandler (restored, simplified vs original)

- Validates project exists (`GetByName`)
- On cache miss: fetches HTML from `http://127.0.0.1:<hostPort>/` using container's published port (via `ContainerAddr`), rewrites HTML, caches, serves
- On cache hit: serves cached HTML
- Serves `/agent/:name/sw.js` with a dynamically generated SW file
- Serves all other paths (`/agent/:name/assets/*`, `/agent/:name/favicon*`, etc.) by proxying from the container
- If container is unreachable: serves cached HTML if available (graceful degradation), otherwise 503

**HTML rewriting** (same stable `="/` → `="/agent/:name/` replacement as before):
- Rewrites `="/` to `="/agent/:name/` in all HTML attribute values
- Injects SW registration script + loading overlay before `</head>`
- Injects WebSocket URL patcher before `</head>`
- Does NOT rewrite JS bundle content (SW handles runtime fetch behaviour)

**Cache invalidation:** `startHook` is re-added to `project.Manager`. Called when a project starts, clears the cached HTML for that project. This ensures the UI reflects container changes after a restart.

### AgentAPIHandler (restored)

Transparent proxy for all HTTP methods at `/api/agent/:name/*`:
- Strips `/api/agent/:name` prefix before forwarding
- Strips `appx_session` cookie
- Injects `Authorization: Basic` from `ContainerSecret`
- Handles SSE (`FlushInterval: -1`)
- Handles WebSocket upgrades via `proxyWebSocket`
- Accepts session cookie (bearer token support deferred to Phase 6)

### SW file: `/agent/:name/sw.js`

Served dynamically by `AgentUIHandler` with the project name embedded as a string literal. The project name is JSON-encoded to prevent XSS. About 25 lines of JS.

```javascript
const PROJECT = "test3"; // JSON-encoded project name embedded at serve time
const SCOPE_PREFIX = "/agent/" + PROJECT + "/";
const API_PREFIX = "/api/agent/" + PROJECT;

self.addEventListener("activate", e => e.waitUntil(clients.claim()));
self.addEventListener("fetch", e => {
    const url = new URL(e.request.url);
    if (url.origin !== self.location.origin) return; // pass through external
    if (url.pathname.startsWith(SCOPE_PREFIX)) return; // already prefixed
    if (url.pathname.startsWith("/api/")) return; // already an API route
    if (url.pathname.startsWith("/ws/")) return; // terminal WS, leave alone

    // Assets: /assets/*, /favicon*, /site.webmanifest, /social-share*, /apple-*
    if (url.pathname.startsWith("/assets/") ||
        url.pathname.match(/^\/(favicon|apple-touch|site\.webmanifest|social-share)/)) {
        e.respondWith(fetch(SCOPE_PREFIX.slice(0, -1) + url.pathname + url.search, {
            headers: e.request.headers, credentials: e.request.credentials
        }));
        return;
    }
    // API calls: /session, /global/*, /provider, /config, /path, /pty/*, etc.
    e.respondWith(fetch(API_PREFIX + url.pathname + url.search, {
        method: e.request.method,
        headers: e.request.headers,
        body: e.request.body,
        credentials: e.request.credentials,
    }));
});
```

---

## Injected Scripts

Both injected before `</head>` by `rewriteHTML`.

### Script 1: SW Registration + Loading Overlay

Shows a dark overlay until the SW is controlling the page, preventing the OpenCode SPA from making unintercepted requests during first-visit SW installation.

```html
<style>#_appx_sw{position:fixed;inset:0;background:#0d0d0d;z-index:9999;display:flex;align-items:center;justify-content:center;color:#6b7280;font-family:monospace;font-size:.85rem}</style>
<div id="_appx_sw">starting…</div>
<script>
(function(){
  var el = document.getElementById('_appx_sw');
  if (!('serviceWorker' in navigator)) { el.remove(); return; }
  if (navigator.serviceWorker.controller) { el.remove(); return; }
  navigator.serviceWorker.register('/agent/PROJECT/sw.js', {scope:'/agent/PROJECT/'});
  navigator.serviceWorker.addEventListener('controllerchange', function(){ el.remove(); });
})();
</script>
```

`PROJECT` is replaced with the JSON-encoded project name at serve time.

**Behaviour:**
- If SW already controls the page (all visits after the first): overlay removed instantly, no visible flash
- First visit only: overlay shows "starting…" for ~100–300ms while SW installs and claims clients via `clients.claim()`

### Script 2: WebSocket URL Patcher

Service workers cannot intercept `new WebSocket()` calls (browser spec constraint). This script wraps the WebSocket constructor to rewrite OpenCode's PTY WebSocket URLs.

```html
<script>
(function(){
  var _WS = window.WebSocket;
  var origin = location.origin;
  var prefix = "/api/agent/PROJECT";
  window.WebSocket = function(url, protocols) {
    if (typeof url === 'string' && url.startsWith(origin.replace('https://','wss://').replace('http://','ws://'))) {
      url = url.replace(/^(wss?:\/\/[^/]+)\//, '$1' + prefix + '/');
    }
    return protocols ? new _WS(url, protocols) : new _WS(url);
  };
  window.WebSocket.prototype = _WS.prototype;
  window.WebSocket.CONNECTING = _WS.CONNECTING;
  window.WebSocket.OPEN = _WS.OPEN;
  window.WebSocket.CLOSING = _WS.CLOSING;
  window.WebSocket.CLOSED = _WS.CLOSED;
})();
</script>
```

Patches `window.WebSocket` (stable browser API, not a minified variable) so any WS connection OpenCode opens is routed through `AgentAPIHandler`. 

---

## Auth Simplification

Since the OpenCode UI is now on the same origin as the dashboard, cross-subdomain cookie sharing is no longer needed. Revert to:

- `SameSite: http.SameSiteStrictMode` (was changed to Lax for subdomains)
- No `Domain` attribute on the cookie (was `Domain: baseDomain`)
- `Auth.BaseDomain` field removed (no longer used for cookies; can be kept for middleware CSP if needed)

The agent token handoff (`CreateAgentToken`, `ValidateAndConsumeAgentToken`, `SetSubdomainSessionCookie`, `handleCreateAgentToken`, frontend `createAgentToken`) is removed entirely — it was only needed to work around the cross-subdomain cookie problem.

---

## What Is Removed

| Code | Reason |
|------|--------|
| `ContainerHandler` | Replaced by `AgentUIHandler` + `AgentAPIHandler` |
| Subdomain dispatch in `NewRouter` | Path-based routing used instead |
| `extractSubdomain` / `extractProjectName` | No longer needed |
| `proxy.ProjectNameKey` context injection | No longer needed |
| `CreateAgentToken` / `ValidateAndConsumeAgentToken` | Token handoff not needed |
| `SetSubdomainSessionCookie` | Not needed |
| `handleCreateAgentToken` handler | Not needed |
| `createAgentToken` in `client.ts` | Not needed |
| `SameSite=Lax` cookie change | Revert to Strict |
| `Domain: baseDomain` cookie change | Revert to no Domain |
| `Auth.BaseDomain` (if only used for cookie) | Remove if unused |

---

## What Is Restored

| Code | Notes |
|------|-------|
| `AgentUIHandler` | Simpler: no JS rewriting, adds SW injection |
| `AgentAPIHandler` | Unchanged from original; handles SSE + WS |
| `AssetCache` (HTML only) | Server-side HTML cache for graceful degradation |
| `startHook` on `project.Manager` | Cache invalidation on project start |
| `/agent/` and `/api/agent/` routes on dashboard mux | Same as original Phase 4 |

---

## CSP

With path-based routing, the CSP for `/agent/` routes needs to allow Service Workers:
- `script-src 'self' 'unsafe-inline'` — OpenCode uses inline scripts
- `worker-src 'self'` — required for SW registration
- `connect-src 'self' wss: ws:` — SSE and WebSocket

These apply to responses under `/agent/` only. Dashboard CSP is unchanged.

---

## Testing

**Automated:**
- `TestAgentUIHandlerServesModifiedHTML` — HTML rewriting + SW scripts injected
- `TestAgentUIHandlerServesSWFile` — SW file served with correct project name
- `TestAgentAPIHandlerForwardsSSE` — SSE proxied correctly
- `TestAgentAPIHandlerForwardsBasicAuth` — container auth injected
- `TestAgentAPIHandlerStripsCookie` — session not forwarded to container

**Manual verification:**
1. `task build && ./appx -port 8443`
2. Create and start a project
3. Click "Open" → navigates to `https://localhost:8443/agent/:name/`
4. Accept cert for `localhost:8443` (one-time, covers all projects)
5. Verify brief "starting…" overlay on first visit, instant on second
6. Verify OpenCode UI loads and API calls succeed (Network tab shows `/api/agent/:name/*`)
7. Verify no `?_appx_token` in URL
8. Verify session cookie has `SameSite=Strict`, no `Domain` attribute
9. Verify back/forward navigation works without re-auth
10. Verify the agent tab in `Project.tsx` loads the UI inline (not just a link)
