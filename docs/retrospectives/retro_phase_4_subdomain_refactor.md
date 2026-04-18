# Retrospective: Phase 4 — Proxy + Subdomain Refactor

**Date:** 2026-04-06
**Scope:** `phase-4-proxy` branch — proxy implementation, subdomain routing refactor
**Spec:** `docs/plans/phase_5_plan.md`

## Summary

Phase 4 delivered the reverse proxy and OpenCode web UI integration, culminating in a subdomain-based routing architecture that replaced a fragile HTML/JS content-rewriting pipeline. The subdomain approach is architecturally sound, but the refactor left a critical regression: `Project.tsx` still loads the agent UI via `/agent/:name/` iframe — a path that no longer exists after the refactor. Additionally, the subdomain routing is only wired for `localhost` deployments and will silently fail for production (`--domain`) setups.

---

## Issues

### 1. Project.tsx iframe loads a dead route

**Category:** Code Bug
**Severity:** Critical
**Location:** `web/src/pages/Project.tsx:376`

**Problem:**
The Project page renders an iframe with `src="/agent/${project.name}/"` when the Agent tab is active. This path was handled by the old `AgentUIHandler` which was deleted in the subdomain refactor. Under the new routing, a request for `/agent/test3/` arrives at `localhost:8443`, has no subdomain, is dispatched to `dashboardHandler`, falls through the mux to the SPA handler, and returns the React app's `index.html` — not the OpenCode UI. The Agent tab currently shows the appx dashboard, not the opencode interface.

**Suggested Fix:**
Two options:

Option A — Remove the iframe, replace with a link that opens in a new tab (simpler):
```tsx
// Project.tsx
if (activeTab === 'agent') {
  const port = window.location.port ? `:${window.location.port}` : '';
  const agentURL = `${window.location.protocol}//${project.name}.localhost${port}/`;
  return (
    <div style={styles.centered}>
      <p style={{ color: 'var(--text-muted)' }}>OpenCode runs in its own tab.</p>
      <a href={agentURL} target="_blank" rel="noopener noreferrer" data-btn="outline-green">
        Open in new tab →
      </a>
    </div>
  );
}
```

Option B — Keep the iframe but load from the subdomain origin (requires CSP fix):
Change the subdomain CSP `frame-ancestors 'self'` to `frame-ancestors https://localhost:8443` (or `frame-ancestors *` for dev). Then update the iframe src to use the subdomain URL. More complex because the iframe origin is now `test3.localhost:8443` while the parent is `localhost:8443` — cross-origin communication between them is restricted by browser sandbox.

Option A is strongly recommended. The cross-origin iframe model (parent at `localhost`, frame at `test3.localhost`) creates ongoing complexity around postMessage, focus events, and CSP. A full-page separate tab is the correct UX for a separate origin.

**Suggested Tests:**
- Visual regression: verify Agent tab no longer shows the React dashboard's login/project list
- Verify the link opens to the correct subdomain URL with the right port

---

### 2. `extractProjectName` hardcodes `.localhost`, diverges from `extractSubdomain`

**Category:** Architectural
**Severity:** High
**Location:** `internal/proxy/proxy.go:60-70`

**Problem:**
The router's `extractSubdomain(host, baseDomain)` correctly uses a `baseDomain` parameter, making it work for any domain. But `ContainerHandler`'s `extractProjectName(host)` hardcodes `.localhost`. When the router dispatches a request to `ContainerHandler` because `extractSubdomain` matched (e.g., on a production domain `test3.appx.example.com`), `extractProjectName` will return `""` and `ContainerHandler` will respond with 400. The routing would work correctly for the router but silently fail inside the proxy.

