# Security Review: Appx

**Date:** 2026-04-06  
**Reviewer:** Internal security audit  
**Scope:** Full codebase — `phase-4-proxy` branch  
**Threat model:** Self-hosted single-user tool. Attacker could be (a) an external party gaining unauthorized access, (b) a compromised/malicious AI agent running inside a container, or (c) a supply chain attacker targeting the build.

---

## Executive Summary

The codebase has a solid security foundation: bcrypt passwords, cryptographically random session tokens stored as SHA-256 hashes, parameterized SQL queries, capability-dropped containers, proper cookie flags. No injection vulnerabilities, no hardcoded credentials, no trivially exploitable authentication bypasses.

The most serious issues are in the **build pipeline** (supply chain attack via `curl | bash`), **resource management** (missing limits on proxied data and WebSocket tunnels), and **secret storage** (API keys and container secrets in plaintext SQLite). Several gaps also exist in the proxy and auth layers that matter for production deployments.

---

## Findings

### FINDING-01: Dockerfile installs OpenCode via pipe-to-bash

**Severity:** High  
**Location:** `internal/project/Dockerfile.project:10`  
**Category:** Supply Chain

```dockerfile
RUN curl -fsSL https://opencode.ai/install | bash
```

This executes arbitrary code from a remote server at image build time with root privileges. A compromised `opencode.ai` server, a CDN breach, or a DNS hijack delivers malicious code to every appx instance that builds or rebuilds containers. The `-fsSL` flags suppress errors and follow redirects, making this worse. The `-f` flag will fail on 4xx/5xx but does nothing against a malicious 200 response.

**Fix:** Pin to a specific version and verify a checksum. The Phase 5 plan already includes this:
```dockerfile
ARG OPENCODE_VERSION=1.3.14
ARG OPENCODE_SHA256=<hash>
RUN curl -fsSL -o /tmp/opencode \
    "https://github.com/sst/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-x64" \
  && echo "${OPENCODE_SHA256}  /tmp/opencode" | sha256sum -c - \
  && install -m 755 /tmp/opencode /usr/local/bin/opencode \
  && rm /tmp/opencode
```

Investigate whether `build-essential` is actually required (it enables compiling attack binaries inside the container). If it's only needed for npm native modules, consider moving it to a multi-stage build and excluding it from the final image.

---

### FINDING-02: Proxied request and WebSocket bodies have no size limits

**Severity:** High  
**Location:** `internal/proxy/proxy.go` (ContainerHandler, ProxyHandler), `internal/proxy/ws.go`

The API mux applies `limitBody(1MB)` to all protected routes. The container proxy — which handles all requests to project subdomains and `/apps/:name/*` — does not. An authenticated client can upload an arbitrarily large body to a container:

```go
// router.go — API routes get limitBody, proxy routes do not
mux.Handle("/api/", limitBody(a.Middleware(requireJSON(api))))
mux.Handle("/apps/", a.Middleware(proxy.ProxyHandler(pm)))  // no size limit
// Subdomain dispatch also has no size limit
```

`proxyWebSocket` in `ws.go` copies without any limit:
```go
io.Copy(backendConn, clientConn)  // unbounded
io.Copy(clientConn, backendConn)  // unbounded
```

It also has no idle timeout — an attacker can open many WebSocket connections and abandon them, exhausting file descriptors on the server.

**Fix:**
1. For HTTP: Wrap `ContainerHandler` and `ProxyHandler` requests with a generous but finite body limit (e.g., 100MB) using `http.MaxBytesReader`. Large legitimate uploads (AI agent file operations) may need this to be tunable.
2. For WebSocket: Set deadlines on the underlying connections and add an idle timeout. After X minutes without traffic, close the tunnel.

```go
// ws.go — add idle timeout
clientConn.SetDeadline(time.Now().Add(idleTimeout))
backendConn.SetDeadline(time.Now().Add(idleTimeout))
// Reset deadline on each io.Copy write via a custom writer
```

---

### FINDING-03: Auth cookie domain hardcoded to `"localhost"` — production deployments broken

**Severity:** High  
**Location:** `internal/auth/auth.go:49`

```go
Domain: "localhost",
```

This is hardcoded and cannot be overridden by the `--base-domain` flag we introduced. In production with `--domain example.com` and `BaseDomain: "example.com"`, the Open button navigates to `test3.example.com` but the `appx_session` cookie has `Domain=localhost` — browsers will not send it to `test3.example.com`. The user lands on the project subdomain with no session and gets a 401 from every API call.

`Auth` currently has no knowledge of the base domain. This needs to be threaded in.

**Fix:**
```go
type Auth struct {
    Store      *Store
    BaseDomain string
}

func New(store *Store, baseDomain string) *Auth {
    return &Auth{Store: store, BaseDomain: baseDomain}
}

func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
    http.SetCookie(w, &http.Cookie{
        // ...
        Domain: a.BaseDomain,
    })
}
```

