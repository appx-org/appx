# Appx + Knos: Two-Product Architecture (v1)

Date: 2026-04-09

## Vision

A platform for human-agent collaboration that combines knowledge management with agent execution. Two independent products that can be used standalone or together through a unified UI.

1. **Appx** — Agent orchestrator and personal app hosting for remote servers (Go)
2. **Knos** (Knowledge OS) — Shared .md notes and tasks with offline support, separate DB and server (TypeScript/Bun)

The combined experience is a UI wrapper that talks to both servers, bringing together notes/tasks and execution environments. Agents talk to knos through a CLI that calls the knos server API.

## Deployment Model

### Hosted tier

- One dedicated server (Hetzner) per customer for isolation
- Both appx and knos run on the same VM as separate processes
- Provisioning service manages server lifecycle, DNS, updates, billing

### Self-hosted tier

- User runs appx and/or knos on their own server
- Knos can also run locally on a laptop (offline-first)
- No Docker — bare metal / bare VM for native OS feel. Enables OS-user isolation, agents can use full system tooling, no Docker-in-Docker problems.

## Why Two Products, Not One

The separation is driven by a hard requirement: knos must work without appx. Local agents (Claude Code, Cursor, OpenCode on a laptop) need to connect to knos. Mobile and desktop clients need offline access. This cannot be a feature bolted onto an agent orchestrator.

### Adoption

- Knos alone has a much larger potential user base (anyone who takes notes/manages tasks)
- Appx alone serves the agent-hosting niche
- The combined experience is the differentiator and premium offering
- Each product finds its own audience independently; wider adoption funnel

### Failure isolation

- Notes keep working if the agent server is down (and vice versa)
- Independent release cycles — knos can ship weekly, appx monthly

### Tech stack optimization

- Go for appx: single binary, excellent for proxy/process management/OS-user isolation
- TypeScript/Bun for knos: native CRDT/sync ecosystem, fast iteration on product features

### Tradeoffs accepted

- Two services to run for the combined experience (mitigated: same VM, unified UI hides it)
- Cross-service API versioning and auth coordination required
- Slightly more complex self-hosting setup when using both

## Architecture Overview

```
+--------------------------------------------------+
|           Unified UI (web / desktop / mobile)     |
|           Talks to both servers                   |
|           Degrades to knos-only when offline       |
+--------+----------------------------+-------------+
         |                            |
         v                            v
+-------------------+    +-------------------------+
|  Knos server      |    |  Appx server (Go)       |
|  (TypeScript/Bun) |    |  TLS + auth gateway     |
|                   |    |  Reverse proxy           |
|  Notes + tasks    |    |  Single OpenCode process |
|  .md files        |    |  iptables egress control |
|  Offline sync     |    |                          |
|  PostgreSQL       |<---|  Agents call knos        |
|  PowerSync        |    |  via CLI / API           |
|                   |    |                          |
|  Runs anywhere:   |    |  Runs on:                |
|  laptop, phone,   |    |  dedicated server        |
|  server           |    |                          |
+-------------------+    +-------------------------+
```

## Appx: Agent Orchestrator (Go)

Single Go binary serving on one port. TLS-terminating auth proxy for OpenCode and agent-built apps.

### Responsibilities

- TLS termination (self-signed or ACME) — single HTTPS entry point
- Auth + session management (login once, covers all subdomains)
- Reverse proxy for OpenCode agent server (subdomain routing)
- Reverse proxy for agent-built apps (subdomain routing)
- Process supervision for a single OpenCode process (start, restart on crash)
- Project CRUD (create workspace directories, configure OpenCode directory scoping)
- iptables-based egress control

### Why Go

- Single binary deployment — no runtime dependencies, ideal for self-hosting
- Low memory footprint (20-50MB) — leaves resources for agents
- `os/exec` + `syscall.Credential` for privilege dropping
- `httputil.ReverseProxy` battle-tested for proxy workloads
- Already implemented (Phase 1-5 complete)

### Go vs Node: trade-off analysis

Appx's distinguishing characteristic — what makes it different from just "another web API" — is OS-level control: running as a specific user, dropping privileges, enforcing iptables egress, managing child processes with credential control. That's exactly where Go's stdlib shines and where Node requires workarounds.

**Go pros**
- Single binary — no runtime on the server, `scp` and run. Critical for self-hosting UX.
- OS-level primitives — `syscall.Credential`, `os/exec` with uid/gid dropping, iptables via raw sockets all feel natural.
- Memory — 20–50 MB idle leaves headroom for OpenCode and agents on a small VM.
- Proxy stdlib — `httputil.ReverseProxy`, WebSocket hijacking, TLS cert generation are all well-trodden in stdlib. No heavy framework needed.
- Trivial cross-compilation — build on a Mac, deploy to arm64 Linux, no changes.

