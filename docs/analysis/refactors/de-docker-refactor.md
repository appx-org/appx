# De-Docker Refactor: Single OpenCode Server Architecture

**Date:** 2026-04-07
**Status:** Brainstorming
**Context:** After completing Phase 4 (SW proxy), we realized that per-project Docker containers add complexity that doesn't earn its keep for a single-user dedicated server model. This document captures the analysis and design decisions for removing per-project Docker.

---

## Motivation

The current architecture runs a separate OpenCode instance inside a dedicated Docker container for each project. This creates:

1. **Proxy complexity** — OpenCode's SPA assumes it owns the origin root. Serving it from appx required three iterations (JS patching → subdomain routing → Service Worker proxy) to solve URL rewriting.
2. **No Docker-in-Docker** — agents inside containers can't run Docker, which prevents them from building and deploying real apps with containers.
3. **Resource overhead** — each project container consumes memory for its own Node.js runtime, OpenCode process, and Docker networking.
4. **Complexity budget** — container lifecycle management (create, start, stop, delete, reset, recover stale states, port publishing, secret injection) is the largest code surface in appx.

The target deployment model (Phase 7) is **one dedicated server per user** — either self-hosted or provisioned by the hosted service. In this model, per-project Docker isolation protects the user from their own agents' mistakes, which is a weaker justification for the complexity cost.

---

## Key Insight: The Proxy Problem Shifts

The SW proxy exists because OpenCode's SPA calls `fetch('/session')` which resolves to the appx server root, not the container. Three solutions were attempted:

| Approach                | Pro                        | Fatal flaw                                                                |
| ----------------------- | -------------------------- | ------------------------------------------------------------------------- |
| Server-side JS patching | Works                      | Fragile — matches minified variable names that change on OpenCode updates |
| Subdomain routing       | SPA works at origin root   | Chrome requires per-hostname cert trust for self-signed certs             |
| Service Worker proxy    | Robust to OpenCode updates | Complex (SW install overlay, WS patcher, 401 header detection)            |

