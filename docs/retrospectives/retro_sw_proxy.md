# Retrospective: Service Worker Proxy (Phase 4 final)

**Date:** 2026-04-06
**Scope:** `phase-4-proxy` branch — complete Phase 4 including SW proxy refactor, security hardening, network cleanup
**Spec:** `docs/plans/phase_5_plan.md`, `docs/plans/phase_6_plan.md`

## Summary

Phase 4 delivered the reverse proxy and OpenCode web UI integration via a path-based approach using a Service Worker to intercept and rewrite fetch calls from the SPA. The implementation is clean and the SW approach correctly avoids the brittle JS bundle patching of earlier attempts. Two issues stand out: a dead variable in `rewriteHTML` with a misleading safety comment, and a missing `skipWaiting()` in the SW that means new SW versions won't activate until the user closes all agent tabs.

---

## Issues

### 1. Dead variable in `rewriteHTML` — misleading XSS safety comment

**Category:** Code Bug
**Severity:** Medium
**Location:** `internal/proxy/proxy.go:198-199`

**Problem:**
`rewriteHTML` computes a JSON-encoded project name for "safe embedding in JavaScript string literals" but immediately discards it:

```go
encodedName, _ := json.Marshal(projectName)
_ = string(encodedName) // e.g. "test3" (with quotes, JSON-encoded)
```

The scripts below continue to embed `projectName` directly via string concatenation — without JSON encoding. The comment implies XSS protection that is not actually applied. Project names are validated slugs (`[a-z][a-z0-9-]+`) so there is no practical injection risk, but a future maintainer reading this code will believe the encoding is in place and may not add it when extending the scripts.

Compare with `generateSWScript` (line 248-253), which correctly uses the JSON-encoded name:

```go
encodedName, _ := json.Marshal(projectName)
name := string(encodedName)
script := `const PROJECT=` + name + `;`
```

The inconsistency is confusing and `rewriteHTML` could trivially use the same approach.

**Suggested Fix:**
Either use the encoded name in the injected scripts (for consistency and future safety), or delete the dead lines entirely and add a comment explaining why direct embedding is safe:

