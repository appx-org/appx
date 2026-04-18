# Security Review 2: Appx

**Date:** 2026-04-06
**Reviewer:** Internal security audit
**Scope:** Full codebase — `phase-4-proxy` branch, post-service-worker-proxy implementation
**Threat model:** Self-hosted single-user tool. Attacker is (a) an external party gaining unauthorized access, (b) a malicious/compromised AI agent running inside a container, or (c) a supply chain attacker targeting the build.

---

## Executive Summary

The codebase has matured significantly from the first review. Most previous findings were addressed: body limits, timeouts, logging, cookie hardening. (The opencode loopback binding was reverted — Docker port publishing requires 0.0.0.0; the host-side 127.0.0.1 binding provides the actual isolation.) The service worker proxy is a sound architectural choice — it eliminates the brittle JS bundle rewriting while keeping same-origin semantics. The main new concerns are: container environment variables expose secrets to anyone with Docker socket access, the SW's 401 handler can mis-trigger on container-level authentication failures, and `SetPassword` has no minimum length validation.

---

## Findings

### FINDING-01: Container secrets exposed to anyone with Docker socket access

**Severity:** Medium
**Location:** `internal/project/container.go:355-362` (env var injection in `doFullCreate`)

```go
env = append(env, "ANTHROPIC_API_KEY="+key)
env = append(env, "OPENCODE_SERVER_PASSWORD="+proj.ContainerSecret)
```

Both the Anthropic API key and the per-container secret are injected as Docker environment variables. Anyone who can run `docker inspect <container-id>` on the host can read these values in plaintext from the container's environment. On a developer machine this is the same user, so the risk is low. On a shared server, any OS user who can connect to the Docker socket (typically `root` or members of the `docker` group) can read all container secrets.

This is an inherent limitation of Docker environment variable secrets — they are not encrypted at rest. The Docker best practice for sensitive values is Docker secrets (Swarm mode) or bind-mounted secret files. For this self-hosted single-user tool, the threat model doesn't require a fix, but operators deploying on shared infrastructure should be aware.

**Fix:** Document this limitation. If stronger isolation is needed in future, pass secrets via a tmpfs-mounted file instead of environment variables:
```dockerfile
# In entrypoint: read secret from file, not env
OPENCODE_SERVER_PASSWORD=$(cat /run/secrets/opencode_password)
```

**No test needed** — this is an operational limitation, not a code bug.

---

### FINDING-02: SW 401 handler mis-triggers on container-level auth failures

**Severity:** Medium
**Location:** `internal/proxy/proxy.go` (in `generateSWScript`)

```javascript
e.respondWith(fetch(new Request(rewritten,init)).then(resp=>{
  if(resp.status===401){
    clients.matchAll({type:"window"}).then(cs=>
      cs.forEach(c=>c.navigate("/login"))
    );
  }
  return resp;
}));
```

The SW redirects to `/login` on any 401 response from `/api/agent/:name/*`. However, the container's `opencode serve` also returns 401 if the Basic Auth (`OPENCODE_SERVER_PASSWORD`) doesn't match. In the (rare) case where the container secret is out of sync with the database — for example, after a `Reset` that rotated the secret but before the container was restarted — requests to the container return 401, and the user is incorrectly redirected to login even though their appx session is valid.

The appx server's auth middleware returns 401 with body `"unauthorized\n"`. The container's OpenCode returns a JSON 401 or an HTML page. The SW could distinguish these, but it currently treats all 401s identically.

**Fix:** Distinguish appx auth failures from container auth failures. One approach: appx adds a custom header on its own 401s:

In `auth.go`:
```go
w.Header().Set("X-Appx-Auth", "required")
http.Error(w, "unauthorized", http.StatusUnauthorized)
```

In the SW:
```javascript
if(resp.status===401 && resp.headers.get("X-Appx-Auth")==="required"){
  // Only redirect for appx auth failures, not container auth failures
  clients.matchAll(...).then(cs=>cs.forEach(c=>c.navigate("/login")));
}
```

**Suggested Tests:**
- Verify that a 401 with `X-Appx-Auth: required` triggers a login redirect
- Verify that a 401 without that header does NOT trigger a redirect

