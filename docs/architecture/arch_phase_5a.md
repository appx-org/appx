# Architecture: Phase 5 (de-Docker) + Phase 5a (Custom Agent UI)

**Branch:** `de-docker-refactor`
**Date:** 2026-04-10
**Commits:** ~90 commits, 96 files changed

---

## Table of Contents

1. [Overview](#overview)
2. [System Map](#system-map)
   - [Component Diagram](#component-diagram)
   - [API Endpoints](#api-endpoints)
   - [Database Schema Changes](#database-schema-changes)
3. [Code Review Guide](#code-review-guide)
   - [Data Models: project package](#data-models-project-package)
   - [Data Access: project/store.go](#data-access-projectstorogo)
   - [Project Lifecycle: project/manager.go](#project-lifecycle-projectmanagergo)
   - [Health Checking: project/health.go](#health-checking-projecthealthgo)
   - [Egress Control: egress package](#egress-control-egress-package)
   - [OpenCode Client: opencode package](#opencode-client-opencode-package)
   - [HTTP Server: server package](#http-server-server-package)
   - [Routing: router.go](#routing-routergo)
   - [Auth and Cookie Scoping: auth package](#auth-and-cookie-scoping-auth-package)
   - [Frontend: Agent Core (headless)](#frontend-agent-core-headless)
   - [Frontend: Agent React hooks](#frontend-agent-react-hooks)
   - [Frontend: Components and Pages](#frontend-components-and-pages)
4. [Testing Guide](#testing-guide)
5. [Architecture and Code Pitfalls](#architecture-and-code-pitfalls)
6. [Fixed Pitfalls](#fixed-pitfalls)
7. [TODOs and Future Improvements](#todos-and-future-improvements)

---

## Overview

### The problem being solved

The pre-Phase-5 architecture ran each appx project inside its own Docker container. Docker would pull an image, boot a container with Claude Code inside it, and appx would proxy terminal and web traffic to the container's exposed port. This worked but had serious operational problems: Docker is a large dependency, container startup takes seconds, port allocation between container creation and INSERT was a TOCTOU race, and the whole setup required Docker to be installed — making clean VPS deployment difficult.

Phase 5 eliminates Docker entirely. A single `opencode serve` process runs permanently on `localhost:4096`. This process knows about all project directories via the `x-opencode-directory` request header: the OpenCode SDK sends this header on every API call to scope operations to a specific directory. Appx becomes a pure management shell: it handles TLS termination, authentication, reverse proxying to OpenCode, subdomain routing for agent-built apps, and outbound traffic control via an egress CONNECT proxy.

Phase 5a builds a custom agent UI on top of this. Instead of embedding or iframing OpenCode's built-in UI, appx implements a full agent interface using the OpenCode TypeScript SDK. The central architectural choice is the **headless core pattern**: all session state logic lives in `agent-core/` as pure TypeScript (no React dependency), making it directly reusable in Phase 8's native mobile clients without rewriting.

### Key design decisions

**Single OpenCode process vs per-project:** Sharing one process reduces startup overhead to zero (no container pull/boot), eliminates the container lifecycle state machine, and removes Docker as a runtime dependency. The downside is less isolation between projects — all projects share OpenCode's process memory. This is acceptable for a single-user self-hosted tool.

**Port range 10000–10999 for agent-built apps:** Each project gets a statically assigned port from this range at creation time. The assignment is permanent and persisted in the database. This gives the subdomain proxy a stable target and lets the AGENTS.md scaffold tell the agent exactly which port to use — no negotiation at runtime.

**Port allocation inside a transaction:** The `Create` method wraps port-selection and INSERT in a single SQLite transaction. This prevents the race where two concurrent creates both read the same "next available" port and one silently overwrites the other with a unique-constraint error.

**Headless core / React adapter split:** `agent-core/` contains types, a pure event reducer, and a connection manager — zero React. `agent-react/` wraps these with React-idiomatic hooks. This split was motivated by Phase 8 (native clients): the React Native app will import the same reducers and connection logic, not rewrite them.

**Browser-safe OpenCode SDK import:** The bare `@opencode-ai/sdk` export pulls in Node.js server code that breaks in browsers. Only `@opencode-ai/sdk/v2/client` is browser-safe. All SDK usage is gated through `web/src/api/opencode.ts` which enforces the correct import path.

**SSE write deadline removal:** The Go HTTP server has a 60-second `WriteTimeout`. OpenCode's SSE event streams and WebSocket PTY tunnels are indefinitely long — they would be cut at 60s. The OpenCode proxy handler disables the write deadline per-request with `http.NewResponseController(w).SetWriteDeadline(time.Time{})` immediately before proxying. `ReadHeaderTimeout` still guards against slow-header attacks.

**SameSite=Lax with Domain=.localhost:** Projects are served on subdomains (`<name>.localhost`). The session cookie must be valid on both the dashboard origin and subdomain origins. `Domain=.localhost` covers all subdomains. `SameSite=Lax` (not `Strict`) allows the cookie to be sent on top-level navigations from `myapp.localhost` to `localhost` (e.g., clicking "Back to Dashboard"), which `Strict` would block. `Lax` still blocks cross-site form POSTs and XHR.

**Egress CONNECT proxy:** The OpenCode agent can make outbound HTTP requests. Rather than using iptables (Phase 6), Phase 5 implements a Go HTTP CONNECT proxy on `127.0.0.1:9080`. Any outbound connection must tunnel through it. The proxy enforces an allowlist (default: Anthropic API, npm, Go module proxy), logs every attempt, and rejects connections that DNS-resolve to internal IPs even if the hostname passed the allowlist check.

### How the pieces fit together

```
Browser
  ↓ HTTPS (TLS terminated by appx)
appx (single binary)
  ├─ auth middleware (session cookie)
  ├─ /api/* — project CRUD, settings, config, egress management
  ├─ /api/opencode/* — reverse proxy → OpenCode (strips prefix, disables write deadline)
  │     OpenCode process (localhost:4096)
  │       scoped per project via x-opencode-directory header
  ├─ <name>.<domain>/* — reverse proxy → project dev server (port 10000-10999)
  └─ / — React SPA (embedded in binary)

Egress CONNECT proxy (127.0.0.1:9080)
  ← OpenCode agent outbound connections
  ← allowlist check → allow/block + log
```

---

## System Map

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                         appx binary                             │
│                                                                 │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────────┐   │
│  │  TLS /   │  │ Auth Middleware│  │  React SPA (embedded) │   │
│  │ HTTP srv │  │ (cookie-based)│  │  web/dist/             │   │
│  └────┬─────┘  └──────┬───────┘  └────────────────────────┘   │
│       │               │                                         │
│  ┌────▼───────────────▼────────────────────────────────────┐   │
│  │                   NewRouter                              │   │
│  │  [NEW]  GET /api/opencode/* → openCodeProxyHandler      │   │
│  │  [NEW]  <name>.<domain>/* → subdomainDispatcher         │   │
│  │  [UPD]  GET /api/projects → handleListProjects           │   │
│  │  [UPD]  GET /api/projects/{id} → handleGetProject       │   │
│  │  [NEW]  GET /api/config → handleGetConfig               │   │
│  │  [NEW]  GET/PUT /api/egress/* → egressHandlers          │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
│  ┌───────────────┐  ┌──────────────┐  ┌────────────────────┐  │
│  │ project.Mgr   │  │ opencode.Cli │  │  egress.Proxy      │  │
│  │ [UPD] no Dock │  │ [NEW]        │  │  [NEW] CONNECT     │  │
│  │ port 10000-   │  │ health+auth  │  │  127.0.0.1:9080    │  │
│  │ 10999 range   │  │ inject       │  │  allowlist+log     │  │
│  └───────────────┘  └──────────────┘  └────────────────────┘  │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐ │
│  │              SQLite (WAL mode)                             │ │
│  │  projects(assigned_port, opencode_project_id) [NEW cols]  │ │
│  │  egress_log(destination, port, allowed) [NEW]             │ │
│  │  settings, sessions [EXISTING]                            │ │
│  └───────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
         ↕ proxy                           ↕ CONNECT tunnel
┌────────────────────┐          ┌──────────────────────────────┐
│ OpenCode process   │          │ External hosts (Anthropic,   │
│ localhost:4096     │          │ npm, Go proxy, etc.)         │
│ single process,    │          │                              │
│ all projects via   │          │                              │
│ x-opencode-dir hdr │          └──────────────────────────────┘
└────────────────────┘

Frontend layers [NEW - Phase 5a]:
┌──────────────────────────────────────────────────────────────┐
│  Pages: Project.tsx, Dashboard.tsx, Egress.tsx               │
│  Components: ChatPanel, SessionList, ToolCallCard,           │
│              PermissionDock, QuestionDock, StatusBar         │
├──────────────────────────────────────────────────────────────┤
│  agent-react/ (React adapter)                                │
│    useSession, useEventStream, usePermissions                │
├──────────────────────────────────────────────────────────────┤
│  agent-core/ (pure TypeScript, no React)                     │
│    types.ts, reducers.ts, connection.ts                      │
├──────────────────────────────────────────────────────────────┤
│  api/opencode.ts — SDK client factory (directory-scoped)     │
│  api/client.ts — appx REST API typed client                  │
└──────────────────────────────────────────────────────────────┘
```

### API Endpoints

| Method | Path | Auth | Request | Response | Errors |
|--------|------|------|---------|----------|--------|
| POST | `/api/login` | None (rate-limited) | `{password}` | `{status}` + cookie | 401 wrong password |
| DELETE | `/api/session` | Cookie | — | `{status}` | 401 |
| GET | `/api/projects` | Cookie | — | `Project[]` (with `appRunning`, `projectDir`) | 401, 500 |
| POST | `/api/projects` | Cookie | `{name}` | `Project` 201 | 400 invalid name, 409 duplicate, 507 no port |
| GET | `/api/projects/{id}` | Cookie | — | `Project` (with `appRunning`, `projectDir`) | 401, 404 |
| DELETE | `/api/projects/{id}` | Cookie | — | 204 | 401, 404 |
| GET | `/api/config` | Cookie | — | `{baseDomain}` | 401 |
| GET | `/api/opencode/health` | Cookie | — | `{healthy: bool}` | 401 |
| `/api/opencode/*` | all | Cookie | proxied | proxied (SSE, WS) | 401, 502 |
| GET | `/api/egress/log` | Cookie | `?limit&offset` | `{entries, total}` | 401 |
| GET | `/api/egress/allowlist` | Cookie | — | `{entries: string[]}` | 401 |
| PUT | `/api/egress/allowlist` | Cookie | `{entries: string[]}` | `{status}` | 400 invalid, 401 |
| PUT | `/api/settings/password` | Cookie | `{currentPassword, newPassword}` | `{status}` + new cookie | 400, 401 |
| GET | `/api/settings/api-key` | Cookie | — | `{set: bool}` | 401 |
| PUT | `/api/settings/api-key` | Cookie | `{key}` | `{status}` | 400, 401 |
| DELETE | `/api/settings/api-key` | Cookie | — | `{status}` | 401 |

The `/api/opencode/*` routes are a passthrough reverse proxy. The appx session cookie is stripped before forwarding (`req.Header.Del("Cookie")`) so OpenCode never receives appx credentials. Project scoping happens because the SDK client is constructed with a `directory` parameter, which it sends as the `x-opencode-directory` header on every call.

### Database Schema Changes

**Migration 4** (`000004_project_model.up.sql`):
```sql
ALTER TABLE projects ADD COLUMN assigned_port INTEGER;
ALTER TABLE projects ADD COLUMN opencode_project_id TEXT;
CREATE UNIQUE INDEX idx_assigned_port_unique ON projects(assigned_port)
  WHERE assigned_port IS NOT NULL;
```
The unique partial index on `assigned_port` enforces no two projects share a port at the database level, providing a second line of defense behind the transaction-based allocation logic.

**Migration 5** (`000005_egress_allowed.up.sql`):
```sql
ALTER TABLE egress_log ADD COLUMN allowed BOOLEAN NOT NULL DEFAULT 1;
```
Added the `allowed` column to the egress log so entries record whether the connection was permitted or blocked. The `DEFAULT 1` handles existing rows (pre-migration, all logged connections were implicitly allowed).

**Legacy columns retained** in `projects`: `container_id`, `network_id`, `image_name`, `container_secret`, `resources`. These are never written by new code. The `Store.projectColumns` constant omits them from all SELECT queries. They will be removed in a future migration once the branch has been deployed and all row formats confirmed.

---

## Code Review Guide

### Data Models: project package

**`internal/project/project.go`**

The `Project` struct has two fields that are populated at query time, not persisted:

- `AppRunning bool` — set by `HealthChecker.Check()` at each list/get request
- `ProjectDir string` — set by `Manager.ProjectDir(name)` at each list/get request

These fields carry `json:"..."` tags so they appear in API responses but are never written to the database. This is the right pattern — they are computed facts, not stored state. The risk is that callers forget to populate them; the pattern of populating in handlers (rather than in the store) keeps the concern centralized.

`StatusStarting`, `StatusStopping`, and `StatusError` are retained for backward compatibility with existing DB rows from the Docker era. New code only writes `StatusStopped` and `StatusRunning`. The comment documents this but a reviewer should verify nothing still writes the transitional states.

**What to verify:** Search for any write path that sets `StatusStarting` or `StatusStopping` — there should be none in the current handlers. The only write that matters is `Create`, which sets `StatusStopped`.

---

### Data Access: project/store.go

**`internal/project/store.go`**

The `projectColumns` constant is the single source of truth for which columns are read. It explicitly omits Docker-era columns. This is safe because `scanInto` only scans what `projectColumns` selects.

Port allocation is the critical section. `Create` opens a transaction, calls `nextAvailablePortTx` (which queries within the transaction), then does the INSERT, then commits. SQLite's serializable default isolation ensures the query and INSERT are atomic. Two concurrent creates cannot both read the same "next" port because the second transaction will either block (if the first hasn't committed) or see the first's INSERT in its own query.

**What to verify:** The unique index `idx_assigned_port_unique` provides a database-level guard. If the transaction logic were ever bypassed, the INSERT would fail with a unique constraint error. The error mapping (`strings.Contains(err.Error(), "UNIQUE constraint")`) is SQLite-driver-specific string matching — this is a fragile pattern documented in the pitfalls section.

`nextAvailablePort` (without `Tx`) is still present and used nowhere — it is dead code. A reviewer should confirm it has no callers outside tests.

---

### Project Lifecycle: project/manager.go

**`internal/project/manager.go`**

`Manager.Create` is the only lifecycle operation that matters in Phase 5. It:
1. Inserts a DB record (gets a port)
2. Creates the directory
3. Writes `AGENTS.md` from a template
4. Runs `git init`, `git add .`, `git commit`

The `git init` + initial commit is required because OpenCode discovers projects via their git repository. Without it, OpenCode may not recognize the directory as a project.

If step 2, 3, or 4 fails, `scaffoldProject` returns an error, and `Create` calls `os.RemoveAll(projectDir)` then `m.Store.Delete(proj.ID)` to roll back. This cleanup was added after the prior retro identified that a partial scaffold could leave orphaned DB records.

The `agentsTemplate` uses `{{name}}`, `{{port}}`, and `{{subdomain}}` placeholders. `BaseDomain` is read from `m.BaseDomain` (defaulting to `"localhost"`) so the subdomain URL in AGENTS.md reflects `--domain` if set. The `Manager.BaseDomain` field is set by `main.go` after construction, not in `NewManager` — so the zero value ("") is a valid transient state. The scaffolding handles this with `if domain == "" { domain = "localhost" }`.

**What to verify:** Does `runGit` fail gracefully if `git` is not installed on the host? It returns the combined output as the error message, so the failure will propagate to the HTTP handler as an internal error. This could be a confusing failure mode if git is absent — but it's an acceptable assumption for a system that requires git-aware tooling.

---

### Health Checking: project/health.go

**`internal/project/health.go`**

`HealthChecker.Check` dials each project's assigned port concurrently with a semaphore of 20 workers. The 500ms per-dial timeout means worst case for 20 projects is 500ms (all in parallel), vs 10 seconds sequentially. Each goroutine acquires the semaphore before dialing and releases after. The mutex protects the shared `result` map.

The implementation is correct. The bound of 20 concurrent dials was chosen to avoid file descriptor exhaustion; with the 1000-project port range, all projects could theoretically be checked in 50 batches × 500ms = 25 seconds maximum, but in practice most ports are closed quickly (TCP RST, not timeout).

**What to verify:** `conn.Close()` is called without checking the error. This is fine — the only purpose of the connection is to verify the port is open; we close it immediately and don't care about the error.

The `handleListProjects` handler calls `hc.Check(projects)` synchronously on the request goroutine. With many closed ports, this adds up to 500ms to the response time for the list endpoint. The dashboard polls every 10 seconds, so one slow health check could delay the next poll. See pitfalls section.

---

### Egress Control: egress package

**`internal/egress/proxy.go` and `internal/egress/store.go`**

The egress proxy is an HTTP CONNECT proxy: it accepts only `CONNECT` method requests, checks the destination against the allowlist, logs the attempt, and if allowed, dials the destination and bidirectionally copies data.

The security-relevant logic:
1. **Allowlist check before dial** (`IsAllowed`): O(1) in-memory map lookup, mutex-protected.
2. **Logging before allow/deny decision**: `LogEntry` is called *before* the early-return 403, ensuring blocked connections are always logged.
3. **Post-dial internal IP check**: Even if a hostname passes the allowlist, if its resolved IP is loopback, private, or link-local, the connection is rejected. This defends against DNS rebinding: an attacker who controls a DNS record could make `allowed-host.example.com` resolve to `127.0.0.1` at connection time.

The `allowInternal bool` field on `Proxy` bypasses the internal IP check in tests (because the test echo server necessarily binds to 127.0.0.1). This is intentional and clearly documented.

**`Store.SetAllowlist`** updates both the in-memory map and the persistent settings table atomically (within a single write, under the mutex). `GetAllowlist` and `IsAllowed` use `sync.RWMutex` for concurrent safe reads. The `reloadAllowlist` function reads from DB at startup; subsequent changes go through `SetAllowlist`.

**What to verify:** The allowlist validation in `handleSetAllowlist` rejects `localhost` and `*.localhost` by name, and rejects IPs via `net.ParseIP` + `IsLoopback()`/`IsPrivate()`. But hostnames that are not localhost and not IPs (e.g. `internal-server.corp`) are not blocked — the post-dial IP check is the only safety net for those.

---

### OpenCode Client: opencode package

**`internal/opencode/client.go` and `internal/opencode/startup.go`**

`Client` is a thin HTTP wrapper for a few OpenCode endpoints:
- `HealthCheck` — `GET /global/health`
- `SetAuth` — `PUT /auth/:providerID` with `{"type":"api","key":"..."}`

`InjectAPIKey` polls health until ready, then calls `SetAuth("anthropic", key)`. It is called from `main.go` in a goroutine with a 2-minute context timeout — startup failures are logged but not fatal, because the user can re-inject the key from the Settings page.

The `SetAuth` body shape (`{"type":"api","key":"..."}`) was verified against OpenCode source (server.ts:99-129). If OpenCode changes its auth endpoint schema, this will silently fail — the PUT will get a 4xx, and the error is logged with "user can re-inject via Settings". Not ideal but acceptable for a self-hosted tool.

**What to verify:** `io.LimitReader(resp.Body, maxResponseSize)` (10 MB) is used on all response reads to prevent memory exhaustion from a misbehaving OpenCode process.

---

### HTTP Server: server package

**`internal/server/server.go`**

`Run` dispatches to three modes based on flags:
- `--http`: plain HTTP, binds to `127.0.0.1` only (`runHTTP`)
- `--domain example.com` + `CLOUDFLARE_API_TOKEN`: automatic Let's Encrypt via CertMagic + Cloudflare DNS-01 challenge (`runWithCertMagic`)
- default: self-signed ECDSA P-256 cert with `*.localhost` SAN (`runWithSelfSigned`)

The server has:
- `ReadHeaderTimeout: 10s` — guards against slow-header attacks
- `WriteTimeout: 60s` — applied to most responses; disabled per-connection for OpenCode proxy streams
- `IdleTimeout: 90s`

A background goroutine prunes expired sessions and egress log entries every hour.

**What to verify:** `--http` and `--domain` are mutually exclusive (validated in `Run`). The HTTP mode binds to `127.0.0.1`, not `0.0.0.0`, so it can't be accidentally exposed. The HSTS header includes `includeSubDomains` — this will force HTTPS for all `*.example.com` subdomains in browsers that visit a `--domain` deployment. This is documented in CLAUDE.md as a deployment note.

---

### Routing: router.go

**`internal/server/router.go`**

`NewRouter` builds a two-tier mux: the outer mux handles Host-header-based dispatch; the inner mux handles path routing for dashboard requests.

**OpenCode proxy** (`openCodeProxyHandler`): A single `ReverseProxy` instance is created once (not per-request). The `Director` function strips the `/api/opencode` prefix using `path.Clean(strings.TrimPrefix(...))`. `path.Clean` normalizes paths like `/api/opencode/../../../etc/passwd` to just `../../etc/passwd`, which is then forwarded to OpenCode. Since OpenCode is a local process controlled by appx, this is not a security concern — but it's worth noting.

The `path.Clean` call also handles `/api/opencode` (no trailing slash) which would produce `"."` as the path. This is remapped to `"/"`. Without this, a request to `/api/opencode` would forward to the backend with path `"."`, which most HTTP servers would reject.

**Subdomain dispatcher**: Extracts `projectName` from the Host header by trimming the base domain suffix. Looks up the project by name (single DB read per request). The project's `AssignedPort` is used to construct the upstream URL. Note: a new `ReverseProxy` is constructed per subdomain request (not per-request, but per subdomain handler invocation). The `subdomainTransport` is shared, so connection pooling still works. This is a minor inefficiency compared to caching one proxy per project.

**Cookie stripping**: Both the OpenCode proxy and the subdomain proxy call `req.Header.Del("Cookie")` before forwarding. For OpenCode, this prevents the appx session cookie from reaching OpenCode's API (which doesn't use it). For subdomains, this prevents agent-built apps from receiving the user's session token — important since those apps are user-controlled.

**What to verify:** The auth middleware wraps the subdomain proxy inline (inside the dispatch closure). This means every subdomain request is authenticated. If a project's app should be publicly accessible without login, this is the wrong behavior — but for Phase 5, all access is authenticated.

---

### Auth and Cookie Scoping: auth package

**`internal/auth/auth.go`**

Cookie domain is set to `"." + baseDomain` in `server.Run`. For `--http` mode this is `".localhost"`. For `--domain example.com` this is `".example.com"`. The leading dot means the cookie is sent to all subdomains. Without it, `myapp.localhost` would not receive the cookie set on `localhost`.

`SameSite=Lax` is used instead of `Strict`. The comment explains: `Strict` would prevent the cookie from being sent when a user navigates from `myapp.localhost` to `localhost` (top-level cross-origin navigation). `Lax` allows this while still blocking cross-site POSTs.

`X-Appx-Auth: required` is set on 401 responses so the frontend can distinguish an appx session expiry (should redirect to `/login`) from an OpenCode backend error (should not redirect). The frontend `api/client.ts` checks for 401 status (not the header directly) but the header is available for more fine-grained handling if needed.

---

### Frontend: Agent Core (headless)

**`web/src/lib/agent-core/types.ts`**

`SessionState` holds the complete UI-visible state for one session: messages (by id), parts (nested under message id), agent status, pending permissions, pending questions, todos, and error. This is a flat, serializable value — easy to snapshot and diff. The `parts` field is keyed by `messageID` (a map of message ID → array of parts), not by part ID, because parts are queried and updated in the context of their parent message.

**`web/src/lib/agent-core/reducers.ts`**

`applyEvent` is a pure function: `(SessionState, Event) → SessionState`. It handles every SSE event type from OpenCode. Key patterns:
- `upsertById` replaces or appends — idempotent, enabling reconnect replay without duplicates
- `message.part.delta` applies incremental text diffs to a specific part's field (e.g. `text` on a `TextPart`)
- `__reset` returns `initialSessionState` — called when the active session changes

The reducer pattern means session state can be exactly replayed from a sequence of events. After reconnect, `useSession` re-fetches all messages and dispatches them as `message.updated` and `message.part.updated` events — idempotent via `upsertById`.

**`web/src/lib/agent-core/connection.ts`**

`createConnection` manages the SSE lifecycle: connect → stream events → reconnect on heartbeat timeout or error. It:
1. Calls `client.event.subscribe()` to open the stream
2. Dispatches each event to `onEvent`
3. Resets a 15-second heartbeat timer on each event
4. If the timer fires, aborts the current connection (triggering reconnect)
5. On network errors, waits with exponential backoff (3s → 4.5s → ... → 30s max)
6. On clean stop (`stopped = true`), exits the loop

The returned cleanup function sets `stopped = true` and aborts the current connection. React's `useEffect` cleanup calls this when the component unmounts.

**What to verify:** `AbortError` from `currentAbort.abort()` is caught and causes an immediate retry (no delay). This is correct — it's a deliberate reconnect. Other errors (network failures) go through the backoff. A reviewer should confirm the `DOMException` + `name === 'AbortError'` check covers all browsers (it does — this is the standard AbortController signal pattern).

---

### Frontend: Agent React hooks

**`web/src/lib/agent-react/useEventStream.ts`**

Wraps `createConnection` as a React hook. Uses `useRef` to hold the latest `onEvent` callback — this prevents the `useEffect` that starts the connection from re-running just because the callback identity changes (which would cause an infinite reconnect loop). The connection only restarts when `client` changes (i.e., when the project directory changes).

**`web/src/lib/agent-react/useSession.ts`**

Combines `useEventStream`, `useReducer`, and side effects:
1. **Session change detection**: `dispatch({ type: '__reset' })` when `sessionId` changes — clears all messages before loading the new session's history.
2. **SSE event filtering**: Events are filtered by `sessionId` before dispatch. This is necessary because the OpenCode SSE stream broadcasts events for all sessions — the frontend only cares about the active one. `getSessionID` handles both top-level and nested `sessionID` properties across different event shapes.
3. **Reconnect replay**: When `connectionStatus` transitions to `'connected'`, fetches all messages for the session and dispatches them as synthetic events. The `upsertById` reducer makes this idempotent.
4. **Stale closure prevention**: `sessionIdRef` is kept in sync with `sessionId` so the `handleEvent` callback always checks against the current session, not the one captured at hook instantiation.

**`web/src/lib/agent-react/usePermissions.ts`**

Exposes three async actions: `respondPermission`, `answerQuestion`, `rejectQuestion`. These call the OpenCode SDK directly and return immediately — the SSE stream will deliver the `permission.replied` / `question.replied` / `question.rejected` events to update state. No optimistic updates — the state change is driven by events.

---

### Frontend: Components and Pages

**`web/src/api/opencode.ts`**

`getClient(directory)` is a module-level cache (`Map<string, OpencodeClient>`). Clients are keyed by project directory path. The SDK client is constructed with `baseUrl` pointing to `/api/opencode` (appx's proxy prefix) and `directory` (scopes all calls to that project). The cache prevents creating multiple SSE connections for the same project on re-renders.

**`web/src/pages/Project.tsx`**

The project page fetches `GET /api/projects/{id}` to get project data (including `projectDir` and `appRunning`) and `GET /api/config` to get `baseDomain`. Both fetches happen on mount via `useEffect`. `projectDir` is consumed directly from `project.projectDir` (not hardcoded), which is the Phase 5 fix: `const projectDir = project?.projectDir ?? ''`. The subdomain URL is built from `project.name` + `baseDomain` fetched from `/api/config`.

The project page passes `projectDir` to both `SessionList` and `ChatPanel`. Both components use it to call `getClient(projectDir)` and get a directory-scoped SDK client.

**`web/src/pages/Dashboard.tsx`**

Fetches `GET /api/config` once on mount to get `baseDomain`, then passes it to each `ProjectCard`. Polls `GET /api/projects` every 10 seconds to detect app start/stop events. The `ProjectCard` uses `baseDomain` to build the "Open App" subdomain URL.

**`web/src/components/agent/ChatPanel.tsx`**

Calls `useSession(sessionId, projectDir)` for all state. Groups messages into turns (user + associated assistants by `parentID`). Renders each turn's parts via `renderPart`: text → `<Markdown>`, tool → `<ToolCallCard>`, reasoning → collapsible `<details>`. Permission and question docks float above the input bar. The abort button replaces the send button while `status === 'running'`.

---

## Testing Guide

### Automated test coverage

**`internal/server/router_test.go`**

The main test file. Uses `setupTest()` which creates an in-memory SQLite DB with the current schema, builds a real router (no mocks), and returns `(http.Handler, *auth.Store, *sql.DB)`.

Key test scenarios:
- All project CRUD endpoints: success, 401 unauthenticated, 404 not found, 400 invalid name, 409 duplicate
- `TestGetProject_HasProjectDir` and `TestListProjects_HasProjectDir`: verify the `projectDir` field is populated in responses
- `TestGetProject_AppRunning` and `TestListProjects_AppRunningField`: start a real TCP listener on the project's assigned port, call the endpoint, assert `appRunning: true`
- `TestOpenCodeProxy_*`: verify the proxy strips the prefix, forwards query strings, returns 401 unauthenticated
- `TestGetConfig_ReturnsDomain`: verify `/api/config` returns the configured base domain
- Egress endpoints: `TestGetEgressLog`, `TestGetAllowlist`, `TestSetAllowlist`, `TestSetAllowlist_*` (validation cases)

**`internal/project/store_test.go`**

Tests `Create` (success, duplicate name, no port available), `List`, `Get`, `Delete`, `GetByName`. Uses in-memory SQLite.

**`internal/project/manager_test.go`**

Tests `Create` with filesystem scaffolding: verifies AGENTS.md exists, git repo is initialized, DB rollback on scaffold failure.

**`internal/project/health_test.go`**

Tests `Check` with a real TCP listener: asserts `true` for projects with a listening port, `false` for closed ports, and `false` for port 0.

**`internal/egress/proxy_test.go`**

Tests the CONNECT proxy end-to-end with a real TCP echo server: allowed host passes through, unlisted host gets 403, internal IP check blocks connections that resolve to loopback (`allowInternal: false`).

**`internal/opencode/client_test.go` and `startup_test.go`**

Tests against an `httptest.Server`: health check success/failure, `SetAuth` request shape, `WaitForHealthy` with immediate success and after retries, `InjectAPIKey` skips when key is empty.

### Manual verification checklist

```
[ ] Build: run `task build` — binary at ./appx, no compile errors

[ ] Fresh start (HTTP mode):
    rm -rf ./data && ./appx --http --port 8080
    Check stderr: initial password printed, also in ./data/initial_password

[ ] Login:
    Navigate to http://localhost:8080
    Should redirect to /login
    Login with initial password → redirected to dashboard

[ ] Create project:
    Click "+ New Project", enter name "demo"
    Card appears with status STOPPED
    Check ./data/projects/demo/ exists
    Check ./data/projects/demo/AGENTS.md contains correct port (10000)
    Check `git -C ./data/projects/demo log` shows initial commit

[ ] App running indicator:
    Start a server on port 10000: `python3 -m http.server 10000`
    Wait for next dashboard poll (10s) or refresh
    Card should show "RUNNING" badge in green
    Open App link should appear pointing to http://demo.localhost:8080/

[ ] Project detail page:
    Click the project card to navigate to /project/<id>
    Agent tab: Sessions panel shows empty "No sessions yet"
    Create a session → session appears in list, chat panel shows "Send a prompt to start"
    Terminal tab: connects to OpenCode PTY

[ ] projectDir correctness:
    In --http mode, the terminal header should show ./data/projects/demo as the cwd
    Agent SDK calls should target the correct project (session list is non-empty after creating)

[ ] Subdomain URL uses baseDomain:
    Dashboard and project page: "Open App" link shows demo.localhost (not hardcoded)
    If running with --domain example.com, link shows demo.example.com

[ ] Egress log:
    Navigate to /egress
    Install a package or make an outbound call from the agent
    Connections appear in the log (ALLOWED/BLOCKED status)
    Remove api.anthropic.com:443 from allowlist, retry → BLOCKED

[ ] Settings:
    Navigate to /settings
    Set an Anthropic API key → "Set" indicator changes to "Configured"
    Check that OpenCode receives the key (agent can make calls)

[ ] Auth boundary:
    Log out → redirected to /login
    Attempting /api/projects directly returns 401 with X-Appx-Auth header

[ ] TLS mode (default):
    ./appx --port 8443
    curl -kv https://localhost:8443/ — TLS handshake succeeds, cert SAN includes *.localhost

[ ] Password change:
    Change password in Settings → all other sessions invalidated
    Refreshing in another window redirects to /login

[ ] Reconnect behavior:
    Open agent session, kill and restart OpenCode process
    Status bar shows "connecting..." then reconnects automatically
    Existing messages are restored after reconnect
```

---

## Architecture and Code Pitfalls

### 1. `handleGetProject` calls health check for a single project — 500ms worst case

**Location:** `internal/server/project_handlers.go:87`
**Severity:** Low (single-project case; the 500ms hit only affects the detail page load)

`hc.Check([]*project.Project{proj})` is called with one project. If the port is closed, this waits up to 500ms. The handler returns only after the health check completes. For the detail page, this adds up to 500ms to every page load when the app is not running. Low severity because the list page (polled every 10s) parallelizes checks and is the primary data source for `appRunning`.

A fix would cache the last known health result in memory and return it immediately while refreshing in the background.

### 2. Subdomain reverse proxy creates `ReverseProxy` inline, not cached per project

**Location:** `internal/server/router.go:145-155`
**Severity:** Low

A new `httputil.ReverseProxy` is constructed inside the subdomain handler closure on every request. The `subdomainTransport` is shared so connection pooling works, but the proxy struct allocation adds minor GC pressure. With few projects, this is inconsequential. A map of `projectName → *httputil.ReverseProxy` cached on first use would eliminate the allocation.

### 3. Error detection uses `strings.Contains` on error messages

**Location:** `internal/project/store.go:53-58`
**Severity:** Medium

```go
if strings.Contains(err.Error(), "projects.name") { ... }
if strings.Contains(err.Error(), "UNIQUE constraint") { ... }
```

This string-matches against SQLite driver error messages, which are not part of any API contract. If the driver version changes the message format, `ErrDuplicateName` will not be returned and the handler will return 500 instead of 409. The more robust approach is to inspect the SQLite error code (1555 for `SQLITE_CONSTRAINT_PRIMARYKEY`, 2067 for `SQLITE_CONSTRAINT_UNIQUE`) using driver-specific error unwrapping.

### 4. `SessionList` does not re-fetch on SSE events

**Location:** `web/src/components/agent/SessionList.tsx`
**Severity:** Low

Sessions are fetched once on mount. If a new session is created in another tab, the list does not update. There is no polling or SSE subscription for the session list. A `session.created`/`session.deleted` event handler in the reducer could trigger a re-fetch, or the session list could subscribe to the SSE stream via `useEventStream` directly. Low severity because sessions are typically managed in one tab.

### 5. Allowlist validation does not reject all RFC 1918 hostnames

**Location:** `internal/server/egress_handlers.go:83-92`
**Severity:** Low

The allowlist validation rejects `localhost`, `*.localhost`, loopback IPs, and private IPs. But private hostnames that are not localhost (e.g. `db.internal`, `redis.local`) pass the validation. The post-dial IP check in the proxy (`egress/proxy.go:98-107`) is the only protection for those. An operator who adds a seemingly-harmless internal hostname to the allowlist could inadvertently expose internal services to the agent.

### 6. `isUniqueViolation` in store.go is dead code

**Location:** `internal/project/store.go:248-253`
**Severity:** Low

A helper function `isUniqueViolation` exists but has no callers. The unique-violation check is done inline in `Create` with `strings.Contains`. The function should be deleted.

### 7. `handleGetTerminalBufferSize` and `handleSetTerminalBufferSize` control nothing

**Location:** `internal/server/router.go:53-54`, `internal/server/settings_handlers.go`
**Severity:** Low

These routes remain registered and have tests, but the `internal/terminal/` package they were intended to control is no longer wired. The setting persists to SQLite but is never read by anything. The handlers and their tests are noise that mislead readers into thinking terminal buffer size is configurable. See also pitfall #8.

### 8. `internal/terminal/` package is dead code (~700 lines)

**Location:** `internal/terminal/`
**Severity:** Low

The package compiles but has no callers. Terminal WebSocket routes were removed from the router in Phase 5. The package is a confusion risk for future contributors. It should be deleted or explicitly marked with a package-level comment if intentionally retained.

---

## Fixed Pitfalls

> **Problem:** Port TOCTOU race — two concurrent `Create` calls could both read the same next-available port before either committed.
> **Fix:** `Create` now wraps port allocation and INSERT in a single SQLite transaction (`store.go:31-66`). The unique partial index on `assigned_port` provides a database-level backstop.

> **Problem:** Egress proxy logged connections asynchronously — tests needed `time.Sleep` to wait for the log entry.
> **Fix:** `LogEntry` is now called synchronously before the `http.Error(w, ..., 403)` return, so the log entry is committed before the response is sent.

> **Problem:** `handleGetProject` did not populate `AppRunning` — the project detail page always showed the app as not running.
> **Fix:** `handleGetProject` now calls `hc.Check([]*project.Project{proj})` and sets `proj.AppRunning` before responding.

> **Problem:** `Project.tsx` hardcoded `projectDir` to `/home/opencode/projects/<name>` — wrong in `--http` mode where the actual path is `./data/projects/<name>`.
> **Fix:** `const projectDir = project?.projectDir ?? ''` — reads from the API response.

> **Problem:** `ProjectCard` and `Project.tsx` hardcoded the subdomain URL to `.localhost` — wrong for `--domain` deployments.
> **Fix:** Both components receive `baseDomain` from `GET /api/config` (fetched in `Dashboard.tsx` and `Project.tsx` on mount) and use it to construct subdomain URLs.

> **Problem:** OpenCode proxy created a new `httputil.ReverseProxy` on every request — unnecessary allocation, especially for SSE reconnects.
> **Fix:** One `ReverseProxy` is created in `openCodeProxyHandler` at startup and reused for all requests. The per-request handler only disables the write deadline.

> **Problem:** Scaffold directory not cleaned up on git failure — orphaned directory left on disk after failed project creation.
> **Fix:** `manager.go:58` calls `os.RemoveAll(projectDir)` before the DB rollback if scaffolding fails.

> **Problem:** `SetAuth` used the wrong HTTP method and body shape for OpenCode's auth endpoint.
> **Fix:** Verified against OpenCode source: uses `PUT /auth/:providerID` with `{"type":"api","key":"..."}`.

> **Problem:** SSE events were not filtered by session ID — switching sessions could show events from the previous session.
> **Fix:** `useSession.handleEvent` checks `getSessionID(event)` against the active `sessionIdRef.current` and discards non-matching events. A `__reset` action clears state on session change.

> **Problem:** `SameSite=Lax` comment was incorrect/missing, leaving future maintainers unsure why `Strict` was not used.
> **Fix:** `auth.go:64` and `middleware.go` now have explicit comments explaining why `Lax` is required for subdomain navigation.

---

## TODOs and Future Improvements

### Known code TODOs

No explicit `TODO` / `FIXME` comments found in the current code.

### Deliberate trade-offs accepted

**Health checks run synchronously in request handlers.** The list endpoint blocks while checking all project ports. With the concurrent pool this is bounded to ~500ms per 20 projects, but it still adds latency. A background goroutine that caches health results on a timer would decouple health state from request latency.

**Anthropic API key stored in plaintext in SQLite.** Documented in `settings_handlers.go`. Acceptable for a self-hosted single-user tool. Phase 6 might add at-rest encryption.

**Legacy Docker columns in the `projects` table.** Still present in the schema, never written or read. Will be dropped in a future migration once confirmed safe.

### Deferred to future phases

**Phase 6 (Installer + Security):**
- Bearer token auth: small extension to `auth.Middleware` to check `Authorization: Bearer` header before falling back to cookie — sessions table infrastructure is in place
- Initial password file deletion on first login
- iptables/nftables enforcement as a backstop for the egress proxy (Phase 5 proxy is process-level only, not kernel-enforced)
- OS user isolation for the OpenCode process

**Phase 8 (Native Clients):**
- `agent-core/` reducers and `connection.ts` should be extracted into a shared package (`packages/agent`). The React Native app imports them directly — no rewrite needed.
- `getClient()` in `opencode.ts` caches by directory path. For React Native (connecting to a remote server), the cache key should include the server URL.
- `EventSessionCreated`, `EventSessionUpdated`, `EventSessionDeleted` events are not handled in the reducer — the session list does not auto-update from SSE. This would need to be wired before Phase 8.

**Subdomain routing for OpenCode (`oc.localhost`):** Currently the dashboard uses `/api/opencode/*` path prefix. A future change could route `oc.localhost` as a dedicated subdomain directly to OpenCode, simplifying the proxy and enabling direct WebSocket connections without the path-stripping logic.
