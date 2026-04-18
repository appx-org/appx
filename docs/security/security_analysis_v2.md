Security Review

CRITICAL — Fix Immediately

1. Anthropic API key stored in plaintext in SQLite

internal/auth/store.go:167-172 — SetSetting stores the API key as plain text in the settings table. If
the SQLite database is leaked (backup exposure, directory traversal in a future feature, server
compromise), the attacker gets a valid Anthropic API key they can use to run up charges.

Recommendation: Encrypt the API key at rest using a key derived from the server's TLS private key or a
separate secret. At minimum, document this as a known limitation.

2. Containers run as root with no resource limits

internal/project/Dockerfile.project — The container runs as root (no USER directive). Combined with no
CPU/memory/PID limits in container.go:229-238 (the Resources struct exists but is never applied to
HostConfig), a malicious or buggy Claude Code session could:

- Fork-bomb the host (no PID limit)
- OOM-kill other processes (no memory limit)
- Consume all CPU
- Write to any path inside the container as root

container.go:229-238 — HostConfig sets no resource constraints at all. The Resources field on Project is
dead code — it's parsed from DB but never passed to Docker.

Recommendation: Add USER node to the Dockerfile. Apply default resource limits in HostConfig (Memory,
NanoCPUs, PidsLimit). Even conservative defaults (2GB RAM, 2 CPUs, 256 PIDs) prevent host-level DoS.

3. No request body size limit on most endpoints

internal/server/auth_handlers.go:18 — handleLogin correctly uses MaxBytesReader(1MB). But no other
handler does this. handleCreateProject, handleSetAPIKey, and future handlers accept unbounded request
bodies. An attacker with a valid session could send a multi-GB POST body to exhaust server memory.

Recommendation: Add a global MaxBytesReader middleware or apply it per-handler. 1MB is a reasonable
default for all current JSON endpoints.

4. Race condition in Manager.SetAnthropicKey / AnthropicKeySet

container.go:75-82 — These read/write m.anthropicKey (a plain string) with no synchronization.
SetAnthropicKey is called from HTTP handlers (settings PUT/DELETE), while Start reads m.anthropicKey
from another goroutine. This is a data race — go test -race would flag it.

Recommendation: Protect with sync.RWMutex or use atomic.Value.

---

HIGH — Should Fix Before Production

5. Password printed to stdout / logs on first run

cmd/appx/main.go:62 — fmt.Printf("Generated password: %s\n", pw). If stdout is captured to a log file
(systemd journal, Docker logs, cloud logging), the password persists in plaintext in the log system. The
systemd service file likely captures this.

Recommendation: Write to stderr with a clear "copy this now" message, or write it to a file in the data
directory with 0600 permissions and delete it after first login.

6. Session tokens not invalidated on password change

There is no "change password" endpoint yet, but when one is added: if the password is changed (e.g.
compromise recovery), all existing sessions should be invalidated. Currently SetPassword doesn't touch
the sessions table. This is a design gap to track now.

7. No HTTPS redirect — HTTP requests silently fail

internal/server/server.go — The server only listens on HTTPS. If a user mistypes http:// instead of
https://, they get a connection refused or a cryptic TLS error. There's no HTTP listener that redirects
to HTTPS. More importantly, if the server is placed behind an HTTP load balancer, cookies (including the
session cookie) could be sent in the clear.

Recommendation: Add an HTTP listener on port 80 (or port-1) that redirects to HTTPS. Or at minimum,
document that HTTPS is required.

8. Rate limiter is per-process, in-memory only

internal/server/ratelimit.go — The rate limiter uses an in-memory map. If the server restarts, all rate
limit state is lost. An attacker can trigger a restart (if they find a way) and get a fresh 10 attempts.
Also, the rate limiter counts both successful and failed login attempts equally — a successful login
shouldn't consume rate limit budget.

More critically: the rate limiter uses r.RemoteAddr which is the TCP peer. Behind a reverse proxy, this
is always the proxy's IP, making the rate limiter useless (all users share one limit) or trivially
bypassable (different source ports).

Recommendation: Support X-Forwarded-For / X-Real-IP when behind a trusted proxy (configurable). Don't
count successful logins against the limit.

9. docker/ directory may still contain stale files

Minor but worth checking: docker/Dockerfile.project was deleted, but is the docker/ directory itself
still present? If other files existed there, they could be confusing. Verified earlier it's gone — just
noting the check.

---

MEDIUM — Good Practice / Hardening

10. API key status leaks through timing

settings_handlers.go:17 — handleGetAPIKeyStatus calls pm.AnthropicKeySet() which checks m.anthropicKey
!= "". This is a string comparison on a potentially secret value. While the endpoint is behind auth, the
response ({"set": true/false}) is fine — but the actual check happens on the in-memory key, not the DB.
If SetAnthropicKey("") is called (delete), the DB key is gone but an attacker who somehow got a session
could observe the state change. This is low risk since the endpoint is authenticated.

11. No session limit per user

There's no cap on how many sessions can exist simultaneously. The sessions table grows unbounded until
CleanExpiredSessions runs (hourly). An attacker who knows the password could create millions of sessions
via rapid login requests. The rate limiter helps (10 per 5 min), but after each window they get 10 more
sessions that persist for 30 days.

Recommendation: Either limit total active sessions (e.g., 100) or revoke oldest session on new login.

