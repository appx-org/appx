# Retrospective: Phase 5 + Phase 5a (Full Branch Review)

**Date:** 2026-04-10
**Scope:** Branch `de-docker-refactor` — 90 commits, 96 files changed (~22k insertions, ~6k deletions)
**Spec:** `docs/plans/phase_6_plan.md` (next phase), `docs/plans/phase_8_plan.md` (native clients)
**Prior retro:** `docs/retrospectives/retro_phase_5_dedocker.md` (2026-04-07)

## Summary

Phase 5 (de-Docker) and Phase 5a (custom agent UI) are complete. The core architectural shift — from Docker containers to a single OpenCode process with appx as management shell — is clean and well-executed. The headless agent core (`agent-core/`) with pure reducers is a strong foundation for Phase 8's shared hooks. Several issues from the Phase 5 retro were fixed (port TOCTOU race, async egress logging, scaffold cleanup, SameSite comment). However, three issues flagged in the prior retro remain unfixed in the frontend, and a few new issues surfaced from the Phase 5a agent UI work.

The highest-priority items are the two hardcoded frontend values (`projectDir` and subdomain URL) — the backend infrastructure to fix them is already in place but the frontend hasn't been updated to use it.

---

## Issues

### 1. Frontend still hardcodes `projectDir` to `/home/opencode/projects/`

**Category:** Correctness
**Severity:** High
**Location:** `web/src/pages/Project.tsx:43`

**Problem:**

```typescript
const projectDir = project ? `/home/opencode/projects/${project.name}` : '';
```