**Go cons**
- No code sharing with knos or the frontend — three codebases, three mental contexts.
- Slower iteration — boilerplate for everything, longer feedback loops than TypeScript.
- Weaker type expressiveness — union types, discriminated unions, mapped types don't exist. Error handling is verbose.

**Node (TypeScript/Bun) pros**
- Shared types with knos and frontend — one monorepo, types flow across the stack. Auth tokens, project shapes, API response types defined once.
- Fast iteration — Bun + hot reload is substantially faster than `task build`.
- Same language as knos — one hiring profile, one toolchain, lower context-switching cost.

**Node cons**
- No single binary — `bun build --compile` produces a standalone executable but it's ~60–80 MB baseline vs Go's ~12 MB, and native module coverage is still maturing.
- OS-level ops are awkward — privilege dropping, iptables, process credential management requires native addons or shell-out. Go has `syscall.SysProcAttr` natively.
- Memory — Bun is leaner than Node but still heavier than Go at idle. Matters on a 2–4 GB self-hosted VM.
- Runtime dependency — even with `bun compile`, native modules (sqlite bindings) may require platform-specific builds.

**Conclusion**: if appx were purely a REST API + proxy, Node would be the obvious choice given the knos/frontend stack. The OS-level work (privilege dropping, iptables egress, process credential control) tips it toward Go — and the self-hosting single-binary story is a real product advantage. The main cost is no shared types with knos; mitigate with a well-documented OpenAPI spec that both sides generate clients from.

### Single OpenCode Server (not one per project)

OpenCode natively supports multiple concurrent projects through a single server instance. The `x-opencode-directory` header scopes each request to a directory, and the server creates an isolated `Instance` context per directory. Verified from OpenCode source:

- **Per-directory isolation via `InstanceState`**: Session runners, LSP servers, PTY sessions, and file watchers are all created per-directory through a scoped cache keyed by directory path. No global locks, singletons, or serialization across directories.
- **Per-session execution**: Each session gets its own `Runner` state machine (Idle → Running → Idle). Multiple sessions in different directories can run prompts simultaneously with unbounded concurrency.
- **Shared SQLite DB**: One database for all sessions, but all queries filter by `project_id` (derived from directory/git root). No cross-project data leakage.
- **Stateless HTTP transport**: No cookies, per-request Basic Auth. Trivial to proxy — no sticky sessions or session affinity needed.
- **Stateful application data**: SQLite persists sessions, messages, conversation history, todos, and an immutable event log (CQRS/event sourcing). All survives process restarts.
- **In-memory only (lost on restart)**: Active prompt runners, PTY sessions, SSE subscribers, pending permission dialogs. Clients must handle reconnection.

This eliminates per-project process management:

```
Before: appx spawns N OpenCode processes, manages N ports, health-checks N processes
After:  appx spawns ONE OpenCode process on port 4096, sets x-opencode-directory per request
```

### Appx as TLS Proxy for OpenCode

OpenCode is HTTP-only by design. Its documentation explicitly recommends a reverse proxy (Caddy/nginx) for TLS. Since appx already terminates TLS and manages auth, it serves as this proxy — adding nginx/caddy would be a redundant third service.

**Why subdomain routing, not path prefix**: OpenCode does not support path prefixes. Routes are `/session`, `/event`, `/pty`, etc. at root. A path-prefix proxy (`/api/opencode/*`) requires stripping the prefix, and OpenCode generates `Link` pagination headers using `new URL(c.req.url)` which would reflect the internal path. Subdomain routing (`oc.localhost`) eliminates this entirely — OpenCode sees requests at its native root paths.

**Auth consolidation**: User logs into appx once (session cookie with `Domain=localhost`, `SameSite=Lax` covers all `*.localhost` subdomains). Appx injects OpenCode Basic Auth credentials on the proxy hop. The browser never sees or stores OpenCode credentials.

**Latency**: Go's `httputil.ReverseProxy` over localhost is byte copying — sub-millisecond overhead per request. Negligible for SSE token streaming. Same architecture as nginx → app server, except both are on localhost.

**SSE proxying**: Set `FlushInterval: -1` on the reverse proxy for immediate streaming. OpenCode sets `X-Accel-Buffering: no` for nginx compatibility; Go doesn't buffer by default.

**WebSocket proxying**: Requires WebSocket-aware proxy (hijack + io.Copy), not plain `httputil.ReverseProxy`. Appx already does this for the terminal.

### Request Routing

```
appx Go server (port 443, TLS)
|
+-- /api/*                     -> appx handlers (auth, project CRUD, settings)
+-- oc.localhost/*             -> proxy to OpenCode (localhost:4096)
|                                 injects x-opencode-directory per project
|                                 injects Basic Auth header
+-- <project>.localhost/*      -> proxy to agent-built app port
```

