# Appx

Agentic Application Proxy — self-hostable tool to build and host personal apps with AI agents powered by [OpenCode](https://github.com/anomalyco/opencode).

## Quick Reference

```bash
task local          # Build and run appx in HTTP dev mode (127.0.0.1.sslip.io, port 8080)
task build          # Build frontend + Go binary → ./appx (without running)
task web            # Build frontend only, copy to cmd/appx/web/dist
task test           # Run all Go tests
task lint           # Lint frontend
task clean          # Remove build artifacts
./appx -port 8443   # Run (default port 443, requires root)
```

## Architecture

Single Go binary serves everything on one port (HTTPS or HTTP in dev mode). OpenCode runs as a separate process on `localhost:4096` — appx proxies to it and adds auth + TLS. Routing is Host-header based:

- `localhost:<port>` — appx dashboard (React SPA + REST API)
  - `/` — React SPA (embedded via `go:embed`)
  - `/api/*` — REST API (public: `POST /api/login`; protected: everything else)
  - `/api/opencode/*` — reverse proxy to OpenCode server (strips prefix, forwards to `localhost:4096`)
- `<project-name>.localhost:<port>` — reverse proxy to agent-built apps (port 10000–10999)

Auth: session cookie (`appx_session`), `Domain=localhost`, `SameSite=Lax`, bcrypt password, rate-limited login. The `Domain=localhost` setting makes the cookie available across all `*.localhost` subdomains so login on the dashboard is shared with project subdomains.

TLS cert includes `*.localhost` SAN so browsers accept subdomain connections without extra configuration. Modern browsers resolve `*.localhost` to `127.0.0.1` natively.

OpenCode is HTTP-only by design. Appx terminates TLS and injects auth — the browser never talks to OpenCode directly.

## Project Structure

```
cmd/appx/main.go               # Entry point, CLI flags, wires all dependencies
internal/
  auth/
    auth.go                     # Auth struct, middleware, session cookie helpers
    store.go                    # Password + session CRUD; generic key-value settings
  db/
    db.go                       # SQLite connection, versioned migrations runner
    migrations/                 # Numbered up/down SQL files (golang-migrate)
  opencode/
    client.go                   # HTTP client for OpenCode server (health, auth injection)
    startup.go                  # WaitForHealthy polling + InjectAPIKey on startup
  project/
    project.go                  # Project struct, status constants, sentinel errors
    store.go                    # Project CRUD + TransitionStatus CAS
  egress/
    proxy.go                    # Go CONNECT proxy for egress control
  terminal/
    ringbuf.go                  # Fixed-size circular byte buffer for output replay
    manager.go                  # Session registry: create/close sessions, output pump, subscriber pub/sub
    handler.go                  # WebSocket handler: upgrade, I/O pumps, resize, CSWSH protection
  server/
    server.go                   # HTTPS server, TLS config, graceful shutdown
    router.go                   # Route registration, SPA handler, OpenCode proxy, writeJSON
    auth_handlers.go            # Login/logout handlers
    project_handlers.go         # Project CRUD + start/stop handlers
    settings_handlers.go        # API key + settings get/set/delete handlers
    middleware.go               # Security headers (CSP, HSTS), limitBody
    ratelimit.go                # IP-based rate limiter
  tls/
    selfsigned.go               # Self-signed cert generation with auto-detected SANs
web/src/
  App.tsx                       # React router (Login / Dashboard / Settings / Project)
  api/
    client.ts                   # Typed API client for appx endpoints
    opencode.ts                 # OpenCode SDK client factory (browser-safe /v2/client import)
  lib/
    agent-core/                 # Headless core — no React dependency
      types.ts                  # SessionState shape
      reducers.ts               # Pure event→state reducer for SSE events
      connection.ts             # SSE subscription, heartbeat, auto-reconnect
    agent-react/                # React hooks wrapping agent-core
      useSession.ts             # Session state via useReducer + SSE + initial load
      useEventStream.ts         # SSE lifecycle tied to React mount/unmount
      usePermissions.ts         # Permission/question respond actions
  pages/
    Login.tsx                   # Password login page
    Dashboard.tsx               # Project list with polling for transitional states
    Project.tsx                 # Full-page project view with Agent/Terminal tabs
    Settings.tsx                # Anthropic API key management
    Egress.tsx                  # Egress log and allowlist management
  components/
    ProjectCard.tsx             # Per-project card: status badge, open/start/stop/delete
    CreateProjectModal.tsx      # New project form with name + port validation
    Terminal.tsx                # xterm.js wrapper: WebSocket, reconnect, resize
    Markdown.tsx                # Markdown renderer: marked + DOMPurify + code copy buttons
    ToolCallCard.tsx            # Collapsible tool call card with status badge
    PermissionDock.tsx          # Permission request UI: allow/deny/always
    QuestionDock.tsx            # Agent question UI: options + submit
    StatusBar.tsx               # Agent status + connection health indicators
    agent/
      ChatPanel.tsx             # Agent conversation: turns, streaming, parts, docks, abort
      SessionList.tsx           # Session list: create, select, delete
deploy/
  appx.service                  # systemd unit for appx
  opencode.service              # systemd unit for OpenCode server
```

## Tech Stack

- **Backend**: Go 1.26, stdlib `net/http` (no framework), `database/sql` + `modernc.org/sqlite`
- **Frontend**: React 19, Vite 8, TypeScript 5.9, react-router-dom 7
- **DB**: SQLite with WAL mode, versioned migrations via `golang-migrate` (SQL files in `internal/db/migrations/`)
- **Auth**: bcrypt passwords, SHA-256 hashed session tokens, 30-day sessions
- **TLS**: Self-signed ECDSA P-256 certs, auto-renewed 7 days before expiry
- **Agent**: [OpenCode](https://github.com/anomalyco/opencode) — runs as separate process, appx proxies to it
- **Agent SDK**: `@opencode-ai/sdk` — browser-safe client at `/v2/client` entry point (never import bare `@opencode-ai/sdk`)
- **Markdown**: `marked` + `dompurify` for rendering agent responses

## Conventions

### Go

- Every exported and unexported function/method/type must have a doc comment explaining what it does and the context in which it is used. Follow Go convention: start with the name of the identifier, write in complete sentences, and explain _why_ not just _what_ when the purpose isn't obvious from the signature. For handlers, document the HTTP method/path, request/response shape, and auth requirements. For store methods, mention the table(s) they operate on.
- Standard `internal/` layout — no exported packages
- Handlers return `http.HandlerFunc` closures (e.g. `handleLogin(a *auth.Auth) http.HandlerFunc`)
- Dependency injection via struct fields, not globals
- Migrations are numbered functions in `db.go` (`migration1`, `migration2`, ...) added to the `migrations` slice
- Tests use in-memory SQLite (`:memory:`) — no test fixtures or mocks
- Error wrapping with `fmt.Errorf("context: %w", err)`

### Frontend

- Every exported function and component must have a JSDoc comment (`/** ... */`) explaining its purpose and behavior. For components, describe what the page/component renders and its key interactions. For API functions, document the endpoint, method, and return type.
- Inline styles via `Record<string, React.CSSProperties>` objects (no CSS modules/Tailwind)
- Darksynth cyberpunk aesthetic — always use CSS variables from `web/src/index.css`, never hardcode colours. See [`docs/guides/style-guide.md`](docs/guides/style-guide.md) for the full palette, typography rules, button types, and spacing conventions.
- Appx API client in `web/src/api/client.ts` — all appx endpoint calls go through the `request<T>()` helper
- OpenCode SDK client in `web/src/api/opencode.ts` — use `getClient(directory)` for all OpenCode calls. **Always import from `@opencode-ai/sdk/v2/client`** — the bare `@opencode-ai/sdk` import pulls in Node-only server code and breaks in browsers.
- Agent state lives in `web/src/lib/agent-core/` (pure TypeScript) and `web/src/lib/agent-react/` (React hooks). Do not put agent state logic directly in components.
- On 401, redirect to `/login`

### Build

- Uses [Task](https://taskfile.dev) (`Taskfile.yml`) instead of Make. Run `task --list` to see all targets.
- `task build` builds frontend first (with file-based caching via `sources`/`generates`), copies `web/dist` into `cmd/appx/web/dist`, then `go build`
- Frontend is embedded into the Go binary via `//go:embed web/dist/*`

## Verification Loop (mandatory)

Every code change — new feature, bug fix, refactor, or modification — MUST go through this verification loop before the work is considered done. No exceptions. Do not skip steps or defer them.

### 1. Build and compile

Run `task build` (or `task web` for frontend-only changes). The change is not valid if it does not compile cleanly.

### 2. Run existing tests

Run `task test` and `task lint`. All existing tests must pass. If a change breaks an existing test, fix the root cause — do not delete or weaken the test.

### 3. Write new tests

Every change must include at least one new or updated test that specifically covers the introduced behavior. Follow these guidelines:

- **New API endpoint**: Add request/response tests in `router_test.go` covering success, auth failure, and validation error cases. Use the `setupTest()` helper.
- **New store/data method**: Add unit tests in the corresponding `*_test.go` file using in-memory SQLite.
- **New migration**: Add a test in `db_test.go` that verifies the new table/column exists and works.
- **Bug fix**: Add a regression test that reproduces the bug and proves it is fixed.
- **Refactor**: Existing tests should still pass. If coverage gaps are found, add tests before refactoring.
- **Frontend logic**: If the change involves non-trivial logic (state management, API integration, conditional rendering), describe how you verified it manually and what a test would cover.

### 4. Manual verification

Simulate what a real user would experience. Think about it from the end user perspective:

- **Backend changes**: Use `curl` or `httptest` to exercise the endpoint end-to-end. Verify response status codes, headers, body shape, and cookie behavior. Test both happy path and error cases.
- **Frontend changes**: Build with `task build`, run the server, and verify the UI renders correctly. Check that navigation, forms, error states, and loading states work as expected.
- **Database changes**: Verify migrations run on a fresh database (`rm -rf data/ && ./appx`). Confirm data survives a restart.
- **Auth changes**: Verify authenticated and unauthenticated access. Check that session cookies are set/cleared correctly.
- **TLS changes**: Verify cert generation and HTTPS connection with `curl -kv`.

### 5. Run full suite again

After writing new tests and making any adjustments, run `task test` one final time to confirm everything passes together.

### Summary checklist

```
[ ] task build — compiles cleanly
[ ] task test — all existing tests pass
[ ] New/updated tests written for the change
[ ] Manual verification performed (describe what was checked)
[ ] task test — full suite passes with new tests included
```

If any step fails, fix the issue and restart the loop from step 1. Do not proceed to the next task until all five steps are green.

## Adding a New API Endpoint

1. Add handler function in the appropriate `internal/server/*_handlers.go` file
2. Register the route in `NewRouter()` in `router.go` — public routes on `mux`, protected routes on `api`
3. Add corresponding function in `web/src/api/client.ts`
4. Write a test in `router_test.go` using `setupTest()` helper — note it returns `(handler, store, db)`

## Adding a New Migration

1. Add `migrationN` function in `internal/db/db.go`
2. Append it to the `migrations` slice
3. Add a test case in `db_test.go`

## Open Source

This project is intended to be open sourced. No credentials, personal data, or internal hostnames in code or comments.

## Documentation

After implementing any change, update all relevant documentation in `docs/` to reflect the new behaviour. Do not leave docs describing the old implementation.

### Architecture Documentation

Deep-dive references for understanding the system — read these before making significant changes:

- `docs/architecture/arch_phase_1.md` — Foundation: HTTPS server, TLS, auth, session middleware, SPA serving
- `docs/architecture/arch_phase_3.md` — In-browser terminal: ring buffer, session manager, WebSocket handler (I/O pumps, resize, CSWSH), persistent sessions, cleanup hooks, xterm.js frontend, reconnection
- `docs/architecture/arch_auth_system.md` — Auth system deep-dive: password hashing, session tokens, cookie security, rate limiting, security headers, middleware wiring, and known pitfalls
- `docs/architecture/arch_phase_5.md` — Phase 5 de-Docker simplification: single OpenCode process, appx-assigned ports, TCP health checker, AGENTS.md scaffolding, OpenCode SDK agent UI, Go CONNECT egress proxy with allowlist and logging, subdomain routing, --http dev mode, SameSite=Lax cookie scoping
- `docs/architecture/arch_phase_5_5a.md` — Full branch deep-dive: de-Docker architecture, egress CONNECT proxy, OpenCode proxy handler, concurrent health checks, headless agent-core (types/reducers/connection), agent-react hooks (useSession, useEventStream, usePermissions), ChatPanel/SessionList components, pitfalls and fixed issues
- `docs/architecture/arch_phase_5b_self_hosting.md` — Self-hosting deployment: bootstrap pipeline, OS users/groups, directory permissions, tools installation, systemd services, verification suite, security isolation model
- `docs/architecture/appx_knos_v1.md` — Two-product architecture (Appx + Knos): deployment model, OpenCode proxy design, frontend headless core pattern, offline sync strategy
- `docs/plans/phase_5a_plan.md` — Custom agent frontend plan: feature inventory (P0-P3), headless core architecture, SDK browser compatibility, implementation details with OpenCode source references

## Conventions (meta)

When the user says **"Memorise please: \<statement\>"**, rephrase the statement optimally for CLAUDE.md and append it to the relevant section (or create a new section if needed).

## Current State

Phase 5 + Phase 5a complete.

**Phase 5 (de-Docker):** Per-project Docker containers removed. A single `opencode serve` process on `localhost:4096` manages all projects natively via `x-opencode-directory` header scoping. Appx is the management shell: auth, TLS, `/api/opencode/*` reverse proxy, subdomain proxy for agent-built apps (ports 10000–10999), egress control, `--http` dev mode.

**Phase 5a (custom agent UI):** Full agent UI built on OpenCode SDK. Headless core (`web/src/lib/agent-core/`) manages session state via pure event reducers on SSE events. React hooks (`web/src/lib/agent-react/`) adapt the core for components. UI features: markdown rendering, streaming tool call cards, permission dock, question dock, session management with create/select/delete, agent status + connection health indicators, abort button.

**Key SDK note:** OpenCode SDK must be imported from `@opencode-ai/sdk/v2/client` (browser-safe). The bare `@opencode-ai/sdk` import pulls in Node-only server code.

**OpenCode is a prerequisite:** Must run as a separate process (`opencode serve --hostname 127.0.0.1 --port 4096`) before appx starts. See `deploy/opencode.service` for systemd setup.

**Next up:** Phase 6 (installer, OS users, iptables enforcement) and subdomain routing for OpenCode (`oc.localhost` instead of `/api/opencode/*` path prefix).

## Superpowers

All brainstorm specs, implementation plans, and design documents generated by superpowers skills go in `.superpowers/specs/`. Use the naming convention `YYYY-MM-DD-<topic>-<type>.md` (e.g. `2026-04-12-terminal-unify-design.md`).

## Deployment Note

The HSTS header includes `includeSubDomains`. When deploying with `--domain example.com`, this forces all subdomains of `example.com` to HTTPS for 2 years in browsers that visit appx. Do not point appx at a shared domain that also hosts HTTP services on subdomains.