12. ValidSession doesn't use constant-time comparison

auth/store.go:130-138 — ValidSession does a SQL lookup of the SHA-256 hash. The hash comparison happens
in SQLite (string equality in the WHERE clause), which is not constant-time. However, since the token is
already hashed with SHA-256 before comparison, timing side-channels on the hash reveal nothing about
the original token. This is actually fine — just noting the analysis.

13. CSP allows unsafe-inline for styles

middleware.go:14 — style-src 'self' 'unsafe-inline' allows inline styles. This is required because the
frontend uses inline style={} objects, so it's intentional. However, it weakens CSP protection against
CSS injection attacks. Low risk for this app since there's no user-generated content rendered as HTML.

14. No connect-src directive in CSP

middleware.go:14 — The CSP doesn't restrict connect-src, so it defaults to default-src 'self'. This is
actually fine — fetch() calls to /api/\* are same-origin. But explicitly adding connect-src 'self' would
be more defensive.

15. SQLite database file permissions

internal/db/db.go:36 — sql.Open("sqlite", dbPath) creates the file with default permissions. On most
systems this is 0644 (world-readable). The database contains password hashes, session token hashes, and
the Anthropic API key in plaintext. The data directory is created with 0700, which protects it, but the
file itself may be readable if permissions are loosened.

Recommendation: After opening, explicitly os.Chmod(dbPath, 0600).

16. Docker socket access = root equivalent

This is inherent to the architecture, not a bug, but worth documenting: any process that can talk to the
Docker socket (/var/run/docker.sock) effectively has root access to the host. If an attacker
compromises the appx process, they can create privileged containers, mount the host filesystem, etc.
This is the standard Docker security model and is acceptable for a self-hosted tool, but should be
documented.

17. No egress filtering on containers

container.go:204-244 — Containers are created on a bridge network with no egress restrictions. Claude
Code inside a container has unrestricted internet access. The egress_log table exists in the schema
(migration 1) suggesting this is a planned Phase 5 feature. Until then, containers can exfiltrate data
to any destination.

18. TLS private key file permissions

tls/selfsigned.go:87 — os.WriteFile(keyPath, keyPEM, 0600) — this is correct. Good.

19. Generated password entropy

auth/store.go:94-98 — 16 bytes = 128 bits of entropy, hex-encoded to 32 chars. This is strong. Good.

---

LOW / Informational

20. Error messages expose internal details

Several handlers return raw error messages to clients:

- project_handlers.go:49 — err.Error() for invalid name (includes regex pattern)
- project_handlers.go:53 — err.Error() for invalid port
- project_handlers.go:122 — err.Error() for no API key
- project_handlers.go:129 — err.Error() for Docker unavailable

These are all sentinel errors with fixed messages, so no dynamic internal state leaks. This is fine for
current code but be careful not to wrap these errors with internal details.

21. No CORS configuration

No CORS headers are set, which means the API is same-origin only. This is correct for an embedded SPA.
If a future external client needs access, CORS would need to be added carefully.

22. spaHandler path traversal

router.go:54-57 — The SPA handler strips the leading / and checks fs.Stat. This operates on an embed.FS
which is read-only and sandboxed — there's no path traversal risk. The http.FileServerFS also handles
this safely. No issue.

---

Summary Table

┌─────┬──────────┬─────────────────────────────────────────────┬───────────────────┐
│ # │ Severity │ Finding │ Status │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 1 │ CRITICAL │ API key stored plaintext in DB │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 2 │ CRITICAL │ Containers run as root, no resource limits │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 3 │ CRITICAL │ No request body size limit on most handlers │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 4 │ CRITICAL │ Data race on anthropicKey field │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 5 │ HIGH │ Password printed to stdout/logs │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 6 │ HIGH │ No session invalidation on password change │ Design gap │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 7 │ HIGH │ No HTTP→HTTPS redirect │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 8 │ HIGH │ Rate limiter ignores reverse proxy headers │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 9 │ — │ Stale docker/ directory │ Verified clean │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 10 │ MEDIUM │ API key timing leak (low risk, authed) │ Acceptable │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 11 │ MEDIUM │ No session count limit │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 12 │ — │ ValidSession timing (non-issue, SHA-256) │ No action │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 13 │ MEDIUM │ CSP unsafe-inline for styles │ Intentional │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 14 │ LOW │ No explicit connect-src in CSP │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 15 │ MEDIUM │ SQLite file permissions │ Open │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 16 │ — │ Docker socket = root (architectural) │ Document │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 17 │ MEDIUM │ No container egress filtering │ Planned (Phase 5) │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 18 │ — │ TLS key file permissions │ Correct │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 19 │ — │ Password entropy │ Correct │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 20 │ LOW │ Error messages (fixed strings, OK) │ Acceptable │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 21 │ — │ No CORS (correct for SPA) │ Correct │
├─────┼──────────┼─────────────────────────────────────────────┼───────────────────┤
│ 22 │ — │ SPA path traversal (embed.FS, safe) │ No issue │
└─────┴──────────┴─────────────────────────────────────────────┴───────────────────┘

The most impactful fixes would be #2 (container hardening), #3 (body size limits), and #4 (data race).
These are straightforward to implement and eliminate real exploitation vectors. Want me to fix any of
these?
