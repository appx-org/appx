# Phase 6 Plan: Installer + Security Hardening + Bearer Token Auth

**Date:** 2026-04-07
**Status:** Draft
**Scope:** One-command installer (`scripts/install.sh`), OS user separation with iptables egress enforcement, bearer token auth for API/native clients, production security hardening
**Analysis:** See [`docs/analysis/refactors/de-docker-refactor.md`](../analysis/refactors/de-docker-refactor.md) Q5 for the OS user/iptables design rationale. See [`docs/architecture/egress_iptables.md`](../architecture/egress_iptables.md) for the exact iptables rules.

---

## Vision

Phase 5 built the single-OpenCode architecture with a Go CONNECT egress proxy. The proxy is Layer 1 (cooperative enforcement via `HTTPS_PROXY`). Phase 6 completes the security model by wiring up Layer 2 (kernel-level iptables enforcement) via the installer, and adds bearer token auth so API clients and the future native mobile app (Phase 8) can authenticate without browser cookies.

The installer is also what Phase 7 uses to provision each dedicated user server. `install.sh` is literally the provisioning script for the hosted service — everything it does on a fresh VPS must work end-to-end without human intervention.

---

## Goals

1. **`scripts/install.sh`** — one-command bootstrap for a fresh Debian/Ubuntu VPS: installs binaries, creates OS users, configures systemd services, applies iptables egress rules, generates initial password
2. **iptables egress enforcement** — block all outbound from the `opencode` UID except to localhost; persisted across reboots; closes the HTTPS_PROXY bypass gap from Phase 5
3. **Bearer token auth** — `POST /api/login` returns a bearer token alongside the session cookie; all protected endpoints accept `Authorization: Bearer <token>`; no schema change
4. **Production file permissions** — appx binary and data owned by `appx` user, not world-readable; initial password file auto-deleted after first login
5. **Log rotation** — appx and opencode systemd journal logs managed by `journald` limits in the service units

---

## Context: What Phase 5 Built

Phase 5 delivered:

- Single `opencode serve` process (multi-project) replacing N per-project Docker containers
- `/api/opencode/*` reverse proxy to `localhost:4096`
- Go CONNECT egress proxy on `127.0.0.1:9080`, `HTTPS_PROXY` cooperative enforcement
- Subdomain routing for agent-built apps (`<name>.<base>`)
- `--http` dev mode locked to localhost
- OpenCode runs with `HTTPS_PROXY=http://127.0.0.1:9080` and `NO_PROXY=localhost,127.0.0.1` in its systemd environment

What Phase 5 did NOT do:
- Create the `appx` and `opencode` OS users
- Apply the iptables rules (the hard-block Layer 2)
- Write systemd unit files to disk
- Write the installer script

These are Phase 6 deliverables.

---

## Components

### 1. `scripts/install.sh` — One-Command Installer

A self-contained bash script that bootstraps appx on a fresh Debian/Ubuntu VPS. Designed to be idempotent: running it twice is safe.

#### Invocation

```bash
# Minimal (localhost self-signed HTTPS)
curl -fsSL https://get.appx.app/install.sh | bash

# With domain (Let's Encrypt via CertMagic)
curl -fsSL https://get.appx.app/install.sh | bash -s -- --domain myserver.example.com

# With explicit version
curl -fsSL https://get.appx.app/install.sh | bash -s -- --version 0.3.0

# For development/testing: run local copy
sudo bash scripts/install.sh --domain myserver.example.com
```

#### Script Structure

