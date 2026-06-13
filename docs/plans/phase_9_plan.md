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
| 3 | appx creates/supervises the outer container at startup | **appx** ✅ |
| 4 | **Productionize**: deploy is container-mode only (host mode removed), appx as a systemd service, secrets, docker access, soak | **appx** |
| 5 | Hardening | both |

---

## Stage 1 — Deployment handshake (appx side)

- [x] `internal/agentserver/client.go`: `EnsureProject(ctx, name string, dep Deployment) error` with `Deployment{Dev, Prod EnvTarget}` and `EnvTarget{Port int; URL string}`; marshal as the nested `deployment` object (omit empty)
- [x] `internal/project/store.go`: allocate a **DEV+PROD port pair** atomically; `Project` gains `DevPort` + `ProdPort` (or keep `AssignedPort` as prod, add `DevPort`); cap via `PublishedPortRangeEnd` (≈ 50 projects); `ErrNoPortAvailable` message updated
- [x] `internal/project/project.go`: `ValidateName` rejects names ending in `-dev` (reserved suffix, D2)
- [x] `internal/project/manager.go`:
  - `AgentRegistrar` interface carries the dev+prod deployment payload
  - `Manager` gains URL construction for prod + dev (`<name>` / `<name>-dev`) — needs `HTTPMode`/external-port knowledge threaded from `main.go`, not guessed
  - `Create` and `ReconcileAgentProjects` pass `{dev:{devPort, devURL}, prod:{prodPort, prodURL}}`
- [x] `internal/server/router.go`: subdomain dispatcher selects DEV vs PROD port from the `-dev` label (D5); WebSocket upgrade passes through
- [x] Tests: fake-registrar payload (create + reconcile, dev+prod), URL construction (prod/dev × https/http modes), pair allocation + cap boundary, `ValidateName` rejects `-dev`, **router: `<name>-dev`→DevPort, `<name>`→ProdPort, and a WebSocket upgrade proxies through**