**In production** (real domain + Let's Encrypt wildcard): subdomain routing works perfectly. `project.username.appx.app` gets a wildcard cert, SPA runs at origin root, no rewriting needed.

**The SW proxy was solving a localhost-with-self-signed-cert problem.** For a product aimed at dedicated servers, the production path matters more.

---

## Key Findings

### F1: OpenCode natively supports `HTTPS_PROXY`

OpenCode respects standard proxy env vars (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`). This enables a cooperative egress proxy:

```bash
# OpenCode's systemd service routes traffic through appx's Go CONNECT proxy
HTTPS_PROXY=http://127.0.0.1:9080 \
NO_PROXY=localhost,127.0.0.1 \
opencode serve
```

All outbound traffic from cooperative programs (OpenCode, npm, pip, curl) routes through appx's proxy for logging and allowlist enforcement. `NO_PROXY` is critical — OpenCode's web UI talks to its own server via localhost; without the bypass, you get a routing loop.

**Important limitation:** `HTTPS_PROXY` is cooperative — only programs that respect the env var route through it. A compromised agent can bypass the proxy using raw sockets, `curl --noproxy`, or any program that ignores proxy env vars. This is why egress control requires a second layer: **iptables UID-based rules** that block all direct outbound traffic from the `opencode` user at the kernel level, forcing everything through the proxy. See Phase 5 plan Step 6 for the two-layer design.

**Source:** https://opencode.ai/docs/network/

### F2: OpenCode SDK (`@opencode-ai/sdk`) provides typed programmatic access

The SDK gives full control over OpenCode from TypeScript/JavaScript:

```typescript
import { createOpencodeClient } from "@opencode-ai/sdk/v2"
const client = createOpencodeClient({ baseUrl: "http://localhost:4096" })

// Projects
await client.project.list()
await client.project.update({ projectID, name, commands: { start: "npm run dev" } })

// Sessions
await client.session.create({ title: "Build auth module" })
await client.session.prompt({ sessionID, parts: [{ type: "text", text: "..." }] })
await client.session.abort({ sessionID })

// Real-time events (SSE)
for await (const event of (await client.event.subscribe()).stream) { ... }

// Permissions (auto-approve or forward to user)
await client.permission.reply({ requestID, reply: "once" })

// Auth (inject API key programmatically)
await client.auth.set({ providerID: "anthropic", auth: { type: "api_key", key: "..." } })
```

**What this means for appx:** The SDK is the data layer for building custom UIs (web, desktop, mobile) that drive AI agent sessions. Appx's frontends use the SDK to interact with OpenCode — creating sessions, sending prompts, subscribing to events, managing permissions — while appx's Go backend handles what OpenCode doesn't: user auth, app hosting, egress control, billing.

For the **mobile app** especially: the React Native client uses `createOpencodeClient` pointed through appx's auth proxy, getting the full agent interaction surface without appx having to reimplement every OpenCode API endpoint.

**Source:** https://opencode.ai/docs/sdk/

### F3: Single OpenCode instance handles multiple projects

From the OpenCode project spec:

> _"The goal is to let a single instance of OpenCode run sessions for multiple projects and different worktrees per project."_

The API is explicitly multi-project:

```
GET  /project              → list all projects
GET  /project/current      → current active project
POST /project/:projectID/session   → create session within a specific project
```

Each session is scoped to a `projectID` and a `directory` (worktree). One process, one port, multiple projects. This eliminates the need for multiple OpenCode processes or containers.

**Source:** https://opencode.ai/docs/server/#how-it-works

---

## Open Questions

### Q1: Is OpenCode per-project or multi-project?

**Status:** Resolved — see Finding F3

Single `opencode serve` instance handles multiple projects natively. No need for multiple processes. Appx starts one OpenCode server and manages all projects through it.

**Decision:** Single OpenCode process. Appx supervises it as a child process, restarts on crash.

---

### Q2: What is a "project" without Docker?

**Status:** Partially resolved by F3

With a single OpenCode instance managing projects natively (F3), the lifecycle simplifies. OpenCode has its own project CRUD (`project.list()`, `project.update()`), so appx may not need to manage project directories at all — OpenCode discovers projects from directories where `opencode.json` exists or where the user opens a session.

| Concept  | Current (Docker)                       | Proposed (no Docker)                                           |
| -------- | -------------------------------------- | -------------------------------------------------------------- |
| Create   | DB record + container image + volume   | OpenCode project (directory + `opencode.json`) + appx metadata |
| Start    | `docker start` container               | OpenCode server is always running; "start" = open a session    |
| Stop     | `docker stop` container                | Close session (or just leave it idle — no resource cost)       |
| Reset    | Delete container + recreate from image | Git-based: `git clean -fdx && git checkout .`                  |
| Delete   | Remove container + volume + DB record  | Remove directory + appx metadata                               |
| Terminal | `docker exec` into container           | Shell on host, `cd /projects/<name>/`                          |

**Key concern:** "Reset" is still scarier without Docker. Git-based reset only works for tracked files.

**Resolved: appx keeps its own `projects` table.** OpenCode is the source of truth for AI sessions and agent state. Appx is the source of truth for infrastructure concerns that OpenCode doesn't handle:

- **Project initialization** — scaffold new projects with custom `AGENTS.md` templates, config files, or starter code before OpenCode touches them
- **Periodic automation** — sync `.md` files to an external knowledge base, auto-commit uncommitted work before snapshots, scheduled backups
- **App container lifecycle** — track which Docker containers belong to which project, cleanup, proxy routing
- **Billing metadata** — compute hours, storage, session counts
- **User preferences** — per-project settings that aren't OpenCode config

The `projects` table stores a mapping to the OpenCode `projectID` plus all the above metadata. "Create project" in appx = create directory + scaffold files + let OpenCode discover it.

**Remaining sub-question:** What does "create project" look like in the UI? Likely: user provides a name → appx creates `/projects/<name>/` → writes `AGENTS.md` template → OpenCode auto-discovers it. Or user provides a git URL → appx clones it → same flow.

**Decision:** Appx keeps `projects` table for infrastructure metadata. OpenCode owns AI state. Two-source model with appx `projectID` ↔ OpenCode `projectID` mapping.

---

### Q3: How do agent-built apps get hosted?

**Status:** Resolved

When an agent builds an app and wants to serve it, the current model runs it inside the project container and proxies via `/apps/:name/*`. Without per-project containers, three options were considered:

**Option A: Agent runs `docker run` on the host** — agent creates app containers directly. Appx needs to discover them (labels? scanning?). Unclear ownership, orphaned container cleanup problem.

**Option B: Agent starts a process on the host** — `npm run dev` as a host process. Simplest, but port conflicts when multiple projects try to use the same port.

**Option C: Appx provides a deploy API** — agent calls `POST /api/projects/:id/deploy`. Feasible but adds new API surface, CLI tooling, and the agent must be taught to use it.

#### Decision: Option B with appx-assigned ports (Phase 5), Option C for persistent hosting (later)

**Phase 5 — process-based dev hosting:**

Appx assigns each project a unique port from a reserved range (e.g. 10000–10999) at project creation time. The port is communicated to the agent via the `AGENTS.md` template that appx scaffolds into each project:

```markdown
When running a dev server, always use port $APP_PORT (currently 10001).
This port is assigned by appx and has proxy routing configured.
```

The agent reads this, runs `npm run dev -- --port 10001`, and appx proxies `myapp.username.appx.app` → `localhost:10001`.

**App discovery via health check (no agent cooperation needed):**

Appx doesn't know when the agent starts or stops an app. Instead, it health-checks the assigned port with a simple TCP dial:

```go
conn, err := net.DialTimeout("tcp", "127.0.0.1:10001", 500*time.Millisecond)
if err != nil {
    // nothing listening → dashboard shows "App: not started ○"
} else {
    conn.Close()
    // app is up → dashboard shows "App: running ● → myapp.username.appx.app"
}
```

This runs as part of the existing dashboard polling cycle. The proxy route always exists; the UI shows the link only when the health check passes. If a user hits the subdomain before the app is up, appx serves a friendly "app not running yet" page (not a raw 502).

**Auth and cookie scoping:**

All app subdomains go through appx's auth middleware — no app is publicly accessible without an appx session. The session cookie must be available across subdomains:

```
Production:  Domain=.username.appx.app   SameSite=Lax
Dev HTTP:    Domain=.localhost            SameSite=Lax
```

This means reverting from the current `SameSite=Strict` (no Domain) to `SameSite=Lax` with an explicit Domain attribute. `Strict` blocks the cookie on subdomain navigation. `Lax` is still safe — it only sends cookies on top-level navigations, not cross-site POST or iframe requests. User logs in once at the dashboard, all app subdomains are authenticated.

```
Browser → myapp.username.appx.app
       → appx receives request (same server IP, same process)
       → auth middleware checks appx_session cookie (available via Domain=...)
       → proxy to localhost:10001
```

**Later — persistent Docker hosting:**

For apps that need to survive agent session restarts and run in the background, add Docker-based deployment (Option A with label conventions, or Option C with a deploy API). This is a Phase 6+ concern once the dev workflow is validated.

---

### Q4: What's the proxy model for the new architecture?

**Status:** Resolved

With a single OpenCode server (F3), the proxy model is dramatically simpler. There's ONE OpenCode instance to proxy to, not N.

**Agent interaction — integrated into appx SPA, not a separate UI:**

OpenCode's web UI is NOT served to the browser. Instead, appx builds its own agent interaction components using the OpenCode SDK (`@opencode-ai/sdk`). The SDK calls route through appx's API proxy:

```
Browser (appx SPA)
  │  SDK call: fetch('/api/opencode/session')
  ▼
appx server
  │  strips /api/opencode prefix → forwards to localhost:4096/session
  ▼
opencode serve (localhost:4096)
```

This is a simple reverse proxy route — `/api/opencode/*` → `localhost:4096/*`, with auth middleware in front. No HTML rewriting, no SW, no subdomain for the agent.

**Why not serve OpenCode's web UI (subdomain or path)?** Building agent interaction into appx's own SPA means:
- Shared React components reusable across web, mobile (React Native), and desktop (Electron/Tauri)
- One cohesive UI — no navigating between dashboard and a separate agent app
- Full design control
- The SPA-at-root proxy problem disappears entirely — OpenCode's web UI is never served to the browser

**For agent-built apps:** See Q3. Each project gets an appx-assigned port and a subdomain (`myapp.username.appx.app`). Appx health-checks the port and proxies when the app is up. Auth via `Domain=.<base>` cookie sharing (`SameSite=Lax`).

**Summary:**
- Base domain (`username.appx.app`) → appx SPA + `/api/*` + `/api/opencode/*` proxy + terminal WS
- App subdomains (`myapp.username.appx.app`) → reverse proxy to assigned app port
- No `agent.*` subdomain needed

**Decision:** Integrated UI via OpenCode SDK. OpenCode API proxied at `/api/opencode/*`. Subdomains only for agent-built apps.

---

### Q5: What are the agent guardrails without container isolation?

**Status:** Resolved

Without Docker, a buggy or confused agent can damage the host. Two distinct threats:

1. **Cross-project interference** — agent in project A modifies project B's files, kills other processes, installs conflicting global packages, exhausts disk/CPU/memory
2. **Appx self-destruction** — agent deletes or corrupts appx's binary, database, certs, or config, causing the management layer itself to stop working

#### Threat 1: Cross-project interference

**Risk assessment for single-user dedicated server:**

- All projects belong to the same user — cross-project access is inconvenient, not a security breach
- Daily Hetzner snapshots provide rollback
- OpenCode has its own guardrails (confirmation prompts, undo)

**Accepted risk.** On a personal server, cross-project interference is an "oops, restore from snapshot" situation. Not worth the complexity of per-project OS users or namespaces.

#### Threat 2: Protecting appx from agents

**Primary defense: separate OS users.**

```
OS Users:
  appx     — owns /opt/appx/ (binary), /var/lib/appx/ (DB, certs, config)
  opencode — owns /home/opencode/projects/*

appx process (runs as 'appx' user):
  ├── Binary at /opt/appx/appx         → opencode user can't modify
  ├── Database at /var/lib/appx/data/   → opencode user can't read/write
  ├── TLS certs, config                 → opencode user can't touch
  └── Managed by systemd                → Restart=always, auto-recovers

opencode process (runs as 'opencode' user):
  ├── Can read/write /home/opencode/projects/*  → project files only
  ├── Cannot write to /opt/appx/                → permission denied
  ├── Cannot kill appx process                  → different user, no sudo
  ├── Can run docker (for building apps)        → see caveat below
  └── Started by appx as child process          → appx restarts it on crash
```

Standard Unix file permissions prevent the agent from touching appx's files. Systemd ensures appx auto-restarts even if something unexpected kills the process.

**Caveat: Docker group membership is root-equivalent.**

The `docker` group grants effective root access to the host. The Docker daemon runs as root; when any user in the group requests a bind mount, the daemon mounts it with root privileges, ignoring the requesting user's file permissions:

```
Normal Unix security:
  opencode user → read /var/lib/appx/data/appx.db
                → Permission denied (owned by appx:appx, mode 0600)
                ✓ Protection works

Docker group bypass:
  opencode user → docker run -v /var/lib/appx:/mnt alpine cat /mnt/data/appx.db
                → Container runs as root inside
                → Bind mount gives root access to the host path
                → Can read, write, or delete appx's database
                ✗ Protection bypassed
```

This isn't a bug — it's how Docker works and is documented by Docker as an expected consequence of group membership. The agent with Docker access could:

- Read appx's database (session tokens, password hashes)
- Overwrite the appx binary
- Read TLS private keys
- Mount `/etc/shadow` and read system passwords
- Essentially do anything root can do, through container bind mounts

**Mitigations for the Docker group bypass (lightest to heaviest):**

| Mitigation | How it works | Trade-offs |
|------------|-------------|------------|
| **Accept the risk** | It's a personal server, you trust the agent "enough," snapshots provide rollback. Agent would have to specifically try to attack appx — not a realistic threat for your own coding agent. | Simplest. No protection if agent goes rogue. |
| **Rootless Docker** (recommended) | Run Docker in rootless mode (`dockerd-rootless`). Daemon runs as `opencode` user, not root. Container root maps to `opencode`'s UID. Bind mounts respect real user permissions — mounting `/var/lib/appx/` still gets permission denied. | One-time setup. Well-supported on modern Linux. Some limitations: no privileged containers, some networking differences. Closes the privilege escalation cleanly. |
| **Docker socket proxy** | Run a filtering proxy (e.g. Tecnativa/docker-socket-proxy) between the agent and the Docker socket. Block dangerous API calls: volume mounts outside `/home/opencode/projects/`, privileged mode, host networking. | Targeted protection. More moving parts. |
| **AppArmor/SELinux policy** | Write a mandatory access control policy that restricts what paths the Docker daemon can mount for the `opencode` user. | Surgical. Complex to write and maintain. Distribution-specific. |

**Decision:** Separate OS users (appx vs opencode) for file-level protection + systemd for process resilience. Recommend rootless Docker to close the Docker-group privilege escalation. Cross-project interference accepted as low-risk for single-user servers with snapshot rollback.

#### Implementation: Two systemd services (recommended)

Appx and OpenCode run as independent systemd services, each with their own OS user:

```ini
# /etc/systemd/system/appx.service
[Service]
User=appx
ExecStart=/opt/appx/appx
Restart=always

# /etc/systemd/system/opencode.service
[Service]
User=opencode
Environment=HTTPS_PROXY=http://127.0.0.1:9080
Environment=NO_PROXY=localhost,127.0.0.1
ExecStart=/usr/local/bin/opencode serve
Restart=always
WorkingDirectory=/home/opencode
```

Appx does not manage OpenCode as a child process. Both are independent services supervised by systemd. Appx communicates with OpenCode via HTTP (`localhost:4096`) and health-checks it (`GET /global/health`). If OpenCode is down, appx shows a "service unavailable" state in the UI.

**Why not have appx start OpenCode directly?** Starting a child process as a different user requires root or `CAP_SETUID`. Two systemd services avoids running appx as root, keeps the services independently restartable, and follows standard Linux service management patterns.

**API key injection without env vars:** Since appx doesn't control OpenCode's environment, it can't pass `ANTHROPIC_API_KEY` as an env var at startup. Instead, appx uses the OpenCode SDK to inject the key programmatically after OpenCode starts:

```typescript
await client.auth.set({
  providerID: "anthropic",
  auth: { type: "api_key", key: storedApiKey }
})
```

This is cleaner than env var injection anyway — the key can be rotated at runtime without restarting OpenCode.

---

### Q6: What happens to existing Phase 4 code?

**Status:** Resolved

**Delete entirely — architecture no longer applies:**

- `internal/proxy/` — the whole package (SW proxy, asset cache, WS tunnel, HTML rewriting). Replaced by simple subdomain reverse proxy.
- `internal/project/container.go` — Docker lifecycle (doFullCreate, doStop, Delete, ContainerAddr, port publishing, startHook). The largest file in appx. Gone.
- `internal/project/Dockerfile.project`, `.tmux.conf` — container image artifacts. Gone.
- `internal/project/fake_docker_test.go`, `manager_test.go` — Docker-specific tests. Gone.
- DB migration 3 (`container_secret`) — no container secrets without containers. Needs a new migration to drop the column.
- Docker SDK dependency (`github.com/moby/moby/client`) — removed from `go.mod`.

**Keep as-is — valuable regardless of architecture:**

- `internal/auth/store.go` — bcrypt cost 12, min password length 12
- `internal/server/auth_handlers.go` — auth event logging
- `internal/terminal/` — ring buffer, session manager, WebSocket handler, idle timeouts, replay cap
- `internal/tls/` — self-signed cert generation, `*.localhost` SAN
- `internal/server/ratelimit.go` — rate limiting
- Body size limits in middleware

**Rewrite/adapt — same purpose, different implementation:**

- `internal/auth/auth.go` — cookie scoping changes from `SameSite=Strict` (no Domain) to `SameSite=Lax` with explicit Domain. Exact domain TBD (depends on `--domain` flag at runtime).
- `internal/server/router.go` — `/agent/:name/` and `/api/agent/:name/*` routes removed. Router still serves appx's own application: `/` (custom React SPA), `/api/*` (REST API for auth, project CRUD, billing, health status), `/ws/term/:id` (terminal WebSocket). Subdomain dispatch added on top for OpenCode web UI and agent-built apps.
- `internal/server/middleware.go` — CSP simplifies (no more per-route agent CSP with `unsafe-inline` and `worker-src`)
- `internal/server/project_handlers.go` — project CRUD adapts (no container lifecycle, adds port assignment, health check status)
- `internal/project/store.go` — schema changes (drop container_secret, add assigned_port, add opencode_project_id mapping)
- `web/src/` — custom UI built with OpenCode SDK as data layer for agent interactions. Agent/Term buttons rewired. No more iframe/SW.

**New code needed:**

- OpenCode health checker (`GET /global/health` polling)
- Go CONNECT proxy for egress (`HTTPS_PROXY`)
- Port allocator (assign from range, persist in DB)
- TCP health checker for app ports
- Subdomain router (dispatch by Host header to OpenCode or app port)
- `AGENTS.md` template scaffolding on project create

**Decision:** Single focused refactor branch. Delete dead code first, adapt surviving layers, add new pieces. The routing and project model change too fundamentally for incremental migration.

---

### Q7: What does the dev-on-localhost experience look like?

**Status:** Resolved

The SW proxy was built to make localhost HTTPS development smooth. With the new architecture, `--http` mode eliminates the need entirely.

#### Decision: `--http` mode, locked to localhost

```bash
# Dev (local machine)
./appx --http --port 8080
# → http://localhost:8080            (dashboard)
# → http://agent.localhost:8080      (OpenCode UI)
# → http://myapp.localhost:8080      (agent-built app)
# All *.localhost subdomains resolve to 127.0.0.1 natively in modern browsers
# No certs, no trust issues, no SW proxy needed

# Production (Hetzner server)
./appx --domain username.appx.app
# → https://username.appx.app          (dashboard)
# → https://agent.username.appx.app    (OpenCode UI)
# → https://myapp.username.appx.app    (agent-built app)
# Wildcard Let's Encrypt cert, HTTPS enforced
```

#### Safety: prevent HTTP in production

`--http` mode is physically locked to localhost — appx refuses to serve HTTP on a public interface:

```go
if httpMode {
    if host != "127.0.0.1" && host != "localhost" && host != "::1" {
        log.Fatal("--http mode only allowed on localhost (127.0.0.1)")
    }
}
```

Additional safeguards:

- `--http` and `--domain` are **mutually exclusive** — passing both is a fatal error
- Startup log: `"WARNING: running in HTTP mode — for local development only"`
- No HSTS header in HTTP mode
- Default mode (no flags) uses self-signed HTTPS on localhost — `--http` is an explicit opt-in

#### Why not keep the SW proxy or use mkcert?

- **SW proxy (Option B):** Two code paths to maintain for a dev-only problem. Not worth the complexity. Deleted with the rest of Phase 4 proxy code.
- **mkcert (Option C):** Good tool, but adds a setup dependency. Could be documented as an optional alternative for developers who need HTTPS locally, but not built into appx.

---

## Proposed Phase Restructuring

| New Phase                         | What it does                                                                                                                                 | Maps to old                                         |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| **Phase 5: Simplification**       | Remove per-project Docker, single OpenCode process, simplified proxy (one subdomain), `--http` dev mode, egress proxy via `HTTPS_PROXY` (F1) | Replaces old Phase 5, absorbs egress                |
| **Phase 6: Installer + Security** | `install.sh`, bearer token auth, security hardening for new model, Docker socket access for agents                                           | Merges old Phase 5 security + old Phase 6 installer |
| **Phase 7: Hosted Service**       | Control plane, `username.appx.app`, hibernation, billing                                                                                     | Mostly unchanged                                    |
| **Phase 8: Native Client**        | React Native app using OpenCode SDK (F2) through appx auth proxy                                                                             | Extracted from old Phase 6                          |

**Rationale for pushing native client to Phase 8:** The API surface needs to stabilize after the refactor. Building a native client against an API that's about to change is wasted work.

**Key simplification from findings:** Egress control (old Phase 5's hardest problem) uses a two-layer approach: Go CONNECT proxy via `HTTPS_PROXY` (F1) for cooperative programs + iptables UID-based rules for hard enforcement against bypass. No eBPF or Docker network tricks needed. Project management delegates to OpenCode's native multi-project support (F3). Custom UI development gets a typed SDK for free (F2). The overall complexity of the remaining phases drops significantly.

---

## Decisions Log

Record final decisions here as questions are resolved.

| Question              | Decision                                                              | Date       | Notes                                               |
| --------------------- | --------------------------------------------------------------------- | ---------- | --------------------------------------------------- |
| Q1: OpenCode model    | **Resolved.** Single process, multi-project native support.           | 2026-04-07 | See Finding F3. Confirmed by docs + manual testing. |
| Q2: Project lifecycle | **Resolved.** Two-source: OpenCode owns AI state, appx owns infra metadata. | 2026-04-07 | Appx keeps `projects` table with OC projectID mapping. |
| Q3: App hosting       | **Resolved.** Appx-assigned ports + health check + subdomain proxy + Lax cookie. Docker deploy later. | 2026-04-07 | Phase 5: process-based. Phase 6+: Docker deploy API. |
| Q4: Proxy model       | **Resolved.** Integrated UI via SDK. `/api/opencode/*` proxies to OC. Subdomains for apps only. | 2026-04-07 | No agent subdomain. See F2, F3, Q3, Q7. |
| Q5: Agent guardrails  | **Resolved.** Separate OS users (appx/opencode) + systemd + rootless Docker. Cross-project risk accepted. | 2026-04-07 | Docker group is root-equivalent; rootless Docker closes the gap. |
| Q6: Phase 4 code      | **Resolved.** Delete proxy/container/Docker code. Keep auth/terminal/security. Adapt router+project model. | 2026-04-07 | Single refactor branch, not incremental. |
| Q7: Dev experience    | **Resolved.** `--http` mode locked to localhost. `--domain` for prod. Mutually exclusive. | 2026-04-07 | SW proxy deleted. mkcert documented as optional. |
