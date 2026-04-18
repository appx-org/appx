# Security Review — Appx

**Date**: 2026-04-08
**Branch**: `de-docker-refactor`

## Executive Summary

Appx demonstrates strong security fundamentals: bcrypt with cost-12 for passwords, high-entropy SHA-256-hashed session tokens, parameterized SQL everywhere, CSRF protection via `Content-Type: application/json` enforcement + `SameSite=Lax`, WebSocket origin validation, and proper TLS defaults. The most actionable findings are in the egress proxy (DNS rebinding bypasses the allowlist's loopback check) and HTTP server configuration (missing `WriteTimeout` enables slow-read DoS). None of the findings are remotely exploitable without authentication or local access, which is appropriate for a single-user self-hosted tool.

---

## Findings

### [MEDIUM] DNS Rebinding Bypasses Egress Proxy Loopback Check

- **Location**: `internal/server/egress_handlers.go:82-86` (validation) + `internal/egress/proxy.go:78` (dial)
- **Description**: The allowlist validator rejects loopback *hostnames* (`localhost`, `127.0.0.1`, `::1`) but the proxy dials by DNS name. An attacker-controlled domain in the allowlist can resolve to an internal IP at connection time, bypassing the loopback restriction.
- **Exploit Scenario**: A user adds `attacker.example.com:443` to the allowlist. The attacker configures DNS to alternate between a public IP and `127.0.0.1`. When the OpenCode agent makes an outbound connection, the proxy's `net.DialTimeout("tcp", r.Host, dialTimeout)` resolves to `127.0.0.1:443`, allowing the agent to reach the appx dashboard, the egress proxy itself (port 9080), or any other localhost service — all of which the allowlist was meant to prevent.
- **Remediation**: After `net.DialTimeout` succeeds, inspect the resolved IP before tunneling. Reject connections that resolved to loopback or RFC1918 ranges:
  ```go
  destConn, err := net.DialTimeout("tcp", r.Host, dialTimeout)
  if err != nil { ... }
  if addr, ok := destConn.RemoteAddr().(*net.TCPAddr); ok {
      if addr.IP.IsLoopback() || addr.IP.IsPrivate() || addr.IP.IsLinkLocalUnicast() {
          destConn.Close()
          http.Error(w, "resolved to internal address", http.StatusForbidden)
          return
      }
  }
  ```

---

### [MEDIUM] Missing WriteTimeout on All HTTP Servers — Slow-Read DoS

- **Location**: `internal/server/server.go:115-120`, `server.go:137-142`, `server.go:154-158`, `internal/egress/proxy.go:43-46`
- **Description**: All four `http.Server` instances set `ReadHeaderTimeout` and `IdleTimeout` but omit `WriteTimeout`. A client that accepts response bytes extremely slowly ties up a goroutine indefinitely. Enough such connections exhaust the goroutine/FD budget.
- **Exploit Scenario**: An attacker (or misbehaving client on the same network) opens dozens of connections to `/api/projects`, sends valid requests, then reads the response at ~1 byte/second. Each connection holds a goroutine and file descriptor. With ~10K connections, the server runs out of FDs and stops accepting new connections.
- **Remediation**: Add `WriteTimeout: 30 * time.Second` (or `60 * time.Second` for the main server to accommodate SSE/streaming via the OpenCode proxy). For the WebSocket upgrade path (which needs long-lived connections), the handler manages its own deadlines via `SetReadDeadline`.

---

### [MEDIUM] Egress Allowlist Permits RFC1918 Private Addresses

- **Location**: `internal/server/egress_handlers.go:82-86`
- **Description**: The allowlist validator blocks `localhost`, `127.0.0.1`, `::1`, and `*.localhost` but does not block private IP ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`). A user could add `10.0.0.5:8080` to the allowlist, allowing the agent to reach internal network services.
- **Exploit Scenario**: In a deployment where appx sits on a LAN with internal services (databases, admin panels), a user could accidentally (or a prompt-injected agent could request) adding an internal host to the allowlist, giving the agent access to internal infrastructure.
- **Remediation**: Extend the validation to reject private and link-local IPs:
  ```go
  if ip := net.ParseIP(host); ip != nil {
      if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
          http.Error(w, "internal addresses may not be added", http.StatusBadRequest)
          return
      }
  }
  ```

---

### [LOW] Initial Password File Not Auto-Cleaned

- **Location**: `cmd/appx/main.go:87-91`
- **Description**: On first run, the auto-generated password is written to `data/initial_password` (perms 0600) and a log message tells the user to delete it. If forgotten, the plaintext password persists on disk indefinitely.
- **Exploit Scenario**: An attacker with read access to the data directory (e.g., via a compromised backup, file share, or secondary user on the host) reads the initial password file and gains full access.
- **Remediation**: Auto-delete the file after the first successful login. In `handleLogin`, after `CreateSession` succeeds, spawn a goroutine (or synchronously) to `os.Remove(filepath.Join(dataDir, "initial_password"))`. Alternatively, set a timer to delete it 24 hours after creation.

---

### [LOW] No Password Change Endpoint

- **Location**: `internal/server/settings_handlers.go` (absent), `internal/auth/store.go:79` (`SetPassword` exists but is unreachable via API)
- **Description**: `Store.SetPassword()` and `Store.DeleteAllSessions()` exist in the auth layer, but no HTTP handler exposes password rotation. The only way to change the password is by deleting the database. `DeleteAllSessions()` has a documented contract ("Must be called when the password is changed") but no API path enforces it.
- **Exploit Scenario**: If a session token is compromised, the user cannot rotate the password to invalidate it (aside from deleting `appx.db`). There is also no way to log out all sessions from the UI.
- **Remediation**: Add `PUT /api/settings/password` that calls `SetPassword` + `DeleteAllSessions`. Gate it behind re-authentication (require current password in the request body).

---

### [LOW] OpenCode Proxy Path Not Canonicalized After Prefix Strip

- **Location**: `internal/server/router.go:140-143`
- **Description**: `openCodeProxyHandler` does `r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/opencode")` but does not call `path.Clean()` on the result. While Go's ServeMux canonicalizes paths before routing (neutralizing `..` traversal), the `RawPath` field is not updated, and some proxied backends may interpret `RawPath` differently.
- **Exploit Scenario**: An authenticated user sends a request with an encoded path segment like `/api/opencode/%2e%2e/admin`. The ServeMux routes it correctly (path is cleaned to `/api/admin` which wouldn't match the `/api/opencode/` pattern), so this is not directly exploitable against Go's mux. However, if the OpenCode backend interprets `RawPath` differently, it could see `/../admin`. Severity is low because the backend is a trusted localhost service.
- **Remediation**: Add `r.URL.RawPath = ""` after modifying `r.URL.Path` to ensure the proxy uses the cleaned path, or apply `path.Clean()`:
  ```go
  r.URL.Path = path.Clean(strings.TrimPrefix(r.URL.Path, "/api/opencode"))
  r.URL.RawPath = ""
  ```

---

### [LOW] Reverse Proxy Created Per-Request in Subdomain Handler

- **Location**: `internal/server/router.go:114-123`
- **Description**: The subdomain handler creates a new `httputil.ReverseProxy` for every request. While not a vulnerability, each proxy allocates its own transport pool. Under load this could lead to FD exhaustion and removes the benefit of connection pooling.
- **Remediation**: Cache proxies by project, or use a shared `http.Transport`:
  ```go
  // At router setup time, create a shared transport
  transport := &http.Transport{MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second}
  // In the handler, use: proxy.Transport = transport
  ```

---

### [INFO] Anthropic API Key Stored Plaintext in SQLite

- **Location**: `internal/server/settings_handlers.go:42-44`, `internal/auth/store.go:186-191`
- **Description**: The API key is stored unencrypted in the `settings` table. This is documented and acknowledged. The database file has 0600 permissions.
- **Impact**: Acceptable for single-user self-hosted deployment where database access implies full system access. Note: if backups of `appx.db` are stored in less-protected locations, the key is exposed.
- **Remediation**: Consider at-rest encryption (e.g., encrypt with a key derived from the user's password or a machine-specific secret) for defense in depth.

---

### [INFO] `unsafe-inline` in CSP style-src

- **Location**: `internal/server/middleware.go:28`
- **Description**: The CSP includes `style-src 'self' 'unsafe-inline'` to support the React inline styles pattern (`Record<string, React.CSSProperties>`). This weakens XSS protection for style injection vectors (e.g., CSS exfiltration via `background-image: url(...)`).
- **Impact**: Minimal in practice — no user-generated content is rendered, and all API responses are JSON. Style injection requires an existing XSS vector, which the strict `script-src 'self'` makes very difficult.
- **Remediation**: No action needed unless the app begins rendering user-supplied HTML. If desired, migrate to CSS-in-JS with nonce-based CSP.

---

### [INFO] TLS MinVersion is 1.2 — Consider 1.3 Only

- **Location**: `internal/server/server.go:112,133`
- **Description**: `MinVersion: tls.VersionTLS12` allows TLS 1.2 connections. TLS 1.3 removes vulnerable cipher suites and is faster (1-RTT handshake).
- **Impact**: TLS 1.2 is still considered secure when configured correctly. Go's default cipher suite selection is safe.
- **Remediation**: For a new self-hosted tool with no legacy client requirements, `tls.VersionTLS13` is a reasonable hardening step.

---

## Summary Table

| Severity | Title | Location |
|----------|-------|----------|
| MEDIUM | DNS rebinding bypasses egress loopback check | `egress_handlers.go:82`, `proxy.go:78` |
| MEDIUM | Missing WriteTimeout — slow-read DoS | `server.go:115,137,154`, `proxy.go:43` |
| MEDIUM | Egress allowlist permits RFC1918 private IPs | `egress_handlers.go:82-86` |
| LOW | Initial password file not auto-cleaned | `main.go:87-91` |
| LOW | No password change endpoint | `settings_handlers.go` (absent) |
| LOW | OpenCode proxy path not canonicalized | `router.go:140-143` |
| LOW | Reverse proxy created per-request | `router.go:114-123` |
| INFO | API key stored plaintext in SQLite | `settings_handlers.go:42`, `store.go:186` |
| INFO | `unsafe-inline` in style-src CSP | `middleware.go:28` |
| INFO | TLS MinVersion 1.2, not 1.3 | `server.go:112,133` |

---

## Positive Security Observations

- **Authentication is textbook-correct**: bcrypt cost 12, 256-bit session tokens, SHA-256 token hashing, `HttpOnly`+`Secure`+`SameSite=Lax` cookies. The architecture comment in `store.go:1-27` demonstrates understanding of *why* each decision was made.
- **All SQL is parameterized** — zero instances of string concatenation in queries across the entire codebase.
- **CSRF defense is layered**: `Content-Type: application/json` enforcement (triggers CORS preflight) + `SameSite=Lax` cookies. The `requireJSON` middleware is correctly applied to all state-changing routes.
- **WebSocket origin validation** rejects empty and mismatched origins, with explicit tests (`TestWS_WrongOrigin`, `TestWS_MissingOrigin`).
- **Rate limiting** on login with proper client IP extraction that only trusts `X-Forwarded-For` from RFC1918/loopback peers.
- **Database file permissions** are hardened to 0600 immediately after creation.
- **Cookie stripping** on reverse proxies prevents the appx session cookie from leaking to backend services.
- **Body size limits** (1 MB) on all API routes prevent memory exhaustion.
- **No `dangerouslySetInnerHTML`** or raw HTML rendering in the React frontend.
- **Clean dependency posture** — all major dependencies are recent, actively maintained versions with no known CVEs.
- **Egress proxy binds to 127.0.0.1 only**, preventing external access.
- **`X-Appx-Auth` header** on 401s distinguishes appx auth failures from backend 401s, preventing redirect loops.
