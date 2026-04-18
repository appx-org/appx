# Phase 5 Retrospective: De-Docker Simplification

**Branch:** de-docker-refactor  
**Date:** 2026-04-07  
**Reviewer:** Code Review

---

## Summary

Phase 5 delivered a significant architectural simplification. The per-project Docker container lifecycle — the largest and most complex code surface in appx — was deleted entirely. In its place: a single OpenCode server process, appx-assigned ports for agent-built apps, AGENTS.md scaffolding with git init, a TCP health checker, an OpenCode HTTP client with startup polling and API key injection, a Go CONNECT egress proxy with allowlist and logging, and a new agent-interaction frontend built on the OpenCode SDK.

The plan was followed closely. All seven steps were completed. The core abstraction shift — from appx managing N OpenCode containers to appx being a thin management shell around a single OpenCode process — is clean and coherent. Test coverage is good for the new packages. The egress proxy implementation is correct and well-tested.

The issues below are real problems, not theoretical ones. Several will affect correctness or security in production.

---

## Issues

### 1. Port allocator has a TOCTOU race under concurrent creates

**Category:** Correctness  
**Severity:** Critical  
**Location:** `/Users/max/misc/pj/appx/internal/project/store.go` — `nextAvailablePort` and `Create`

**Problem:**

`nextAvailablePort` reads allocated ports in one query, then `Create` does the INSERT in a separate statement. There is no transaction or row lock spanning both operations. Under concurrent requests (two users hitting "Create project" simultaneously), both calls can read the same set of allocated ports, assign the same next port, and then one INSERT will succeed while the other receives a UNIQUE constraint violation on `assigned_port`.

The current code handles the UNIQUE violation on the `name` column and maps it to `ErrDuplicateName`, but there is no corresponding check for a UNIQUE violation on `assigned_port`. A simultaneous port collision will produce a generic `"insert project: UNIQUE constraint failed: projects.assigned_port"` error that propagates to the caller as a 500 Internal Server Error.

The constraint violation error string is: `"UNIQUE constraint failed: projects.assigned_port"`. The `isUniqueViolation` helper only checks the substring `"UNIQUE constraint"` — it does not differentiate which column caused it — so the duplicate port case would also return `ErrDuplicateName`, which is semantically wrong and confusing.

**Suggested Fix:**

Wrap `nextAvailablePort` and the INSERT in a single SQLite transaction with `BEGIN IMMEDIATE` to serialize concurrent creates. Alternatively, detect port UNIQUE violations in `Create` and return `ErrDuplicateName` only when the error references the `name` column, or retry with the next port on a port collision.

```go
func (s *Store) Create(name string) (*Project, error) {
    if err := ValidateName(name); err != nil {
        return nil, err
    }
    id := uuid.New().String()
    tx, err := s.db.Begin()
    if err != nil {
        return nil, fmt.Errorf("begin tx: %w", err)
    }
    defer tx.Rollback()

    port, err := s.nextAvailablePortTx(tx)
    if err != nil {
        return nil, err
    }
    _, err = tx.Exec(
        "INSERT INTO projects (id, name, status, assigned_port) VALUES (?, ?, ?, ?)",
        id, name, StatusStopped, port,
    )
    if err != nil {
        if strings.Contains(err.Error(), "projects.name") {
            return nil, ErrDuplicateName
        }
        return nil, fmt.Errorf("insert project: %w", err)
    }
    if err := tx.Commit(); err != nil {
        return nil, fmt.Errorf("commit: %w", err)
    }
    return s.Get(id)
}
```

**Suggested Tests:**

A test that fires 10 concurrent `Create` calls with distinct names and asserts that all succeed, all get distinct ports in range, and none returns a 500 error.

---

### 2. Subdomain URL in the frontend is hardcoded to `localhost` regardless of deployment domain

**Category:** Correctness  
**Severity:** Critical  
**Location:** `/Users/max/misc/pj/appx/web/src/pages/Project.tsx` line 49, `/Users/max/misc/pj/appx/web/src/components/ProjectCard.tsx` line 43

**Problem:**

The "Open App" link and the subdomain URL used in both `Project.tsx` and `ProjectCard.tsx` are constructed as:

```typescript
return `${proto}//${project.name}.localhost${portSuffix}/`;
```

This hardcodes `localhost` as the base domain. In a production deployment where appx is running at `username.appx.app`, the "Open App" link will point to `myapp.localhost:443/` — a URL that resolves to the developer's own machine, not the production server. The link will silently fail or open something entirely unrelated.

The backend already knows the base domain (it is passed in `RouterConfig.BaseDomain` and used for subdomain routing and cookie scoping). This information is not currently exposed to the frontend.

**Suggested Fix:**

Add a `GET /api/config` endpoint (or extend the existing login/health response) that returns the server's `baseDomain`. The frontend should use this value to construct subdomain URLs at runtime, falling back to `localhost` only when the response is absent or the server is in HTTP mode.

```typescript
// api/client.ts
export function getServerConfig() {
  return request<{ baseDomain: string }>('/config');
}
```

```typescript
// Project.tsx
const subdomainUrl = project ? `${proto}//${project.name}.${serverConfig.baseDomain}${portSuffix}/` : '';
```

**Suggested Tests:**

A router test for `GET /api/config` that asserts the correct `baseDomain` is returned for both HTTP mode and domain mode.

---

### 3. Project directory path hardcoded to `/home/opencode/projects/` in the frontend

**Category:** Correctness  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/web/src/pages/Project.tsx` line 43

**Problem:**

The OpenCode `projectDir` passed to the Terminal and agent components is constructed as:

```typescript
const projectDir = project ? `/home/opencode/projects/${project.name}` : '';
```

This path is correct for the Phase 6 installer model where the `opencode` user has a home directory at `/home/opencode`. However:

1. It bakes in an OS-level deployment assumption into frontend code, making the path wrong for any other deployment layout (e.g., macOS development where `--http` mode is used, the data directory is `./data/projects/`).
2. The `projectRoot` is already known to the backend (set from `--data` flag in `main.go`). A project's full filesystem path is backend knowledge, not frontend knowledge.

The Terminal PTY creation already sends `x-opencode-directory: projectDir` to OpenCode. If the path is wrong, the PTY will start in the wrong directory and OpenCode will assign it to the wrong project.

**Suggested Fix:**

Add a `projectDir` field to the `Project` API response. The backend already has `Manager.ProjectRoot` and `project.Name`, so it can compute the full path at query time and include it in the JSON response.

```go
// project.go
type Project struct {
    // ... existing fields ...
    ProjectDir string `json:"projectDir,omitempty"` // absolute path, set by Manager
}
```

The frontend then uses `project.projectDir` directly without constructing a path.

**Suggested Tests:**

A test that `handleGetProject` and `handleListProjects` return a non-empty `projectDir` field that matches the expected path on disk.

---

### 4. `internal/terminal/` package is orphaned — routes and handlers are gone but the package remains

**Category:** Correctness / Dead Code  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/internal/terminal/` (entire package), `/Users/max/misc/pj/appx/internal/server/` (terminal handlers deleted)

**Problem:**

The plan stated that `internal/terminal/` could potentially be removed since the Terminal component now connects directly to OpenCode's PTY endpoint. The phase 5 implementation did remove the terminal handler routes from `router.go` — there is no `/ws/term/:id` endpoint registered anywhere. However, the entire `internal/terminal/` package (ring buffer, session manager, WebSocket handler, 6 Go files, ~700 lines) remains on disk and compiles as part of the module.

The `client.ts` file still exports `createSession`, `listSessions`, and `deleteSession` functions that target `/api/projects/:id/sessions` — endpoints that no longer exist in the router. These are dead exports that will return 404 at runtime if any code calls them.

The `bufSizeKB` variable in `main.go` is computed from settings but immediately discarded with `_ = bufSizeKB`, with a TODO comment acknowledging it is unused. The `handleGetTerminalBufferSize` and `handleSetTerminalBufferSize` handlers remain registered and functional — they read/write the setting — but the setting has no effect on anything.

**Suggested Fix:**

One of:
- Delete `internal/terminal/` entirely. Remove the `createSession`, `listSessions`, and `deleteSession` exports from `client.ts`. Remove the terminal buffer size settings handlers (or keep them if they will be repurposed).
- Or document clearly that the package is intentionally retained for Phase 6/7 use, and add a `// not currently wired — see issue #N` comment at the package level.

The `bufSizeKB` dead code and the stale client.ts exports should be cleaned up regardless.

**Suggested Tests:**

No new tests needed; verifying removal compiles cleanly is sufficient.

---

### 5. Egress proxy logs asynchronously but `ServeHTTP` may have already returned a 403 before the log write finishes