The prior retro (issue #3) flagged this. Since then, the fix was partially implemented: `Project.ProjectDir` field exists on the Go struct, `handleGetProject` and `handleListProjects` both populate it, `client.ts` includes `projectDir?: string` on the `Project` type, and there are passing tests (`TestGetProject_HasProjectDir`, `TestListProjects_HasProjectDir`).

But `Project.tsx` still hardcodes the path instead of reading `project.projectDir` from the API response. This means:
- In `--http` dev mode the path is wrong (actual: `./data/projects/<name>`, hardcoded: `/home/opencode/projects/<name>`)
- The terminal opens with the wrong `x-opencode-directory` header, so OpenCode assigns it to the wrong project (or no project)
- The agent SessionList and ChatPanel receive the wrong directory, making SDK calls to a non-existent project

**Suggested Fix:**

One-line change:

```typescript
const projectDir = project?.projectDir ?? '';
```

**Suggested Tests:**
- Manual: run in `--http` mode, open a project, verify the terminal PTY starts in the correct directory and the agent session list loads

---

### 2. Subdomain URLs still hardcoded to `localhost`

**Category:** Correctness
**Severity:** High
**Location:** `web/src/pages/Project.tsx:49`, `web/src/components/ProjectCard.tsx:43`

**Problem:**

Both components construct the "Open App" URL as:

```typescript
return `${proto}//${project.name}.localhost${portSuffix}/`;
```

The prior retro (issue #2) flagged this. The backend fix was implemented: `GET /api/config` returns `{ baseDomain: "..." }`, and `getServerConfig()` exists in `client.ts`. But neither `Project.tsx` nor `ProjectCard.tsx` calls `getServerConfig()` — they hardcode `.localhost`.

In a production deployment at `user.appx.app`, the "Open App" link points to `myapp.localhost:443/`, which resolves to the user's local machine.

**Suggested Fix:**

In `Dashboard.tsx`, fetch the server config once and pass `baseDomain` down to `ProjectCard`. In `Project.tsx`, fetch it on mount (or accept it from the router context). Replace:

```typescript
`${proto}//${project.name}.localhost${portSuffix}/`
```

with:

```typescript
`${proto}//${project.name}.${baseDomain}${portSuffix}/`
```

Fallback to `localhost` if the config fetch fails.

**Suggested Tests:**
- `TestGetConfig_ReturnsDomain` already exists and passes
- Manual: verify the link text changes when running with `--domain`

---

### 3. `handleGetProject` doesn't populate `AppRunning`

**Category:** Correctness
**Severity:** High
**Location:** `internal/server/project_handlers.go:73-88`

**Problem:**

`handleListProjects` runs the health checker and sets `p.AppRunning` on each project. But `handleGetProject` does not — it only sets `ProjectDir`:

```go
proj.ProjectDir = pm.ProjectDir(proj.Name)
writeJSON(w, proj)
```

Since `AppRunning` defaults to `false`, the Project detail page always shows the app as not running, even when it is. This affects:
- The "APP RUNNING" badge in the header (never shown)
- The "Open App" link (never shown, since it's gated on `project.appRunning`)

The user navigates to a project, sees no "Open App" link, goes back to the dashboard where `appRunning: true` is correctly shown. Inconsistent behavior.

**Suggested Fix:**

Add a health check in `handleGetProject`:

```go
func handleGetProject(pm *project.Manager, hc *project.HealthChecker) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ...
        proj.ProjectDir = pm.ProjectDir(proj.Name)
        health := hc.Check([]*project.Project{proj})
        proj.AppRunning = health[proj.ID]
        writeJSON(w, proj)
    }
}
```

Update `NewRouter` to pass `hc` to `handleGetProject`.

**Suggested Tests:**
- Add a test in `router_test.go` that inserts a project, starts a TCP listener on its port, calls `GET /api/projects/{id}`, and asserts `appRunning: true`

---

### 4. Health checks run sequentially — O(n) latency

**Category:** Performance
**Severity:** Medium
**Location:** `internal/project/health.go:26-43`

**Problem:**

`HealthChecker.Check()` dials each project's port sequentially with a 500ms timeout. With 50 projects (half the port range), the worst case is 25 seconds — 500ms per project when the port is closed (full timeout). The dashboard polls `GET /api/projects` every 10 seconds. If health checks take longer than the poll interval, requests pile up.

The comment acknowledges this: "callers that need low latency should invoke Check from a goroutine." But the actual caller (`handleListProjects`) calls it synchronously on the request goroutine.

**Suggested Fix:**

Parallelize the health checks with a bounded worker pool:

```go
func (hc *HealthChecker) Check(projects []*Project) map[string]bool {
    result := make(map[string]bool, len(projects))
    var mu sync.Mutex
    var wg sync.WaitGroup
    sem := make(chan struct{}, 20) // limit concurrent dials
    for _, p := range projects {
        if p.AssignedPort <= 0 {
            result[p.ID] = false
            continue
        }
        wg.Add(1)
        go func(p *Project) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()
            addr := "127.0.0.1:" + strconv.Itoa(p.AssignedPort)
            conn, err := net.DialTimeout("tcp", addr, healthDialTimeout)
            mu.Lock()
            if err != nil {
                result[p.ID] = false
            } else {
                conn.Close()
                result[p.ID] = true
            }
            mu.Unlock()
        }(p)
    }
    wg.Wait()
    return result
}
```

Worst case drops from 25s to ~1.5s (50 projects / 20 workers * 500ms).

**Suggested Tests:**
- The existing `TestListProjects_AppRunningField` already covers the functional path
- A benchmark test with many closed ports would validate the improvement

---

### 5. OpenCode proxy creates a new `ReverseProxy` per request

**Category:** Performance
**Severity:** Medium
**Location:** `internal/server/router.go:161-181`

**Problem:**

`openCodeProxyHandler` creates a new `httputil.ReverseProxy` on every request:

```go
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // ...
    proxy := &httputil.ReverseProxy{
        Director: func(req *http.Request) { ... },
        FlushInterval: -1,
    }
    proxy.ServeHTTP(w, r)
})
```

Each proxy uses the `http.DefaultTransport` (since no `Transport` is specified), so connection pooling technically works via the shared default transport. However, the per-request allocation is unnecessary overhead — one `ReverseProxy` should be created once (like the subdomain proxy at lines 86-91 which has a shared transport).

For SSE streaming (long-lived connections to OpenCode), creating a proxy per-request means extra GC pressure on every event stream reconnect.

**Suggested Fix:**

Create the `ReverseProxy` once in `openCodeProxyHandler` and rewrite the path in the `Director`:

```go
func openCodeProxyHandler(backendURL string) http.Handler {
    target, _ := url.Parse(backendURL)
    proxy := &httputil.ReverseProxy{
        Director: func(req *http.Request) {
            req.URL.Path = path.Clean(strings.TrimPrefix(req.URL.Path, "/api/opencode"))
            req.URL.RawPath = ""
            if req.URL.Path == "." {
                req.URL.Path = "/"
            }
            req.URL.Scheme = target.Scheme
            req.URL.Host = target.Host
            req.Host = target.Host
            req.Header.Del("Cookie")
        },
        FlushInterval: -1,
    }
    return proxy
}
```

**Suggested Tests:**
- Existing proxy tests (`TestOpenCodeProxy_Authed_ForwardsRequest`, `TestOpenCodeProxy_Authed_PreservesQueryString`) validate the behavior; no new tests needed

---

### 6. AGENTS.md template hardcodes `.localhost` subdomain

**Category:** Correctness
**Severity:** Medium
**Location:** `internal/project/manager.go:113`

**Problem:**

```go
content = strings.ReplaceAll(content, "{{subdomain}}", fmt.Sprintf("%s.localhost", proj.Name))
```

The AGENTS.md scaffolded into every new project always says the app will be at `<name>.localhost`. In a production deployment with `--domain user.appx.app`, the correct subdomain is `<name>.user.appx.app`. The agent reads AGENTS.md for port and URL guidance — wrong guidance means the agent may configure apps with incorrect URLs.

**Suggested Fix:**

Accept `baseDomain` as a parameter to `NewManager` (or `scaffoldProject`):

```go
func NewManager(store *Store, projectRoot string, baseDomain string) *Manager {
    return &Manager{Store: store, ProjectRoot: projectRoot, BaseDomain: baseDomain}
}
```

Then:

```go
content = strings.ReplaceAll(content, "{{subdomain}}", fmt.Sprintf("%s.%s", proj.Name, m.BaseDomain))
```

**Suggested Tests:**
- A manager test that creates a project with a non-localhost base domain and verifies the AGENTS.md content

---

### 7. `internal/terminal/` package is dead code

**Category:** Dead Code
**Severity:** Low
**Location:** `internal/terminal/` (6 files, ~700 lines)

**Problem:**

The prior retro (issue #4) flagged this. The package is still on disk, compiles as part of the module, but is not imported by any Go file. The terminal WebSocket handler routes were removed from `router.go`. The `Terminal.tsx` component connects directly to OpenCode's PTY endpoint.

Additionally, `handleGetTerminalBufferSize` and `handleSetTerminalBufferSize` are still registered in the router (`router.go:52-53`) and have passing tests. They read/write a setting that controls nothing — the setting is unused because the terminal package isn't wired.

**Suggested Fix:**

Delete `internal/terminal/` entirely. Remove the terminal buffer size handler registrations and their tests. Remove `handleGetTerminalBufferSize` and `handleSetTerminalBufferSize` from `settings_handlers.go`.

If the package is intentionally retained for future use, add a `// Retained for Phase N — not currently wired` comment at the package level.

