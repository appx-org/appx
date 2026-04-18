# Plan: Write `docs/architecture/arch_phase_2.md`

## Context

Phase 2 (project management + Docker containers) and a security hardening pass are complete. The existing `docs/architecture/arch_v0.md` documents only Phase 1 (auth, TLS, SPA). We need a comprehensive architecture document that covers the **full current system** — everything a human or AI developer needs to understand, maintain, and extend the codebase. This goes into `docs/architecture/arch_phase_2.md`.

The document should follow the same high-quality style as `arch_v0.md` (ASCII diagrams, deep dives, data flow traces) but cover the complete system including Docker container management, API key injection, security hardening, and the testing architecture.

---

## Document Structure

### 1. What is Appx (brief, ~same as v0)

### 2. High-Level Architecture

Updated diagram showing Docker containers, the full route tree (login, projects CRUD, settings, start/stop, SPA), and the data directory. Show the relationship between Go binary, SQLite, Docker daemon, and containers.

### 3. Request Lifecycle

Updated pipeline diagram showing:

- `securityHeaders` → `limitBody` (new) → `mux`
- Public route: `POST /api/login` with rate limiter
- Protected routes: auth middleware → inner mux → all project/settings endpoints
- SPA fallback
- Show 202 async pattern for start/stop

### 4. Project Layout

Updated file tree reflecting ALL current files (container.go, store.go, settings_handlers.go, middleware.go, Dockerfile.project, fake_docker_test.go, manager_test.go, migrations/, frontend components, etc.)

### 5. Build Pipeline

Same as v0 but include `task test RESET=1` and any updates.

### 6. Backend Deep Dive

#### 6.1 Entry Point (main.go)

Updated startup sequence: Docker client init, API key resolution (DB → env var), project manager creation, RecoverStaleStates, password-to-file (not stdout).

#### 6.2 Server & Lifecycle (server.go)

ReadHeaderTimeout, IdleTimeout additions. Graceful shutdown.

#### 6.3 Routing (router.go)

Full route tree with ALL endpoints. Two-mux pattern. limitBody wrapping. Show every route registered.

#### 6.4 Authentication System

Same as v0 (still accurate). Add DeleteAllSessions mention.

#### 6.5 Database & Migrations

Updated schema showing migration 2 columns (network_id, image_name, last_error, resources). File permissions (0600).

#### 6.6 TLS Certificate Management

Same as v0 (still accurate).

#### 6.7 Rate Limiting

Same as v0 but note 5-min window / 10 attempts (updated from 15 min).

#### 6.8 Security Headers & Middleware

Updated: limitBody (1MB), CSP with connect-src 'self', full header table.

#### 6.9 Project Management (NEW — the big section)

**6.9.1 Data Model (project.go)**

- Project struct with all fields
- ProjectStatus enum and state machine diagram
- Name validation rules (regex, 2-63 chars)
- Port validation (1-65535)
- Docker resource naming convention (appx-{name}, appx-{name}-net, appx-{name}-data)
- Sentinel errors table

**6.9.2 Store (store.go)**

- CRUD operations
- State transitions (TransitionStatus — atomic UPDATE with source state check)
- SetRunning, SetStopped (preserves container_id), SetError
- ListByStatus (for recovery)

**6.9.3 Container Lifecycle (container.go)**

- Manager struct (store + docker + mutex + anthropicKey)
- `dockerer` interface (the abstraction over Docker SDK)
- Start flow diagram:
  ```
  Start() → TransitionStatus(starting) → goroutine doStart()
    → tryReuseContainer() or doFullCreate()
  ```
- doFullCreate step-by-step:
  1. ensureBaseImage (build from embedded Dockerfile if missing)
  2. NetworkCreate (bridge, labeled)
  3. Resolve /app mount: bind mount to `{projectsDir}/{name}` if `-projects` set, else Docker named volume `appx-{name}-data`
  4. ContainerCreate (hardened config — full detail)
  5. ContainerStart
  6. SetRunning
- Stop flow diagram
- Delete flow (synchronous, full teardown)
- RecoverStaleStates (startup reconciliation)
- Error handling: cleanup partial resources on failure, SetError with message

**6.9.4 Container Security Hardening**
Detailed section on every security measure:

- `User: "node"` (non-root, UID 1000 from node:22-slim)
- `CapDrop: ["ALL"]` — why and what it prevents
- `SecurityOpt: ["no-new-privileges:true"]` — blocks setuid escalation
- `ReadonlyRootfs: true` — immutable container filesystem
- Tmpfs mounts: `/tmp` (100m, noexec, nosuid), `/home/node` (50m, noexec, nosuid)
- `/app` mount: Docker named volume (default) or host bind mount when `-projects` is set
- Resource limits: 1GB memory, 2 CPUs, 256 PIDs
- RestartPolicy: unless-stopped
- ANTHROPIC_API_KEY env var injection