**Category:** Correctness  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/internal/egress/proxy.go` lines 71–75

**Problem:**

The log write is dispatched in a goroutine:

```go
go func() {
    if err := p.store.LogEntry(host, port, allowed); err != nil {
        log.Printf("egress: failed to log %s:%d: %v", host, port, err)
    }
}()
```

For blocked requests, `ServeHTTP` immediately returns a 403 response. The goroutine writes to SQLite in the background. Under high load or when the SQLite connection is busy, the log write can lag behind the response by an arbitrary amount of time. In tests, `TestProxy_LogsEntries` already papers over this with `time.Sleep(50 * time.Millisecond)`.

More importantly, if the process is killed or restarted immediately after a blocked request (e.g., by systemd), in-flight log goroutines are abandoned and some blocked connections will never appear in the egress log. For a security audit trail, this is a loss of data integrity.

**Suggested Fix:**

Write the log entry synchronously before sending the 403 response. The log write is a simple INSERT with a 5 ms typical latency on a local SQLite database — this is not a bottleneck worth optimizing with async dispatch. Remove the goroutine:

```go
if err := p.store.LogEntry(host, port, allowed); err != nil {
    log.Printf("egress: failed to log %s:%d: %v", host, port, err)
}
if !allowed {
    http.Error(w, "destination not in allowlist", http.StatusForbidden)
    return
}
```

For allowed connections, the log write can optionally remain async (or move to after the tunnel closes, capturing the total duration), but blocking on the INSERT before establishing the tunnel is also acceptable.

**Suggested Tests:**

The existing `TestProxy_LogsEntries` can be simplified by removing the `time.Sleep` — the log entry should be present immediately after the response is received, not 50ms later.

---

### 6. `requireJSON` middleware comment claims `SameSite=Strict` but cookies are now `SameSite=Lax`

**Category:** Documentation / Security Reasoning  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/internal/server/middleware.go` lines 50–51

**Problem:**

The `requireJSON` middleware comment reads:

> "Combined with SameSite=Strict cookies, this makes CSRF attacks impractical."

Phase 5 changed cookies from `SameSite=Strict` to `SameSite=Lax` to support subdomain navigation. The CSRF argument is still broadly correct — requiring `Content-Type: application/json` forces preflight on cross-origin requests — but the `SameSite=Strict` reference is now factually wrong. `auth/store.go` still references `SameSite=Strict` in its package comment as well.

The security implication of the change: `SameSite=Lax` sends the cookie on top-level navigations (clicking a link from an external site that navigates to the appx dashboard). A CSRF attack via a cross-site form POST still fails because forms cannot set `Content-Type: application/json`. However, GET requests to state-changing endpoints (if any existed) would now be CSRF-vulnerable in a way they were not under `Strict`. Appx currently has no state-changing GETs, so there is no actual vulnerability, but the reasoning should be documented correctly.

**Suggested Fix:**

Update the `requireJSON` comment to reflect the actual cookie mode and explain why CSRF is still mitigated under `Lax`:

> "Combined with SameSite=Lax cookies and the Content-Type=application/json requirement (which triggers CORS preflight on cross-origin requests), CSRF attacks against state-changing endpoints are impractical. SameSite=Lax was chosen over Strict to allow the session cookie to be sent on subdomain navigation."

Also update the `auth/store.go` comment that still references `SameSite=Strict`.

---

### 7. OpenCode client `SetAuth` POST body field names may not match the actual OpenCode API

**Category:** Correctness  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/internal/opencode/client.go` lines 78–82

**Problem:**

The `SetAuth` method sends:

```go
body := struct {
    ProviderID string `json:"providerId"`
    APIKey     string `json:"apiKey"`
}{ProviderID: providerID, APIKey: apiKey}
```

The analysis document (de-docker-refactor.md) shows the SDK usage as:

```typescript
await client.auth.set({
  providerID: "anthropic",
  auth: { type: "api_key", key: "..." }
})
```

The SDK sends `auth` as a nested object with `type` and `key` fields. The Go client sends a flat structure with `providerId` and `apiKey` at the top level. These are different shapes. If the OpenCode REST API expects the SDK's shape, `SetAuth` will silently succeed (200 status) without actually injecting the key, or fail with an opaque error.

The plan says "plain `net/http` calls for ~3 endpoints" and acknowledges this is hand-rolled. Without a test against a real or mock OpenCode server with the correct schema, it is unknown whether the request body is correct.

**Suggested Fix:**

Verify the actual OpenCode `/auth` endpoint schema against the source or OpenCode documentation. If the endpoint expects `{ auth: { type: "api_key", key: "..." } }`, update the Go struct accordingly. Add a comment citing the OpenCode API version this was written against so that future OpenCode upgrades can be checked.

**Suggested Tests:**

The existing `TestSetAPIKey_InjectsIntoOpenCode` already tests the round-trip through the router. Add a unit test in `opencode/client_test.go` that inspects the request body sent to a mock HTTP server and asserts the exact JSON shape.

---

### 8. Manager.scaffoldProject does not clean up on partial failures

**Category:** Correctness  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/internal/project/manager.go` lines 97–123