---

### FINDING-03: No minimum password length or complexity validation

**Severity:** Medium
**Location:** `internal/auth/store.go:68-78` (`SetPassword`)

```go
func (s *Store) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
```

`SetPassword` accepts any string, including a single character. There is no password change UI in the current codebase (the only entry point is the auto-generated initial password), so in practice this can only be exploited by someone who already has shell access to the server. Still, if a password-change endpoint is added in a future phase, this validation gap would become exploitable.

The auto-generated initial password (`GeneratePassword()`) is a 32-char hex string (16 random bytes = 128 bits), which is excellent. The gap is in the policy enforcement for user-supplied passwords.

**Fix:** Add validation in `SetPassword`:
```go
func (s *Store) SetPassword(password string) error {
    if len(password) < 12 {
        return fmt.Errorf("password must be at least 12 characters")
    }
    hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
```

**Suggested Tests:**
- `SetPassword("")` returns an error
- `SetPassword("short")` returns an error  
- `SetPassword("at-least-12-chars")` succeeds

---

### FINDING-04: Initial password file is created but never cleaned up

**Severity:** Low
**Location:** `cmd/appx/main.go:83-88`

```go
pwFile := filepath.Join(*dataDir, "initial_password")
if err := os.WriteFile(pwFile, []byte(pw+"\n"), 0600); err != nil {
    log.Fatalf("write password file: %v", err)
}
fmt.Fprintf(os.Stderr, "Initial password: %s\n", pw)
fmt.Fprintf(os.Stderr, "Also written to %s — delete this file after logging in.\n", pwFile)
```

The file is created with 0600 permissions and the user is told to delete it, but it remains on disk indefinitely until the user acts. If the user never changes their password (uses the auto-generated one), the password is permanently stored in plaintext at `data/initial_password`.

**Fix:** Delete the file after printing, or overwrite it with zeros after first successful login:

```go
// Delete the file after printing so it doesn't persist
os.WriteFile(pwFile, []byte(pw+"\n"), 0600)
fmt.Fprintf(os.Stderr, "Initial password: %s\n", pw)
// Immediately delete — it's printed to stderr already
if err := os.Remove(pwFile); err != nil {
    log.Printf("warning: could not remove %s: %v", pwFile, err)
}
```