```
scripts/install.sh
├── 1. Preflight checks
│   ├── Must run as root (or sudo)
│   ├── OS detection: require Debian/Ubuntu (uname, /etc/os-release)
│   ├── Required commands: curl, systemctl, iptables, iptables-save (or netfilter-persistent)
│   └── Arch detection: amd64 / arm64 for binary download
│
├── 2. Install OpenCode
│   ├── Download latest opencode binary from https://github.com/sst/opencode/releases
│   ├── Install to /usr/local/bin/opencode, mode 0755
│   └── Verify: opencode --version
│
├── 3. Install appx
│   ├── Download appx binary (from GitHub releases or local path if APPX_BIN env set)
│   ├── Install to /opt/appx/appx, mode 0750
│   └── Verify: /opt/appx/appx --version
│
├── 4. Create OS users
│   ├── useradd --system --no-create-home --shell /usr/sbin/nologin appx
│   ├── useradd --system --create-home --home-dir /home/opencode --shell /bin/bash opencode
│   └── (idempotent: skip if user exists)
│
├── 5. Configure rootless Docker for opencode user
│   ├── Install docker-ce-rootless-extras (apt)
│   ├── Run dockerd-rootless-setuptool.sh install as opencode user
│   ├── Enable: systemctl --user --machine=opencode@ enable docker
│   └── Verify: sudo -u opencode docker run --rm hello-world
│
├── 6. Create directory structure and set permissions
│   ├── /opt/appx/           owned by appx:appx, mode 0750
│   ├── /var/lib/appx/       owned by appx:appx, mode 0700  (DB, certs, config)
│   ├── /var/lib/appx/data/  owned by appx:appx, mode 0700
│   └── /home/opencode/projects/  owned by opencode:opencode, mode 0750
│
├── 7. Install systemd service units (see §1.1 and §1.2 below)
│   ├── /etc/systemd/system/appx.service
│   ├── /etc/systemd/system/opencode.service
│   └── systemctl daemon-reload
│
├── 8. Apply iptables egress rules (see §1.3 below)
│   ├── Resolve opencode UID
│   ├── Apply the three rules from egress_iptables.md
│   └── Persist: netfilter-persistent save  (or iptables-save > /etc/iptables/rules.v4)
│
├── 9. Generate initial password
│   ├── openssl rand -base64 24 → /var/lib/appx/initial_password
│   ├── chmod 0600 /var/lib/appx/initial_password, chown appx:appx
│   └── Pass to appx via --initial-password flag in service unit (or env var)
│
├── 10. Enable and start services
│   ├── systemctl enable opencode.service appx.service
│   ├── systemctl start opencode.service
│   ├── sleep 3 && curl -sf http://localhost:4096/global/health  (wait for OC)
│   └── systemctl start appx.service
│
└── 11. Print completion summary
    ├── Print server URL (https://localhost or https://<domain>)
    ├── Print initial password (from /var/lib/appx/initial_password)
    └── "Log in and change your password. The initial_password file will be deleted on first login."
```

#### §1.1 appx.service Unit

```ini
# /etc/systemd/system/appx.service
[Unit]
Description=Appx — Agentic Application Proxy
After=network-online.target opencode.service
Wants=network-online.target

[Service]
Type=simple
User=appx
Group=appx
WorkingDirectory=/var/lib/appx
ExecStart=/opt/appx/appx \
    --data-dir /var/lib/appx/data \
    --port 443
# Populated by installer if --domain was passed:
# ExecStart=/opt/appx/appx --data-dir /var/lib/appx/data --port 443 --domain <domain>
Restart=always
RestartSec=5

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=appx

# Journal size limit (log rotation via journald)
LogNamespace=appx

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/appx
# Port 443 requires CAP_NET_BIND_SERVICE for non-root
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

#### §1.2 opencode.service Unit

```ini
# /etc/systemd/system/opencode.service
[Unit]
Description=OpenCode — AI Coding Agent Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=opencode
Group=opencode
WorkingDirectory=/home/opencode
ExecStart=/usr/local/bin/opencode serve --port 4096 --hostname 127.0.0.1

# Egress proxy: all outbound from opencode goes through appx's CONNECT proxy
# NO_PROXY prevents routing loop (opencode UI <-> opencode server)
Environment=HTTPS_PROXY=http://127.0.0.1:9080
Environment=HTTP_PROXY=http://127.0.0.1:9080
Environment=NO_PROXY=localhost,127.0.0.1,::1

Restart=always
RestartSec=5

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=opencode

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=no
ReadWritePaths=/home/opencode

