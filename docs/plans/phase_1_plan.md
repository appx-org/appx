# Phase 2: Project Management + Docker

## Context

Phase 1 gave us a running Go/React server with auth and an empty project list. Phase 2 makes projects real: users can create named projects with a port, start/stop their Docker containers, and delete them. Each project gets an isolated container running a minimal base image (`node:22-slim` + git + Claude Code); the user works inside via terminal in Phase 3 — Claude installs whatever else the project needs. The design must extend cleanly into Phase 3 (terminal WebSocket exec), Phase 4 (reverse proxy via port), and Phase 5 (egress logging).

---

## Architecture Changes

### New package: `internal/project/`

Three files with clear separation:

```
internal/project/
  project.go     — Project type, ProjectStatus enum, Manager struct, name validation
  store.go       — DB CRUD (ProjectStore embedded in Manager)
  container.go   — Docker lifecycle (dockerer interface + real impl, ensureBaseImage)
  store_test.go  — DB CRUD + state transition tests
```

### New `project.Manager` struct

Wraps the store and Docker client. Handlers import only the `project` package.

```go
type Manager struct {
    store  *Store            // DB layer
    docker dockerer          // Docker API (interface — mockable in tests)
}
```

`Manager` is initialized in `main.go` and injected into `NewRouter`.

### Router signature change

```go
// Before:
NewRouter(db *sql.DB, a *auth.Auth, webFS fs.FS)

// After:
NewRouter(db *sql.DB, a *auth.Auth, pm *project.Manager, webFS fs.FS)
```

---

## DB Migration #2

File: `internal/db/migrations/000002_project_docker.up.sql`

Adds columns to the existing `projects` table (avoids table recreation):

```sql
ALTER TABLE projects ADD COLUMN network_id  TEXT;
ALTER TABLE projects ADD COLUMN image_name  TEXT;
ALTER TABLE projects ADD COLUMN last_error  TEXT;
ALTER TABLE projects ADD COLUMN resources   TEXT;   -- JSON: {"memory":"1g","cpus":"1"}, NULL = host defaults
```

Down migration drops the same four columns (modernc.org/sqlite supports DROP COLUMN).

`internal_port` already exists from migration 1 — rename is not needed, keep as-is.
Add a test case in `db_test.go` verifying all four columns exist and accept values.

---

## Data Model

### `project.Project` (Go)

```go
type Project struct {
    ID          string        `json:"id"`
    Name        string        `json:"name"`
    Status      ProjectStatus `json:"status"`
    Port        int           `json:"port"`         // app port inside container; used by Phase 4 proxy
    ContainerID string        `json:"containerId,omitempty"`
    NetworkID   string        `json:"-"`            // internal only; not sent to frontend
    ImageName   string        `json:"imageName,omitempty"`
    LastError   string        `json:"lastError,omitempty"`
    Resources   *Resources    `json:"resources,omitempty"` // stubbed for future resize
    CreatedAt   string        `json:"createdAt"`
}

type Resources struct {
    Memory string `json:"memory,omitempty"`  // e.g. "1g"
    CPUs   string `json:"cpus,omitempty"`    // e.g. "1.0"
}

type ProjectStatus string
const (
    StatusStopped  ProjectStatus = "stopped"
    StatusStarting ProjectStatus = "starting"
    StatusRunning  ProjectStatus = "running"
    StatusStopping ProjectStatus = "stopping"
    StatusError    ProjectStatus = "error"
)
```

`building`/`built` are reserved for future per-project Dockerfiles; not used in Phase 2.

### Name validation

Project name = Docker container/network/volume prefix. Enforced at `Manager.Create()`:

- Pattern: `^[a-z][a-z0-9-]{0,61}$`
- Returns `ErrInvalidName` (HTTP 400) if violated
- Returns `ErrDuplicateName` (HTTP 409) on unique constraint violation

---

## API Endpoints (all behind auth middleware)

| Method   | Path                       | Description                                                  | Response           |
| -------- | -------------------------- | ------------------------------------------------------------ | ------------------ |
| `POST`   | `/api/projects`            | Create project                                               | 201 + Project JSON |
| `GET`    | `/api/projects`            | List all projects                                            | 200 + array        |
| `GET`    | `/api/projects/{id}`       | Get single project                                           | 200 + Project JSON |
| `DELETE` | `/api/projects/{id}`       | Delete project (stop → remove container/network/volume → DB) | 204                |
| `POST`   | `/api/projects/{id}/start` | Start container (async)                                      | 202                |
| `POST`   | `/api/projects/{id}/stop`  | Stop container (async)                                       | 202                |

