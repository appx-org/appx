# Auth System — Architecture Reference

## Table of Contents

- [1. Overview](#1-overview)
- [2. System Map](#2-system-map)
  - [2.1 Component Relationships](#21-component-relationships)
  - [2.2 API Endpoints](#22-api-endpoints)
  - [2.3 Database Schema](#23-database-schema)
  - [2.4 Cookie Attributes](#24-cookie-attributes)
  - [2.5 Security Headers](#25-security-headers)
- [3. Code Review Guide](#3-code-review-guide)
  - [3.1 Database Layer — migrations/000001_initial.up.sql](#31-database-layer--migrations000001_initialupsql)
  - [3.2 Auth Store — internal/auth/store.go](#32-auth-store--internalauthstorrego)
  - [3.3 Auth Struct and Middleware — internal/auth/auth.go](#33-auth-struct-and-middleware--internalauthauthorgo)
  - [3.4 Rate Limiter — internal/server/ratelimit.go](#34-rate-limiter--internalserverratelimitgo)
  - [3.5 Security Headers and Body Limit — internal/server/middleware.go](#35-security-headers-and-body-limit--internalservermiddlewarego)
  - [3.6 HTTP Handlers — internal/server/auth_handlers.go](#36-http-handlers--internalserverauth_handlersgo)
  - [3.7 Router Wiring — internal/server/router.go](#37-router-wiring--internalserverroutergo)
  - [3.8 Server Startup — internal/server/server.go and cmd/appx/main.go](#38-server-startup--internalserverservergo-and-cmdappxmaingo)
  - [3.9 Frontend — web/src/pages/Login.tsx and web/src/api/client.ts](#39-frontend--websrcpageslogintsx-and-websrcapiclientts)
- [4. Testing Guide](#4-testing-guide)
  - [4.1 Automated Test Coverage](#41-automated-test-coverage)
  - [4.2 Manual Verification Checklist](#42-manual-verification-checklist)
- [5. Architecture and Code Pitfalls](#5-architecture-and-code-pitfalls)
- [6. Fixed Pitfalls](#6-fixed-pitfalls)
- [7. TODOs and Future Improvements](#7-todos-and-future-improvements)

---

## 1. Overview

Appx is a single-user, self-hosted tool. Its auth system is intentionally minimal: one password, session cookies, no user accounts or OAuth. The design priorities are correctness, simplicity, and defence-in-depth — each layer independently limits what an attacker can do if another layer is bypassed.

**The problem being solved.** The server exposes sensitive endpoints (project lifecycle, Anthropic API key management) over the public internet. It needs to authenticate the operator without relying on external identity providers, and without storing credentials in a form that is useful to an attacker who gains access to the database file.

**Key design decisions.**

1. *Password hashing with bcrypt.* bcrypt's intentional slowness makes offline dictionary attacks against a leaked hash expensive. The plaintext password is never written to disk after the first run.

2. *Session tokens stored as SHA-256 hashes.* Once a 256-bit random token is issued, only its hash is persisted. SHA-256 (not bcrypt) is correct here because the token already has full entropy — slowness is unnecessary, and it would penalise every authenticated request. If the database is compromised, the attacker gains 64-character hex hashes that cannot be reversed to valid tokens.

3. *Cookie-based sessions, not JWT or Bearer tokens.* `HttpOnly` cookies are invisible to JavaScript, providing defence against XSS. `SameSite=Strict` blocks cross-site request forgery without requiring a separate CSRF token. `Secure` ensures the cookie is never sent over plain HTTP. Together these three flags give equivalent CSRF protection to a double-submit cookie scheme, with less code.

4. *Rate limiting only on the login endpoint.* Protecting every endpoint with a rate limiter would add overhead and complexity. Only the login endpoint is exposed without authentication and is the only viable target for credential-stuffing. Authenticated endpoints are already protected by session validation.

5. *Session cleanup on a ticker, not on every request.* Expired sessions do not cause security vulnerabilities (they are checked against `expires_at` on every request), so cleanup is deferred to a background goroutine that runs hourly. This avoids a write on every read path.

**How the pieces fit together.** On first run, `main.go` generates a random password, bcrypt-hashes it, stores the hash in the `settings` table, and writes the plaintext to `data/initial_password` (mode `0600`). On subsequent runs the password check finds the hash and skips generation. When the operator submits the password via the login form, the handler calls `CheckPassword` (bcrypt comparison), creates a session token via `CreateSession` (random bytes → SHA-256 → `sessions` table), and sets the `appx_session` cookie. Every subsequent request to a protected route is intercepted by `auth.Middleware`, which reads the cookie, hashes it, and looks up the hash in `sessions`. On logout, the session row is deleted and the cookie is expired.

**Trade-offs accepted.** The single-password model means there is no per-user audit trail and no way to revoke a specific session without access to the database. This is appropriate for a single-operator tool. A multi-user model would require a `users` table, per-user password rows, and session ownership tracking — none of which is needed here.

---

## 2. System Map

### 2.1 Component Relationships

```
Browser
  │
  │  HTTPS (TLS 1.2+, self-signed ECDSA P-256)
  ▼
┌─────────────────────────────────────────────────────┐
│  net/http server                                    │
│                                                     │
│  securityHeaders (outermost middleware)             │
│    │                                                │
│    ▼                                                │
│  ServeMux                                           │
│    │                                                │
│    ├── POST /api/login  ──► limitBody               │
│    │                         └─► rateLimiter        │
│    │                               └─► handleLogin  │
│    │                                     │          │
│    │                               auth.Store       │
│    │                         (CheckPassword,        │
│    │                          CreateSession,        │
│    │                          SetSessionCookie)     │
│    │                                                │
│    └── /api/*  ──► limitBody                        │
│                     └─► auth.Middleware             │
│                           │  (reads appx_session    │
│                           │   cookie, ValidSession) │
│                           │                         │
│                           └─► protected API mux     │
│                                 ├── DELETE /api/session ─► handleLogout
│                                 └── (project, settings routes)
│                                                     │
│  auth.Store ◄─── SQLite sessions + settings tables  │
└─────────────────────────────────────────────────────┘
```

### 2.2 API Endpoints

| Method | Path | Auth | Request body | Response | Errors |
|--------|------|------|--------------|----------|--------|
| `POST` | `/api/login` | None | `{"password": "..."}` | `{"status": "ok"}` + `Set-Cookie` | 400 bad JSON, 401 wrong password, 429 rate limit |
| `DELETE` | `/api/session` | Required | — | `{"status": "ok"}` | 401 no/invalid session |

### 2.3 Database Schema

**`settings` table** — generic key-value store used for both the password hash and the Anthropic API key.

| Column | Type | Notes |
|--------|------|-------|
| `key` | `TEXT PRIMARY KEY` | e.g. `password_hash`, `anthropic_api_key` |
| `value` | `TEXT` | bcrypt hash for `password_hash`; plaintext for other keys |

**`sessions` table** — one row per active session.

| Column | Type | Notes |
|--------|------|-------|
| `token` | `TEXT PRIMARY KEY` | SHA-256 hex digest of the raw token |
| `created_at` | `DATETIME` | Defaults to `CURRENT_TIMESTAMP` |
| `expires_at` | `DATETIME` | `NOW + 30 days` at creation |

### 2.4 Cookie Attributes

| Attribute | Value | Why |
|-----------|-------|-----|
| `Name` | `appx_session` | Stable identifier for middleware lookup |
| `HttpOnly` | `true` | Inaccessible to JavaScript; blocks XSS token theft |
| `Secure` | `true` | Never sent over HTTP |
| `SameSite` | `Strict` | Blocks cross-origin form submissions (CSRF) |
| `MaxAge` | `2592000` (30 days) | Client-side expiry hint; server checks `expires_at` authoritatively |
| `Path` | `/` | Cookie sent on all paths |

### 2.5 Security Headers

Applied to every response via `securityHeaders` middleware:

| Header | Value |
|--------|-------|
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains` |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |

---

## 3. Code Review Guide

Files are presented in logical dependency order: database schema → data layer → middleware → handlers → wiring → frontend.

### 3.1 Database Layer — `migrations/000001_initial.up.sql`

**What it does.** Creates the `settings` and `sessions` tables (along with `projects` and `egress_log`). Both tables are created with `IF NOT EXISTS` so re-running migrations is safe.

**Key decisions.** `settings` uses `key TEXT PRIMARY KEY`, making upserts simple (`ON CONFLICT(key) DO UPDATE SET value = ?`). `sessions` uses the SHA-256 token hash as its primary key, so lookups are O(1) B-tree searches with no secondary index needed. The schema does not have a `user_id` column because appx is single-user.

**What to verify.**
- Is the `expires_at` column indexed? Currently it is not. Cleanup queries (`DELETE WHERE expires_at < ?`) scan the full table. For a single-user tool this is acceptable, but worth noting if session counts grow unexpectedly.
- No NOT NULL constraint on `expires_at` — a NULL value would cause `time.Now().Before(nil)` to never be reached because `Scan` would fail first. Acceptable but could be tighter.

### 3.2 Auth Store — `internal/auth/store.go`

**What it does.** All password and session persistence. Five password methods (`IsPasswordSet`, `SetPassword`, `CheckPassword`, `GeneratePassword`), three session methods (`CreateSession`, `ValidSession`, `DeleteSession`, `DeleteAllSessions`, `CleanExpiredSessions`), and three generic settings methods (`GetSetting`, `SetSetting`, `DeleteSetting`).

**Key decisions.**

*bcrypt for passwords, SHA-256 for session tokens.* This is the right split. bcrypt's cost is justified for low-entropy user passwords; it would be wasteful for 256-bit random tokens where collision resistance, not slowness, is needed.

*`hashToken` is unexported.* The only callers are within this package. The middleware passes the raw cookie value to `ValidSession`, which hashes it internally. This prevents accidental use of un-hashed tokens as storage keys.

*`DeleteAllSessions` errors are silently discarded.* `s.db.Exec("DELETE FROM sessions")` — the error return is ignored. For a destructive operation called on password change, this is a correctness risk: if the DELETE fails silently, old sessions remain valid. The same pattern appears in `DeleteSession` and `CleanExpiredSessions`.

*`GetSetting` returns `""` (not an error) for missing keys.* This is intentional — callers treat missing keys as "not configured." The trade-off is that there is no way to distinguish "key was explicitly set to empty string" from "key does not exist." This is fine for current use cases (the API key is validated to be non-empty before being stored) but could be confusing if new keys with meaningful empty values are added.

**What to verify.**
- Thread safety: each method opens its own query on `s.db`. `database/sql` connections are concurrency-safe at the pool level, so these are safe to call from concurrent goroutines.
- `CheckPassword`: if `sql.ErrNoRows` is returned (no password set), the function returns `false, sql.ErrNoRows`. The handler treats any error as a login failure, which is correct, but the error is silently swallowed. Could be logged for diagnostics.
- `CreateSession` inserts before returning the token. If the INSERT fails (e.g. token collision on PRIMARY KEY — astronomically unlikely for 256-bit tokens), the function returns an error and the handler returns 500. Correct.

### 3.3 Auth Struct and Middleware — `internal/auth/auth.go`

**What it does.** `Auth` is a thin wrapper around `Store` that adds HTTP-level concerns: middleware and cookie management. Keeping these separate from the store means the store can be tested without an HTTP layer.

**Key decisions.** `Middleware` returns `401` when the cookie is missing or invalid. It does not distinguish between "no cookie" (not logged in) and "cookie present but expired" (session aged out) — both return `401`. This is correct; the client should treat both identically and redirect to `/login`.

**What to verify.**
- `SetSessionCookie` sets `MaxAge` to `int(sessionDuration.Seconds())`. `sessionDuration` is 30 days = 2,592,000 seconds. This fits comfortably in an `int`. On a 32-bit platform (not relevant for a server, but worth noting) `int` is 32 bits and max is ~2.1 billion — well above 2.5 million, so no overflow.
- The cookie does not set a `Domain` attribute, meaning it is scoped to the exact host/port the browser used to load the page. This is correct for a single-domain tool.

### 3.4 Rate Limiter — `internal/server/ratelimit.go`

**What it does.** Sliding-window in-memory rate limiter, keyed by client IP. Configured at 10 attempts per 5-minute window for the login endpoint. A background goroutine (`cleanupLoop`) prunes stale entries every window duration.

**Key decisions.** The sliding window is implemented by storing a slice of `time.Time` values per IP and filtering out entries older than `window` on each `allow()` call. This is correct but has O(n) cost per request where n is the number of attempts within the window. For a max of 10 attempts, this is negligible.

**What to verify.**
- *IP spoofing*: `r.RemoteAddr` is used directly. If appx runs behind a reverse proxy (e.g. Caddy), `RemoteAddr` is the proxy's IP, not the real client IP. All login attempts would then share one bucket and could be rate-limited by a single legitimate user. If a proxy is used, the limiter should read `X-Forwarded-For` or `X-Real-IP`. Currently this is not done, and there is no proxy support wired up yet, so the risk is deferred but should be addressed before Phase 4 (reverse proxy).
- *Goroutine leak*: `newRateLimiter` starts a `cleanupLoop` goroutine but there is no shutdown mechanism. The goroutine runs for the lifetime of the process, which is acceptable for a server binary but would leak in tests that create multiple limiters. Tests call `setupTest` which calls `NewRouter` which creates a new `rateLimiter` on every call — each leaving a background goroutine. For short-lived tests this is harmless but worth noting.
- *Clock manipulation*: `allow()` and `cleanup()` both call `time.Now()`. Two goroutines could run simultaneously with no lock between the cleanup pruning and the allow check. The `sync.Mutex` is held for the full duration of both operations, so this is safe.

### 3.5 Security Headers and Body Limit — `internal/server/middleware.go`

**What it does.** Two middleware functions: `securityHeaders` adds five headers to every response; `limitBody` wraps `http.MaxBytesReader` to cap request bodies at 1 MB.

**Key decisions.** `securityHeaders` is applied at the outermost layer (wrapping the entire mux) so it covers every response including 4xx/5xx errors and static files. `limitBody` is applied to the API mux only, because the SPA file server handles its own read sizes.

**What to verify.**
- The CSP allows `'unsafe-inline'` for `style-src`. This is acceptable if no user-controlled content is rendered as styles, but is worth re-examining if the UI starts accepting user input that could reach style attributes.
- HSTS `max-age` is 63,072,000 seconds (2 years). Since appx uses a self-signed certificate, a browser that has pinned the HSTS policy and then encounters a different self-signed cert will refuse to connect. For a development/personal tool this is acceptable but could cause confusion on certificate renewal.

### 3.6 HTTP Handlers — `internal/server/auth_handlers.go`

**What it does.** Two handlers: `handleLogin` and `handleLogout`.

`handleLogin`:
1. Decodes `{"password": "..."}` from the request body.
2. Calls `a.Store.CheckPassword` — bcrypt comparison.
3. On success, calls `a.Store.CreateSession` and `a.SetSessionCookie`.
4. Returns `{"status": "ok"}`.

`handleLogout`:
1. Reads the `appx_session` cookie.
2. Calls `a.Store.DeleteSession` to remove the row.
3. Sets a `MaxAge: -1` cookie to expire the cookie on the client.
4. Returns `{"status": "ok"}`.

**Key decisions.** The login handler treats both `err != nil` and `!ok` from `CheckPassword` as `401`. This prevents timing-side-channel information about whether the password hash exists vs. is wrong — both cases return the same error. However, a timing difference still exists: if no password hash is set, `CheckPassword` returns immediately with `sql.ErrNoRows`, while if the hash exists, bcrypt runs for ~100ms. A constant-time code path is not implemented, but this is a low-risk issue because on a correctly-configured server the password is always set.

**What to verify.**
- `handleLogout` does not require the cookie to be present (`err == nil` guard). If the cookie is absent, it still sets a `MaxAge: -1` cookie and returns 200. This is harmless — a client that has already lost its cookie just gets a no-op response.
- The logout handler is behind `auth.Middleware`, so the cookie must be valid to reach the handler. Deleting the session inside the handler is therefore safe (the session is confirmed valid before deletion).
- `handleLogin` does not call `DeleteAllSessions` on successful login. If someone knows the old password and obtains a session, changing the password (if that feature is added) would need to explicitly invalidate old sessions. `DeleteAllSessions` exists for this purpose but is not called from any handler currently.

### 3.7 Router Wiring — `internal/server/router.go`

**What it does.** Registers all routes and wires middleware in the correct order.

```
securityHeaders
  └── mux
        ├── POST /api/login ─► limitBody ─► rateLimiter ─► handleLogin
        └── /api/*          ─► limitBody ─► auth.Middleware ─► api mux
                                                                  └── DELETE /api/session ─► handleLogout
                                                                  └── (other protected routes)
```

**Key decisions.** The protected `api` sub-mux is nested inside `mux.Handle("/api/", ...)` rather than registering individual routes with auth wrappers. This means every route registered on `api` automatically requires authentication — it is impossible to accidentally add a route to `api` that bypasses auth. New handlers must be registered on `api`, not `mux`, to be protected. The comment in the source makes this explicit.

`POST /api/login` is registered on the outer `mux` without auth middleware, but *with* `limitBody` and `rateLimiter`. This is the only unauthenticated write endpoint.

**What to verify.**
- Method specificity: `POST /api/login` is registered with an explicit method prefix (Go 1.22+ routing). Requests to `GET /api/login` will fall through to the `/api/` handler, which requires auth, and return 401. This is correct — there is no login form at a GET endpoint.
- The `spaHandler` falls through to `index.html` for any path that does not match a real file. This means `GET /api/anything-unknown` with no cookie returns the SPA HTML, not 404. This could confuse API clients that send requests to unknown endpoints without a cookie. In practice, any real API endpoint is registered and will return 401 from the middleware before the file server is consulted.

### 3.8 Server Startup — `internal/server/server.go` and `cmd/appx/main.go`

**What it does.** `server.Run` wires `auth.New(cfg.AuthStore)`, calls `CleanExpiredSessions` at startup, starts the hourly cleanup ticker, and builds the router. `main.go` handles first-run password generation, writing the initial password to `data/initial_password` (mode `0600`).

**Key decisions.** The initial password file is written with `0600` permissions (owner-read-only) and is separate from the database, so the operator can delete it after reading. The message printed to stderr (`"Use this to log in. Delete the file after reading it."`) prompts this action. The database file itself is also `chmod`'d to `0600` in `db.Open` after SQLite creates it.

**What to verify.**
- `main.go` writes the initial password to `data/initial_password` and prints a notice to `stderr`. It does not print the password itself to `stdout` or `stderr`. An attacker with access to process logs would not see the password.
- `CleanExpiredSessions()` is called synchronously at startup before the server begins accepting connections. Errors from cleanup are silently discarded (the `Exec` error return is ignored in `CleanExpiredSessions`). This is consistent with the rest of the session management but means a database error during cleanup is invisible.
- The hourly ticker goroutine is started with `defer cleanupTicker.Stop()` in `Run`. When `Run` returns (on shutdown), the ticker stops and the goroutine exits on the next tick. There is no explicit `ctx.Done()` signal to the goroutine, so it may fire once after shutdown is initiated but before the ticker is stopped. This is harmless for a DELETE operation.

### 3.9 Frontend — `web/src/pages/Login.tsx` and `web/src/api/client.ts`

**What it does.** `Login.tsx` renders a centered password form. On submit it calls `login(password)` from the API client. On success it navigates to `/`. On failure it shows "Invalid password". The `client.ts` `request<T>()` helper checks `res.ok` and on 401 throws with the error body text; the caller catches this and shows a generic message. The cookie is set server-side — the frontend never sees or stores the session token.

**Key decisions.** The frontend does not intercept 401 globally and redirect to `/login` at the `client.ts` level. A 401 on the login endpoint itself would be caught by the form's catch block. For other endpoints (project list, settings), a 401 results in the thrown error propagating to the calling component. `App.tsx` may or may not handle this — worth reviewing if a global auth-error handler is needed.

**What to verify.**
- The password input uses `type="password"`, which prevents autocomplete from leaking the value in form history.
- The form does not implement its own rate limiting. Rate limiting is entirely server-side, which is correct.
- There is no "forgot password" or "change password" UI. The only recovery path is editing the database directly or deleting `data/appx.db` and restarting.

---

## 4. Testing Guide

### 4.1 Automated Test Coverage

**`internal/auth/store_test.go`**

Sets up an in-memory SQLite database manually (no migration runner) to isolate auth tests from migration changes. Key scenarios:

| Test | What it verifies |
|------|-----------------|
| `TestIsPasswordSet_Empty` | Fresh DB reports no password |
| `TestSetAndCheckPassword` | Round-trip: set → check correct → check wrong |
| `TestSetPassword_Overwrite` | Second `SetPassword` replaces first; old password rejected |
| `TestGeneratePassword` | 32-char hex output; two calls produce different values |
| `TestCreateAndValidateSession` | 64-char hex token; `ValidSession` true immediately after creation; false for unknown token |
| `TestValidSession_Expired` | Directly inserts a row with past `expires_at`; `ValidSession` returns false |
| `TestDeleteAllSessions` | Two sessions created; after `DeleteAllSessions` both are invalid |
| `TestCleanExpiredSessions` | One valid + one expired session; after cleanup, valid survives and expired is gone |

**Coverage gaps.** `DeleteSession` (single-session deletion) has no direct test — it is exercised indirectly by `TestLogout_ClearsSession` in `router_test.go`. `GetSetting`, `SetSetting`, and `DeleteSetting` have no direct unit tests; they are covered indirectly by the settings handler tests.

**`internal/server/router_test.go`**

Uses `setupTest()` which creates a full router with in-memory SQLite and a nil Docker client. Auth-specific scenarios:

| Test | What it verifies |
|------|-----------------|
| `TestLogin_Success` | 200 response; `appx_session` cookie set with `HttpOnly` and `Secure` |
| `TestLogin_WrongPassword` | 401 on bad password |
| `TestLogin_BadJSON` | 400 on malformed body |
| `TestProtectedRoute_NoAuth` | 401 on missing cookie |
| `TestProtectedRoute_WithAuth` | 200 with valid session cookie |
| `TestProtectedRoute_InvalidSession` | 401 on invalid/bogus token |
| `TestLogout_ClearsSession` | Session valid before logout; 401 after logout with same token |
| `TestSecurityHeaders` | All five security headers present on every response |
| `TestRateLimit_Login` | 11th failed attempt receives 429 |
| `TestSPA_FallbackToIndex` | Unknown path returns `index.html` (SPA routing) |

**Coverage gaps.** No test for `SameSite` attribute on the session cookie. No test for the `MaxAge` value on the logout cookie (`-1`). No test for concurrent login attempts (race detector coverage). No test for the initial-password generation path in `main.go`.

### 4.2 Manual Verification Checklist

```
[ ] 1. First run — fresh database:
        rm -rf ./data && ./appx -port 8443
        Observe: "Initial password written to data/initial_password" on stderr.
        cat data/initial_password — should contain a 32-char hex string.
        ls -l data/initial_password — permissions should be -rw-------.
        ls -l data/appx.db — permissions should be -rw-------.

[ ] 2. Login with initial password:
        curl -kc cookies.txt -X POST https://localhost:8443/api/login \
          -H 'Content-Type: application/json' \
          -d '{"password":"<contents of initial_password>"}'
        Expect: 200, {"status":"ok"}, Set-Cookie header with
        appx_session; HttpOnly; Secure; SameSite=Strict.

[ ] 3. Wrong password:
        curl -k -X POST https://localhost:8443/api/login \
          -H 'Content-Type: application/json' \
          -d '{"password":"wrong"}'
        Expect: 401.

[ ] 4. Rate limiting:
        Run the wrong-password curl 11 times in quick succession.
        The 11th attempt should return 429.

[ ] 5. Protected route without session:
        curl -k https://localhost:8443/api/projects
        Expect: 401.

[ ] 6. Protected route with valid session:
        curl -kb cookies.txt https://localhost:8443/api/projects
        Expect: 200, empty array [].

[ ] 7. Security headers:
        curl -kv https://localhost:8443/ 2>&1 | grep -E 'Strict-Transport|X-Frame|X-Content|Content-Security|Referrer'
        Expect: all five headers present.

[ ] 8. Logout:
        curl -kb cookies.txt -X DELETE https://localhost:8443/api/session
        Expect: 200, {"status":"ok"}, Set-Cookie: appx_session=; MaxAge=-1 (or Max-Age=0).

[ ] 9. Session invalidated after logout:
        curl -kb cookies.txt https://localhost:8443/api/projects
        Expect: 401 (cookie was just cleared).

[ ] 10. Session persistence across restart:
         Log in, obtain a cookie, restart the server, use the same cookie.
         Expect: still authenticated (sessions survive process restart via SQLite).

[ ] 11. Second run — password not regenerated:
         Kill and restart the server.
         Expect: no "Initial password written" message on stderr.

[ ] 12. Verify database file security:
         ls -l data/appx.db
         Expect: permissions -rw-------.
         sqlite3 data/appx.db "SELECT key, value FROM settings WHERE key = 'password_hash'"
         Expect: value starts with "$2a$" (bcrypt format).
         sqlite3 data/appx.db "SELECT token FROM sessions"
         Expect: 64-char hex strings (SHA-256 hashes, not raw tokens).
```

---

## 5. Architecture and Code Pitfalls

**Pitfall 1 — Silent errors from session mutation operations**

- **Location:** `internal/auth/store.go` — `DeleteSession`, `DeleteAllSessions`, `CleanExpiredSessions`
- **The problem:** All three methods call `s.db.Exec(...)` and discard the error return. If the database is locked, corrupt, or has a schema mismatch, the deletion silently fails. The caller has no indication that sessions were not deleted.
- **Why it matters:** For `DeleteAllSessions` (called when a password is changed) a silent failure means old sessions remain valid after a credential rotation. Severity: **high** for `DeleteAllSessions`; **low** for cleanup.
- **Fix:** At minimum, log errors from `DeleteSession` and `DeleteAllSessions`. For `DeleteAllSessions`, consider propagating the error to the caller so the password change can be aborted or the operator alerted.

**Pitfall 2 — Rate limiter does not account for reverse proxy**

- **Location:** `internal/server/ratelimit.go`, line 96 — `net.SplitHostPort(r.RemoteAddr)`
- **The problem:** If appx is placed behind a reverse proxy, all requests arrive from the proxy's IP. The rate limiter sees one IP for all clients, so 10 failed attempts from different real clients collectively exhaust the limit.
- **Why it matters:** A legitimate operator could be locked out by a minor attack volume. Severity: **medium** if a proxy is used (not currently wired up, deferred to Phase 4).
- **Fix:** Read `X-Forwarded-For` or `X-Real-IP` when present, with a configurable "trust proxy" flag. Without such a flag, the header must not be trusted (it can be spoofed by clients that connect directly).

**Pitfall 3 — Rate limiter goroutine leak in tests**

- **Location:** `internal/server/ratelimit.go`, `newRateLimiter` — `go rl.cleanupLoop()`
- **The problem:** Each call to `newRateLimiter` starts a goroutine that runs until the process exits. `setupTest()` in `router_test.go` calls `NewRouter()` on every test, which creates a new limiter. With many tests, this accumulates background goroutines.
- **Why it matters:** No functional impact; tests pass. The goroutines each hold a ticker and a small map. Severity: **low** (test-time resource leak only).
- **Fix:** Accept a `context.Context` in `newRateLimiter` or add a `Stop()` method to terminate `cleanupLoop`. Call `Stop()` in test cleanup.

**Pitfall 4 — No session index on `expires_at`**

- **Location:** `internal/db/migrations/000001_initial.up.sql`
- **The problem:** `CleanExpiredSessions` performs a full table scan on `sessions` (`DELETE WHERE expires_at < ?`). `ValidSession` performs a primary key lookup but then reads `expires_at` in the same row — this is fast.
- **Why it matters:** For a single-user tool, the session table will rarely exceed tens of rows. Severity: **low** in practice, but would matter if sessions were issued at high volume (e.g. an automated test suite that creates thousands of sessions).
- **Fix:** `CREATE INDEX idx_sessions_expires ON sessions(expires_at)` in a migration. Not urgent.

**Pitfall 5 — No change-password endpoint**

- **Location:** Absent from `internal/server/router.go` and handlers
- **The problem:** There is no API endpoint or UI to change the password. The only mechanism is direct database manipulation or deleting `data/appx.db` (which also destroys all projects and sessions).
- **Why it matters:** An operator who suspects their initial password was observed has no in-app remediation path. Severity: **medium** for a production deployment.
- **Fix:** Add `PUT /api/settings/password` that accepts `{"current": "...", "new": "..."}`, validates the current password, sets the new hash, and calls `DeleteAllSessions` to invalidate existing sessions.

**Pitfall 6 — Timing difference between "no password hash" and "wrong password"**

- **Location:** `internal/auth/store.go`, `CheckPassword`; `internal/server/auth_handlers.go`, `handleLogin`
- **The problem:** If no password hash exists in the database (`sql.ErrNoRows`), `CheckPassword` returns immediately. If a hash exists but is wrong, bcrypt runs for ~100ms. A timing oracle could reveal whether a password hash has been set.
- **Why it matters:** On a correctly configured server, the password is always set. Severity: **low** — exploitable only before first-run setup is complete.
- **Fix:** If `sql.ErrNoRows`, perform a dummy `bcrypt.CompareHashAndPassword` against a fixed dummy hash to equalise timing. Not urgent.

---

## 6. Fixed Pitfalls

> **Problem:** Session tokens stored as raw hex in the database would expose valid credentials if the database file was read by an attacker (e.g. via a backup, a directory traversal vulnerability in a future feature, or physical access).
> **Fix:** Only the SHA-256 hash of each token is persisted. The raw token is held in memory only long enough to set the `Set-Cookie` header. This is documented explicitly in the package comment and in the code comment on `hashToken`.

> **Problem:** bcrypt used for session token storage would add ~100ms latency to every authenticated request, since the middleware would need to bcrypt-hash the cookie on each call.
> **Fix:** SHA-256 is used for session tokens (O(microseconds) per lookup), while bcrypt is reserved for the low-entropy user password. The package comment explains this split explicitly to prevent future developers from "upgrading" session storage to bcrypt.

> **Problem:** Plaintext password visible in logs or crash dumps if printed to `stdout`/`stderr` during first-run setup.
> **Fix:** The initial password is written to a `0600` file (`data/initial_password`) and never printed to any stream. Only the file path is printed to `stderr`.

> **Problem:** Unbounded growth of the `sessions` table if cleanup is never run.
> **Fix:** `CleanExpiredSessions` is called at startup and every hour via a ticker goroutine. Expired sessions are removed even if no one logs in or out.

---

## 7. TODOs and Future Improvements

**Explicit TODOs in code.**

| File | Line | Note |
|------|------|------|
| `cmd/appx/main.go` | 98 | `TODO: start with Anthropic key but be flexible to add other Coding Agent providers in the future (Codex, OpenCode, Gemini etc)` — not auth-related but lives in the same startup path |

**Known limitations accepted as deliberate trade-offs.**

- *Single password, no multi-user support.* Appx is a single-operator tool. Adding users would require a `users` table, per-user sessions, and role-based access control. Deferred indefinitely; not a design goal.
- *No "remember me" toggle.* Sessions always last 30 days. Shorter sessions (e.g. browser-session cookies with `MaxAge: 0`) would be more secure on shared machines but reduce convenience on personal servers. Deferred.
- *Self-signed TLS.* Browsers warn on first visit. Operators can use Caddy or another terminator with Let's Encrypt certs for a cleaner experience. This is called out in the roadmap (Phase 6).

**Deferred work for future phases.**

- *Change-password endpoint* (see Pitfall 5) — needed before Phase 6 (installer/public exposure).
- *Proxy-aware rate limiting* (see Pitfall 2) — needed before Phase 4 (reverse proxy).
- *Global 401 handler in the frontend* — currently each component surfaces its own error message on auth failure. A global interceptor in `client.ts` that catches 401 and redirects to `/login` would be cleaner.
- *Session activity refresh* — sessions expire on a fixed 30-day wall-clock timer regardless of activity. A "refresh on use" approach (extending `expires_at` on each valid request) would keep active operators logged in indefinitely while still expiring idle sessions.
