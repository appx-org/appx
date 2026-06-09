# Appx

Agentic Application Proxy: a self-hostable tool to build and host personal apps with AI agents powered by Pi.

## Quick Reference

```bash
task local          # Build and run appx in HTTP dev mode (127.0.0.1.sslip.io, port 8080)
task build          # Build frontend + Go binary -> ./appx (without running)
task web            # Build frontend only, copy to cmd/appx/web/dist
task test           # Run all Go tests
task lint           # Lint frontend
task clean          # Remove build artifacts
./appx -port 8443   # Run (default port 443, requires root)
```

## Architecture

Single Go binary serves everything on one port (HTTPS or HTTP in dev mode). Pi runs behind the sibling `agent-server` service on `localhost:4001`. agent-server owns project identity, the on-disk project directory (including each project's `.pi/` harness), session transcripts, models, and credentials; appx is a **control plane + authorizing gateway** that owns auth, TLS, port/subdomain assignment, egress policy, and a per-project SQLite record, and proxies agent traffic to agent-server. See `.superpowers/specs/2026-06-09-project-ownership-and-agent-chat-integration-adr.md`.

- `localhost:<port>`: appx dashboard, embedded React SPA, and REST API.
- `/api/*`: public `POST /api/login`, protected everything else.
- `/api/pi/*`: same-origin 1:1 mirror of the agent-server `/v1` contract, consumed by the `@appx-org/agent-chat-ui` SDK. Authorizes project-scoped session traffic against the caller's registered projects (by slug) and never exposes project-lifecycle routes.
- `/api/projects/:id/agent/*`: legacy project-scoped Pi session proxy (no remaining frontend consumer; retained pending cleanup).
- `/api/agent/*`: shared Pi provider auth, subscription login, model, and custom provider proxy.
- `<project-name>.<base-domain>`: reverse proxy to agent-built apps on assigned ports.

Auth uses a single-user password login with an `appx_session` cookie, bcrypt password hashing, rate-limited login, and 30-day sessions. TLS uses generated self-signed certificates by default or Let's Encrypt with Cloudflare DNS-01 when configured.

## Project Structure

```text
cmd/appx/main.go               # Entry point, CLI flags, dependency wiring
internal/
  agentserver/
    client.go                  # Client for agent-server project lifecycle (EnsureProject/DeleteProject)
  auth/
    auth.go                    # Auth struct, middleware, session cookie helpers
    store.go                   # Password + session CRUD, generic key-value settings
  db/
    db.go                      # SQLite connection and migration runner
    migrations/                # Numbered up/down SQL files
  egress/
    proxy.go                   # Go CONNECT proxy for agent egress control
    store.go                   # Allowlist and connection log persistence
  project/
    manager.go                 # Project lifecycle: register name with agent-server + appx record (no filesystem scaffolding)
    store.go                   # Project CRUD, port assignment, status transitions
  server/
    router.go                  # Route registration, SPA handler, subdomain proxy
    agent_proxy.go             # agent-server reverse proxies: /api/pi mirror, project-scoped, and global
    agent_handlers.go          # Pi provider auth and custom-provider handlers
    project_handlers.go        # Project CRUD and app health shape
    settings_handlers.go       # Account and app settings
    shell_handlers.go          # Local PTY shell endpoints
  terminal/
    local.go                   # Local PTY sessions for server/project terminals
  tls/
    selfsigned.go              # Self-signed certificate generation
web/src/
  api/client.ts                # Typed Appx API client
  pages/Project.tsx            # Agent (via @appx-org/agent-chat-ui) and terminal tabs
  components/Terminal.tsx      # xterm.js wrapper for local PTY shell
  pages/Dashboard.tsx          # Project list
  pages/Project.tsx            # Agent and terminal tabs
  pages/Settings.tsx           # Pi credentials, subscriptions, custom providers
deploy/
  appx.service                 # systemd unit for appx
  agent-server.service         # systemd unit for Pi agent-server
  bootstrap.sh                 # Full install/update flow
  system-setup.sh              # Users, directories, services
  tools-install.sh             # Go, Node.js, Pi, agent-server, Claude Code, uv
```

## Tech Stack

- Backend: Go 1.26, stdlib `net/http`, `database/sql` with `modernc.org/sqlite`.
- Frontend: React 19, Vite 8, TypeScript 5.9, react-router-dom 7.
- Agent runtime: Pi CLI plus Appx org `agent-server`.
- Streaming: Appx frontend consumes the agent-server HTTP/SSE session contract.
- Markdown: `marked` + `dompurify`.
- Deployment: Task, systemd, two OS users (`appx` and `appx-agent`) sharing the `projects` group.

## Conventions

### Go

- Every exported and unexported function, method, and type should have a useful doc comment. For handlers, document method/path, request/response shape, and auth requirements.
- Use dependency injection through parameters or config structs, not package globals.
- Keep handlers as `http.HandlerFunc` closures.
- Tests use in-memory SQLite (`:memory:`) and `httptest`.
- Wrap errors with context using `fmt.Errorf("context: %w", err)`.

### Frontend

- Every exported function and component should have a JSDoc comment.
- Keep endpoint calls in `web/src/api/client.ts`.
- Use the existing dark Appx design tokens from `web/src/index.css`; avoid one-off hardcoded colors unless a component already does so.
- Agent chat UI is provided by the `@appx-org/agent-chat-ui` package (linked via a `file:` dependency to the sibling `agent-chat` repo and consumed as TypeScript source). It talks to the `/api/pi` mirror; do not reintroduce a hand-written session store/reducer. Re-theme via the `--ac-*` token bridge in `web/src/index.css`.
- On 401, redirect to `/login`.

### Build

- Use Task targets from `Taskfile.yml`.
- `task build` builds the frontend, copies `web/dist` into `cmd/appx/web/dist`, then builds Go.
- The frontend is embedded in the Go binary via `go:embed`.

## Verification Loop

For any code change, run the narrowest useful checks while iterating and finish with:

```text
[ ] task build
[ ] task test
[ ] task lint
[ ] Manual verification for affected UI/API/deploy behavior
```

Add or update tests when behavior changes, especially for server routes, database migrations, store methods, and regression fixes.

## Deployment Notes

- `deploy/bootstrap.sh` is first-run setup.
- `task server:deploy` pulls, rebuilds, installs, restarts `agent-server` and `appx`, then verifies.
- The active agent service user is `appx-agent`.
- Pi credentials live under the agent service user's Pi storage and are managed through Settings.
- Provider traffic from Pi goes through the Appx egress proxy; loopback traffic to agent-server stays local.
- The HSTS header includes `includeSubDomains`. Do not point appx at a shared domain that also hosts HTTP services on subdomains.

## Documentation

After implementing behavior changes, update `README.md`, this file, and any current docs that describe the changed behavior. Historical planning documents may describe old phases, but active development docs should reflect the current Pi-only architecture.

## Superpowers

All brainstorm specs, implementation plans, and design documents generated by superpowers skills go in `.superpowers/specs/`. Use the naming convention `YYYY-MM-DD-<topic>-<type>.md`.