**Acceptance (cross-repo, manual):** `task local` + agent-server `npm run dev` (Docker Desktop/podman as the agent's `APP_CONTAINER_RUNTIME`) → create project in UI → prompt agent to build+deploy → DEV app at `http://<name>-dev.127.0.0.1.sslip.io:8080`, PROD at `http://<name>.127.0.0.1.sslip.io:8080` → refine → DEV updates → promote → PROD updates. UI shows running state via `AppRunning`.

## Stage 2 — Containerised agent-server (no appx code changes)

Run the outer container manually (script lives in agent-server repo), point appx at it with `APPX_AGENT_SERVER_URL=http://127.0.0.1:4001` and the bearer token set. Re-run the Stage 1 acceptance flow. This isolates "nested environment breaks the flow" from "appx manages containers correctly".

## Stage 3 — appx supervises the outer container

### `internal/containerruntime`

- [x] Interface (sketch):
  ```go
  type Supervisor interface {
      // EnsureRunning creates the container if absent, starts it if stopped,
      // and waits until the readiness URL responds. Idempotent.
      EnsureRunning(ctx context.Context, spec ContainerSpec) error
      Status(ctx context.Context, name string) (ContainerStatus, error)
  }
  ```
  `ContainerSpec`: image, name, port publishes (API + app range), volumes (workspace + podman storage), env (`ANTHROPIC_API_KEY` etc. passthrough, `AGENT_SERVER_TOKEN`, `WORKSPACE_DIR=/workspace`, `APPX_TEMPLATE_DIR`, proxy vars), extra flags = the **proven Stage 0 set** transcribed verbatim from `run-outer.sh`: `--device /dev/net/tun`, `--security-opt seccomp=<tailored profile>`, `--security-opt apparmor=unconfined`, `--security-opt systempaths=unconfined`, plus `--memory`/`--cpus` and `--add-host=host.docker.internal:host-gateway`. **No `--privileged`, no `--cap-add SYS_ADMIN`, no `/dev/fuse`** (the spike's file-cap `newuidmap` + native overlay removed the need for those)
- [x] Docker CLI implementation: `docker inspect --format json` for state, `docker run -d` for create, `docker start` for stopped; readiness = poll agent-server `GET /` with timeout; structured errors (image missing vs daemon down vs unhealthy)
- [x] Fake implementation for unit tests

### Wiring (`cmd/appx/main.go`)

- [x] `APPX_AGENT_CONTAINER=true` → build spec from config (`APPX_AGENT_IMAGE`, ranges, data dirs), `EnsureRunning` **before** `ReconcileAgentProjects`; fail loudly with a remediation hint if docker is unavailable
- [x] `AGENT_SERVER_TOKEN` becomes **mandatory in container mode**: generate once, persist to `.appx-internals` (0600), pass to both the container env and the proxy clients. The API port is published (even if loopback-only); loopback is no longer a sufficient trust boundary on a multi-process host (OWASP A01/A07)
- [x] Mismatched config detection: if the existing container's spec (image tag, published range) differs from desired, log instructions (or `--recreate-agent-container` flag); **never silently recreate** — that kills running user apps

### Egress

- [x] Egress CONNECT proxy must be reachable from inside the container: listen on the docker bridge gateway (configurable bind addr) instead of loopback-only; container env sets `HTTPS_PROXY=http://host.docker.internal:9080`, `NODE_USE_ENV_PROXY=1` (mirrors the current `agent-server.service` setup)
- [x] Verify the egress internal listener (permission requests) path works from the container, or scope it explicitly out with a documented follow-up

### Deploy scripts

- [x] `deploy/system-setup.sh`: install docker (or podman) on the host; drop the `appx-agent` user/`agent-server.service` path for container mode; decide and document how appx invokes docker — recommend **rootless docker or host podman for the appx user** over adding appx to the `docker` group (docker group membership is root-equivalent; avoid if practical, document the trade-off if not) — **Resolved (see Stage 3 Decisions / Stage 4): outer must be rootful Docker (rootless-docker-outer breaks nested podman), so the `appx` user uses the `docker` group; tighter scoping is Stage 5 hardening.**
- [x] `deploy/tools-install.sh` / `bootstrap.sh`: pull/build the outer image (pin by tag/digest), remove host Node/agent-server install steps for container mode
- [x] Keep the systemd host-mode path working until container mode has run in production for a while (delete in a later cleanup phase) — **superseded: Stage 4 removes host mode from deploy entirely (local dev = manual, no systemd).**

### Tests (Stage 3)

- [x] Unit: supervisor logic against fake CLI runner (absent→create, stopped→start, running→noop, unhealthy→error), spec construction from config, token generation/persistence
- [x] `scripts/smoke-deploy.sh` (Linux, CI nightly): build/pull outer image → start appx in container mode (`--http`) → `POST /api/projects` → assert agent-server inside the container has the project with correct dev+prod port metadata → build **the seeded template** once and run DEV+PROD instances via `docker exec` running the deploy skill's literal commands (**deliberately no LLM** — deterministic infra validation) → `curl` both `http://<name>-dev.127.0.0.1.sslip.io:<port>` and `http://<name>.127.0.0.1.sslip.io:<port>` through the appx proxy → redeploy a modified DEV → assert DEV changed while PROD is unchanged → promote → assert PROD changed → restart outer container → assert registry intact and UI shows apps stopped
- [x] Router tests: assert DEV/PROD port selection from the `-dev` label and WebSocket upgrade passthrough (the proxy target is still `127.0.0.1:<port>`; only the port choice is new)

**Acceptance:** fresh Linux VM → bootstrap → appx boots, container exists and is healthy → full UI e2e; appx restart and outer-container restart both recover cleanly.

---

## Stage 3 — Results (appx supervises the outer container)

**Date:** 2026-06-12
**Status:** COMPLETE — `scripts/smoke-deploy.sh` exits 0 (38/38) on the same
Ubuntu 26.04 / kernel 7.0 Hetzner VM as Stages 0–2; agent-server's Stage 0
`container/smoke.sh` (11/11) and Stage 2 `scripts/container-smoke.sh` (31/31)
remain green (baseline re-run before this work). All Go unit tests pass,
including the supervisor state machine, spec construction, token gen/persist, and
the router DEV/PROD + WebSocket-passthrough tests (the last landed in Stage 1).
`docker inspect` on the **appx-created** container confirms `Privileged=false`,
`CapAdd=[]`, no `no-new-privileges`, no `/dev/fuse`, publishes loopback-only.

### What landed (appx side)

- **`internal/containerruntime`** — `Supervisor` interface + a docker-CLI
  implementation (`DockerSupervisor`) + a fake `CommandRunner` at the seam.
  `EnsureRunning` is the idempotent absent→create / stopped→start / running→noop
  state machine, then polls agent-server `GET /` until healthy. Structured errors
  (`ErrDaemonUnavailable`, `ErrImageMissing`, `ErrUnhealthy`, `SpecDriftError`).
  `ContainerSpec.RunArgs()` transcribes the proven run-outer.sh flag set
  **verbatim** (deletion-tested by `TestRunArgs_VerbatimSecurityFlagSet`, which
  also asserts the forbidden flags are absent). `Recreate` is the explicit
  operator path; drift **never** auto-recreates.
- **Wiring (`cmd/appx/main.go`)** — `APPX_AGENT_CONTAINER=true` builds the spec
  from config and `EnsureRunning`s the container *before* `ReconcileAgentProjects`,
  failing loudly with per-class remediation hints. Token mandatory in container
  mode: generated once, persisted `0600` to `.appx-internals/agent-server-token`,
  injected into both the container env and the proxy clients.
  `--recreate-agent-container` / `APPX_RECREATE_AGENT_CONTAINER` for explicit drift
  remediation.
- **Egress across the bridge** — in container mode the CONNECT proxy + internal
  listener bind on the docker bridge gateway (auto-detected via
  `docker network inspect bridge`, override `APPX_EGRESS_BIND`); the container
  reaches them via `--add-host=host.docker.internal:host-gateway` and
  `HTTPS_PROXY=http://host.docker.internal:9080` + `NODE_USE_ENV_PROXY=1`.
- **Deploy scripts** — `system-setup.sh`/`tools-install.sh`/`bootstrap.sh` gained
  a container-mode branch (skip the `appx-agent` user + `agent-server.service`,
  install the seccomp profile to `/etc/appx/`, build/pull the outer image, set up
  docker access for the appx user). The systemd host-mode path is preserved until
  container mode soaks in prod.
- **`scripts/smoke-deploy.sh`** — the deterministic NO-LLM cross-service gate
  (sibling of `container-smoke.sh`) exercising the **appx proxy**.

### Decisions (industry-standard option + why)

- **docker-CLI vs Docker Go SDK → CLI (D3).** Industry-standard for a single
  container's lifecycle is debatable, but the SDK's dependency tree isn't worth
  one container; the CLI also works against docker **or** podman on the host for
  free. Behind a `CommandRunner` seam so unit tests need no daemon.
- **Docker invocation privilege → outer = rootful host Docker (decided by the
  spike); the `appx` user reaches it via the `docker` group.** The runtime choice
  is *not* open: SPIKE-FINDINGS T2 validated outer = rootful Docker + inner =
  rootless podman, and rootless-docker-outer is a non-starter (it reintroduces the
  nested subuid-exhaustion that killed rootless-podman-outer, and breaks the
  rootful-bridge egress auto-detect). So the only question is authorization, and
  the answer is the `docker` group — proven in Stages 2–3. Residual risk is stated
  honestly: docker-group is **root-equivalent** (`docker run -v /:/host` owns the
  box), accepted here because it's a dedicated single-purpose box + dedicated
  `appx` user; scoping it tighter (docker-socket proxy / narrow sudoers) is Stage 5
  hardening. (Earlier drafts floated "prefer rootless docker" — that was wrong for
  this nested workload and has been dropped.)
- **Egress bind → docker bridge gateway, not `0.0.0.0`.** The standard options are
  (a) `0.0.0.0` (simple, over-exposed) or (b) the specific bridge gateway IP
  (reachable from the container, not from external interfaces). We chose (b): bind
  the proxy on `172.17.0.1` (auto-detected) so only bridge-network containers can
  reach it, and the allowlist + DNS-rebinding check still apply.

### Deviations / findings

- **HTTPS_PROXY is honoured by podman, not just Node — registry pulls broke.**
  Injecting `HTTPS_PROXY` container-wide (required so agent-server's LLM traffic
  traverses appx's egress proxy) also routed `podman pull` of base images through
  the LLM allowlist, which rejected `registry-1.docker.io` with 403 (surfaced
  immediately by the deterministic smoke — exactly the "egress crossing is the
  highest-risk item" trap). **Fix:** the container-mode default `NO_PROXY`
  bypasses common image registries (`.docker.io`, `.docker.com`, `ghcr.io`,
  `quay.io`, `gcr.io`, `registry.k8s.io`) so image pulls go direct while the
  secret-bearing LLM endpoints (not listed) still traverse the proxy. Overridable
  via `APPX_AGENT_NO_PROXY`. Trade-off: registry pulls are not egress-controlled
  (acceptable — the outer container is the trusted zone; inner apps get no proxy
  env and no creds).
- **`appRunning` (TCP-dial health) gives a false-positive after an outer
  restart.** Loopback (`127.0.0.1`) publishes use docker's userland `docker-proxy`,
  which accepts the host-side TCP connection even when the inner backend is down,
  so the dial-based health check reports `appRunning=true` while the apps are
  actually stopped (`docker restart` leaves inner podman containers `created`). The
  smoke asserts the inner-container ground truth (`podman inspect` state) as the
  required check and records `appRunning` as `[observe]`. **Follow-up (Stage 5):**
  the UI "stopped"/degraded signal needs an HTTP-level probe, not a bare TCP dial.
- **Egress permission-request path (internal listener, 9081) — scoped out.** No
  current agent-server caller posts to `/egress/request`; appx still binds the
  internal listener on the bridge gateway in container mode so it is reachable if
  wired later, but the request path from inside the container is **not** verified
  here. Documented follow-up. The CONNECT proxy (9080) path *is* verified: the
  smoke proves an in-container CONNECT to a non-allowlisted host fails closed
  (403) across the bridge; the full LLM-through-proxy success is the manual e2e
  acceptance step (needs a real key).
- **`systempaths=unconfined` is not a `SecurityOpt` in `docker inspect`** — it
  manifests as cleared `MaskedPaths`/`ReadonlyPaths`. The smoke asserts
  `MaskedPaths == []` rather than grepping `SecurityOpt`.
- **seccomp profile duplicated into appx** (`deploy/builder-container/seccomp-builder.json`).
  appx needs the file on the host at `docker run` time, so it cannot live only in
  the image. It is a verbatim copy of agent-server's canonical profile; re-copy if
  `gen-seccomp.sh` changes there (drift note in that dir's README).
- **Bedrock API key set via the agent-client Settings UI does not work — an
  upstream Pi gap, not appx/agent-server/the container.** `pi` sdk.ts passes the
  stored credential as `options.apiKey`, but `amazon-bedrock.ts` authenticates only
  from `options.bearerToken` / `AWS_BEARER_TOKEN_BEDROCK`; nothing maps
  `apiKey → bearerToken`, so the AWS SDK falls to its default chain → "Could not
  load credentials from any providers." Reproduces in host mode too. **Workaround:**
  supply `AWS_BEARER_TOKEN_BEDROCK` + `AWS_REGION` as env, forwarded into the
  container via `APPX_AGENT_ENV_PASSTHROUGH`. Proper fix is upstream Pi (Stage 5).
- **Non-default provider endpoints must be in the egress allowlist (fails closed).**
  Added `bedrock-runtime.*.amazonaws.com:443` to `egress.DefaultAllowlist` with
  scoped DNS-wildcard matching in `IsAllowed` (`*` = a single label, like a
  wildcard cert; label counts must match so it can't span a dot). Any other
  provider (Vertex/Azure/self-hosted) similarly needs an allowlist entry.
- **`APPX_AGENT_ENV_PASSTHROUGH`** (new) forwards extra env var *names* by value
  into the container (default `ANTHROPIC_API_KEY`); docker forwards them from
  appx's own process env if set, omits otherwise. Injected at container **create**
  time, so changing them requires a recreate (`--recreate-agent-container`).

### Verification on this VM

1. Baseline: agent-server `container-smoke.sh` 31/31 green (re-run before work).
2. `go test ./...` green (supervisor fake state machine, spec/`RunArgs` verbatim
   flag set, token gen/persist, bridge-gateway parse, router DEV/PROD + WS).
3. `scripts/smoke-deploy.sh` 38/38 green: appx (container mode) creates a healthy
   outer container → create project → agent-server inside has dev+prod metadata →
   build seeded template once + run DEV+PROD → curl both **through the appx proxy**
   → redeploy DEV (DEV changes, PROD unchanged) → promote (PROD changes) → outer
   restart (registry intact, apps stopped) → appx restart (no recreate).
4. Manual LLM-through-proxy e2e (create→prompt→deploy→view→refine→promote against
   DEV then PROD URLs) — requires a real key; not part of the deterministic gate.

---

## Stage 4 — Productionize (deploy is container-mode only; appx as a systemd service)

Stage 3 proved appx supervises the outer container when **hand-run with env
vars** (`./appx` with `APPX_AGENT_CONTAINER=true …`). Stage 4 makes that *the*
production deployment — appx running as the `appx` systemd unit, surviving
reboots, secrets never on the command line — and **removes host mode from the
deploy path entirely**.

**Decision (2026-06-12): drop host mode from `deploy/`.** Container mode
supersedes it, so the deploy scripts + systemd become container-mode only: no
`appx-agent` user, no `agent-server.service`, no host Node/Pi/agent-server
install, no mode toggle. Local development does **not** use these scripts — a
developer runs agent-server by hand (e.g. `npm run dev`) and `appx --http` with
`APPX_AGENT_SERVER_URL`, no systemd. The appx **binary** keeps its host-mode
runtime path (`APPX_AGENT_SERVER_URL`) for that local/macOS use; only the
deployment machinery is removed.

What's needed (none of it changes the Stage 3 container/security model):

- [ ] **Strip host mode from deploy** — `system-setup.sh`: remove the `appx-agent`
  user/group + `/home/appx-agent` dirs + the `agent-server.service` install/enable
  and the `APPX_AGENT_CONTAINER` branch (container is the only path); **delete**
  `deploy/agent-server.service`; disable+remove a stale `agent-server.service` on
  upgrade. `tools-install.sh`: drop the host Pi/agent-server install; **build the
  outer image from the agent-server checkout** (`docker build -f
  <agent-server>/container/Dockerfile`), tagged `APPX_AGENT_IMAGE`, pinned by
  **tag** (registry publish + deploy-by-digest is a deferred *Potential
  improvement*). `bootstrap.sh`: no mode
  prompt; always write the container-mode `appx.env`; start only `appx`.
  `verify-installation.sh`: container-mode checks only.
- [ ] **systemd ordering** — in `appx.service` directly (no host-mode unit to keep
  clean now): `Wants=docker.service` + `After=docker.service network.target`. On
  reboot docker starts → appx's idempotent `EnsureRunning` re-attaches (no recreate).
- [ ] **Container restart policy + supervision model** — add `--restart
  unless-stopped` to `ContainerSpec.RunArgs` so the **Docker daemon** resurrects
  the outer container on crash *and* reboot, independent of appx. Closes a real
  Stage 3 gap: appx runs `EnsureRunning` **only at startup** (not a continuous
  watchdog) and the spec set no restart policy, so a `builder-outer` crash *while
  appx keeps running* was not auto-healed. Model to document: **daemon keeps the
  container process alive** (`--restart`); **appx ensures it exists / is correct /
  is healthy** at startup; **`appx.service Restart=on-failure`** covers appx
  itself. A periodic re-`EnsureRunning`/health loop is a Stage 5 call (restart
  policy + the Stage 5 degraded banner may suffice). Verify it composes with the
  entrypoint's stale-`XDG_RUNTIME_DIR` wipe on a daemon-driven restart.
- [ ] **Secrets to the service env** (appx forwards them by name into the
  container; never baked): `ANTHROPIC_API_KEY` and/or `AWS_BEARER_TOKEN_BEDROCK` +
  `AWS_REGION` in `/etc/appx/appx.env` (0600) or an optional
  `EnvironmentFile=-/etc/appx/secrets.env` (`root:root 0600`), plus
  `APPX_AGENT_ENV_PASSTHROUGH` listing the extra names. `AGENT_SERVER_TOKEN` is
  auto-generated + persisted 0600 by appx (no manual step).
- [ ] **appx.env** — always container mode: `APPX_AGENT_CONTAINER=true`,
  `APPX_AGENT_IMAGE=<pinned tag/digest>`,
  `APPX_AGENT_SECCOMP=/etc/appx/seccomp-builder.json`. `system-setup.sh` installs
  the seccomp profile to `/etc/appx/` and sets up docker access for the appx user.
- [ ] **`appx` service user → Docker access** — *runtime is decided + validated
  (SPIKE-FINDINGS T2): outer = **rootful host Docker**, inner = rootless podman —
  not open.* (rootless-docker-outer would reintroduce the nested subuid-exhaustion
  that killed rootless-podman-outer and break the rootful-bridge egress
  auto-detect, so it's not an option.) Only the authorization is to wire:
  **Decision — add `appx` to the `docker` group** (proven in Stages 2–3; under
  `User=appx` the service inherits it after `usermod` + `daemon-reload` + restart).
  Document the residual risk: docker-group is **root-equivalent**, mitigated by the
  dedicated single-purpose box + dedicated `appx` user. Scoping it tighter
  (docker-socket proxy / narrow sudoers) is **Stage 5 hardening**.
- [ ] **443 without root** — already handled (`AmbientCapabilities=CAP_NET_BIND_SERVICE`
  in `appx.service`); the manual `setcap` is only for hand-running the binary.
- [ ] **start/restart semantics** — `Type=simple` (systemd doesn't wait on the
  EnsureRunning health poll). On EnsureRunning failure appx `log.Fatal`s → exits →
  `Restart=on-failure`; pick a `RestartSec` large enough that a missing image /
  down daemon doesn't hot-loop. First boot: `tools-install.sh` builds/pulls the
  pinned image before `appx.service` starts.
- [ ] **Docs** — `README`/`.env.example`: local dev = manual no-systemd flow
  (agent-server by hand + `appx --http` with `APPX_AGENT_SERVER_URL`); production
  = `bootstrap.sh` (container only).
- [ ] **Soak**: reboot recovery, outer-container restart recovery, secrets reach
  the container, full UI e2e over public HTTPS.

**Acceptance:** fresh box → `bootstrap.sh` → reboot → the `appx` systemd unit is
active, the outer container is healthy, and the full create → prompt → deploy →
promote flow works over the public HTTPS URL with provider creds supplied only
via the service env. No `appx-agent` user, no `agent-server.service`, no host
Node/Pi/agent-server on the box. Local dev still works by hand.

## Stage 5 — Hardening (appx items)

- [ ] Resource limits on the outer container (`--memory`, `--cpus`) via config with sane defaults
- [ ] **App health via an HTTP probe, not a bare TCP dial** — the Stage 3 finding: docker's userland `docker-proxy` accepts the loopback connection even when the inner backend is down, so `appRunning` (a TCP dial) false-positives after an outer restart. Probe HTTP (or detect inner-container state) so the UI "stopped"/degraded signal is honest.
- [ ] Dashboard surfacing of builder-container health (degraded banner when `Status` is unhealthy) — small UI addition, big debuggability win
- [ ] **Upstream Pi: Bedrock credential mapping** — map a stored `amazon-bedrock` api_key credential to `bearerToken` (or accept `options.apiKey` in the provider) so the Settings-UI key works without the `AWS_BEARER_TOKEN_BEDROCK` env workaround (Stage 3 finding #1)
- [ ] **Scope the `appx` user's Docker access (remove the root-equivalence).** Stage 4 puts `appx` in the `docker` group — convenient but root-equivalent (`docker run -v /:/host`). Replace with a least-privilege path: a **docker-socket proxy** (e.g. `tecnativa/docker-socket-proxy`) that exposes only the calls appx needs (inspect/run/start/stop/rm + `network inspect` for the egress gateway, scoped to the one container), or a narrow **sudoers** rule for the specific `docker` invocations. After this, appx is genuinely unprivileged + `CAP_NET_BIND_SERVICE`, and a bug in appx no longer implies host root.
- [ ] Security review pass (precedent: `docs/security/*de-docker*`): token handling, port exposure, docker invocation privilege (see the socket-proxy item above), egress from inner containers
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

## Potential improvements (deferred — not committed to a stage)

### Publish the outer image (registry + pinned digest)

Stage 4 builds `builder-outer` from the agent-server checkout **on the box** (prod
carries the source). A later improvement: build it in **CI**, push to a registry,
and set `APPX_AGENT_IMAGE=<registry>/builder-outer@sha256:…` so deploy **pulls a
pinned digest** instead of building — removing the agent-server source + build
step from prod and making the running image immutable/reproducible/auditable.
`tools-install.sh` already takes the pull path when `APPX_AGENT_IMAGE` is a
registry ref, so this is mostly a CI/registry task (tagging + signing, e.g. cosign,
and registry ownership), not appx code. Deferred while the image + base recipe are
still moving. (Tracked identically in the agent-server plan's *Potential
improvements*.)

## Risks

1. **Port-range publish overhead** — capped at 100; in-container reverse proxy is the pre-designed escalation (D1).
2. **Egress proxy reachability from the container** — explicitly scoped (Stage 3); classic "works in dev" trap since host-mode dev never crosses the bridge.
3. **Container recreate destroys running apps** — mitigated by never auto-recreating on spec drift; volumes preserve workspace + podman storage regardless.
4. **Docker invocation privilege** — **resolved:** outer = rootful host Docker (spike T2); the `appx` user reaches it via the root-equivalent `docker` group, accepted on a dedicated single-purpose box; tighter scoping (socket proxy / sudoers) is Stage 5 hardening. (Rootless docker is *not* viable for the nested-podman outer.)
5. **macOS/Linux divergence** — accepted and bounded: macOS = flow/prompt dev (host mode), Linux = container truth (CI + VM).
6. **Two ports/project → ~50-project ceiling** under the 100-port publish cap; the in-container reverse proxy (D1 escalation) lifts it if needed.
7. **Subdomain proxy now selects DEV/PROD port and must pass WebSockets** (generic, for user apps) — covered by router tests; the `-dev` reserved-suffix guard (D2) prevents name/route ambiguity.
