# Phase 9 Plan: Containerised Apps — Builder Container + App Deployment

**Date:** 2026-06-11
**Status:** Draft
**Scope:** Deployment metadata handshake (dev + prod) with agent-server, paired port allocation + DEV/PROD subdomain routing, outer-container supervision from appx, port-range publishing, egress wiring, deploy script rewrite
**Prerequisites:** Pi migration complete (agent-server owns projects; appx is the control plane)
**Canonical architecture:** agent-server repo, `docs/architecture/important/builder-container-architecture.md`
**Sibling plan:** agent-server repo, `docs/plans/builder-containers-plan.md` (metadata contract, deploy skill, outer image)

---

## Goal

End-to-end containerised app flow:

1. **appx creates the agent-server outer docker container at startup** (one unprivileged container holding agent-server + rootless podman).
2. User creates a project in the appx UI; appx allocates **two ports** (DEV + PROD) and registers the project with agent-server **including both ports and their public URLs**.
3. User prompts the builder agent; the agent builds one image and runs it as **two inner podman containers** (DEV + PROD), each publishing its reserved port.
4. appx's subdomain proxy exposes both: `<name>.<domain>` → PROD port, `<name>-dev.<domain>` → DEV port.
5. The user iterates against the DEV URL (refinements rebuild + redeploy DEV); **promote** rebuilds PROD, visible at the PROD URL.

### What already exists (foundation to extend)

- Port allocation: `project.Store.Create` atomically assigns from 10000–10999 (`internal/project/store.go`) — **extend to allocate a DEV+PROD pair** (Stage 1).
- Subdomain proxy to `127.0.0.1:<AssignedPort>` with auth wrapping (`internal/server/router.go`) — **extend to select DEV vs PROD port from the subdomain label** (D5).
- agent-server registration + startup reconcile (`internal/agentserver/client.go`, `Manager.ReconcileAgentProjects`) — reused; payload gains dev+prod.
- Health checker → `AppRunning` in the UI (`internal/project/health.go`) — reused as-is (now checks both ports).
- Bearer-token seam (`AGENT_SERVER_TOKEN`) — reused as-is.

The architecture's key payoff for appx: **the subdomain proxy's *target* is unchanged across all stages** — the outer container publishes app ports on host loopback, so `127.0.0.1:<port>` means the same thing whether agent-server runs on the host (Stage 1) or inside the container (Stages 2+). The proxy gains **one** small change: choosing the DEV vs PROD port from the subdomain label (D5).

---

## Design decisions

### D1 — Publish the app port range on the outer container at create time

`docker run -p 127.0.0.1:4001:4001 -p 127.0.0.1:10000-10099:10000-10099 ...`

- Docker cannot add port mappings to a running container, so the range is fixed at container create.
- **Cap the published (and allocated) range at 100 ports.** Docker spawns one `docker-proxy` process per published port; 100 is plenty for a single admin. **Each project consumes a pair (DEV + PROD), so 100 ports ≈ 50 projects.** Keep the DB range constants; cap allocation via `PublishedPortRangeEnd = 10099` so existing rows above the cap still resolve.
- Loopback-only publish (`127.0.0.1:`) — apps are reachable solely through appx's authenticated proxy (OWASP A01: no direct unauthenticated exposure).
- Rejected: `--network=host` (discards the network isolation the architecture exists for). Escalation if the range/pair model ever hurts: a single in-container reverse proxy on one published port with appx sending the target port via header — pre-designed, not built now; appx routing is already centralised in one handler so it's a clean swap (and would also lift the ~50-project ceiling).

### D2 — Deployment metadata (dev + prod) flows through `EnsureProject`

appx sends `{name, deployment: {dev: {port, url}, prod: {port, url}}}` on create **and** on startup reconcile (agent-server treats same-name re-POST as a metadata update, healing drift for pre-existing projects). URL construction (appx already knows scheme/host/port):
- **prod:** `https://<name>.<baseDomain>` (`http://<name>.<baseDomain>:<port>` in `--http` dev mode)
- **dev:**  `https://<name>-dev.<baseDomain>` (`http://<name>-dev.<baseDomain>:<port>` in `--http` dev mode)