```go
// projectName is a validated slug ([a-z][a-z0-9-]+) — safe to embed directly.
swScript := `<script>(function(){` +
    `navigator.serviceWorker.register('/agent/` + projectName + `/sw.js', ...)`
```

**Suggested Tests:**
- Verify `rewriteHTML` with a project name containing only valid slug chars embeds without corruption
- Verify `generateSWScript` and `rewriteHTML` produce consistent project name embedding patterns

---

### 2. SW missing `skipWaiting()` — new versions don't activate until all tabs closed

**Category:** Code Bug
**Severity:** Medium
**Location:** `internal/proxy/proxy.go:254` (`generateSWScript`)

**Problem:**
The generated Service Worker calls `clients.claim()` on `activate` but has no `install` handler:

```javascript
self.addEventListener("activate", e => e.waitUntil(clients.claim()));
// No install handler
```

When the server-side asset cache is cleared (container restart triggers `startHook` → `AssetCache.Clear`), fresh HTML is fetched and a new SW script is served. However, if the user already has an agent tab open, the browser:
1. Installs the new SW
2. Waits for all pages it controls to close before activating (default SW lifecycle)
3. The old SW continues intercepting requests

This means after a container rebuild the user must close and reopen the agent tab to get the new routing. In practice, the SW script content is identical between restarts (project name doesn't change), so this is rarely observable today. But if appx itself is updated with a new SW implementation, users would be stuck on the old SW until they manually reload.

**Suggested Fix:**
Add `skipWaiting()` to the install handler in `generateSWScript`:

```javascript
self.addEventListener("install", e => e.waitUntil(skipWaiting()));
self.addEventListener("activate", e => e.waitUntil(clients.claim()));
```

`skipWaiting()` tells the browser to activate the new SW immediately, bypassing the wait. Combined with `clients.claim()`, the new SW takes control of all open pages without requiring a reload.

**Suggested Tests:**
- Verify the generated SW script contains both `skipWaiting` in install and `clients.claim` in activate
- Manual: Install the SW, update the SW script on the server, reload the page — new SW should activate without closing other tabs

---

### 3. `AssetCache.Clear` evicts all projects when any container starts

**Category:** Architectural
**Severity:** Low
**Location:** `internal/proxy/assets.go:57-66`

**Problem:**
`Clear(name string)` accepts a project name parameter but ignores it, evicting all cached HTML and assets:

```go
func (c *AssetCache) Clear(name string) {
    // Clears EVERYTHING, ignoring `name`
    for k := range c.html { delete(c.html, k) }
    for k := range c.assets { delete(c.assets, k) }
}
```

When project A's container restarts, project B's cached assets are also purged. Project B's next page load fetches all assets from its container unnecessarily. This is documented as a "conservative strategy" to handle base image rebuilds, but assets are content-hashed by Vite (e.g., `index-Ca44lNAO.js`) — they don't change unless the content changes. Clearing project B's assets when project A starts is wasteful.

For a personal tool with few projects this has negligible impact. But the `name` parameter in the signature creates a false expectation that per-project clearing is implemented.

**Suggested Fix:**
Either implement per-project clearing (clear HTML for `name`, leave shared assets alone since they're content-addressed), or rename the parameter to make the behavior explicit:

```go
// ClearAll removes all cached HTML and assets for all projects. Called when
// any container starts to ensure stale content is not served after a rebuild.
func (c *AssetCache) ClearAll(_ string) {
```

The `_ string` signature matches the `func(string)` startHook type while making it obvious the argument is unused.

**Suggested Tests:**
- Verify `Clear("proj-a")` also clears `GetHTML("proj-b")` (documents current behavior explicitly)
- If per-project clearing is added: verify clearing proj-a does NOT clear proj-b's assets

---

### 4. Session expiry leaves user stranded in agent iframe

**Category:** Architectural
**Severity:** Low
**Location:** `web/src/pages/Project.tsx:371-380`, `internal/server/router.go`

**Problem:**
When the 30-day session expires, `AgentAPIHandler` returns 401 for all OpenCode API calls. The SW intercepts fetch calls from the SPA and rewrites them to `/api/agent/:name/...`, but neither the SW nor the SPA has a mechanism to detect a 401 and redirect to the login page. The user sees error toasts from OpenCode ("Request failed") with no path back to the login screen.

The iframe doesn't forward the parent's `window.location` — navigating to `/login` from inside the iframe would only navigate the iframe content, not the parent page.

**Suggested Fix:**
Two options:

Option A — intercept 401 in the SW and redirect the parent page:
```javascript
e.respondWith(fetch(new Request(rewritten, init)).then(resp => {
  if (resp.status === 401) {
    clients.matchAll({type: 'window'}).then(clients =>
      clients.forEach(c => c.navigate('/login'))
    );
  }
  return resp;
}));
```

Option B — add a `postMessage` from the iframe to the parent on 401 detection (in the OpenCode SPA, not possible), or poll session validity from the parent `Project.tsx` and unmount the iframe on 401.

Option A is self-contained within the SW and doesn't require modifying OpenCode. It is the simpler fix.

**Suggested Tests:**
- Verify that a 401 response from `AgentAPIHandler` triggers a redirect to `/login` (or iframe navigation is handled gracefully)

---

## Forward-Looking Notes

### Phase 5 compatibility

Phase 5 adds egress logging and Dockerfile supply chain hardening. Neither touches the proxy layer. The SW approach doesn't affect egress logging (which operates at the network/iptables level, not the HTTP proxy level). Phase 5's clickjacking fix (`frame-ancestors 'self'`) is already implemented in the new `securityHeaders`. The Dockerfile pinning is independent. **No conflicts.**

### Phase 6 compatibility

Phase 6 requires `AgentAPIHandler` to accept bearer tokens for React Native clients. The current handler goes through `a.Middleware()` which only checks the session cookie. The Phase 6 plan already documents this gap and proposes updating `auth.Middleware` to check `Authorization: Bearer` before falling back to the cookie. **No architectural changes needed — the interface is ready.**

The path-based `AgentAPIHandler` at `/api/agent/:name/*` is exactly what Phase 6 native clients need. The SW-based browser approach and the bearer-token native approach use the same server-side proxy endpoint. **Good forward compatibility.**
