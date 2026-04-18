# Architecture: Project Management and Docker Lifecycle (Phase 2)

## Table of Contents

1. [Overview](#overview)
2. [System Map](#system-map)
   - [Component Relationships](#component-relationships)
   - [API Endpoints](#api-endpoints)
   - [Database Schema](#database-schema)
   - [Status State Machine](#status-state-machine)
   - [External Dependencies](#external-dependencies)
3. [Code Review Guide](#code-review-guide)
   - [Data Model — internal/project/project.go](#data-model)
   - [Store — internal/project/store.go](#store)
   - [Container Manager — internal/project/container.go](#container-manager)
   - [HTTP Handlers — internal/server/project_handlers.go](#http-handlers)
   - [Settings Handlers — internal/server/settings_handlers.go](#settings-handlers)
   - [Entry Point Wiring — cmd/appx/main.go](#entry-point-wiring)
   - [Frontend — web/src/](#frontend)
4. [Testing Guide](#testing-guide)
   - [Automated Test Coverage](#automated-test-coverage)
   - [Manual Verification Checklist](#manual-verification-checklist)
5. [Architecture and Code Pitfalls](#architecture-and-code-pitfalls)
6. [Fixed Pitfalls](#fixed-pitfalls)
7. [TODOs and Future Improvements](#todos-and-future-improvements)

---

## Overview

Phase 2 adds the first user-facing feature: the ability to create, start, stop, and delete named projects, each running in a hardened Docker container. This is the core of appx's value proposition — every project gets its own isolated container, pre-loaded with Claude Code and the user's Anthropic API key.

### The problem being solved

After Phase 1 (HTTPS server, TLS, auth, SPA), the application had a working shell but nothing to do. Users could log in and see a blank dashboard. Phase 2 adds the full project lifecycle: CRUD, container orchestration, status tracking, and the UI to control it all.

The central design challenge was **how to handle asynchronous Docker operations** without blocking the HTTP request and without leaving the server or UI in an inconsistent state if a crash occurs mid-operation. Docker operations — building an image, creating a network, starting a container — can take seconds or minutes. They cannot be done synchronously in an HTTP handler.

### The key design decisions

**202 Accepted + client polling instead of WebSockets or SSE.** When a user clicks Start, the server transitions the project to `starting` (in a database transaction) and returns 202 immediately. A background goroutine does the Docker work. The client polls `GET /api/projects` every 3 seconds while any project is in a transitional state. This is simple, correct under failure, and requires no persistent connection.

**Atomic status transitions in the database.** The `TransitionStatus` method issues a single `UPDATE ... WHERE status IN (allowed_states)` and checks rows affected. If zero rows were updated, either the project doesn't exist or its status wasn't in the allowed set. This is the key correctness guarantee: two concurrent Start requests for the same project cannot both succeed because the second one will find the status already changed to `starting` and return `ErrInvalidState`.

**Container reuse across stop/start cycles.** When a project is stopped, its Docker container is kept (`SetStopped` preserves `container_id`). On the next Start, the manager first checks whether the container still exists (via `ContainerInspect`) and restarts it if so. This avoids rebuilding the network and container on every start, which is faster. If the container was deleted externally (e.g. by a Docker `rm` command), the manager falls through to a full create.

**Startup recovery via `RecoverStaleStates`.** If the server crashes while a project is in `starting` or `stopping`, those states are stale. On next startup, before accepting requests, the manager inspects the Docker container for each stuck project and reconciles the database to either `running` or `stopped`. Projects with no container ID (crashed before Docker was even called) go to `error`.

**Two storage modes for project data.** The `-projects` flag controls where container data lives: if empty (default), a Docker named volume (`appx-{name}-data`) is used — this works anywhere without setup. If set to a host path (e.g. a Hetzner CSI volume), a bind mount to `{projectsDir}/{name}` is used instead — this survives instance replacement on a cloud VM. Delete handles both cases.

**API key injected at container start.** The Anthropic API key is stored in the `settings` table and cached in-memory in the Manager behind a `sync.RWMutex`. When a container starts, the key is passed as `ANTHROPIC_API_KEY` in the container's environment. The Manager exposes `SetAnthropicKey` so that the Settings handler can update the in-memory value immediately when the user saves a new key. Already-running containers retain their old key until restarted.

### How the pieces fit together

The `project.Manager` is the single orchestrator: it wraps a `project.Store` (database) and a `dockerer` (Docker API client). Every HTTP handler receives a `*project.Manager` and never touches the store or Docker directly. The Manager owns the transition lifecycle: it validates state, calls `TransitionStatus`, kicks off a goroutine, and lets the goroutine call `SetRunning`/`SetStopped`/`SetError` when done.

The `dockerer` interface is the abstraction boundary that makes the entire Docker layer testable: the production code uses `*dockerclient.Client`; tests use `fakeDocker`. Every Manager test runs in-process with an in-memory SQLite database and a fake Docker client, making the test suite fast and hermetic.

On the frontend, the Dashboard polls `GET /api/projects` every 3 seconds when any project is in a transitional state (`starting` or `stopping`), stopping the interval when all projects are stable. The `ProjectCard` component renders different controls depending on status: Start/Stop for stable states, a disabled progress indicator plus Reset for transitional states.

### Trade-offs

**No streaming build output.** The `ensureBaseImage` build step drains Docker's JSON build stream in the background goroutine but does not relay progress to the client. The UI shows `starting` until the image is built. For a first build this could take 30-60 seconds; subsequent starts skip the build entirely. This is an accepted simplicity trade-off for Phase 2.

**No port conflict detection.** The `port` field is stored but not validated for uniqueness. Two projects can be created with the same port. The reverse proxy (Phase 4) will need to handle or prevent this. The validation is deferred because Phase 2 does not route traffic to containers.

**Delete is synchronous.** Unlike Start/Stop, Delete runs Docker operations in the handler goroutine (not a background goroutine) and returns 204 only when fully done. This is appropriate because Delete is a rare, user-initiated action and blocking for a few seconds is acceptable. Making it async would complicate state management (what does the UI show for a project that is "deleting"?).

---

## System Map

### Component Relationships

```
cmd/appx/main.go
  │
  ├── db.Open()                    [UPDATED] migration 2 adds 4 columns to projects
  │
  ├── auth.NewStore()              [unchanged]
  │
  ├── project.NewStore()           [NEW]
  ├── project.NewManager()         [NEW]
  │     ├── project.Store          [NEW] — SQLite CRUD
  │     └── dockerer               [NEW] — Docker API interface
  │
  ├── pm.RecoverStaleStates()      [NEW] — startup reconciliation
  │
  └── server.Run(Config{
          ProjectManager: pm,      [NEW field]
        })

server.NewRouter()                 [UPDATED]
  ├── /api/projects                [NEW endpoints]
  ├── /api/projects/{id}
  ├── /api/projects/{id}/start
  ├── /api/projects/{id}/stop
  ├── /api/projects/{id}/reset
  └── /api/settings/api-key       [NEW endpoints]
      GET / PUT / DELETE

project.Manager                   [NEW]
  ├── Create()   → store.Create()
  ├── List()     → store.List()
  ├── Get()      → store.Get()
  ├── Start()    → store.TransitionStatus() → go doStart()
  ├── Stop()     → store.TransitionStatus() → go doStop()
  ├── Reset()    → store.SetStopped()
  └── Delete()   → docker.ContainerStop/Remove/NetworkRemove/VolumeRemove → store.Delete()

doStart() goroutine
  ├── tryReuseContainer()          ContainerInspect → ContainerStart
  └── doFullCreate()
        ensureBaseImage()          ImageInspect → ImageBuild (if missing)
        NetworkCreate
        ContainerCreate            (hardened: CapDrop=ALL, no-new-privileges, ReadonlyRootfs)
        ContainerStart
        store.SetRunning()

doStop() goroutine
  └── ContainerStop → store.SetStopped()

web/src/
  ├── pages/Dashboard.tsx          [UPDATED] project list + polling
  ├── pages/Settings.tsx           [NEW]
  ├── components/ProjectCard.tsx   [NEW]
  ├── components/CreateProjectModal.tsx [NEW]
  └── api/client.ts                [UPDATED] project + settings API functions
```

### API Endpoints

All endpoints require an authenticated session cookie (`appx_session`) except as noted.

| Method | Path | Auth | Request body | Success response | Error codes |
|--------|------|------|-------------|-----------------|-------------|
| GET | `/api/projects` | Required | — | `200 Project[]` | 401 |
| POST | `/api/projects` | Required | `{name, port}` | `201 Project` | 400 (invalid name/port), 409 (duplicate name), 401 |
| GET | `/api/projects/{id}` | Required | — | `200 Project` | 404, 401 |
| DELETE | `/api/projects/{id}` | Required | — | `204` | 404, 401 |
| POST | `/api/projects/{id}/start` | Required | — | `202` | 404, 409 (invalid state), 400 (no API key), 503 (Docker unavailable), 401 |
| POST | `/api/projects/{id}/stop` | Required | — | `202` | 404, 409 (not running), 503 (Docker unavailable), 401 |
| POST | `/api/projects/{id}/reset` | Required | — | `204` | 404, 409 (stopped or running), 401 |
| GET | `/api/settings/api-key` | Required | — | `200 {set: bool}` | 401 |
| PUT | `/api/settings/api-key` | Required | `{key}` | `200 {status: "ok"}` | 400 (empty key), 401 |
| DELETE | `/api/settings/api-key` | Required | — | `200 {status: "ok"}` | 401 |

**Project JSON shape:**
```json
{
  "id": "uuid",
  "name": "my-app",
  "status": "stopped|starting|running|stopping|error",
  "port": 3000,
  "containerId": "sha256...",
  "imageName": "appx-base:latest",
  "lastError": "",
  "createdAt": "2024-01-01T00:00:00Z"
}
```

Note: `networkId` is excluded from JSON (`json:"-"`) — it is an internal Docker identifier not needed by the frontend.

### Database Schema

Migration 1 (from Phase 1) created the `projects` table with core fields. Migration 2 (`000002_project_docker.up.sql`) adds the columns needed by the Docker lifecycle:

```sql
-- From migration 1 (pre-existing):
CREATE TABLE projects (
    id           TEXT PRIMARY KEY,
    name         TEXT UNIQUE NOT NULL,
    status       TEXT DEFAULT 'stopped',
    container_id TEXT,
    internal_port INTEGER,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Added by migration 2:
ALTER TABLE projects ADD COLUMN network_id  TEXT;
ALTER TABLE projects ADD COLUMN image_name  TEXT;
ALTER TABLE projects ADD COLUMN last_error  TEXT;
ALTER TABLE projects ADD COLUMN resources   TEXT;  -- JSON: {"memory":"1g","cpus":"1.0"}
```

The `settings` table (from Phase 1) gains a new key: `anthropic_api_key`.

### Status State Machine

```
                  ┌───────────────────────────────────┐
                  │                                   │
        Create()  ▼                                   │
         ┌──────────────┐                             │
         │   stopped    │◄────────────────────────────┼──── Reset()
         └──────────────┘         SetStopped()        │     (from starting/stopping)
                │    ▲                                 │
         Start()│    │doStop()                         │
          (CAS) │    │                                 │
                ▼    │                                 │
         ┌──────────────┐                             │
         │   starting   │──────── doStart() ──────────┤
         └──────────────┘         SetRunning()        │
                                                      │
         ┌──────────────┐                             │
         │   running    │                             │
         └──────────────┘                             │
                │                                     │
         Stop() │                                     │
          (CAS) │                                     │
                ▼                                     │
         ┌──────────────┐                             │
         │   stopping   │──────── doStop() ───────────┘
         └──────────────┘         SetStopped()

Any state → error  (via SetError in doStart/doStop on failure)
Any state → Delete (synchronous, removes Docker resources + DB record)
error/starting/stopping → stopped  (via Reset, no Docker interaction)
```

Key: CAS = Compare-And-Swap, enforced by `TransitionStatus` with a conditional UPDATE. Start is allowed from `stopped` or `error`. Stop is allowed only from `running`.

### External Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| `github.com/moby/moby/client` | v28+ | Docker API client |
| `github.com/moby/moby/api/types/container` | — | Container config types |
| `github.com/moby/moby/api/types/mount` | — | Volume/bind mount types |
| `github.com/moby/moby/api/types/network` | — | Network endpoint config |
| Docker Engine | 20.10+ | Container runtime (host dependency) |

---

## Code Review Guide

### Data Model

**File:** `internal/project/project.go`

This file defines the `Project` struct, the five status constants, seven sentinel errors, and the name validation regex. It is the vocabulary of the whole feature.

**What to verify:**

- The `namePattern` regex (`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`) requires at least 2 characters (first char `[a-z]`, last char `[a-z0-9]`) but allows single-char names to slip through `ValidateName` if `len(name) < 2` is not checked separately. The code does check `len(name) < 2` explicitly before calling `namePattern.MatchString`, so single-char names are correctly rejected. However, a name like `"a"` returns `ErrInvalidName` from the length check while a name like `"a1"` is valid — confirm the regex and length check together are consistent for all two-character inputs.

- `NetworkID` is tagged `json:"-"`. Confirm the frontend does not need it (it doesn't — the Phase 4 reverse proxy routes by name, not network ID).

- `Resources` is `*Resources` (nullable pointer). When nil the container runs with only the Manager's hardcoded limits (1 GB RAM, 2 CPUs, 256 PIDs). The UI never sets resources in Phase 2, so this field is scaffolding for a future settings page.

---

### Store

**File:** `internal/project/store.go`

The store is a thin SQL layer. All queries use the `projectColumns` constant to stay in sync with `scanInto`. Nullable columns (`container_id`, `network_id`, `image_name`, `last_error`, `resources`) are scanned into `sql.NullString`.

**Key decision — TransitionStatus as the safety gate:**

```go
res, err := s.db.Exec(
    "UPDATE projects SET status = ?, last_error = '' WHERE id = ? AND status IN (...)",
    to, id, from...,
)
```

This single SQL statement is the entire concurrency guard. SQLite serialises writes, so two goroutines racing to start the same project will both issue this UPDATE; only the one that wins the serialization will find `rows_affected = 1`. The other will find 0 rows affected, look up the project, confirm it exists, and return `ErrInvalidState`. This is correct because SQLite's write serialization guarantees that the two UPDATEs cannot interleave.

Note that `TransitionStatus` also clears `last_error` (`SET status = ?, last_error = ''`). This means transitioning from `error` to `starting` automatically removes the stale error message without needing a separate call. The error message only becomes visible again if `SetError` is called.

**What to verify:**

- `isUniqueViolation` uses `strings.Contains(err.Error(), "UNIQUE constraint")`. This is a string-match on the error message from `modernc.org/sqlite`. If the library changes its error message format, duplicate detection silently breaks and returns a generic 500. This is an accepted pragmatic trade-off for a library that has been stable in this respect — but it is fragile.

- `SetStopped` does not clear `container_id`. This is intentional: the container still exists in Docker (just stopped), and the next Start call needs the ID to reuse it. Only `Delete` (via the Manager, not the Store) removes Docker resources. A reviewer should confirm no path calls `SetStopped` and then passes the stale container ID to Docker as if the container were gone.

- `scanInto` silently ignores JSON parse errors for the `resources` column (the `if err := json.Unmarshal... { p.Resources = &r }`). Corrupted JSON in that column yields `nil` Resources, which the Manager treats as "no custom limits". This is acceptable but worth noting.

---

### Container Manager

**File:** `internal/project/container.go`

This is the most complex file. The Manager combines Store (persistence) with Docker (container lifecycle). Review it in layers.

#### The `dockerer` interface

The interface is defined in this file rather than a separate `docker.go` to keep the abstraction close to its use. It includes all Exec methods (ExecCreate, ExecStart, ExecAttach, ExecInspect, ExecResize) even though none of them are used in Phase 2. They are stubs for Phase 3 (terminal WebSocket) and are implemented as `return ..., fmt.Errorf("not implemented in fake")` in the test double. This forward-declaration approach avoids a breaking interface change in Phase 3 but means the interface is larger than needed now.

#### The API key concurrency pattern

```go
type Manager struct {
    mu           sync.RWMutex
    anthropicKey string
}
```

`getAnthropicKey()` acquires a read lock; `SetAnthropicKey()` acquires a write lock. This is correct for the use case: concurrent reads (background goroutines calling `getAnthropicKey()`) with occasional writes (user saves a new key via Settings). However, the key is snapshotted at the start of `doStart` via `getAnthropicKey()` and then baked into the container environment. If the key changes between when the goroutine reads it and when `ContainerCreate` actually runs, the container gets the new key. This is fine — the container gets a valid key either way.

#### The `doStart` goroutine

```
doStart(proj)
  └── tryReuseContainer() — if proj.ContainerID != ""
        ContainerInspect → if error, return false (full create)
        ContainerStart → if error, SetError + return true
        SetRunning → return true
  └── doFullCreate()
        ensureBaseImage()
        NetworkCreate → createdNetworkID = netRes.ID
        ContainerCreate → createdContainerID = ctrRes.ID
        ContainerStart → if error, cleanup() + SetError
        SetRunning
```

The `cleanup` closure captures `createdNetworkID` and `createdContainerID` by reference. It is called via the `fail` helper on any error after partial resource creation. This ensures that if `ContainerCreate` fails (e.g. disk full), the already-created network is removed. The cleanup context is a fresh `context.Background()` with a 30-second timeout, independent of the create context, so it is not cancelled if the create context times out.

**What to verify:**

- The cleanup function only removes `createdNetworkID` and `createdContainerID`, both of which are set after the respective successful creation call. If `NetworkCreate` fails, `createdNetworkID` is empty and cleanup is a no-op — correct. If `ContainerCreate` fails after network creation, both IDs are set and both are cleaned up — correct.

- `tryReuseContainer` returns `true` whether it successfully restarted the container or failed. The contract is: `true` means "we handled it (success or error), don't call doFullCreate"; `false` means "container doesn't exist, do a full create". Verify that every early return in `tryReuseContainer` that sets an error also returns `true`.

- The 5-minute create timeout (`context.WithTimeout(context.Background(), 5*time.Minute)`) is shared across the entire `doFullCreate` call. If image build takes longer than 5 minutes (unlikely but possible on a slow machine), all subsequent steps will fail. This is intentional — a 5-minute timeout prevents hung goroutines from accumulating.

- The 30-second stop timeout in `doStop` uses `Timeout: &timeout` (a pointer to int `10`). This is the seconds the Docker daemon waits for the container to exit gracefully before sending SIGKILL. The outer `context.WithTimeout(context.Background(), 30*time.Second)` is the Go-side deadline for the entire stop operation. Confirm these two timeouts are not confused: the inner one is passed to Docker, the outer one cancels the Go context.

#### Container hardening

The `doFullCreate` method applies these security settings to every project container:

```go
CapDrop:        []string{"ALL"},
SecurityOpt:    []string{"no-new-privileges:true"},
ReadonlyRootfs: true,
Tmpfs: map[string]string{
    "/tmp":       "rw,noexec,nosuid,size=100m",
    "/home/node": "rw,noexec,nosuid,size=50m",
},
```

Dropping all capabilities removes CAP_NET_RAW (raw packet injection), CAP_SYS_PTRACE (process inspection), CAP_SYS_ADMIN, and others. `no-new-privileges` prevents setuid binaries from acquiring additional privileges. `ReadonlyRootfs` ensures container file system changes (outside `/tmp` and `/home/node`) require an explicit mount. The `noexec` flag on tmpfs mounts prevents binaries written to `/tmp` from being executed directly.

**What to verify — security:**

- The container runs as user `node` (set via `Config.User = "node"`), not root. Verify the Dockerfile creates the `node` user with appropriate ownership of `/app`.
- There is no `--network=host` or `--privileged` flag. Containers are isolated on their own bridge network.
- The `ANTHROPIC_API_KEY` is passed as an environment variable, which is visible to all processes in the container via `/proc/self/environ`. For a single-user self-hosted tool this is acceptable, but a multi-tenant deployment would need a secrets management approach.

---

### HTTP Handlers

**File:** `internal/server/project_handlers.go`

Seven handlers following the standard pattern: receive a `*project.Manager`, return an `http.HandlerFunc` closure. Error mapping is explicit and consistent:

| Error | HTTP code |
|-------|-----------|
| `ErrNotFound` | 404 |
| `ErrInvalidName`, `ErrInvalidPort`, `ErrNoAPIKey` | 400 |
| `ErrDuplicateName`, `ErrInvalidState` | 409 |
| `ErrDockerUnavailable` | 503 |
| anything else | 500 |

**Key decisions:**

- Start and Stop return 202, not 200. This is semantically correct (the operation has been accepted but not completed) and signals to clients that they should poll for the result.

- Delete returns 204 (no content) rather than 202. Delete is synchronous — it only returns when the Docker resources and DB record are gone. A client can treat 204 as a definitive "project is gone" signal.

- Reset returns 204. It is a synchronous, in-process DB write with no Docker interaction, so it can respond immediately with a definitive result.

**What to verify:**

- `handleCreateProject` validates port range (`port < 1 || port > 65535`) redundantly: the handler checks this before calling `pm.Create`, and `pm.Create` calls `store.Create` which also checks it. The double validation is harmless but slightly redundant. The handler-level check is there as a fast path before any store interaction; the store check is the authoritative one.

- No handler calls `pm.Stop` before `pm.Delete`. The `Delete` method on the Manager handles stopping internally (it calls `ContainerStop` directly). If `handleDeleteProject` also called `handleStopProject` first, it would race with the delete goroutine. The current design is correct: Delete is fully in control of cleanup order.

---

### Settings Handlers

**File:** `internal/server/settings_handlers.go`

Three handlers for `GET/PUT/DELETE /api/settings/api-key`. Each handler receives both `*auth.Store` (for DB persistence) and `*project.Manager` (for the in-memory key cache).

The GET handler calls `pm.AnthropicKeySet()` rather than reading from the store. This means the response reflects the Manager's in-memory state, which is authoritative (it is always loaded from the DB at startup and updated by Set/Delete). The actual key value is never exposed — the endpoint returns only `{"set": true}` or `{"set": false}`.

The DELETE handler falls back to the `ANTHROPIC_API_KEY` environment variable after clearing the DB key:

```go
pm.SetAnthropicKey(os.Getenv("ANTHROPIC_API_KEY"))
```

This ensures that if the user removes the UI-stored key but had originally set the env var on the host, the Manager does not suddenly lose its key.

**What to verify:**

- There is a gap: if the user sets the key via env var only (never via the UI), the GET endpoint will show `set: true` (correct), but the DELETE endpoint will clear the env var's effect if the user accidentally clicks "Remove Key" — wait, no: the DELETE clears the DB key (which was never set) and then calls `pm.SetAnthropicKey(os.Getenv(...))` which restores the env var value. This is idempotent and correct.

---

### Entry Point Wiring

**File:** `cmd/appx/main.go`

The main function gained several new responsibilities:

1. **Docker client initialization** — tries `findDockerHost()` (which probes well-known socket paths for Docker Desktop, Colima, Rancher Desktop) then falls back to SDK defaults (`FromEnv`). Pings the daemon with a 5-second timeout; exits fatally if unavailable.

2. **Anthropic key resolution** — DB setting has priority over env var. This is loaded once at startup and passed to `NewManager`. The Manager's in-memory key is the live value after startup.

3. **`RecoverStaleStates`** — called with a 30-second timeout before `server.Run`. This must complete before the server starts accepting requests, otherwise a user could start a project that is already `starting` in the DB (from a previous crash), and the recovery goroutine might overwrite the new transition.

4. **`-projects` flag** — controls bind-mount vs Docker-volume storage. Passed through to `NewManager` as `projectsDir`.

**What to verify:**

- Recovery has a 30-second budget. If the host has many stale projects, each requiring a 5-second Docker inspect, this could be tight. In practice it is very unlikely to have more than a handful of projects in transitional states simultaneously.

---

### Frontend

**Files:** `web/src/pages/Dashboard.tsx`, `web/src/pages/Settings.tsx`, `web/src/components/ProjectCard.tsx`, `web/src/components/CreateProjectModal.tsx`, `web/src/api/client.ts`

#### Dashboard polling logic

```tsx
const hasTransitional = projects.some(
  p => p.status === 'starting' || p.status === 'stopping'
);

if (hasTransitional && !pollRef.current) {
  pollRef.current = setInterval(fetchProjects, 3000);
} else if (!hasTransitional && pollRef.current) {
  clearInterval(pollRef.current);
  pollRef.current = null;
}
```

`pollRef` is a `useRef` so the interval ID persists across renders without triggering re-renders itself. The effect runs whenever `projects` changes (the dependency array includes `projects`). The cleanup function in the `useEffect` return clears the interval when the component unmounts. This is correct and does not leak intervals.

**What to verify:**

- The effect runs on every `projects` state update. If polling fires every 3 seconds and updates `projects`, the effect runs again, checks `hasTransitional`, and... does nothing if the interval is already running (guarded by `!pollRef.current`). This is correct — no interval accumulation.

- On unmount (e.g. navigating away), the cleanup function runs and `clearInterval` is called. This is correct even if a poll response arrives after unmount: `setProjects` will be called on an unmounted component, which React 18 silently ignores.

#### ProjectCard state logic

The card shows different controls based on `project.status`:
- `stopped` or `error`: Start button
- `running`: Stop button
- `starting` or `stopping`: disabled progress button + Reset button

The Delete button is disabled while `isTransitional` to prevent deleting a project that is mid-operation. This is a UX guard — the server-side Delete would succeed regardless of status, but it is confusing UX to allow deletion while the status badge is spinning.

The delete confirmation uses local state (`confirming`): clicking Delete shows "Delete all data? Yes / No" inline. This avoids a separate modal.

#### CreateProjectModal validation

Client-side validation mirrors the server's rules: `^[a-z][a-z0-9-]{0,61}[a-z0-9]$` for name, `1-65535` for port. The Submit button is disabled until both pass. This improves responsiveness (no round-trip for obviously bad inputs) but the server re-validates — the client validation is never the authoritative check.

One edge case: the regex in the modal has a special case for two-character names (`name.length === 2 && /^[a-z][a-z0-9]$/.test(name)`) that is separate from the main regex. The main regex `^[a-z][a-z0-9-]{0,61}[a-z0-9]$` requires the last character to be `[a-z0-9]`, which already matches two-character names (`{0,61}` can be zero). The special case is redundant but harmless.

---

## Testing Guide

### Automated Test Coverage

#### `internal/project/store_test.go`

Helper: `setupTestDB(t)` creates an in-memory SQLite database with the full Phase 2 schema (`SetMaxOpenConns(1)` is critical — without it, goroutines in `doStart`/`doStop` would open a second connection to the in-memory DB and see an empty database).

Covers: `Create` (valid names, invalid names, duplicate, port validation), `List` (empty, multiple), `Get` (found, not found), `Delete` (success, not found), `TransitionStatus` (success, invalid state, not found, clears last_error), `SetRunning`, `SetStopped` (preserves container_id), `SetError`, `ListByStatus`.

Notable gap: no test for `resources` JSON roundtrip. If the JSON in the DB is malformed, `scanInto` silently returns `nil` Resources.

#### `internal/project/manager_test.go`

Helper: `waitForStatus` polls the store every 50ms for up to 2 seconds. This is necessary because Start and Stop are asynchronous.

Covers:
- Start scenarios: first time (triggers image build), image already exists (skips build), reuse stopped container, container deleted externally (falls through to full create), failure mid-create (cleanup NetworkRemove called), no API key, Docker unavailable
- Stop: success (container_id preserved), invalid state (already stopped)
- Delete: full cleanup (ContainerStop + ContainerRemove + NetworkRemove + VolumeRemove + DB delete), stopped project with no container
- RecoverStaleStates: running container (→ running), stopped container (→ stopped), no container (→ error), parent context cancelled (project not incorrectly set to stopped)
- Security config: verifies CapDrop=ALL, no-new-privileges, ReadonlyRootfs, tmpfs entries, memory/CPU/pid limits, user=node
- Bind mount mode: host directory created on start, bind mount used instead of volume, VolumeRemove not called, directory removed on delete
- Reset: from starting, stopping, error; invalid from stopped or running; not found
- SetAnthropicKey: set, unset, re-check

Notable gaps: no test for `tryReuseContainer` failure path where `ContainerInspect` succeeds but `ContainerStart` fails. The project would be set to error without cleanup.

#### `internal/server/router_test.go`

Helper: `setupTest` creates an in-memory DB with full schema, sets up auth + project Manager with `nil` Docker (so Docker operations return `ErrDockerUnavailable`). `authedRequest` creates a fresh session for each request.

Covers: all project CRUD endpoints (success, auth failure, validation errors), Start/Stop returning 503 with nil Docker, Reset (from starting, not found, invalid state stopped), Settings endpoints (get/set/delete API key, empty key, unauthenticated access).

Notable gaps: no test for 202 status codes from Start/Stop reaching the client (the tests confirm the request is rejected or accepted, but not that the project eventually transitions state — that is covered by manager_test.go).

### Manual Verification Checklist

```
[ ] 1. Fresh start: rm -rf data/ && ./appx -port 8443
        → Server starts, prints "Initial password written to data/initial_password"
        → Open https://localhost:8443, accept self-signed cert, log in

[ ] 2. Create project: click "New Project", enter name "test-app", port 3000, click Create
        → Modal closes, project card appears with status "stopped"

[ ] 3. Create duplicate: open "New Project" again, enter "test-app", port 4000
        → "Create" button submits, modal shows error "project name already exists"

[ ] 4. Create invalid name: enter "Test App" (spaces/uppercase)
        → Inline validation error appears, "Create" button remains disabled

[ ] 5. Settings: click "Settings" in header, page shows API key status
        → If ANTHROPIC_API_KEY env var is set on host, shows "Configured"
        → If not, shows "Not set"

[ ] 6. Set API key: enter a key in the Settings input, click Save
        → Success message appears, status changes to "Configured"

[ ] 7. Start project: return to Dashboard, click "Start" on test-app
        → Card immediately shows "starting" status, Start button replaced with
          "Starting..." + Reset button. Polling begins (visible in Network tab).
        → After 30-60 seconds (first run, image build), card shows "running"
        → Check: docker ps shows container "appx-test-app" running

[ ] 8. Stop project: click "Stop" on running project
        → Card shows "stopping" briefly, then "stopped"
        → Check: docker ps | grep test-app shows no running container
        → Check: docker ps -a shows container still exists (not removed)

[ ] 9. Restart project: click "Start" again on stopped project
        → Should be faster (reuse path, no image build, no network create)
        → Confirm in server logs: no "building base image" message
        → Check: docker inspect appx-test-app shows same container ID as before

[ ] 10. Delete API key: go to Settings, click "Remove Key"
         → Status shows "Not set"
         → Return to Dashboard, click "Start" on test-app
         → Error 400: "Anthropic API key not set — go to Settings to configure it"

[ ] 11. Reset stuck project: manually set a project to "starting" in DB:
         sqlite3 data/appx.db "UPDATE projects SET status='starting' WHERE name='test-app'"
         → Refresh dashboard, card shows "starting" with Reset button
         → Click Reset → card returns to "stopped"

[ ] 12. Delete project: click Delete on test-app, confirm "Delete all data? Yes"
         → Card disappears from list
         → Check: docker ps -a | grep test-app shows no container
         → Check: docker network ls | grep test-app shows no network
         → Check: docker volume ls | grep test-app shows no volume

[ ] 13. Startup recovery: while a project is starting, kill -9 the server
         → Restart: ./appx -port 8443
         → Dashboard shows project status reflecting actual Docker state
           (running if container started, stopped if it didn't)

[ ] 14. Docker unavailable: stop Docker daemon, try to start a project
         → 503 response, card shows error message

[ ] 15. Logout and re-login: verify all project state persists across logout/login
```

---

## Architecture and Code Pitfalls

### Pitfall 1 — `isUniqueViolation` relies on error message string matching

**Location:** `internal/project/store.go`, `isUniqueViolation()`

**The problem:** The function checks `strings.Contains(err.Error(), "UNIQUE constraint")`. This works with `modernc.org/sqlite` today but is brittle: a library upgrade could change the error message format (e.g. to "SQLITE_CONSTRAINT_UNIQUE") and silently break duplicate name detection. The user would then get a 500 Internal Server Error instead of a 409 Conflict.

**Why it matters:** Medium severity. Not a safety issue, but it causes confusing error messages and is a maintenance hazard on library upgrades.

**What a fix looks like:** Check the SQLite error code (1555 = SQLITE_CONSTRAINT_PRIMARYKEY, 2067 = SQLITE_CONSTRAINT_UNIQUE) by asserting the error to the driver's error type. `modernc.org/sqlite` exposes error codes via its `Error` type.

---

### Pitfall 2 — `TransitionStatus` disambiguation requires a second DB query

**Location:** `internal/project/store.go`, `TransitionStatus()`

**The problem:** When `rows_affected == 0`, the code issues a second query (`SELECT 1 FROM projects WHERE id = ?`) to distinguish "not found" from "wrong state". This is two round-trips when one suffices in most error cases.

**Why it matters:** Low severity. Performance is negligible with SQLite. The real risk is a TOCTOU window: between the UPDATE returning 0 rows and the SELECT checking existence, the project could be deleted. In that case, the SELECT would return `exists = false` and `ErrNotFound` would be returned — which is consistent and correct (the project was effectively not found for the operation).

**What a fix looks like:** No fix needed; the current behavior is correct. Documented here so future readers don't mistake the second query for a redundancy they can remove.

---

### Pitfall 3 — `doStart` goroutine and `Reset` can race

**Location:** `internal/project/container.go`, `Start()` / `Manager.Reset()`

**The problem:** After `Start()` transitions the project to `starting` and spawns `doStart`, a user can immediately call `Reset()` to transition it back to `stopped`. The background `doStart` goroutine continues running and will eventually call `SetRunning` — which unconditionally sets `status = 'running'` regardless of the current status. So a project that was reset to `stopped` will be overwritten to `running` when the goroutine finishes.

**Why it matters:** High severity for correctness. The user believes they reset the project, but it comes back as running. They cannot stop it (since `Stop` is only allowed from `running`, which is now the actual state), but the goroutine's running container is not tracked by the expected project state.

**What a fix looks like:** `SetRunning` (and `SetStopped`, `SetError`) could be guarded by a CAS: only set `running` if the current status is still `starting`. This would require the same `UPDATE ... WHERE status = 'starting'` approach used in `TransitionStatus`. If the update affects 0 rows (because Reset changed it to `stopped`), the goroutine would skip the update, leaving the container running but the DB as `stopped`. The container would then be cleaned up by the next Delete or by RecoverStaleStates after a restart. This is still not perfect but far less confusing than the current behavior.

---

### Pitfall 4 — API key stored in plaintext in SQLite

**Location:** `internal/auth/store.go`, `SetSetting()` / `GetSetting()`

**The problem:** The Anthropic API key is stored unencrypted in the `settings` table, protected only by the `0600` file permission on `appx.db`. Anyone with read access to the database file (e.g. if the data directory is accidentally world-readable, or if backup files are exposed) can retrieve the key.

**Why it matters:** Medium severity for a self-hosted single-user tool, since the file is owned by root by default and access is restricted. Higher severity if the data directory is on shared storage.

**What a fix looks like:** Encrypt the key at rest using a key derived from the user's password (KDF + symmetric encryption). This would require reading the password or a derived key on startup, which complicates the startup flow. For Phase 2 this is an accepted trade-off.

---

### Pitfall 5 — No container health check after start

**Location:** `internal/project/container.go`, `doFullCreate()`

**The problem:** After `ContainerStart` succeeds, the manager immediately calls `SetRunning`. But "container started" is not the same as "Claude Code is ready to accept work." The container's main process is `sleep infinity`, so it starts immediately — but any setup work done in the Dockerfile (e.g. `npm install -g @anthropic-ai/claude-code`) could fail silently without the manager noticing.

**Why it matters:** Low severity in practice, because the base image is pre-built once and reused. If the build succeeds, the image is correct. The issue would only surface if the image were somehow corrupted.

**What a fix looks like:** Phase 3 (terminal) will provide a way to exec into the container and verify state. For now, accepting this limitation is reasonable.

---

## Fixed Pitfalls

> **Problem:** Container deletion on start failure could leave orphaned Docker networks if the container ID was never set.
> **Fix:** `doFullCreate` tracks `createdNetworkID` and `createdContainerID` as local variables set only after successful creation of each resource. The `cleanup` closure checks whether each variable is non-empty before calling the corresponding Remove API. An empty variable means the resource was never created (or its creation failed), so there is nothing to clean up.

> **Problem:** Recovery after server crash could mark containers as stopped based on stale Docker state if the parent context had already expired.
> **Fix:** `RecoverStaleStates` checks `ctx.Err() != nil` after a Docker inspect error. If the parent context has been cancelled (e.g. the startup timeout fired), the function returns immediately rather than incorrectly marking remaining projects as stopped. A test (`TestManager_RecoverStaleStates_ParentContextCancelled`) covers this case.

> **Problem:** In-memory SQLite databases for tests with goroutines: goroutines calling `doStart`/`doStop` would open a second connection from the connection pool, seeing an empty database.
> **Fix:** `setupTestDB(t)` calls `db.SetMaxOpenConns(1)`, forcing all goroutines to use the same connection. This serializes all database access in tests and ensures goroutines see the data written by the test setup.

---

## TODOs and Future Improvements

### Explicit TODOs in Code

- `cmd/appx/main.go:98` — `// TODO: start with Anthropic key but be flexible to add other Coding Agent providers in the future (Codex, OpenCode, Gemini etc)`. The settings handler is currently hardcoded to the `anthropic_api_key` setting key. Supporting multiple AI providers would require a more general settings UI and multiple Manager fields.

### Known Limitations (Deliberate Trade-offs)

- **No port uniqueness constraint.** Two projects can be assigned the same port. The reverse proxy (Phase 4) will route by project name, and port conflicts will be an error at the container level, not caught at project creation. A UNIQUE constraint on `internal_port` would be a one-line schema change but was deferred to avoid breaking the API (the error message and code would need to be specified and handled in the UI).

- **No streaming start progress.** The UI shows `starting` until the background goroutine completes. If the base image needs building (first run only), this could take 1-2 minutes with no progress indicator. Addressing this requires either server-sent events or a WebSocket endpoint for build log streaming.

- **`resources` column is unused by the UI.** The `Resources` struct and the `resources` JSON column exist but the API never sets them, and the UI never shows them. They are scaffolding for a future project settings page.

### Prerequisites for Phase 3 (Terminal)

- The `dockerer` interface already declares the five Exec methods needed for terminal access (`ExecCreate`, `ExecStart`, `ExecAttach`, `ExecInspect`, `ExecResize`). Phase 3 only needs to implement the WebSocket handler that calls them.
- The `Manager.Get` method gives the terminal handler the `ContainerID` to exec into.
- The frontend `Dashboard.tsx` will need a "Terminal" button on `ProjectCard` that is enabled only when `status === 'running'`.
