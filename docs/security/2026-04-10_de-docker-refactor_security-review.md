# Security Review: appx (de-docker-refactor branch)

**Date:** 2026-04-10
**Branch:** `de-docker-refactor`
**Reviewer:** Automated security audit (Claude)
**Scope:** Full codebase — backend (Go), frontend (React/TypeScript), infrastructure configuration

---

## Executive Summary

Appx has a **strong security foundation** for a self-hosted single-user application. Authentication uses bcrypt (cost 12) with SHA-256 hashed session tokens, CSRF protection is layered (SameSite=Lax + JSON Content-Type enforcement), and the egress proxy includes DNS rebinding mitigation. Cookie stripping on reverse proxies prevents session leakage to backend services.

The most significant findings are: (1) subdomain-proxied responses lack security headers, allowing content-type sniffing and framing of agent-built apps; (2) the `AGENTS.md` scaffold doesn't instruct dev servers to bind to localhost, meaning agent-built apps may be directly reachable on public interfaces without auth; and (3) the initial password file persists on disk after first login with no automatic cleanup.

No CRITICAL issues were found. The codebase is suitable for its intended deployment context (personal self-hosted tool) with the remediations below applied.

---

## Findings

### [HIGH] Subdomain proxy responses lack security headers

- **Location:** `internal/server/router.go:99-148` (subdomain dispatch block)
- **Description:** The `securityHeaders` middleware wraps only the `dashboard` handler (line 77). Subdomain requests routed through the reverse proxy bypass this middleware entirely. Responses served through `<project>.localhost` have no `X-Content-Type-Options`, `X-Frame-Options`, or `Strict-Transport-Security` headers.
- **Exploit Scenario:** An attacker who can influence content in an agent-built app (e.g., by submitting a crafted file that gets served) can exploit content-type sniffing to execute JavaScript in the context of the subdomain. Since the `appx_session` cookie has `Domain=.localhost`, the cookie is sent to all subdomains — though `HttpOnly` prevents JavaScript access, the absence of `X-Frame-Options` means the subdomain page can be framed by a malicious site for clickjacking. In HTTPS mode, the missing HSTS header means subdomain traffic could be downgraded via an active MITM.
- **Remediation:** Add a minimal set of security headers to subdomain proxy responses. Apply `X-Content-Type-Options: nosniff` and HSTS unconditionally. Do **not** apply the dashboard's strict CSP (it would break user apps). Example:

```go
// In the subdomain dispatch handler, before proxy.ServeHTTP:
w.Header().Set("X-Content-Type-Options", "nosniff")
if !rcfg.HTTPMode {
    w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
}
```

Consider `X-Frame-Options: SAMEORIGIN` (allows framing from dashboard but blocks external framing) or omit it if user apps need framing capability.

---

### [MEDIUM — mitigated by cloud firewall] Agent-built apps may bind to all network interfaces

- **Location:** `internal/project/manager.go:13-26` (AGENTS.md template)
- **Description:** The scaffolded `AGENTS.md` tells the agent to use a specific port but does not instruct it to bind to `127.0.0.1` (localhost only). Many frameworks (Express, Flask, Django) default to `0.0.0.0` when given only a port number. If an agent creates a dev server bound to `0.0.0.0:10001`, that service is directly accessible from the network on port 10001 — bypassing appx's auth, TLS, and egress controls entirely.
- **Exploit Scenario:** A user deploys appx on a VPS without a cloud firewall. The agent creates an Express app on port 10001. Express defaults to binding `0.0.0.0`. Anyone on the internet can access `http://<server-ip>:10001` without authentication, reaching the dev server directly.
- **Mitigation:** A cloud firewall that only allows inbound TCP 22 (SSH), 443 (appx HTTPS), and optionally ICMP blocks external access to ports 10000-10999 regardless of how the app binds. This is documented as a required deployment step in [`docs/guides/security-guide.md`](../guides/security-guide.md).
- **Residual risk:** Without a cloud firewall (e.g., bare-metal or misconfigured VPS), the exposure remains. Phase 6 plans host-level iptables/nftables rules as a second layer.
- **Defense in depth:** Update the `AGENTS.md` template to instruct localhost binding as a best-effort additional control:

```go
const agentsTemplate = `# Project: {{name}}

## App Port