`-dev` is a **reserved suffix**: reject project names ending in `-dev` at creation so `<name>-dev` is unambiguously project `<name>`'s dev env (see D5).

### D5 — Subdomain routing: DEV/PROD selection + WebSocket passthrough

The subdomain dispatcher (`router.go`) parses the label:
- `<name>-dev.<domain>` → the project's **DEV** port
- `<name>.<domain>`     → the project's **PROD** port

It still proxies to `127.0.0.1:<port>` — only the port *choice* is new. The
`-dev` reserved-suffix guard (D2) prevents name/route ambiguity (a project
`foo-dev` can't exist to collide with `foo`'s dev URL).

**WebSocket passthrough is a generic requirement, not an HMR one.** The dev=prod
model (agent-server plan D6) drops the hot-reload dev server, so the build/refine
loop does *not* depend on WebSockets. But user apps (chat, live dashboards,
realtime anything) do, so the subdomain proxy must forward `Connection: Upgrade`
/ `ws://` correctly regardless. Go's `httputil.ReverseProxy` has supported this
since 1.12 — **verify with a test**, don't assume; it's table stakes for a
general app proxy.

### D3 — Outer container management: shell out to the `docker` CLI behind an interface

New `internal/containerruntime` package: small interface + docker-CLI implementation (`--format json` parsing) + fake for tests — same fake-at-the-seam pattern as `project.AgentRegistrar`. Rationale: one container's lifecycle doesn't justify the Docker Go SDK's dependency tree, and CLI compatibility means the host runtime can be docker **or** podman for free.

### D4 — Container mode is opt-in config until Stage 3 lands

`APPX_AGENT_CONTAINER=true` switches appx from "expect agent-server at `APPX_AGENT_SERVER_URL`" to "ensure the outer container is running, then use it". Host mode remains for local dev (macOS cannot run the nested setup natively) and as a fallback.

---

## Staging (shared with agent-server plan)

| Stage | What | Repo focus |
|---|---|---|
| 0 | Nested rootless podman spike (timeboxed) | agent-server |
| 1 | Full user flow with agent-server **on host** | both |
| 2 | agent-server inside the outer container, started manually | agent-server |
| 3 | appx creates/supervises the outer container at startup | **appx** |
| 4 | Hardening | both |

---

## Stage 1 — Deployment handshake (appx side)

- [ ] `internal/agentserver/client.go`: `EnsureProject(ctx, name string, dep Deployment) error` with `Deployment{Dev, Prod EnvTarget}` and `EnvTarget{Port int; URL string}`; marshal as the nested `deployment` object (omit empty)
- [ ] `internal/project/store.go`: allocate a **DEV+PROD port pair** atomically; `Project` gains `DevPort` + `ProdPort` (or keep `AssignedPort` as prod, add `DevPort`); cap via `PublishedPortRangeEnd` (≈ 50 projects); `ErrNoPortAvailable` message updated
- [ ] `internal/project/project.go`: `ValidateName` rejects names ending in `-dev` (reserved suffix, D2)
- [ ] `internal/project/manager.go`:
  - `AgentRegistrar` interface carries the dev+prod deployment payload
  - `Manager` gains URL construction for prod + dev (`<name>` / `<name>-dev`) — needs `HTTPMode`/external-port knowledge threaded from `main.go`, not guessed
  - `Create` and `ReconcileAgentProjects` pass `{dev:{devPort, devURL}, prod:{prodPort, prodURL}}`
- [ ] `internal/server/router.go`: subdomain dispatcher selects DEV vs PROD port from the `-dev` label (D5); WebSocket upgrade passes through
- [ ] Tests: fake-registrar payload (create + reconcile, dev+prod), URL construction (prod/dev × https/http modes), pair allocation + cap boundary, `ValidateName` rejects `-dev`, **router: `<name>-dev`→DevPort, `<name>`→ProdPort, and a WebSocket upgrade proxies through**

