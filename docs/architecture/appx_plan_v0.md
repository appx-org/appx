# Appx v0 Implementation Plan

## Context

Appx is an **Agentic Application Proxy** — a self-hostable tool that lets users build and host personal applications with AI agents. The core problems it solves:

1. Agents (Claude Code, OpenCode) work best in autonomous mode, but you want visibility and control
2. There's a gap between "agent built me an app" and "I can use it from my phone"
3. Hosting apps is hard for most people

The v0 goal: install on a VPS, get a web dashboard, run AI agents via OpenCode, and access your apps from any device — all behind auth on a single port.

**The long-term vision** (Phase 7): a **digital fortress** — every user gets their own dedicated private server holding all their apps, data, and AI models. Not a slice of shared infrastructure, but a complete isolated machine. Self-hosters run their own instance; hosted users get theirs provisioned automatically at `username.appx.app`.

## Tech Stack Decisions

| Layer     | Choice                                                             | Rationale                                                                               |
| --------- | ------------------------------------------------------------------ | --------------------------------------------------------------------------------------- |
| Backend   | **Go**                                                             | Single binary, excellent proxy/networking stdlib, low memory                            |
| Frontend  | **React + Vite**                                                   | Flexible for complex UI (terminal, real-time updates), wide ecosystem                   |
| AI Engine | **OpenCode** (single server, multi-project)                        | Open-source, provider-agnostic, web UI mode, TypeScript SDK for programmatic control    |
| Isolation | **OS user separation** (appx/opencode users)                       | Lightweight, no Docker overhead; iptables + rootless Docker for enforcement              |
| Proxy     | **Built-in Go** (`httputil.ReverseProxy`)                          | Single port, `/api/opencode/*` proxy + subdomain routing for apps                       |
| Egress    | **Go CONNECT proxy** + iptables UID enforcement                    | Two-layer defense: proxy for visibility/allowlist, iptables to prevent bypass            |
| TLS       | **Self-signed default**, Let's Encrypt for `--domain`              | Zero-config encrypted traffic out of the box; production certs via CertMagic             |
| Auth      | **Session cookie + password** (bearer tokens in Phase 6)           | Single-user personal tool; simple and sufficient                                        |
| DB        | **SQLite**                                                         | Zero ops, embedded, single file backup                                                  |
| Terminal  | **OpenCode PTY** via WebSocket proxy                               | Delegated to OpenCode; runs as `opencode` user for consistent sandboxing                |

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Server (single port, HTTPS or --http for dev)           │
│                                                          │
│  ┌───────────────────────────────────┐                   │
│  │  appx (Go binary, user: appx)     │                   │
│  │  ├─ TLS (self-signed / CertMagic) │                   │
│  │  ├─ Auth middleware                │                   │
│  │  ├─ Path router                   │                   │
│  │  │  /              → React SPA (embedded)             │
│  │  │  /api/*         → REST API                         │
│  │  │  /api/opencode/*→ reverse proxy → :4096            │
│  │  ├─ Subdomain router                                  │
│  │  │  myapp.<base>   → reverse proxy → :assigned_port   │
│  │  └─ Go CONNECT proxy (:9080, egress control)          │
│  └──┬────────────────────────────────┘                   │
│     │  HTTP (localhost:4096)                              │
│  ┌──▼────────────────────────────────┐                   │
│  │  opencode serve (user: opencode)   │                   │
│  │  ├─ Multi-project, multi-session   │                   │
│  │  ├─ HTTPS_PROXY → appx egress     │                   │
│  │  └─ PTY for terminal sessions      │                   │
│  └────────────────────────────────────┘                   │
│     │  Rootless Docker                                    │
│  ┌──▼───┐  ┌──▼───┐  ┌──▼───┐                           │
│  │:10001 │  │:10002 │  │:10003 │  Agent-built apps       │
│  │app-a  │  │app-b  │  │app-c  │  (or host processes)    │
│  └───────┘  └───────┘  └───────┘                          │
└──────────────────────────────────────────────────────────┘
```

## Project Structure

```
appx/
├── cmd/
│   └── appx/
│       └── main.go              # Entry point, CLI flags (--http, --domain, --port)
├── internal/
│   ├── server/
│   │   ├── server.go            # HTTP/HTTPS server, TLS config, graceful shutdown
│   │   ├── router.go            # Path + subdomain routing, SPA handler
│   │   ├── middleware.go        # Security headers, CSP, limitBody
│   │   ├── ratelimit.go         # IP-based rate limiter
│   │   ├── auth_handlers.go     # Login/logout handlers
│   │   ├── project_handlers.go  # Project CRUD, health status
│   │   ├── settings_handlers.go # API key, buffer size, egress allowlist
│   │   └── egress_handlers.go   # Egress log viewer, allowlist CRUD
│   ├── auth/
│   │   ├── auth.go              # Middleware, session cookies (Domain, SameSite=Lax)
│   │   └── store.go             # Password + session CRUD, bcrypt cost 12
│   ├── proxy/
│   │   └── proxy.go             # /api/opencode/* reverse proxy, app subdomain proxy
│   ├── project/
│   │   ├── project.go           # Project struct, assigned port, health status
│   │   ├── store.go             # Project CRUD, port allocator, OpenCode ID mapping
│   │   └── health.go            # TCP health checker for app ports
│   ├── opencode/
│   │   └── client.go            # Plain net/http client: health, list projects, set auth
│   ├── egress/
│   │   ├── proxy.go             # Go CONNECT proxy, allowlist enforcement, logging
│   │   └── store.go             # Egress log read/write
│   ├── tls/
│   │   └── selfsigned.go        # Self-signed cert generation
│   └── db/
│       ├── db.go                # SQLite connection, migration runner
│       └── migrations/          # Numbered SQL files
├── web/                         # React frontend
│   ├── src/
│   │   ├── App.tsx
│   │   ├── pages/
│   │   │   ├── Login.tsx        # Password login page
│   │   │   ├── Dashboard.tsx    # Project list, app health, agent sessions
│   │   │   ├── Project.tsx      # Agent interaction (SDK), terminal, app status
│   │   │   └── Settings.tsx     # API key, egress allowlist, terminal config
│   │   ├── components/
│   │   │   ├── ProjectCard.tsx  # Status, app health indicator, links
│   │   │   ├── AgentSession.tsx # Chat interface via OpenCode SDK
│   │   │   ├── EventStream.tsx  # Real-time SSE agent activity
│   │   │   ├── EgressLog.tsx    # Outbound connection log viewer
│   │   │   └── Terminal.tsx     # xterm.js via OpenCode PTY WebSocket
│   │   └── api/
│   │       └── client.ts        # Appx API client + OpenCode SDK init
│   ├── package.json
│   └── vite.config.ts
├── Taskfile.yml                 # Build tasks (replaces Make)
├── go.mod
└── go.sum
```

## Implementation Phases

### Phase 1: Foundation (skeleton that runs) ✅

**Goal**: Go server serves React app, self-signed TLS, password auth

**What was built:** HTTPS server with self-signed ECDSA certs, bcrypt auth with session cookies, SQLite with versioned migrations, React SPA embedded in Go binary, rate-limited login.

**Verification**: `task build && ./appx` → opens on https://localhost:443, shows login, authenticates, shows empty dashboard.

### Phase 2: Project Management + Docker ✅

**Goal**: Create/delete projects, start/stop containers

**What was built:** Project CRUD with SQLite store, Docker container lifecycle (create/start/stop/delete/reset), async 202+poll pattern for long operations, startup recovery for stale container states, settings API for Anthropic API key management.

**Verification**: Create a project from the dashboard → Docker container starts → project shows as "running".

### Phase 3: Terminal (shell access in browser) ✅

**Goal**: Open a terminal to any project container for shell access

**What was built:** Ring buffer for output replay, session manager with pub/sub, WebSocket handler with I/O pumps and resize, persistent sessions surviving page reloads, xterm.js frontend with reconnect and mobile copy/paste.

**Key finding:** Claude Code and OpenCode TUI modes cannot render through Docker exec PTY. OpenCode's `serve` mode provides a web UI that bypasses this — became the agent interface in Phase 4.

**Verification**: Open terminal → shell commands work → `opencode run "say hello"` returns AI response.

### Phase 4: Reverse Proxy + AI Agent Web UI ✅

**Goal**: Access OpenCode and user apps through appx's single port.

**What was built:** Three proxy approaches attempted (server-side JS patching → subdomain routing → Service Worker proxy). Final implementation: SW intercepts SPA fetches at the network boundary, rewrites paths. Per-container auth via `OPENCODE_SERVER_PASSWORD`. Security hardening: bcrypt cost 12, password min length 12, body limits, WS/terminal timeouts, replay cap, auth logging.

**Key learning:** The core proxy problem — OpenCode's SPA assumes it owns the origin root — drove significant complexity. This led to the Phase 5 architectural simplification.

**Verification**: Service Worker proxy routes OpenCode UI and API calls correctly through appx.

### Phase 5: De-Docker Simplification 🔜 Next

**Goal**: Remove per-project Docker, single OpenCode server, simplified proxy, egress control.

**See:** [`docs/plans/phase_5_plan.md`](../plans/phase_5_plan.md) for the full implementation plan.

**Architectural shift:** Replace per-project Docker containers with a single `opencode serve` process managing multiple projects natively. Appx becomes a management shell — auth, proxy, egress control, app hosting — around OpenCode as the AI engine.

**Key changes:**
- Delete all Docker/container lifecycle code, SW proxy, asset cache
- Single OpenCode process (systemd service, `opencode` user)
- `/api/opencode/*` reverse proxy replaces complex SW/subdomain proxy
- Agent interaction built into appx SPA using OpenCode SDK (reusable across web/mobile/desktop)
- Egress: Go CONNECT proxy (`HTTPS_PROXY`) + iptables UID-based enforcement
- `--http` mode for dev (locked to localhost), `--domain` for production
- App hosting: appx-assigned ports, TCP health checks, subdomain proxy
- OS user separation (appx/opencode) for security; rootless Docker for agent-built apps
- Terminal delegated to OpenCode's built-in PTY

### Phase 6: Installer + Security

**Goal**: One-command install for self-hosters, bearer token auth, production security.

**Components:**
- `scripts/install.sh` — detects OS, installs OpenCode + rootless Docker, creates system users (appx/opencode), configures systemd services, sets iptables egress rules, generates initial password
- Bearer token auth — `POST /api/login` returns bearer token alongside session cookie; all endpoints accept either
- Security hardening for production: file permissions, iptables persistence, log rotation

```bash
curl -fsSL https://get.appx.dev/install.sh | sh
# → installs opencode + rootless docker
# → creates appx + opencode system users
# → configures systemd services + iptables
# → prints: "Appx running on https://<ip>:443 — password: <generated>"
```

### Phase 7: Hosted Service — Digital Fortress Model

**Goal**: Every user gets their own dedicated server running appx.

**See:** [`docs/plans/phase_7_plan.md`](../plans/phase_7_plan.md) for the full plan.

**The model**: one dedicated VM per user (e.g. Hetzner CX22 at €4/month). Appx runs exactly as it does for self-hosters. A thin **control plane** provisions servers, manages DNS, handles billing — but never touches user data.

```
User signs up → control plane provisions VM → runs install.sh
             → configures DNS: username.appx.app → server IP
             → "Your fortress is ready"
```

**Why dedicated servers:** zero code changes to appx, hardware-level isolation, local LLM support (Ollama), simple GDPR compliance (delete the VM), contained security blast radius.

### Phase 8: Native Client

**Goal**: React Native mobile app for agent interaction from any device.

**Approach**: Uses OpenCode SDK (`@opencode-ai/sdk`) through appx's auth proxy (`/api/opencode/*`). The same React components built in Phase 5 for agent interaction (sessions, events, permissions) are adapted for React Native.

**Screens**: login, project list, agent session (chat + events), egress log.

**Auth**: bearer token from Phase 6, stored in device secure storage (Keychain/Keystore).

## Key Dependencies (Go)

- `modernc.org/sqlite` — pure-Go SQLite driver
- `golang.org/x/crypto/bcrypt` — password hashing
- Standard library: `net/http`, `net/http/httputil`, `crypto/tls`, `crypto/x509`

## Key Dependencies (Frontend)

- `react`, `react-dom`, `react-router-dom`
- `@opencode-ai/sdk` — OpenCode TypeScript SDK for agent interaction
- `xterm`, `xterm-addon-fit`, `xterm-addon-web-links`
- `vite` (build tool)

## What's explicitly NOT in v0

- Multi-user / team support → Phase 7+ (shared fortress)
- OAuth / SSO → Phase 7+
- PII detection in outbound traffic → future
- Per-project Docker containers → removed in Phase 5 (replaced by OS user separation)
- Service Worker proxy → removed in Phase 5 (replaced by `/api/opencode/*` proxy)
- OpenCode web UI as separate subdomain → replaced by integrated SDK-based UI
- Multiple AI agent sessions per project → OpenCode handles natively

## Phase Status

| Phase | Status | Notes |
|-------|--------|-------|
| 1: Foundation | ✅ Done | HTTPS, auth, SPA |
| 2: Project Management + Docker | ✅ Done | Container lifecycle, Docker SDK |
| 3: Terminal | ✅ Done | xterm.js + WebSocket, ring buffer |
| 4: Reverse Proxy + Agent UI | ✅ Done | SW proxy, port publishing, security hardening |
| 5: De-Docker Simplification | 🔜 Next | Remove Docker, single OpenCode, egress control, `--http` mode |
| 6: Installer + Security | 🔜 Planned | `install.sh`, bearer tokens, iptables, systemd |
| 7: Hosted Service | 🔜 Planned | `*.appx.app`, control plane, billing |
| 8: Native Client | 🔜 Planned | React Native app via OpenCode SDK |