Request body for `POST /api/projects`: `{ "name": "my-app", "port": 3000 }`

---

## Docker Container Lifecycle

### Base image: `docker/Dockerfile.project`

```dockerfile
FROM node:22-slim

RUN apt-get update && apt-get install -y \
    git curl build-essential ca-certificates \
    && npm install -g @anthropic-ai/claude-code \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
CMD ["sleep", "infinity"]
```

Minimal image: `node:22-slim` provides Node (required by Claude Code), plus git, curl, and build-essential (needed for native npm addons). Claude installs anything else the project needs (Python, etc.) via the terminal.

The Dockerfile is embedded via `//go:embed docker/Dockerfile.project` in `container.go` — single source of truth (file on disk is readable for manual builds AND embedded for runtime building). `ensureBaseImage(ctx)` checks for `appx-base:latest` via `ImageInspect`; builds it once if absent. Called at the start of every `Start()` operation.

### Per-project Docker resources

When starting a project named `foo`:

| Resource  | Name                                                             |
| --------- | ---------------------------------------------------------------- |
| Container | `appx-foo`                                                       |
| Network   | `appx-foo-net` (custom bridge, enables DNS for Phase 4)          |
| Volume    | `appx-foo-data` → `/app` (persists across container recreations) |
| Label     | `appx.project=foo` (on all resources, for tracking/cleanup)      |

No host port bindings. Phase 4 reaches containers via Docker network DNS (`appx-foo`:`port`).

### Start flow (async goroutine)

```
Manager.Start(id):
  1. Atomic DB update: status "stopped"/"error" → "starting" (returns ErrInvalidState if rowsAffected=0)
  2. Launch goroutine (context.Background() with 5min timeout — survives HTTP request):
     a. ensureBaseImage(ctx)
     b. NetworkCreate "appx-{name}-net"
     c. ContainerCreate "appx-{name}" (image, volume mount, network, labels)
     d. ContainerStart
     e. DB update: status="running", container_id, network_id, image_name
     f. On ANY error: cleanup in reverse order (remove container if created,
        remove network if created), then DB update status="error", last_error=err.Error()
  3. Return nil → handler responds 202
```

### Stop flow (async goroutine)

```
Manager.Stop(id):
  1. Atomic DB update: status "running" → "stopping"
  2. Launch goroutine (context.Background() with 30s timeout):
     a. ContainerStop (10s grace period)
     b. DB update: status="stopped", clear container_id
     c. On error: status="error", last_error
  3. Return nil → handler responds 202
```

### Delete flow (synchronous, 30s timeout)

Allowed from ANY status (including "starting"/"stopping") — best-effort cleanup so projects are never stuck un-deletable.

```
Manager.Delete(id):
  1. If container_id is set: ContainerStop (10s) then ContainerRemove — errors are non-fatal, log and continue
  2. NetworkRemove "appx-{name}-net" — non-fatal
  3. VolumeRemove "appx-{name}-data" — non-fatal
  4. DB DELETE projects WHERE id=?
  5. Return nil → handler responds 204
```

Data volume is removed on delete (personal tool; UI shows confirmation dialog).

### Startup recovery

On server start, `Manager.RecoverStaleStates(ctx)` scans for projects stuck in transitional states (`starting`, `stopping`) — e.g. from a server crash mid-operation. For each:

1. Inspect Docker container state via `ContainerInspect`
2. If container is actually running → set status to `running`
3. If container is stopped/missing → set status to `stopped`
4. If Docker is unavailable → set status to `error` with `last_error = "server restarted during operation"`

This runs once at startup before accepting requests.

---

## Concurrency / State Machine Protection

State transitions use atomic SQL:

```sql
UPDATE projects SET status = $new WHERE id = $id AND status IN ($allowed...)
```

If `rowsAffected == 0`, return `ErrInvalidState` → HTTP 409. This prevents:

- Double-starting an already-running container
- Stopping an already-stopped container
- Concurrent conflicting operations

SQLite single-writer model makes this safe without additional locks.

---

## `dockerer` Interface (for testability)

`container.go` defines a minimal interface covering only the Docker SDK methods we actually call. Method signatures will match the real `*dockerclient.Client` at implementation time — the listing below is illustrative:

```go
type dockerer interface {
    Ping(ctx)
    ImageInspectWithRaw(ctx, imageID)       // check if base image exists
    ImageBuild(ctx, buildContext, options)   // build base image
    NetworkCreate(ctx, name, options)
    NetworkRemove(ctx, networkID)
    ContainerCreate(ctx, config, hostConfig, networkingConfig, platform, name)
    ContainerStart(ctx, containerID, options)
    ContainerStop(ctx, containerID, options)
    ContainerRemove(ctx, containerID, options)
    ContainerInspect(ctx, containerID)       // needed for startup recovery
    VolumeRemove(ctx, volumeID, force)
}
```

Router tests use a `fakeDocker` stub that tracks calls and returns pre-canned results. Real Docker is exercised in manual/integration verification only.

---

## Resources Field (future-proofing)

`resources` is stored as JSON in the DB and parsed into `*Resources`. For Phase 2 it is always `NULL` (= host defaults, no constraints applied to containers). When "resize project" is added later:

- `PATCH /api/projects/{id}` updates the JSON
- A container restart applies the new limits via `HostConfig.Memory` and `HostConfig.NanoCPUs`

No schema migration needed then.

---

## `main.go` Changes

```go
dockerClient, err := dockerclient.NewClientWithOpts(
    dockerclient.FromEnv,
    dockerclient.WithAPIVersionNegotiation(),
)
// non-fatal if Docker unavailable — log warning, project start will return a clear error
if err != nil {
    log.Printf("WARN: Docker not available — project containers will not work. Install: https://docs.docker.com/engine/install/")
} else {
    // Ping to verify Docker daemon is running
    if _, err := dockerClient.Ping(ctx); err != nil {
        log.Printf("WARN: Docker daemon not responding — project containers will not work: %v", err)
    }
}

pm := project.NewManager(db, dockerClient) // dockerClient may be nil — Start() checks and returns descriptive error
pm.RecoverStaleStates(ctx) // reconcile any transitional states from prior crash
router := server.NewRouter(db, a, pm, webFS)
```

---

## Frontend Changes

### `web/src/api/client.ts` — new functions

```typescript
createProject(name: string, port: number): Promise<Project>
getProject(id: string): Promise<Project>
deleteProject(id: string): Promise<void>
startProject(id: string): Promise<void>
stopProject(id: string): Promise<void>
```

`Project` type gains `port`, `containerId`, `imageName`, `lastError`, `resources` fields.

### New: `web/src/components/ProjectCard.tsx`

- Name + port display
- Color-coded status badge: green=running, amber=starting/stopping, red=error, gray=stopped
- Start/Stop button (disabled + shows spinner during transitions)
- Delete button → inline confirmation ("Delete project and all data?")
- Error message shown when `status === "error"`

### Updated: `web/src/pages/Dashboard.tsx`

- "New Project" button in header → opens `CreateProjectModal`
- Grid renders `<ProjectCard>` components
- Status polling: `setInterval(3000)` while any project has status `starting | stopping`; clears when all are stable
- Remove "Phase 2" hint from empty state

### New: `web/src/components/CreateProjectModal.tsx`

Simple modal overlay:

- Name input (client-side slug validation: `/^[a-z][a-z0-9-]{0,61}$/`)
- Port input (number, 1–65535, default 3000)
- Error display for API failures (409 duplicate, 400 invalid)
- Cancel / Create buttons

---

## Files to Create

| File                                                    | Purpose                                                                |
| ------------------------------------------------------- | ---------------------------------------------------------------------- |
| `internal/project/project.go`                           | Types, Manager, validation, NewManager                                 |
| `internal/project/store.go`                             | DB CRUD (Create, Get, List, Delete, UpdateStatus, UpdateContainerInfo) |
| `internal/project/container.go`                         | dockerer interface, ensureBaseImage, start/stop goroutines, cleanup    |
| `internal/project/store_test.go`                        | Unit tests with in-memory SQLite                                       |
| `internal/db/migrations/000002_project_docker.up.sql`   | ALTER TABLE adds 4 columns                                             |
| `internal/db/migrations/000002_project_docker.down.sql` | DROP those 4 columns                                                   |
| `docker/Dockerfile.project`                             | Base image definition                                                  |
| `web/src/components/ProjectCard.tsx`                    | Per-project card                                                       |
| `web/src/components/CreateProjectModal.tsx`             | Create project form                                                    |