[Install]
WantedBy=multi-user.target
```

Note: `ProtectHome=no` because opencode's home is `/home/opencode/` (where projects live). `ProtectSystem=strict` still prevents writes to `/opt/appx/`, `/etc/`, `/usr/`, etc.

#### §1.3 iptables Egress Rules

Applied by the installer after the `opencode` user is created. Exact rules from `docs/architecture/egress_iptables.md`:

```bash
OPENCODE_UID=$(id -u opencode)

# Allow loopback (the appx CONNECT proxy lives at 127.0.0.1:9080)
iptables -A OUTPUT -o lo -m owner --uid-owner $OPENCODE_UID -j ACCEPT

# Allow established connections (responses to connections opencode initiated via proxy)
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -m state --state ESTABLISHED,RELATED -j ACCEPT

# Block all other outbound traffic from the opencode user
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -j REJECT --reject-with icmp-port-unreachable

# Persist across reboots
apt-get install -y netfilter-persistent iptables-persistent
netfilter-persistent save
```

The `appx` user is NOT in any of these rules. The appx process (which runs the CONNECT proxy) can freely reach the internet. Only the `opencode` user is restricted.

#### §1.4 Domain Mode

When `--domain` is passed:

```bash
# Overwrite ExecStart in appx.service with domain flag
sed -i "s|ExecStart=.*|ExecStart=/opt/appx/appx --data-dir /var/lib/appx/data --port 443 --domain ${DOMAIN}|" \
    /etc/systemd/system/appx.service
systemctl daemon-reload
```

CertMagic handles Let's Encrypt certificate issuance and renewal. No Caddy or external proxy needed.

#### §1.5 Idempotency

Each step is guarded:

```bash
# User creation
id appx &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin appx

# Directory creation
[ -d /var/lib/appx ] || mkdir -p /var/lib/appx

# iptables rules (check before adding to avoid duplicates)
iptables -C OUTPUT -o lo -m owner --uid-owner $OPENCODE_UID -j ACCEPT 2>/dev/null \
    || iptables -A OUTPUT -o lo -m owner --uid-owner $OPENCODE_UID -j ACCEPT
```

---

### 2. Bearer Token Auth

Native API clients (CLI tools, the future React Native app, curl scripting) cannot use browser session cookies. They need a token they can store and pass via `Authorization: Bearer`.

#### Design

`POST /api/login` response adds a `token` field alongside the existing session cookie:

```json
{
  "token": "appx_<64 random hex chars>"
}
```

The session cookie (`appx_session`) is still set for browser clients. Native clients ignore the cookie and store the `token` value.

Bearer tokens use the same `sessions` table — they are session tokens delivered via HTTP header rather than cookie. No schema change required.

Token lifetime: 30 days (same as session cookies). Renewal: re-authenticate with `POST /api/login`. Token rotation and refresh tokens are deferred to Phase 8.

#### Auth Middleware Change

`internal/auth/auth.go` — `Middleware` becomes:

```go
// bearerToken extracts the raw token from an Authorization: Bearer header.
// Returns empty string if the header is absent or malformed.
func bearerToken(r *http.Request) string {
    v := r.Header.Get("Authorization")
    if strings.HasPrefix(v, "Bearer ") {
        return strings.TrimPrefix(v, "Bearer ")
    }
    return ""
}