### Isolation Model

Two OS users, not N+1. All projects belong to the same customer on the same server — they don't need OS-level isolation from each other. They need isolation from the orchestrator.

```
Dedicated VM
|
+-- appx (runs as: appx)
|   Owns: appx SQLite, TLS certs, config
|   Permissions: 700 on /var/lib/appx/
|   Cannot be read by: projects user
|
+-- opencode + all projects (runs as: projects)
    Single OpenCode process on port 4096
    Owns: all project dirs under /home/projects/
    Owns: OpenCode SQLite (~/.local/share/opencode/opencode.db)
    Cannot read: /var/lib/appx/ (different OS user)
    iptables: -m owner --uid-owner projects -> egress allowlist
```

Agent processes cannot touch appx internals (auth DB, TLS certs, config). Enforced by the kernel.

**Tradeoff accepted**: Projects are not isolated from each other. An agent in project-alpha could theoretically read project-beta's workspace. Acceptable because all projects belong to the same customer. If per-project isolation is needed later, it can be added by running separate OpenCode instances under separate OS users — but this is not planned.

## Knos: Knowledge OS (TypeScript/Bun)

A standalone notes and task management platform designed for human-agent collaboration.

### Responsibilities

- Markdown-based notes and tasks (CRUD, search, tagging, linking)
- Offline-first sync to web, desktop, and mobile clients
- PostgreSQL as server-side source of truth
- CLI + HTTP API for agent access
- File-system representation (.md files) for interop

### Why TypeScript/Bun

- CRDT libraries (Yjs, Automerge) are TypeScript-native
- Sync engine SDKs (PowerSync, ElectricSQL) are TypeScript-native
- Fast iteration on product features (notes UX is the core value)
- Shared types between server and client

### Offline sync strategy

Recommended starting point: **PowerSync (local SQLite syncing with PostgreSQL)**.

- Notes stored as rows with markdown content in a `content TEXT` column
- Each client gets a local SQLite replica via PowerSync SDK
- Full SQL queries work offline (including FTS5 search)
- Agents write via normal SQL — no special CRDT ceremony
- Conflict resolution: last-write-wins at row level (acceptable for single-user per server)

```
Client (offline)                     Server
+----------------+              +-------------------+
| SQLite         |<-- sync ---->| PostgreSQL        |
| (same schema)  |  (PowerSync) | (source of truth) |
|                |              |                    |
| Queries work   |              | Agents write via   |
| offline        |              | normal SQL / API   |
+----------------+              +-------------------+
```

Upgrade path: if real-time collaborative editing is needed later (user types while agent modifies same note), upgrade note bodies to Yjs CRDTs for that specific feature without rearchitecting.

### Agent interface (CLI + API)

The most important API to design. Any agent, anywhere, can use it:

```bash
knos note create "Meeting notes" --content "..."
knos note list --tag "project-alpha"
knos task create "Fix the login bug" --project alpha
knos task complete 42
```

Appx-managed agents and local agents use the same CLI. Knos is a platform, not a feature of appx.

## Frontend Architecture

### Headless core pattern

Web, mobile, and desktop clients all need the same logic: streaming token accumulation, permission/question queuing, session state, terminal management. A headless core — framework-agnostic TypeScript — is shared across all platforms through thin framework adapters.

The `@opencode-ai/sdk` package provides a browser-safe client at the `/v2/client` entry point (`createOpencodeClient()` + all types + SSE support). **Always import from `@opencode-ai/sdk/v2/client`** — the bare `@opencode-ai/sdk` import pulls in Node-only server code and breaks in browsers.

The SDK handles all HTTP and SSE transport. The headless core sits on top: it manages state (pure event reducers), connection lifecycle (heartbeat, reconnect), and business logic (permission auto-respond).

```
+-----------------------------------------------------+
|            Platform UI (thin layer)                   |
|   React (web/desktop)  |  React Native (mobile)      |
+-----------------------------------------------------+
|         Framework adapter (React hooks)               |
|    useSession  |  useEventStream  |  usePermissions   |
+-----------------------------------------------------+
|           Headless Core  (plain TypeScript)            |
|   Pure event reducers (event -> state)                |
|   SSE connection lifecycle (heartbeat, reconnect)     |
|   Permission / question queue + auto-respond          |
+-----------------------------------------------------+
|    @opencode-ai/sdk/v2/client  (browser-safe)         |
|    Typed REST client  |  SSE client  |  types          |
+-----------------------------------------------------+
```

### Why React everywhere

Mobile is required alongside web and desktop. This rules out Solid (which would need React Native for mobile anyway, creating two frameworks to maintain).