**Suggested Tests:**
- Verify the build compiles cleanly after deletion

---

### 8. `AuthRequiredHeader` comment references Docker containers

**Category:** Documentation
**Severity:** Low
**Location:** `internal/auth/auth.go:31-35`

**Problem:**

```go
// AuthRequiredHeader is set on 401 responses from this middleware so that the
// Service Worker can distinguish an appx session expiry (should redirect to
// login) from a container-level 401 (wrong OPENCODE_SERVER_PASSWORD), which
// should not trigger a login redirect.
const AuthRequiredHeader = "X-Appx-Auth"
```

There are no containers, no `OPENCODE_SERVER_PASSWORD`, and no Service Worker anymore (the SW was part of the Phase 4 proxy, deleted in Phase 5). The header is still functionally useful — it tells the frontend this is an appx auth failure, not an OpenCode backend error — but the comment is misleading.

**Suggested Fix:**

Update the comment:

```go
// AuthRequiredHeader is set on 401 responses from the auth middleware so that
// API clients can distinguish an appx session expiry from an OpenCode backend
// error. The frontend redirects to /login when it sees this header on a 401.
const AuthRequiredHeader = "X-Appx-Auth"
```

---

### 9. `isUniqueViolation` helper in store.go is unused

**Category:** Dead Code
**Severity:** Low
**Location:** `internal/project/store.go:248-253`

**Problem:**

The `isUniqueViolation` function was written as a helper but is never called. The `Create` method handles unique violations inline with `strings.Contains(err.Error(), "projects.name")` and a separate `strings.Contains(err.Error(), "UNIQUE constraint")` catch-all.

**Suggested Fix:**

Delete the function.

---

## Forward-Looking Assessment

### Phase 6 (Installer + Security)