// Middleware authenticates requests by checking (in order):
//   1. Authorization: Bearer <token> header
//   2. appx_session cookie
//
// On 401, JSON {"error":"unauthorized"} is returned (not a redirect) so that
// API clients get a machine-readable response. The frontend handles 401 by
// redirecting to /login in the API client (web/src/api/client.ts).
func (a *Auth) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := bearerToken(r)
        if token == "" {
            if c, err := r.Cookie("appx_session"); err == nil {
                token = c.Value
            }
        }
        if token == "" || !a.Store.ValidSession(r.Context(), token) {
            http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

#### Login Handler Change

`internal/server/auth_handlers.go` — `handleLogin` writes the token in the JSON response body:

```go
// handleLogin handles POST /api/login.
// On success, sets the appx_session cookie AND returns the token in the JSON body
// so native clients can store it for Authorization: Bearer use.
func handleLogin(a *auth.Auth) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ... existing password validation ...

        token, err := a.Store.CreateSession(r.Context())
        if err != nil {
            http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
            return
        }

        // Browser clients: set the session cookie
        a.SetSessionCookie(w, token)

        // Native/API clients: include token in response body
        writeJSON(w, http.StatusOK, map[string]string{
            "token": token,
        })
    }
}
```

#### API Client Update

`web/src/api/client.ts` — the `login()` function already receives the response body; the `token` field should be returned to the caller. Browser usage continues to rely on the cookie; the token is available if the caller wants to store it (e.g., for future desktop/mobile use).

```typescript
/** Authenticates with the server. Returns the bearer token for non-browser clients. */
export async function login(password: string): Promise<{ token: string }> {
  return request<{ token: string }>('POST', '/api/login', { password });
}
```

#### Test Coverage

New test cases in `router_test.go`:

- `POST /api/login` response body contains `token` field
- Protected endpoint accepts `Authorization: Bearer <token>` and returns 200
- Protected endpoint with invalid bearer token returns 401
- Protected endpoint with no auth returns 401
- Both cookie and bearer token work independently (not both required)

---

### 3. Initial Password File Cleanup

The installer writes a random password to `/var/lib/appx/initial_password` and passes it to appx via a flag or env var so appx can bootstrap the first account.

After the user successfully logs in for the first time, appx deletes `/var/lib/appx/initial_password`:

**`cmd/appx/main.go`:**

```go
// --initial-password-file: path to a file containing the initial password.
// If set and the file exists, appx reads the password, creates the account,
// and deletes the file on first successful login. This prevents the password
// from persisting in plaintext after the account is set up.
flag.StringVar(&cfg.InitialPasswordFile, "initial-password-file",
    "/var/lib/appx/initial_password", "...")
```

**`internal/server/auth_handlers.go`:**

After a successful login where `cfg.InitialPasswordFile != ""`:

```go
// On successful first login, delete the initial_password file so the
// plaintext password does not persist on disk.
if cfg.InitialPasswordFile != "" {
    if err := os.Remove(cfg.InitialPasswordFile); err != nil && !errors.Is(err, os.ErrNotExist) {
        log.Printf("warn: failed to delete initial_password file: %v", err)
    }
}
```

This means after first login the file is gone. On subsequent logins (file already absent) the `os.Remove` call is a no-op.

---

### 4. Production File Permissions

The installer enforces strict ownership and modes. Nothing in the appx data directory should be readable by the `opencode` user.

```
/opt/appx/
  appx                  owned by appx:appx, mode 0750   (not world-executable)

/var/lib/appx/
  data/                 owned by appx:appx, mode 0700
  data/appx.db          owned by appx:appx, mode 0600
  certs/                owned by appx:appx, mode 0700
  initial_password      owned by appx:appx, mode 0600   (deleted on first login)

/home/opencode/
  projects/             owned by opencode:opencode, mode 0750
```

The installer sets these at install time. Appx itself ensures the data directory has correct permissions on first startup (defense-in-depth).

In `cmd/appx/main.go`, after determining the data dir:

```go
if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
    log.Fatalf("data dir: %v", err)
}
// Harden permissions in case directory pre-existed with looser perms
if err := os.Chmod(cfg.DataDir, 0700); err != nil {
    log.Printf("warn: chmod data dir: %v", err)
}
```

---

### 5. `--http` Mode Documentation

Phase 5 implemented `--http` mode. Phase 6 documents its safety guarantees here for completeness:

- Binds to `127.0.0.1` only; refuses to start if `--port`'s address is not loopback
- Mutually exclusive with `--domain`
- No HSTS header emitted
- No TLS — plaintext HTTP only, intentionally
- No iptables rules apply (installer is not run in dev mode)
- Subdomain routing still works via `*.localhost` which resolves to `127.0.0.1` natively in modern browsers

No code changes needed. This section is a specification anchor for security reviews.

---

## Implementation Order and Dependencies

```
Step 1: Bearer token auth
  ├── Update auth.Middleware to check Authorization: Bearer header
  ├── Update handleLogin to return token in JSON body
  ├── Update web/src/api/client.ts login() return type
  └── Write router_test.go test cases
  (No external dependencies. Can be done first.)

Step 2: Initial password file
  ├── Add --initial-password-file flag to main.go
  └── Add cleanup logic in handleLogin after successful auth
  (Depends on Step 1 — handleLogin already updated.)

Step 3: Production file permissions
  ├── Add chmod 0700 enforcement in main.go startup
  └── Verify data dir permissions are correct
  (Independent of Steps 1-2, can be done in parallel.)

Step 4: scripts/install.sh
  ├── OS detection + preflight
  ├── Binary installation
  ├── User creation (appx, opencode)
  ├── Rootless Docker setup
  ├── Directory structure + permissions (Step 3 values)
  ├── Systemd unit files (appx.service, opencode.service)
  ├── iptables egress rules + persistence
  ├── Initial password generation
  ├── Service enable + start
  └── Completion summary
  (Depends on Steps 1-3 being complete, so the installed binary has all features.)
```

Step 4 can be developed and tested in parallel with Steps 1–3 using a stub binary, but the final `install.sh` should be tested against the complete binary.

---

## Testing the Installer

The installer cannot be unit-tested with Go. Verification must be manual (or via CI with a real VM).

**Local testing with a Hetzner/DigitalOcean snapshot:**

```bash
# 1. Provision a fresh Debian 12 VPS
# 2. Copy the installer
scp scripts/install.sh root@<ip>:/tmp/install.sh
# 3. Run it
ssh root@<ip> 'bash /tmp/install.sh --domain test.example.com'
# 4. Run the verification checklist below
```

**Minimal verification checklist for the installer:**

```
[ ] Script exits 0
[ ] systemctl status appx.service — active (running)
[ ] systemctl status opencode.service — active (running)
[ ] curl -k https://localhost/api/login (POST with initial password) → 200, token in body
[ ] Token from login works: curl -k -H "Authorization: Bearer <token>" https://localhost/api/projects → 200
[ ] /var/lib/appx/initial_password exists before first login
[ ] /var/lib/appx/initial_password deleted after first login
[ ] ls -la /var/lib/appx/data/ → owned by appx:appx, mode 700
[ ] ls -la /opt/appx/appx → mode 750, owner appx:appx
[ ] id opencode — user exists, home is /home/opencode
[ ] id appx — user exists, no login shell
[ ] sudo -u opencode curl --noproxy '*' https://api.anthropic.com → blocked (connection refused/ICMP unreachable)
[ ] sudo -u opencode curl -x http://127.0.0.1:9080 https://api.anthropic.com → passes through proxy
[ ] sudo -u appx curl https://api.anthropic.com → succeeds (appx user not blocked)
[ ] iptables -L OUTPUT -v | grep opencode → three rules present
[ ] netfilter-persistent rules file exists at /etc/iptables/rules.v4
[ ] Reboot server → iptables rules still present
[ ] Reboot server → both services auto-start
[ ] docker run (as opencode, rootless) → works
[ ] docker run -v /var/lib/appx:/mnt alpine cat /mnt/data/appx.db → permission denied (rootless Docker fix)
```

---

## What Does NOT Change

- `internal/auth/store.go` — bcrypt cost 12, min password length 12, session CRUD, `sessions` table schema
- `internal/terminal/` — ring buffer, WebSocket handler, session manager
- `internal/egress/proxy.go` — the Go CONNECT proxy (Phase 5 deliverable, unchanged in Phase 6)
- `internal/tls/selfsigned.go` — self-signed cert generation
- `internal/server/ratelimit.go` — rate limiting
- `internal/db/` — no new migration needed for bearer tokens (same `sessions` table)
- OpenCode proxy at `/api/opencode/*` — unchanged
- Subdomain routing for agent-built apps — unchanged
- Cookie behavior for browser clients — unchanged (cookie still set on login, still required by session middleware)

---

## Security Notes

### Why Two OS Users?

Separating `appx` and `opencode` at the OS level means an agent cannot touch appx's files even if it executes arbitrary code. Standard Unix file permissions (`chmod 0700`, `chown appx:appx`) enforce this. Systemd's `ProtectSystem=strict` provides an additional layer: even if the `opencode` user somehow escalated, systemd's seccomp/namespace restrictions prevent writes to `/opt/appx/`, `/etc/`, and `/usr/`.

See `docs/analysis/refactors/de-docker-refactor.md` Q5 for the full threat model and rationale for rootless Docker.

### Why iptables + HTTPS_PROXY?

`HTTPS_PROXY` is cooperative — only programs that respect the env var route through the proxy. A compromised agent (or malicious tool the agent installs) can bypass it with `curl --noproxy '*'` or raw TCP sockets. The iptables rules are a kernel-level backstop: even a raw socket connection from the `opencode` UID is rejected before it leaves the host. See `docs/architecture/egress_iptables.md`.

### Bearer Token Security

Bearer tokens have the same security properties as session cookies: they are 32-byte random values stored SHA-256 hashed in the database. The primary difference is the transport: cookies are `HttpOnly` + `Secure` (not accessible to JavaScript, only sent over HTTPS); bearer tokens travel in the `Authorization` header. For API clients this is acceptable — they should store tokens in a secure credential store (OS keychain, etc.), not in a web-accessible location.

CORS: appx does not emit `Access-Control-Allow-Origin: *`. Bearer token endpoints are not accessible from third-party origins.

---

## Open Questions

1. **OpenCode binary distribution** — the installer currently downloads OpenCode from GitHub releases. Verify the release artifact naming conventions (amd64 vs x86_64, etc.) are stable and predictable. If not, we may need to shell out to a detection script.

2. **Rootless Docker setup complexity** — `dockerd-rootless-setuptool.sh` requires `newuidmap`/`newgidmap` and a range in `/etc/subuid`, `/etc/subgid`. Some VPS images don't have these. The installer should check and optionally install `uidmap`.

3. **iptables vs nftables** — Debian 12+ uses nftables by default. The `iptables` command on Debian 12 is a compatibility shim that writes nftables rules. This should work transparently, but verify on a fresh Debian 12 image. If not, add nftables native rules as a fallback.

4. **`--initial-password-file` flag vs env var** — a flag is visible in `ps aux`. Consider using an environment variable (`APPX_INITIAL_PASSWORD`) set in the systemd unit's `EnvironmentFile=` (read from a file with restricted permissions) instead of a CLI flag. This avoids the password appearing in the process list.

5. **journald retention** — the unit files above do not set explicit `SystemMaxUse` or `MaxFileSec` on the journal. Consider adding `LogRateLimitIntervalSec` and `LogRateLimitBurst` to the service units, or documenting how to configure `/etc/systemd/journald.conf` for rotation.

6. **Upgrade path** — the installer is designed for fresh installs. Phase 7 will call it on newly provisioned VMs. For upgrades (new appx version on existing server), a separate `upgrade.sh` or `--upgrade` flag is needed. Out of scope for Phase 6 but worth noting.

---

## Phase 7 Readiness

Phase 7 (Hosted Service) provisions a fresh Debian VPS per user, then calls `install.sh`. For Phase 7 to work:

- `install.sh` must be fully non-interactive (no prompts)
- `install.sh` must exit with a non-zero code on any failure (set -e)
- The initial password must be machine-readable (either from `/var/lib/appx/initial_password` or printed to stdout with a parseable prefix, e.g. `APPX_INITIAL_PASSWORD=<value>`)
- Bearer token auth must work so the Phase 7 control plane can make API calls immediately after provisioning (before the user has set a browser session)
- `install.sh --domain username.appx.app` must configure the domain, generate Let's Encrypt cert via CertMagic, and start cleanly

The Phase 7 provisioning flow will be:

```
Control plane
  1. hcloud server create ...
  2. ssh root@<ip> 'curl -fsSL https://get.appx.app/install.sh | bash -s -- --domain <user>.appx.app'
  3. Parse output for APPX_INITIAL_PASSWORD
  4. POST /api/login with initial password → get bearer token
  5. POST /api/settings/apikey with user's Anthropic key
  6. DELETE /api/initial-password (or file cleanup happens on first login)
  7. Send user welcome email with URL + initial password
```