| Platform | Framework | Code sharing |
|---|---|---|
| Web | React | agent-core + agent-react + UI components |
| Desktop | Electron (React) | 100% of web code (Chromium window) |
| Mobile | React Native | agent-core + agent-react (own UI primitives) |

The valuable parts of OpenCode's existing source (SDK, event reducer logic, types) are already framework-agnostic. The Solid-specific parts (components, context providers) would need rewriting for React Native regardless.

### Frontend talks to multiple servers

The UI composes API clients for each backend. Adding knos later is adding another client + hooks, not rearchitecting.

```typescript
useAppx()           // -> appx Go server (projects, settings, auth)
useAgent(project)   // -> OpenCode via oc.localhost (sessions, SSE, PTY)
useKnos()           // -> knos server (notes, tasks, sync) — future
```

### Package structure

Start with packages in `web/src/lib/`. Promote to a monorepo when knos or mobile work begins.

```
web/src/lib/
  agent-core/              # Headless, no React dependency
    client.ts              # createOpencodeClient() wrapper with proxy baseUrl
    connection.ts          # SSE subscription via SDK, heartbeat, reconnect
    reducers.ts            # Pure event -> state functions
    permissions.ts         # Permission/question queue + auto-respond
    types.ts               # Re-export from @opencode-ai/sdk/v2/client

  agent-react/             # React hooks wrapping agent-core
    useSession.ts          # Session state via useReducer + SDK
    useEventStream.ts      # SSE lifecycle tied to component
    usePermissions.ts      # Permission/question respond actions

web/src/
  pages/                   # Appx-specific pages
  components/              # Appx-specific components
  api/client.ts            # Appx server API client
```

### Key OpenCode SSE events to handle

From the OpenCode API (verified from source):

| Event | Purpose |
|---|---|
| `message.part.delta` | Streaming token chunk — render incrementally |
| `message.updated` | Message completed or state changed |
| `session.status` | Agent status (running / idle / error) |
| `session.idle` | Agent finished — safe to send next prompt |
| `permission.asked` | Agent needs user approval for a tool call |
| `question.asked` | Agent is asking the user a question |
| `file.edited` | A file was modified by the agent |
| `server.heartbeat` | Keep-alive every 10s — use for reconnect detection |

### OpenCode interaction loop

1. Create session: `POST /session` (with `x-opencode-directory` header)
2. Subscribe to SSE: `GET /event?sessionID=...` (open before sending prompt)
3. Send prompt: `POST /session/:id/prompt_async` (returns 204, response streams via SSE)
4. Handle permissions: `POST /session/:id/permission/:permID` with allow/deny
5. Handle questions: `POST /question/:id` with answer
6. Wait for `session.idle` before sending next prompt

## Unified UI: The Integration Layer

A single frontend (React) that talks to both appx and knos APIs. Users never know it's two servers.

### Key integration points

- **Shared auth**: Single sign-on across both services (shared token or SSO)
- **Agent context**: When appx starts an agent, it injects `KNOS_URL` and `KNOS_TOKEN` so the agent can read/write notes immediately
- **Deep links**: Click a task in knos -> opens agent terminal in appx. Click agent output -> creates a note in knos
- **Graceful degradation**: UI works with knos only (offline/no agent server), appx only (no notes), or both
- **OpenCode plugin for knos**: OpenCode's plugin system (`@opencode-ai/plugin`) can add custom tools and intercept events server-side. A knos plugin could expose `knos.note.create`, `knos.task.list`, etc. as agent tools — making notes natively accessible to the agent without requiring the knos CLI. This is a future integration path once both products are stable.

### Client platforms

- **Web**: React SPA, IndexedDB/SQLite via PowerSync web SDK
- **Desktop**: Electron wrapping the same React app, native SQLite
- **Mobile**: React Native with PowerSync mobile SDK, native SQLite

## Build Order

1. **Knos first** — larger audience, works standalone, validates the harder technical problem (offline sync). Define the CLI/API contract early.
2. **Appx continues independently** — refactor to single OpenCode process, subdomain routing for `oc.localhost`, extract headless core from current frontend. Already functional.
3. **Integration layer last** — unified UI, shared auth, agent-knos wiring. Build once both APIs are stable.

## Open Questions

- Knos auth model: per-user passwords? API tokens for agents? OAuth for multi-device?
- How knos handles .md file <-> DB sync (file watcher vs periodic poll vs DB-authoritative with export)
- Whether knos needs its own subdomain routing or always sits behind appx proxy in combined mode
- PowerSync vs ElectricSQL vs cr-sqlite — needs prototyping
- Billing/metering model for the hosted tier
- How the unified UI discovers which services are available (service registry vs config)
- Single OpenCode process failure blast radius (one crash affects all projects) — acceptable for single-customer servers, revisit if multi-tenant