**What Phase 5/5a makes easy:**
- Bearer token auth (Phase 6, Step 1) is a small change to `auth.Middleware` — check `Authorization: Bearer` before falling back to cookie. The token infrastructure (sessions table, SHA-256 hashing) is already there.
- The initial password file is already generated in `main.go:88-93`. Phase 6 just formalizes the deletion on first login.
- The egress proxy and iptables rules are independent — the installer just calls `iptables` commands.

**What needs to be fixed first:**
- Issue #1 (hardcoded `projectDir`) MUST be fixed before Phase 6. The installer sets up `/home/opencode/projects/` as the project root, and the backend already resolves the correct path. The frontend just needs to use it.
- Issue #6 (AGENTS.md subdomain) should be fixed so the installer's `--domain` flag flows through to project scaffolding.
- Issue #7 (dead terminal package) should be cleaned up before Phase 6 adds more code. Otherwise it'll be unclear whether the package is part of the new architecture.

### Phase 8 (Native Clients)

**What Phase 5a makes easy:**
- The `agent-core/` headless layer (types, reducers, connection) is pure TypeScript with no React dependency — exactly what Phase 8 needs for `packages/agent`.
- The `agent-react/` hooks (`useSession`, `useEventStream`, `usePermissions`) map directly to Phase 8's `useAgentStream` and `useSessionManager`.
- The OpenCode SDK client wrapper (`opencode.ts`) uses only `fetch` — works in React Native.

**What needs attention:**
- The reducer (`reducers.ts`) handles all SSE event types with proper upsert/remove semantics. Phase 8's hook layer should import the reducer directly from the shared package, not rewrite it.
- `getClient()` caches SDK clients by directory path. For React Native, the cache key should probably include the server URL too (Phase 8 connects to a remote server, not localhost).

---

## Previously Fixed Issues (from Phase 5 Retro)

For completeness, these issues from the prior retro have been addressed:

- **Issue #1 (Port TOCTOU race):** Fixed in commit `c529efb` — Create now wraps port allocation and INSERT in a transaction.
- **Issue #5 (Async egress logging):** Fixed — `proxy.go` now logs synchronously before returning 403. No more `time.Sleep` in tests.
- **Issue #6 (SameSite comment):** Fixed — `middleware.go:49-53` now correctly documents `SameSite=Lax` and the CSRF reasoning.
- **Issue #7 (SetAuth body shape):** Fixed in commit `76fd223` — uses `PUT /auth/:providerID` with `{type: "api", key: "..."}`.
- **Issue #8 (Scaffold cleanup):** Fixed — `manager.go:57` calls `os.RemoveAll(projectDir)` before DB rollback.
- **Issue #3 (projectDir backend):** Partially fixed — backend returns `projectDir` in responses, but frontend still hardcodes it (see Issue #1 above).
- **Issue #2 (Subdomain URL backend):** Partially fixed — `GET /api/config` exists, but frontend doesn't use it (see Issue #2 above).

---

## Issues Fixed in This Session (2026-04-10)

All issues except #7 (terminal package dead code) were resolved:

- **Issue #1 (projectDir):** `Project.tsx` now reads `project.projectDir` from API response.
- **Issue #2 (subdomain URLs):** `Dashboard.tsx` fetches `baseDomain` via `getServerConfig()` and passes it to `ProjectCard`. `Project.tsx` also fetches and uses it. No more hardcoded `.localhost`.
- **Issue #3 (AppRunning):** `handleGetProject` now accepts `HealthChecker` and populates `AppRunning`. New test: `TestGetProject_HasAppRunning`.
- **Issue #4 (parallel health):** `HealthChecker.Check` now uses goroutines with a bounded semaphore (20 workers). New test: `TestHealthChecker_ManyProjectsConcurrentCorrectness`. Race detector passes.
- **Issue #5 (proxy allocation):** `openCodeProxyHandler` now creates a single `ReverseProxy` at handler construction time with path rewriting in the Director.
- **Issue #6 (AGENTS.md):** `Manager.BaseDomain` field added, wired in `main.go`. Template uses configured domain. New test: `TestManagerCreate_AGENTSmdUsesBaseDomain`.
- **Issue #8 (stale comments):** `AuthRequiredHeader` comment updated to remove Docker/SW references.
- **Issue #9 (dead code):** `isUniqueViolation` helper deleted from `store.go`.