**Acceptance (cross-repo, manual):** `task local` + agent-server `npm run dev` (Docker Desktop/podman as the agent's `APP_CONTAINER_RUNTIME`) → create project in UI → prompt agent to build+deploy → DEV app at `http://<name>-dev.127.0.0.1.sslip.io:8080`, PROD at `http://<name>.127.0.0.1.sslip.io:8080` → refine → DEV updates → promote → PROD updates. UI shows running state via `AppRunning`.

## Stage 2 — Containerised agent-server (no appx code changes)

Run the outer container manually (script lives in agent-server repo), point appx at it with `APPX_AGENT_SERVER_URL=http://127.0.0.1:4001` and the bearer token set. Re-run the Stage 1 acceptance flow. This isolates "nested environment breaks the flow" from "appx manages containers correctly".

## Stage 3 — appx supervises the outer container

### `internal/containerruntime`

- [ ] Interface (sketch):
  ```go
  type Supervisor interface {
      // EnsureRunning creates the container if absent, starts it if stopped,
      // and waits until the readiness URL responds. Idempotent.
      EnsureRunning(ctx context.Context, spec ContainerSpec) error
      Status(ctx context.Context, name string) (ContainerStatus, error)
  }
  ```
  `ContainerSpec`: image, name, port publishes (API + app range), volumes (workspace + podman storage), env (`ANTHROPIC_API_KEY` etc. passthrough, `AGENT_SERVER_TOKEN`, `WORKSPACE_DIR=/workspace`, `APPX_TEMPLATE_DIR`, proxy vars), extra flags = the **proven Stage 0 set** transcribed verbatim from `run-outer.sh`: `--device /dev/net/tun`, `--security-opt seccomp=<tailored profile>`, `--security-opt apparmor=unconfined`, `--security-opt systempaths=unconfined`, plus `--memory`/`--cpus` and `--add-host=host.docker.internal:host-gateway`. **No `--privileged`, no `--cap-add SYS_ADMIN`, no `/dev/fuse`** (the spike's file-cap `newuidmap` + native overlay removed the need for those)
- [ ] Docker CLI implementation: `docker inspect --format json` for state, `docker run -d` for create, `docker start` for stopped; readiness = poll agent-server `GET /` with timeout; structured errors (image missing vs daemon down vs unhealthy)
- [ ] Fake implementation for unit tests

### Wiring (`cmd/appx/main.go`)

- [ ] `APPX_AGENT_CONTAINER=true` → build spec from config (`APPX_AGENT_IMAGE`, ranges, data dirs), `EnsureRunning` **before** `ReconcileAgentProjects`; fail loudly with a remediation hint if docker is unavailable
- [ ] `AGENT_SERVER_TOKEN` becomes **mandatory in container mode**: generate once, persist to `.appx-internals` (0600), pass to both the container env and the proxy clients. The API port is published (even if loopback-only); loopback is no longer a sufficient trust boundary on a multi-process host (OWASP A01/A07)
- [ ] Mismatched config detection: if the existing container's spec (image tag, published range) differs from desired, log instructions (or `--recreate-agent-container` flag); **never silently recreate** — that kills running user apps

### Egress

- [ ] Egress CONNECT proxy must be reachable from inside the container: listen on the docker bridge gateway (configurable bind addr) instead of loopback-only; container env sets `HTTPS_PROXY=http://host.docker.internal:9080`, `NODE_USE_ENV_PROXY=1` (mirrors the current `agent-server.service` setup)
- [ ] Verify the egress internal listener (permission requests) path works from the container, or scope it explicitly out with a documented follow-up

### Deploy scripts

- [ ] `deploy/system-setup.sh`: install docker (or podman) on the host; drop the `appx-agent` user/`agent-server.service` path for container mode; decide and document how appx invokes docker — recommend **rootless docker or host podman for the appx user** over adding appx to the `docker` group (docker group membership is root-equivalent; avoid if practical, document the trade-off if not)
- [ ] `deploy/tools-install.sh` / `bootstrap.sh`: pull/build the outer image (pin by tag/digest), remove host Node/agent-server install steps for container mode
- [ ] Keep the systemd host-mode path working until container mode has run in production for a while (delete in a later cleanup phase)

### Tests (Stage 3)

- [ ] Unit: supervisor logic against fake CLI runner (absent→create, stopped→start, running→noop, unhealthy→error), spec construction from config, token generation/persistence
- [ ] `scripts/smoke-deploy.sh` (Linux, CI nightly): build/pull outer image → start appx in container mode (`--http`) → `POST /api/projects` → assert agent-server inside the container has the project with correct dev+prod port metadata → build **the seeded template** once and run DEV+PROD instances via `docker exec` running the deploy skill's literal commands (**deliberately no LLM** — deterministic infra validation) → `curl` both `http://<name>-dev.127.0.0.1.sslip.io:<port>` and `http://<name>.127.0.0.1.sslip.io:<port>` through the appx proxy → redeploy a modified DEV → assert DEV changed while PROD is unchanged → promote → assert PROD changed → restart outer container → assert registry intact and UI shows apps stopped
- [ ] Router tests: assert DEV/PROD port selection from the `-dev` label and WebSocket upgrade passthrough (the proxy target is still `127.0.0.1:<port>`; only the port choice is new)

**Acceptance:** fresh Linux VM → bootstrap → appx boots, container exists and is healthy → full UI e2e; appx restart and outer-container restart both recover cleanly.

## Stage 4 — Hardening (appx items)

- [ ] Resource limits on the outer container (`--memory`, `--cpus`) via config with sane defaults
- [ ] Dashboard surfacing of builder-container health (degraded banner when `Status` is unhealthy) — small UI addition, big debuggability win
- [ ] Security review pass (precedent: `docs/security/*de-docker*`): token handling, port exposure, docker invocation privilege, egress from inner containers
- [ ] Optional golden-prompt LLM e2e (manual, pre-release) — owned jointly with agent-server plan

---

## Testing strategy summary

| Layer | What | Gate |
|---|---|---|
| Go unit tests (fakes at seams) | client payloads, manager threading, URL/port logic, supervisor state machine | every PR |
| `scripts/smoke-deploy.sh` | full cross-service chain, no LLM (skill commands run literally) | Linux CI nightly + before merge of Stage 3 |
| Router/proxy `httptest` | DEV/PROD port selection from the `-dev` label + WebSocket upgrade passthrough | every PR |
| Golden-prompt LLM run | prompt/skill quality | manual, pre-release |

Principle: every networking boundary is exercised by a real connection at exactly one layer and faked everywhere else. No mocked-docker tests pretending to verify port forwarding; no LLM in the loop for infrastructure verification.

## Risks

1. **Port-range publish overhead** — capped at 100; in-container reverse proxy is the pre-designed escalation (D1).
2. **Egress proxy reachability from the container** — explicitly scoped (Stage 3); classic "works in dev" trap since host-mode dev never crosses the bridge.
3. **Container recreate destroys running apps** — mitigated by never auto-recreating on spec drift; volumes preserve workspace + podman storage regardless.
4. **Docker invocation privilege** — docker-group ≈ root; prefer rootless docker/podman for the appx user, decide during Stage 3 deploy-script work.
5. **macOS/Linux divergence** — accepted and bounded: macOS = flow/prompt dev (host mode), Linux = container truth (CI + VM).
6. **Two ports/project → ~50-project ceiling** under the 100-port publish cap; the in-container reverse proxy (D1 escalation) lifts it if needed.
7. **Subdomain proxy now selects DEV/PROD port and must pass WebSockets** (generic, for user apps) — covered by router tests; the `-dev` reserved-suffix guard (D2) prevents name/route ambiguity.