When running a dev server, always bind to **127.0.0.1** on port **{{port}}**.
Example: \`vite --host 127.0.0.1 --port {{port}}\`
This port is assigned by appx and has proxy routing configured.
Your app will be accessible at {{subdomain}}.
Do NOT bind to 0.0.0.0 — the appx proxy handles external access with authentication.
`
```

---

### [MEDIUM] Initial password file persists on disk after use

- **Location:** `cmd/appx/main.go:88-93`
- **Description:** On first run, the auto-generated password is written to `data/initial_password` (permissions 0600) and printed to stderr. The log message says "delete this file after logging in" but there is no automatic cleanup. If the user forgets to delete it, the plaintext password remains on disk indefinitely.
- **Exploit Scenario:** An attacker gains read access to the filesystem (via a vulnerability in an agent-built app, a backup exposure, or local privilege escalation). They read `data/initial_password` and authenticate to appx. Even if the user changed the password, the initial password may still be the current one if they never bothered to change it.
- **Remediation:** Auto-delete the password file after the first successful login. In `handleLogin`, after a successful password check:

```go
// After successful login, clean up the initial password file.
pwFile := filepath.Join(dataDir, "initial_password")
os.Remove(pwFile) // best-effort, ignore errors
```

Alternatively, display the password only on stdout (not to a file) and require the user to copy it immediately.

---

### [MEDIUM] In-memory rate limiter resets on server restart

- **Location:** `internal/server/ratelimit.go` (entire file)
- **Description:** The login rate limiter stores attempt counts in a Go `map[string][]time.Time`. On server restart (crash, deploy, manual restart), all rate limit state is lost. An attacker can force a restart (e.g., by triggering an unrecoverable error or waiting for a deploy) and then immediately attempt 10 login tries with no rate limit penalty.
- **Exploit Scenario:** An attacker scripts an automated brute-force attack against the login endpoint. After every 10 attempts (5-minute window), they wait 5 minutes. But if the server restarts during this period, they get a fresh 10 attempts immediately. Over time, this significantly increases the brute-force throughput compared to the intended rate limit.
- **Remediation:** For a single-user self-hosted app, the current approach is reasonable. To harden:
  - Option A: Persist rate limit state to SQLite (simple but adds DB writes on every login attempt).
  - Option B: Use exponential backoff with a lockout after N consecutive failures, persisted to the settings table.
  - Option C: Add account lockout after 50 total failed attempts within 24 hours (persisted counter in settings).

---

### [LOW] Egress log table grows without bounds

- **Location:** `internal/egress/store.go:49-58` (LogEntry insert)
- **Description:** Every CONNECT proxy request inserts a row into `egress_log`. There is no TTL, max row count, or periodic cleanup. Over months of operation with active agent network use, this table can grow to millions of rows, degrading query performance and consuming disk space.
- **Exploit Scenario:** An agent makes thousands of outbound requests per day. After months, the egress_log table contains millions of rows. The `SELECT COUNT(*)` in `ListLog` (line 64) becomes slow, and the database file grows to several GB.
- **Remediation:** Add a periodic cleanup that deletes entries older than a configurable retention period (e.g., 30 days). Run it alongside the session cleanup ticker:

```go
// In server.go, in the cleanup goroutine:
es.PruneLog(30 * 24 * time.Hour) // keep last 30 days
```

---

### [LOW] `unsafe-inline` in style-src CSP directive

- **Location:** `internal/server/middleware.go:28`
- **Description:** The CSP includes `style-src 'self' 'unsafe-inline'`, which allows inline `<style>` tags and `style=""` attributes. While necessary for the current React inline-style architecture, it weakens CSP protection against style-based injection attacks (CSS data exfiltration, UI redressing).
- **Exploit Scenario:** If an XSS vulnerability is found in the dashboard (e.g., via a stored payload in a project name — though currently prevented by strict name validation), `unsafe-inline` styles would allow the attacker to inject CSS that exfiltrates data via background-image URL requests.
- **Remediation:** This is acceptable for the current architecture. To eliminate it long-term, migrate to CSS modules or a utility framework and generate CSP nonces for any remaining inline styles. Low priority given the strict input validation on all user-controlled fields.

---

### [LOW] `stripPort` mishandles bare IPv6 addresses

- **Location:** `internal/server/router.go:185-190`
- **Description:** The `stripPort` function uses `strings.LastIndex(host, ":")` to find the port separator. For IPv6 addresses without a port (e.g., `[::1]`), the last colon is inside the brackets, so `stripPort("[::1]")` returns `[::` instead of `[::1]`. This would fail to match any domain/alias, resulting in a 404.
- **Exploit Scenario:** No exploitable impact — the malformed result never matches `BaseDomain` or any alias, so the request gets a 404. This fails safely.
- **Remediation:** Use `net.SplitHostPort` with a fallback for the no-port case:

```go
func stripPort(host string) string {
    h, _, err := net.SplitHostPort(host)
    if err != nil {
        return host // no port present, return as-is
    }
    return h
}
```

---

### [INFO] Sessions use absolute TTL only (no activity-based expiry)

- **Location:** `internal/auth/store.go` (session model)
- **Description:** Sessions expire 30 days after creation regardless of activity. There is no sliding window or last-activity tracking. A session created on day 1 is valid until day 31 even if never used after initial creation. Conversely, an actively used session expires after 30 days and requires re-login.
- **Remediation:** For a single-user self-hosted tool, 30-day absolute TTL is reasonable. If desired, add a sliding window by updating `expires_at` on each successful `ValidSession` call. This trades slightly more DB writes for better UX (active sessions don't expire) and better security (abandoned sessions expire sooner if the window is shortened).

---

### [INFO] OpenCode API key transmitted over plaintext HTTP

- **Location:** `internal/opencode/client.go:90` (SetAuth request)
- **Description:** The Anthropic API key is sent from appx to OpenCode over `http://127.0.0.1:4096`. This is unencrypted HTTP traffic on the loopback interface.
- **Remediation:** This is acceptable for localhost-only communication where both processes run on the same machine. The risk is limited to other processes on the same host intercepting loopback traffic (which requires root privileges). No change needed unless multi-host deployment is planned.

---

## Summary Table

| Severity | Status | Title | Location |
|----------|--------|-------|----------|
| HIGH | ✅ Fixed | Subdomain proxy responses lack security headers | `internal/server/router.go` |
| MEDIUM | ✅ Mitigated | Agent-built apps may bind to all network interfaces | `internal/project/manager.go:13-26` |
| MEDIUM | 🔓 Open | Initial password file persists on disk after use | `cmd/appx/main.go:88-93` |
| MEDIUM | 🔓 Open | In-memory rate limiter resets on server restart | `internal/server/ratelimit.go` |
| LOW | ✅ Fixed | Egress log table grows without bounds | `internal/egress/store.go` |
| LOW | 🔓 Open | `unsafe-inline` in style-src CSP directive | `internal/server/middleware.go:28` |
| LOW | ✅ Fixed | `stripPort` mishandles bare IPv6 addresses | `internal/server/router.go` |
| INFO | ✅ Fixed | Sessions use absolute TTL only (no activity-based expiry) | `internal/auth/store.go` |
| INFO | ✅ No action | OpenCode API key transmitted over plaintext HTTP | `internal/opencode/client.go:90` |

---

## Positive Security Observations

These patterns demonstrate strong security awareness and should be preserved:

1. **Session token hashing (SHA-256):** Raw tokens are never stored in the database. If the DB is exfiltrated, session tokens cannot be recovered. (`internal/auth/store.go`)

2. **Bcrypt cost 12:** Above Go's default of 10 and aligned with OWASP 2023 recommendations. (`internal/auth/store.go`)

3. **Cookie stripping on reverse proxies:** Both the subdomain proxy (`router.go:141`) and OpenCode proxy (`router.go:175`) delete the `Cookie` header before forwarding, preventing session cookie leakage to backend services.

4. **DNS rebinding protection in egress proxy:** Post-dial IP validation (`egress/proxy.go:98-107`) prevents CONNECT requests from reaching internal addresses even when the hostname resolves to a private IP at connection time.

5. **Layered CSRF defense:** The combination of `SameSite=Lax` cookies and `Content-Type: application/json` requirement creates defense-in-depth against CSRF. Neither alone is sufficient; together they are robust. (`middleware.go:45-65`, `auth.go:74`)

6. **Trusted proxy header validation:** Rate limiter only trusts `X-Forwarded-For`/`X-Real-IP` from known private/loopback CIDRs, preventing IP spoofing when appx is exposed directly. (`ratelimit.go:97-170`)

7. **Path canonicalization on OpenCode proxy:** `path.Clean` + `RawPath` clearing prevents directory traversal against the backend. (`router.go:164-168`)

8. **HTTP mode locked to localhost:** `--http` mode binds exclusively to `127.0.0.1`, preventing accidental exposure of unencrypted traffic. (`server.go:157`)

9. **Strict project name validation:** Regex `^[a-z][a-z0-9-]{0,61}[a-z0-9]$` prevents injection through project names in filesystem paths, subdomain routing, and template substitution. (`project/project.go:77`)

10. **File permissions:** Data directory (0700), database (0600), password file (0600), TLS keys (0600) — all owner-only. (`cmd/appx/main.go:54`, `db/db.go:60`)

11. **Parameterized SQL queries:** All database operations use `?` placeholders. No string concatenation in SQL queries. (`project/store.go`, `egress/store.go`, `auth/store.go`)

12. **WebSocket origin validation:** Custom `CheckOrigin` rejects empty origins and enforces exact host matching, preventing CSWSH attacks. (`terminal/handler.go:52-67`)

13. **Server timeouts configured:** `ReadHeaderTimeout` (10s) prevents Slowloris, `WriteTimeout` (60s) and `IdleTimeout` (90s) prevent resource exhaustion. (`server.go:121-123`)

14. **TLS 1.3 minimum:** Both self-signed and CertMagic modes enforce TLS 1.3 as the floor. (`server.go:115, 137`)

15. **Input size limits:** 1MB body limit on API requests (`middleware.go:11`), 10MB response limit on OpenCode client (`opencode/client.go:14`), 1MB WebSocket message limit (`terminal/handler.go:16`).