Wire in `main.go`:
```go
a := auth.New(cfg.AuthStore, cfg.BaseDomain)
```

---

### FINDING-04: API key and container secrets stored in plaintext SQLite

**Severity:** Medium  
**Location:** `internal/auth/store.go` (settings table), `internal/project/store.go` (container_secret column)

The Anthropic API key is stored in the `settings` table as plaintext under the key `"anthropic_api_key"`. The per-container secret (`ContainerSecret`) is stored as a 64-character hex string in `container_secret`. Both are readable in full if the SQLite file at `data/appx.db` is compromised.

For a self-hosted tool where the DB file is on the same machine as the server, this is acceptable threat-model — shell access to the machine already implies access to running processes and their environment. But these mitigations would help:

**Fix:**
- For the API key: Consider encrypting it at rest using a key derived from the server's startup passphrase or a file-based key. Alternatively, accept this risk explicitly in the documentation.
- For container secrets: These rotate on `Reset`, which limits the exposure window. No change strictly required, but documenting the assumption is useful.

---

### FINDING-05: Initial password file is never deleted

**Severity:** Medium  
**Location:** `cmd/appx/main.go:71-89`

On first run, appx generates a random password, writes it to `{dataDir}/initial_password` (0600), and prints it to stderr. The file is never cleaned up. If the user changes their password but the file persists, an attacker who later gains read access to the data directory can recover the original password and try it (it won't work after a password change, but they may not know it was changed).

More importantly, if the user never changes the password, the file is a permanent plaintext copy sitting on disk.

**Fix:** Delete the file after printing it, or add a comment in the file instructing the user to delete it after noting the password. Or print to stderr only, never write to disk:

```go
log.Printf("First run detected. Admin password: %s", password)
// Remove file after printing
os.Remove(filepath.Join(*dataDir, "initial_password"))
```

---

### FINDING-06: Containers have unrestricted outbound internet access

**Severity:** Medium  
**Location:** `internal/project/container.go` (network configuration)  
**Category:** Container Isolation

Each container is on an isolated bridge network (`appx-<name>-net`) with no egress restrictions. The AI agent (`opencode`) and any apps running inside the container have unrestricted outbound internet access. Combined with the fact that `ANTHROPIC_API_KEY` and `OPENCODE_SERVER_PASSWORD` are injected as environment variables, a malicious prompt injection or compromised AI agent could:

- Exfiltrate the Anthropic API key to an attacker-controlled server
- Exfiltrate project source code
- Contact C2 infrastructure
- Mine cryptocurrency

The `build-essential` package (currently in the Dockerfile) allows the agent to compile arbitrary binaries inside the container, removing the constraint of pre-installed tools.

**Fix:**
- Phase 5 egress logging is the right first step — visibility before restriction.
- Future: implement egress allow-listing (only allow `*.anthropic.com`, `*.openai.com`, specific model APIs) via iptables rules on the container network.
- Remove `build-essential` from the Dockerfile if not required for OpenCode itself.

---

### FINDING-07: `limitBody` not applied before WebSocket upgrade check

**Severity:** Medium  
**Location:** `internal/terminal/handler.go`

The terminal WebSocket handler is wrapped in auth middleware but not in `limitBody`. More importantly, WebSocket connections are long-lived and the terminal handler replays the entire ring buffer contents on reconnect:

```go
// handler.go — full ring buffer sent on connect
buf := session.CopyOutput()
if err := conn.WriteMessage(websocket.BinaryMessage, buf); err != nil {
    return
}
```

If the ring buffer is at its maximum size (up to 4096 KB by user configuration), every reconnect sends up to 4MB of data. With multiple simultaneous connections or rapid reconnect loops, this could amplify bandwidth.

**Fix:** Rate-limit reconnects per session. Also consider capping the replay to the last N bytes for the initial connection handshake.

---

### FINDING-08: `opencode serve` runs on `0.0.0.0` inside containers

**Severity:** Low  
**Location:** `internal/project/Dockerfile.project:19`

```dockerfile
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 0.0.0.0 & sleep infinity"]
```

OpenCode listens on all interfaces inside the container. This was required for the Docker Desktop port-binding approach (the host connects via `127.0.0.1:<published-port>`), but it also means any process inside the container that can reach `127.0.0.1:4096` directly bypasses the BasicAuth that the proxy enforces. This is only exploitable if another process inside the same container (not expected, but possible via prompt injection) targets the opencode API.

**Fix:** `--hostname 127.0.0.1` would restrict opencode to loopback inside the container. Since port publishing works at the Docker network layer (not the container's listening interface), this should still work:
```dockerfile
CMD ["sh", "-c", "opencode serve --port 4096 --hostname 127.0.0.1 & sleep infinity"]
```
Verify this still allows the published port to be reachable from the host before applying.

---

### FINDING-09: TLS certificate includes auto-detected network IPs

**Severity:** Low  
**Location:** `internal/tls/selfsigned.go:117-140`

The self-signed cert's SANs include all non-loopback IPs detected from network interfaces at startup:

```go
for _, iface := range ifaces {
    if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
        continue
    }
    // all IPs on this interface added to cert SANs
}
```

This is useful for LAN access but could include unintended interfaces: VPN adapters, Docker bridge IPs, cloud metadata endpoints (e.g., `169.254.169.254` on AWS/GCP — though this is an IP, not DNS). The cert reveals the server's internal network topology to anyone who can inspect it (any browser connecting to the server can view the cert).

**Fix:** This is low risk for a self-hosted tool. Consider logging which IPs were added at cert generation time so operators know what's in the cert.

---

### FINDING-10: No idle timeout on WebSocket terminal sessions

**Severity:** Low  
**Location:** `internal/terminal/manager.go`, `internal/terminal/handler.go`

Terminal sessions (exec processes inside containers) stay alive as long as the WebSocket is open or until the server restarts. Abandoned sessions hold:
- An exec process inside the container (consuming CPU/memory)
- A subscriber channel in the terminal manager
- A goroutine pumping output

There is a one-session-per-project cap (`if len(m.sessions) >= 1`), but if a session leaks, no new sessions can be created for that project until the server restarts.

**Fix:** Add an idle timeout — if no input is received from the WebSocket client for N minutes, close the session. The ring buffer preserves output for replay on reconnect, so this is user-transparent for reconnecting clients.

---

### FINDING-11: No security event logging

**Severity:** Low  
**Location:** Throughout

There is no audit log for security-relevant events:
- Login attempts (success and failure)
- Session creation and expiry
- Project creation and deletion
- API key changes
- Container start/stop

If an unauthorized access occurs, there is no record of what happened. The `egress_log` table exists in the schema but is never populated (also relevant for FINDING-06).

**Fix:** Add structured log lines for authentication events at minimum. This doesn't need a separate audit log table — the existing Go `log` calls are sufficient if they include IP, outcome, and operation. Example:
```go
log.Printf("auth: login %s from %s", result, clientIP)
```

---

## Non-Issues (Explicitly Verified)

The following patterns were examined and are **not vulnerabilities**:

- **SQL injection**: All queries use parameterized `database/sql` statements. No string interpolation.
- **Session token storage**: SHA-256 hash only; raw token never stored. `crypto/rand` for generation.
- **Password hashing**: bcrypt with `DefaultCost`. Correct.
- **CSRF protection**: JSON `Content-Type` requirement + `SameSite=Lax` cookies. Adequate for this app model — HTML forms cannot set `Content-Type: application/json`, and `SameSite=Lax` blocks cross-site POST. State-changing operations are all POST/PUT/DELETE.
- **Terminal CSWSH**: `CheckOrigin` compares `parsed.Host == r.Host` including port. Correct for non-standard ports.
- **Project name injection**: Validated with regex `^[a-z][a-z0-9-]{0,61}[a-z0-9]$` before use in Docker API calls (not shell commands).
- **Path traversal**: Proxy paths come from authenticated user requests. No file system access based on request paths.
- **Sensitive data in error responses**: Login returns generic "invalid password". Proxy handlers return generic error strings without internal detail.
- **Container privilege escalation**: `CapDrop: ALL`, `no-new-privileges:true`, `ReadonlyRootfs: true`. Correct.
- **Docker socket SSRF**: Container addresses are resolved via Docker inspect, not from user-controlled input.
- **Rate limiting**: IP-based sliding window for login. Trusted-proxy handling for `X-Forwarded-For` is correct — only trusts it from RFC 1918 ranges.

---

## Priority Order

| # | Severity | Issue | Effort |
|---|----------|-------|--------|
| 01 | High | curl\|bash supply chain attack | Medium — Phase 5 scope |
| 02 | High | Unbounded proxy body + WebSocket tunnel | Small |
| 03 | High | Cookie domain hardcoded to "localhost" | Small |
| 04 | Medium | Plaintext API key + container secrets | Medium |
| 05 | Medium | Initial password file never deleted | Trivial |
| 06 | Medium | Unrestricted container egress | Large — Phase 5 scope |
| 07 | Medium | Ring buffer replay size on reconnect | Small |
| 08 | Low | opencode listens on 0.0.0.0 in container | Trivial |
| 09 | Low | TLS cert leaks network topology | Trivial |
| 10 | Low | No idle timeout on terminal sessions | Small |
| 11 | Low | No security event logging | Small |