**Problem:**

`scaffoldProject` runs these steps in sequence: `MkdirAll`, write `AGENTS.md`, `git init`, `git add`, `git commit`. If any step after `MkdirAll` fails (e.g., `git` is not installed, or `git commit` fails because no git user is configured globally), the function returns an error and `Create` calls `m.Store.Delete(proj.ID)` to roll back the DB record. However, the partially-created directory (`dir`) is not cleaned up. The filesystem will have an orphaned directory at `projectRoot/<name>/` with potentially partial contents.

On a subsequent attempt to create a project with the same name, `MkdirAll` will succeed (directory exists) but `git init` inside the existing directory may behave differently (reinitializes the repo), and `git commit` will still fail if the `git` environment problem persists. If the git failure was transient (e.g., disk full temporarily), a retry will succeed but on top of a partially-written directory.

**Suggested Fix:**

Add cleanup of the created directory on any error path in `scaffoldProject`, or in the `Create` method's error handler:

```go
func (m *Manager) Create(name string) (*Project, error) {
    proj, err := m.Store.Create(name)
    if err != nil {
        return nil, err
    }

    projectDir := filepath.Join(m.ProjectRoot, name)
    if err := m.scaffoldProject(projectDir, proj); err != nil {
        os.RemoveAll(projectDir) // clean up partial directory
        m.Store.Delete(proj.ID)
        return nil, fmt.Errorf("scaffold project: %w", err)
    }

    return proj, nil
}
```

**Suggested Tests:**

A test that makes `git commit` fail (by passing a directory where git is not a valid repo for committing, e.g., by removing write permissions) and asserts that no project directory is left on disk and no DB record exists after the error.

---

### 9. Phase 6 plan is stale — it describes the pre-Phase-5 Docker architecture

**Category:** Documentation  
**Severity:** Important  
**Location:** `/Users/max/misc/pj/appx/docs/plans/phase_6_plan.md`

**Problem:**

The Phase 6 plan (`phase_6_plan.md`) was written before Phase 5. It describes:
- A React Native client that proxies through `/api/agent/:name/*` to per-project containers
- `OPENCODE_SERVER_PASSWORD` / Basic Auth injection into containers
- `ContainerAddr` lookup for per-project container routing
- Container lifecycle (start/stop) assumed to still exist

None of this applies after Phase 5. Phase 5 eliminated all containers. The Phase 5 plan (`phase_5_plan.md`) correctly describes Phase 6 as "Installer + Security" (install.sh, bearer tokens, iptables, OS users). The `phase_6_plan.md` file contradicts both the current codebase and the Phase 5 plan's description of what comes next.

**Suggested Fix:**

Rewrite `phase_6_plan.md` to describe the Phase 6 that makes sense after Phase 5: the installer (`install.sh`), OS user separation (appx/opencode), iptables egress enforcement, bearer token auth for native clients, and rootless Docker setup. The de-docker analysis document (Q5 and the OS user separation section) already has the content for this.

---

### 10. Egress proxy does not enforce a `NO_PROXY` bypass for localhost

**Category:** Security  
**Severity:** Suggestion  
**Location:** `/Users/max/misc/pj/appx/internal/egress/proxy.go`, `DefaultAllowlist` in `/Users/max/misc/pj/appx/internal/egress/store.go`

**Problem:**