There are now two implementations of the same logic that will drift over time. The inconsistency is already present: `extractSubdomain` rejects multi-level subdomains (`!strings.Contains(sub, ".")`) but `extractProjectName` allows them (the guard was added but checks `sub == "" || strings.Contains(sub, ".")` which is correct — but the point is they're separate code paths with separate guard implementations).

**Suggested Fix:**
The cleanest approach is for the router to attach the project name to the request context before passing to `ContainerHandler`, so the proxy never needs to re-parse the host:

```go
// router.go
type contextKey string
const projectNameKey contextKey = "projectName"

// in NewRouter dispatch:
if projectName != "" {
    ctx := context.WithValue(r.Context(), projectNameKey, projectName)
    containerProxy.ServeHTTP(w, r.WithContext(ctx))
    return
}

// proxy.go ContainerHandler:
name, ok := r.Context().Value(projectNameKey).(string)
if !ok || name == "" {
    // fallback: extract from host (for direct use without router middleware)
    name = extractProjectName(r.Host)
}
```

This eliminates the duplication entirely. If context injection feels heavy, at minimum `extractProjectName` should accept `baseDomain` as a parameter and be shared with the router.

**Suggested Tests:**
- Unit test `extractSubdomain` and `extractProjectName` with the same inputs and verify they produce identical outputs
- Test that multi-level subdomains are rejected by both

---

### 3. `BaseDomain` is hardcoded to `"localhost"` — production deployments broken

**Category:** Architectural
**Severity:** High
**Location:** `cmd/appx/main.go:147`, `internal/server/middleware.go:31`

**Problem:**
`main.go` always passes `BaseDomain: "localhost"` to the server config, regardless of whether `--domain` is set. When appx is running with `--domain appx.example.com` (CertMagic mode), the router's `extractSubdomain` uses `baseDomain="localhost"`, so subdomain routing is effectively disabled — all requests go to the dashboard handler regardless of the host. Additionally, `middleware.go` hardcodes `strings.HasSuffix(host, ".localhost")` for CSP determination, so production subdomain requests get the strict dashboard CSP instead of the permissive container CSP.

The "Open" button in `ProjectCard.tsx` also hardcodes `.localhost`, so it would navigate to `test3.localhost:8443` even when running on a production domain.

**Suggested Fix:**
1. In `main.go`, derive `BaseDomain` from `--domain` if set, otherwise default to `localhost`:
```go
baseDomain := "localhost"
if *domain != "" {
    baseDomain = *domain
}
// pass baseDomain to server.Config
```

2. In `middleware.go`, accept a `baseDomain` parameter or pass it through `Config`:
```go
return securityHeaders(baseDomain, http.HandlerFunc(func(...) {
    ...
    if strings.HasSuffix(host, "."+baseDomain) && host != baseDomain {
        // permissive CSP
    }
}))
```

3. In `ProjectCard.tsx`, derive the base domain from the current host:
```tsx
const hostBase = window.location.hostname; // "localhost" or "appx.example.com"
window.location.href = `${window.location.protocol}//${project.name}.${hostBase}${port}/`;
```

**Suggested Tests:**
- Test `extractSubdomain` with a non-localhost base domain (e.g., `"example.com"`)
- Test that the router dispatches correctly when `baseDomain` is `"example.com"`
- Test that `securityHeaders` applies permissive CSP for `test3.example.com` when `baseDomain` is `example.com`

---

### 4. `ContainerAddr` calls Docker inspect on every proxied request

**Category:** Performance
**Severity:** Medium
**Location:** `internal/project/container.go:138-168`, `internal/proxy/proxy.go:110`

**Problem:**
`ContainerHandler` calls `pm.ContainerAddr(proj.ID, AgentPort)` on every HTTP request. `ContainerAddr` does a full `ContainerInspect` via the Docker daemon, which is a network call to the Docker socket. For a single page load of the OpenCode UI, the browser makes 20+ requests (HTML, JS bundles, CSS, fonts, API calls, SSE connection). Each triggers a Docker inspect call. Under concurrent use, this adds latency and load on the Docker daemon.

The host-mapped port doesn't change while the container is running — it's stable from the time Docker assigns it until the container is removed.

**Suggested Fix:**
Cache the result in the Manager with invalidation on container stop/remove:

```go
// In Manager
type portCache struct {
    mu    sync.RWMutex
    ports map[string]map[int]string // containerID → port → "127.0.0.1:hostPort"
}

func (m *Manager) ContainerAddr(id string, containerPort int) (string, error) {
    // Check cache first
    if addr, ok := m.cache.get(proj.ContainerID, containerPort); ok {
        return addr, nil
    }
    // Inspect and cache
    addr := "127.0.0.1:" + bindings[0].HostPort
    m.cache.set(proj.ContainerID, containerPort, addr)
    return addr, nil
}
```

Invalidate the cache in `doStop` and `Delete`. This reduces Docker API calls for running projects to one per (container, port) pair.

**Suggested Tests:**
- Verify that two calls to `ContainerAddr` for the same container only trigger one Docker inspect
- Verify cache is invalidated when container is stopped

---

### 5. Phase 5 clickjacking finding is already resolved

**Category:** Forward-Looking
**Severity:** Low (informational)
**Location:** `internal/server/middleware.go`, `docs/plans/phase_5_plan.md:113`

**Problem:**
Phase 5 plan lists FINDING-04 ("add `frame-ancestors 'self'` to `/agent/` routes") as a task in its implementation order. This finding is already resolved by the subdomain refactor: `middleware.go` now applies `frame-ancestors 'self'` to all subdomain requests (any host matching `*.localhost`). The `/agent/` path-based check in the Phase 5 plan no longer exists.

**Suggested Fix:**
Update `docs/plans/phase_5_plan.md` to remove the clickjacking task from the implementation order. Remove FINDING-04 from the Phase 5 security findings section and note it was addressed in Phase 4 as part of the subdomain refactor.

**Suggested Tests:**
- Verify `securityHeaders` includes `frame-ancestors 'self'` in CSP for subdomain requests
- Verify this header is absent for dashboard requests (it's implicitly handled by X-Frame-Options: DENY)

---

### 6. `AgentPort` constant duplicated across packages

**Category:** Code Quality
**Severity:** Low
**Location:** `internal/proxy/proxy.go:19`, `internal/project/container.go:29`

**Problem:**
`AgentPort = 4096` is defined twice: once in `proxy` (used by `ContainerHandler`) and once in `project` (used when publishing ports at container creation). Both must stay in sync. A comment in `container.go` acknowledges this: _"Duplicated from the proxy package to avoid a circular import."_

While the circular import concern is real (`proxy` imports `project`, so `project` can't import `proxy`), the duplication is a maintenance hazard — changing the port requires editing two files.

**Suggested Fix:**
Move `AgentPort` to a new `internal/config` package (or `internal/appx` package) that neither `proxy` nor `project` imports, and have both packages import it from there. Alternatively, if the port is meant to be configurable, thread it as a parameter through the Manager constructor and into `doFullCreate`.

**Suggested Tests:**
- No tests needed — this is a structural refactor with no behavior change. The existing port publishing and proxy tests provide sufficient coverage.

---

## Forward-Looking: Phase 5 Compatibility

Phase 5 adds egress logging. The current transparent proxy design has one implication worth noting:

**HTTP-level egress logging is not possible through the appx proxy.** The `ContainerHandler` transparently forwards requests and responses — it doesn't have access to outbound requests the container makes to the internet. Egress logging must be done at the network layer (DNS-based, iptables, or eBPF), exactly as the Phase 5 plan describes. The current proxy architecture doesn't hinder any of the four approaches listed in the Phase 5 plan.

The Phase 5 approach (DNS/iptables/eBPF) is the correct one regardless of the proxy design, since it captures all outbound traffic — not just traffic that happens to flow through the proxy.