## Files to Modify

| File                                  | Change                                                                                |
| ------------------------------------- | ------------------------------------------------------------------------------------- |
| `internal/server/router.go`           | Accept `*project.Manager`, register 5 new routes, update handleListProjects signature |
| `internal/server/project_handlers.go` | Refactor to use Manager, add 5 new handlers                                           |
| `cmd/appx/main.go`                    | Init Docker client + project.Manager, pass to NewRouter                               |
| `go.mod` / `go.sum`                   | Add `github.com/docker/docker`                                                        |
| `web/src/api/client.ts`               | Add 5 new functions, extend Project type                                              |
| `web/src/pages/Dashboard.tsx`         | Create button, polling, use ProjectCard                                               |

---

## Pitfalls to Address

| Pitfall                                                 | Mitigation                                                                                          |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Concurrent start/stop                                   | Atomic SQL state transition (rowsAffected check)                                                    |
| Docker not installed/running                            | Log warning at startup; `Start()` checks for nil client → returns descriptive error in `last_error` |
| Image build on every start                              | `ensureBaseImage` checks `ImageInspectWithRaw` first; builds once and caches as `appx-base:latest`  |
| Project name in container names                         | Validated at creation time; only lowercase alphanumeric + hyphens allowed                           |
| Start fails mid-way (network created but container not) | Explicit reverse-order cleanup in error path before setting status="error"                          |
| Stuck transitional state after crash                    | `RecoverStaleStates()` at startup reconciles with Docker state                                      |
| Delete stuck project                                    | Delete allowed from ANY status — best-effort cleanup, never un-deletable                            |
| Goroutine outlives HTTP request                         | Async goroutines use `context.Background()` + timeout, not request context                          |
| Go module: Docker SDK import                            | `github.com/docker/docker/client` — uses `client.WithAPIVersionNegotiation()` for forward compat    |
| SQLite DROP COLUMN                                      | modernc.org/sqlite (already in use) supports DROP COLUMN from SQLite 3.35+                          |

---

## Verification

### 1. Build

```bash
task build   # must compile cleanly after adding Docker SDK dependency
```

### 2. Tests

```bash
task test    # all existing + new tests pass
task lint    # frontend lints clean
```

New test coverage required:

- `store_test.go`: Create, Duplicate, InvalidName, List, Get, Delete, AtomicStatusTransition
- `db_test.go`: migration 2 columns exist
- `router_test.go`: POST /api/projects (201, 400, 409), GET /api/projects/{id} (200, 404), DELETE (204, 404), POST start (202, 404, 409 for already-running), POST stop (202, 409 for already-stopped) — all using fakeDocker stub

### 3. Manual integration

```bash
# Fresh start
rm -rf data/ && ./appx

# Create project via curl
curl -sk -b cookies.txt -c cookies.txt -X POST https://localhost:8443/api/login \
  -H 'Content-Type: application/json' -d '{"password":"..."}'
curl -sk -b cookies.txt https://localhost:8443/api/projects
curl -sk -b cookies.txt -X POST https://localhost:8443/api/projects \
  -H 'Content-Type: application/json' -d '{"name":"hello","port":3000}'
curl -sk -b cookies.txt -X POST https://localhost:8443/api/projects/{id}/start
# Poll until status="running"
curl -sk -b cookies.txt https://localhost:8443/api/projects/{id}
# Verify container running:
docker ps | grep appx-hello
docker inspect appx-hello-net
docker volume ls | grep appx-hello
# Stop and delete
curl -sk -b cookies.txt -X POST https://localhost:8443/api/projects/{id}/stop
curl -sk -b cookies.txt -X DELETE https://localhost:8443/api/projects/{id}
# Verify cleanup:
docker ps -a | grep appx-hello    # should be empty
docker network ls | grep appx-hello  # should be empty
docker volume ls | grep appx-hello   # should be empty
```

### 4. UI walkthrough

```
task build && ./appx -port 8443
# Open https://localhost:8443
# Login → Dashboard (empty state, "New Project" button visible)
# Create project "demo" port 3000
# Card appears with status "stopped"
# Click Start → card shows "starting" → transitions to "running"
# Click Stop → "stopping" → "stopped"
# Click Delete → confirm dialog → project disappears
```

### 5. Migration sanity

```bash
# Verify migration 2 runs on existing DB from Phase 1
./appx -port 8443 -data ./data   # should migrate cleanly, no errors
```