The plan states that OpenCode should be started with `NO_PROXY=localhost,127.0.0.1` to prevent a routing loop (OpenCode's API calls to its own server going through the egress proxy). This is correct but it is a systemd service configuration concern, not enforced by the proxy itself.

More subtly: the proxy currently allows any `CONNECT host:port` that matches the allowlist. If `localhost:4096` or `127.0.0.1:4096` were somehow added to the allowlist (e.g., by accident or user configuration error), the proxy would happily tunnel traffic to the OpenCode server from the agent's perspective. The `DefaultAllowlist` does not include any localhost entries, which is correct, but there is no validation in `handleSetAllowlist` that prevents a user from adding `localhost:4096` to the allowlist.

**Suggested Fix:**

Add a validation step in `handleSetAllowlist` that rejects allowlist entries whose host resolves to a loopback address (`127.0.0.0/8`, `::1`). This prevents user misconfiguration from opening a proxy-bypass-to-OpenCode path:

```go
if host == "localhost" || host == "127.0.0.1" || host == "::1" ||
    strings.HasSuffix(host, ".localhost") {
    http.Error(w, "loopback addresses may not be added to the allowlist: "+entry, http.StatusBadRequest)
    return
}
```

**Suggested Tests:**

A test that `PUT /api/egress/allowlist` with `["localhost:4096"]` returns 400.

---

### 11. ChatPanel does not display agent responses — only user messages are shown

**Category:** Correctness  
**Severity:** Suggestion  
**Location:** `/Users/max/misc/pj/appx/web/src/components/agent/ChatPanel.tsx`

**Problem:**

`ChatPanel` appends user messages to the `messages` state immediately on send, but never fetches or subscribes to the agent's response. The OpenCode SDK exposes a real-time event stream (`opencode.event.subscribe()`) and session message history (`opencode.session.messages()`). Neither is called in `ChatPanel`. The agent receives the prompt (the `session.prompt` call is wired correctly) and processes it, but the response never appears in the UI.

The `projectDir` prop is received and immediately suppressed with `void projectDir;` — it was included in the props for "future use" but is not used. The `handleSend` callback does not include `projectDir` in its dependency array, which is correct since it is not used, but the unused prop with a suppression comment is a code smell.

**Suggested Fix:**

Wire the event stream subscription in a `useEffect` that calls `opencode.event.subscribe()` and filters events by `sessionId` to append agent messages to the `messages` state. This is the intended usage described in the de-docker analysis (Finding F2). The `projectDir` prop should either be removed until it is needed, or used in the event subscription filter.

**Suggested Tests:**

This is frontend logic; manual verification is the primary path. Document what was verified: that the event subscription fires, agent messages appear, and the subscription is cleaned up on unmount.

---

### 12. Terminal reconnect reuses the same PTY session ID after disconnect

**Category:** Correctness  
**Severity:** Suggestion  
**Location:** `/Users/max/misc/pj/appx/web/src/components/Terminal.tsx` lines 101–108

**Problem:**

On WebSocket close (non-intentional), the `onclose` handler reconnects by calling `connectWs(ptyId)` with the same PTY session ID. This relies on the assumption that OpenCode's PTY session is still alive and reconnectable. The plan notes that this was a "caveat to verify during implementation" — whether OpenCode's PTY supports reconnect with output replay.

If OpenCode's PTY terminates the session when the WebSocket disconnects (common behavior), reconnecting to the same `ptyId` will get a 404 or connection refused from OpenCode. In that case, the terminal will silently fail to reconnect, and the retry loop will exhaust its 5 attempts without any output replay. The user will see "Connection lost" with no explanation.

The original `internal/terminal/` package handled this case with a ring buffer that replayed output on reconnect. That buffer is now effectively unused.

**Suggested Fix:**

Verify OpenCode's PTY reconnect behavior against the OpenCode server implementation. If sessions expire on disconnect, the reconnect logic should call `createPty()` again to get a new PTY, not attempt to reuse the old ID. This would mean losing the shell state on disconnect (no replay), which should be communicated clearly to the user.

---

## Forward-Looking Assessment for Phase 6

Phase 6 (as correctly described in `phase_5_plan.md`, not the stale `phase_6_plan.md`) needs: the installer, OS user separation, iptables egress enforcement, and bearer token auth.

**What Phase 5 makes easy:**

- The egress proxy is already running and logging. Adding iptables rules in the installer to enforce it is straightforward.
- The `auth.Middleware` has a single entry point — adding bearer token support (checking `Authorization: Bearer` before falling back to the cookie) is a small, clean change.
- The OpenCode client is a plain HTTP client, not tied to a specific OpenCode version's API shape. It will survive OpenCode updates as long as the endpoint URLs are stable.

**What Phase 5 makes harder or leaves unresolved:**

- The hardcoded `/home/opencode/projects/` path (Issue 3) will break in any deployment layout other than the Phase 6 installer's exact directory structure. It needs to be resolved before the installer is finalized, since the installer determines the actual paths.
- The subdomain URL hardcoded to `localhost` (Issue 2) will produce broken "Open App" links in any production deployment. This must be fixed before Phase 7 (hosted service) where `baseDomain` is a real domain.
- The `internal/terminal/` package (Issue 4) remains as technical debt. If it is being retained for bearer token terminal access in Phase 6, that should be documented. Otherwise it should be deleted now while the rationale is fresh.
- Phase 6 will add OS user separation (appx/opencode), which means the `projectRoot` path (currently `./data/projects/` in dev) will change to something like `/home/opencode/projects/`. The backend `project.ProjectRoot` is runtime-configurable, but the hardcoded frontend path will not update automatically — this is the same as Issue 3.
- The `phase_6_plan.md` describing Docker containers will cause confusion when the Phase 6 installer is planned. It should be replaced with the correct plan before Phase 6 begins.