**6.9.5 API Key Management**

- Precedence: DB setting > env var
- Settings handlers (GET/PUT/DELETE /api/settings/api-key)
- Never expose actual key value via API
- Thread-safe access via RWMutex
- Running containers keep old key until restarted

**6.9.6 Dockerfile.project (embedded base image)**

- Contents and purpose of each layer
- go:embed mechanism
- ensureBaseImage build-on-first-use pattern

### 7. Frontend Deep Dive

Updated component tree, all pages (Login, Dashboard, Settings), all components (ProjectCard, CreateProjectModal), API client functions, polling mechanism for async operations.

### 8. Data Flow Diagrams

#### First Run (updated)

Password → file (not stdout), Docker client init, API key resolution.

#### Login Flow

Same as v0.

#### Project Lifecycle Flow (NEW)

Create → Start (async, 202) → poll → Running → Stop (async, 202) → poll → Stopped → Start again (reuse) → Delete (sync).

#### API Key Configuration Flow (NEW)

Settings page → PUT /api/settings/api-key → DB + in-memory update → next container start uses new key.

### 9. Database Schema

Updated ER diagram with all migration 2 columns. Table usage by component (updated).

### 10. Security Model

Updated with all layers:

- Layer 1: TLS
- Layer 2: Authentication (add DeleteAllSessions)
- Layer 3: Rate Limiting (updated window)
- Layer 4: Security Headers (add limitBody, connect-src)
- Layer 5: Container Hardening (NEW — the big addition)
- Layer 6: Input Validation
- Cookie properties table (same)
- Known issues table (from security review)

### 11. Testing Architecture

Fully updated:

- Store tests (project + auth)
- Migration tests
- Router tests (including settings endpoints)
- Manager tests with fakeDocker
- fakeDocker pattern explanation
- waitForStatus polling helper
- In-memory SQLite gotcha (SetMaxOpenConns(1) for goroutine tests)

### 12. Error Handling Patterns

Updated with project-specific errors (ErrNotFound → 404, ErrInvalidState → 409, ErrDockerUnavailable → 503, ErrNoAPIKey → 400) and async error propagation (SetError + LastError).

### 13. State Machine

ASCII state machine diagram for project lifecycle:

```
stopped ←→ starting → running ←→ stopping → stopped
   ↑           ↓          ↑          ↓
   └── error ←─┘          └── error ←┘
```

With allowed transitions table.

### 14. Future Phases

Updated roadmap with Phase 3-6 details. Extension points table. Phase 4 proxy routing decision (ContainerInspect for IP, no published ports). Phase 5 egress blocking approach.

### 15. Key Decisions and Trade-offs

Carry forward v0 decisions + add:

- Container reuse model (stop preserves, delete destroys)
- Async start/stop with 202 + polling
- fakeDocker interface for testability
- Embedded Dockerfile via go:embed
- API key in DB vs encrypted (accepted risk)
- Resource limits chosen (1GB/2CPU/256PID rationale)

### 16. Validation Checklist

Updated end-to-end verification covering all Phase 2 functionality.

---

## Key Files to Reference

Read these to ensure accuracy of specific details:

- `internal/project/container.go` — container hardening config, Manager struct, lifecycle methods
- `internal/project/project.go` — types, validation, errors
- `internal/project/store.go` — all DB operations, state transitions
- `internal/server/router.go` — full route registration
- `internal/server/middleware.go` — security headers, limitBody
- `internal/server/settings_handlers.go` — API key endpoints
- `internal/server/project_handlers.go` — all project handlers
- `internal/project/Dockerfile.project` — base image
- `internal/project/fake_docker_test.go` — test double pattern
- `internal/project/manager_test.go` — lifecycle tests
- `cmd/appx/main.go` — startup sequence
- `internal/server/server.go` — server config, timeouts
- `web/src/pages/Dashboard.tsx` — polling, loading state
- `web/src/pages/Settings.tsx` — API key UI
- `web/src/components/ProjectCard.tsx` — status display, actions
- `docs/architecture/arch_v0.md` — style reference

## Output

Single file: `docs/architecture/arch_phase_2.md`

## Verification

1. Read through the document for completeness — every Go file, every endpoint, every component should be mentioned
2. Verify all ASCII diagrams render correctly in markdown preview
3. Cross-check specific technical details (container config fields, route paths, status codes) against actual source code
4. Ensure the document is self-contained — a developer with no prior context should be able to understand the full system