**Suggested Tests:**
- Verify the file is removed after the initial password is printed (or document that it's expected to remain for user convenience)

---

### FINDING-05: `bcrypt.DefaultCost` (10) is on the conservative end for 2026

**Severity:** Low
**Location:** `internal/auth/store.go:69`

```go
hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
```

`bcrypt.DefaultCost` is 10. This was the standard in 2012 and is still widely used, but modern guidance (OWASP 2023) recommends cost 12+ as hardware has improved. At cost 10, an attacker with a GPU can attempt ~10,000 passwords/second. At cost 12, ~2,500/second. The 128-bit random auto-generated password makes this moot for the initial password (it's unguessable regardless of cost), but if a user sets a weak password, the lower cost provides less protection.

**Fix:** Raise the cost:
```go
const bcryptCost = 12 // OWASP 2023 recommendation

hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
```

Note: existing hashes will continue to work — `bcrypt.CompareHashAndPassword` reads the cost from the stored hash. New logins will use the new cost.

**Suggested Tests:**
- Verify that a password hashed with cost 12 still validates correctly
- Verify that a password hashed with the old cost 10 still validates (backward compatibility)

---

### FINDING-06: `build-essential` enables attack binary compilation in containers

**Severity:** Medium  
**Location:** `internal/project/Dockerfile.project:4-8`

```dockerfile
RUN apt-get update && apt-get install -y \
    git curl build-essential ca-certificates tmux ripgrep \
    && rm -rf /var/lib/apt/lists/*
```

`build-essential` provides `gcc`, `g++`, `make`, and related tools. Combined with unrestricted outbound internet, a prompt-injected or malicious AI agent can download, compile, and execute arbitrary native code inside the container. Even without `build-essential`, a Node.js runtime is present and can execute arbitrary JS. Removing `build-essential` reduces the attack surface slightly.

Whether `build-essential` is actually needed by OpenCode or typical AI agent operations is worth investigating. If it's only needed for native npm packages, a build-stage multi-stage Dockerfile could compile them and produce a smaller, less-equipped runtime image.

**This is deferred to Phase 5 alongside Dockerfile pinning.** Already noted in `docs/plans/phase_5_plan.md`.

---

### FINDING-07: HSTS `includeSubDomains` could affect development setups

**Severity:** Low
**Location:** `internal/server/middleware.go:25`

```go
w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
```

The 2-year HSTS header includes `includeSubDomains`. While browsers exempt `localhost` from HSTS processing, this header is also sent in production deployments with `--domain`. In production, it means ALL subdomains of the configured domain are forced to HTTPS for 2 years. If the user later deploys an HTTP service on a subdomain (e.g., `test.example.com`), their browsers will refuse to connect to it over HTTP for up to 2 years.

For appx's intended use case (the domain is appx's own, subdomains are project containers served over HTTPS by appx), this is correct. But operators should be aware of the implication before pointing a shared domain at appx.

**No code change needed** — this is operational documentation. Consider adding a note to CLAUDE.md or the deployment docs.

---

## Non-Issues (Explicitly Verified)

The following concerns were investigated and found to be **not vulnerabilities**:

- **SQL injection**: All queries use parameterized statements. No string interpolation.
- **Session token storage**: SHA-256 hash only, never the raw token.
- **CSRF**: `requireJSON` middleware (Content-Type: application/json required for all state-changing API requests) + `SameSite=Strict` cookie = adequate protection. A CSRF attacker cannot set `Content-Type: application/json` from an HTML form.
- **Project name injection**: Validated as `^[a-z][a-z0-9-]{0,61}[a-z0-9]$` before use in Docker API calls (not shell commands). SW and HTML script injection safe because slugs contain no special characters.
- **Path traversal**: No file system access based on request paths. All paths are Docker API names or URL routes.
- **Container privilege escalation**: `CapDrop: ALL`, `no-new-privileges:true`, `ReadonlyRootfs: true`, non-root user. Correct.
- **WebSocket hijacking**: Origin check `parsed.Host == r.Host` is correct for same-origin validation on non-standard ports.
- **Session expiry during SW operation**: SW correctly detects appx 401 (with caveat from FINDING-02) and redirects to login.
- **Cookie stripping**: Session cookie is stripped in both `AgentAPIHandler` (explicit `r.Header.Del`) and in `newReverseProxy`'s Director. The double stripping is harmless and correct — the explicit strip covers the WebSocket path before the reverse proxy runs.
- **Asset cache race conditions**: `AssetCache` uses `sync.RWMutex` on all reads/writes. Thread-safe.
- **Port cache race conditions**: `portCacheMu sync.RWMutex` protects `portCache`. Thread-safe.
- **fetchClient lacks a cookie jar**: Server-side asset fetches to the container correctly do not include the `appx_session` cookie (no cookie jar configured). The `Authorization: Basic` header is injected explicitly. Correct.

---

## Fixed Since Previous Review

For reference, all findings from the first security review (`docs/security/security_review_2026-04-06.md`) have been addressed:

| Finding | Status |
|---------|--------|
| F02: Unbounded proxy body + WS tunnel | Fixed — 100MB limit + deadlineConn |
| F03: Cookie domain hardcoded | Fixed — back to SameSite=Strict, no Domain |
| F05: Initial password file note | Improved — prominent message |
| F07: Ring buffer replay | Fixed — 64KB cap |
| F08: opencode 0.0.0.0 | Reverted — 127.0.0.1 broke Docker port publishing (EOF). The host-side port binding to 127.0.0.1 provides the actual network isolation. |
| F09: TLS SANs logged | Fixed |
| F10: No terminal idle timeout | Fixed — 30 min |
| F11: No auth logging | Fixed |
| F01: curl\|bash Dockerfile | Deferred to Phase 5 |
| F04: Plaintext API key | Documented, accepted |
| F06: Unrestricted container egress | Deferred to Phase 5 |
