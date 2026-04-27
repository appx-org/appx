# TLS Certificates, Cookie Scoping, and sslip.io

This document explains how appx generates TLS certificates, how session cookies are scoped across subdomains, why bare IP addresses and `localhost` don't support subdomain routing, and how sslip.io solves the problem without a proper domain.

## How appx routes subdomains

Appx uses Host-header routing. When a request arrives:

1. If `Host == baseDomain` (or a registered alias) → serve the dashboard
2. If `Host == <name>.<baseDomain>` → reverse proxy to the project's assigned port
3. Otherwise → 404 "unknown host"

`baseDomain` is derived from `APPX_HOST` (env), `--host` (flag), or defaults to `localhost`.

## TLS certificate generation

On first startup (or when the cert expires within 7 days), appx generates a self-signed ECDSA P-256 certificate. The Subject Alternative Names (SANs) include:

- `localhost` and `*.localhost` (always)
- All non-loopback IPs from the machine's network interfaces (auto-detected)
- Any hostname passed via `--host` / `APPX_HOST`, plus `*.<hostname>` (wildcard)

The wildcard SAN is critical — without it, `https://assistum.example.com` would fail with a certificate error even though `https://example.com` is covered.

**Relevant code**: `internal/tls/selfsigned.go`, `collectSANs()`.

## Session cookie scoping

On login, appx sets an `appx_session` cookie with:

```
Domain=.<baseDomain>   SameSite=Lax   HttpOnly   Path=/
```

The leading-dot `Domain` attribute tells the browser to send the cookie to the base domain and all subdomains. This is how a login on `example.com` is shared with `assistum.example.com`.

**Relevant code**: `internal/server/server.go` (cookie config), `internal/auth/auth.go` (`SetSessionCookie`).

## Why bare IP addresses don't work

`assistum.91.98.144.204` is not a valid hostname — DNS doesn't support subdomain-like prefixes on IP addresses. The browser won't even attempt to resolve it (Chrome shows `about:blank#blocked`).

Additionally, RFC 6265 forbids setting `Domain` on cookies for IP addresses. The code detects this (`net.ParseIP` check in `server.go`) and omits the Domain attribute, making the cookie host-only. This is correct — there are no subdomains to share it with.

## Why localhost doesn't work for subdomain cookies

Modern browsers (per the Public Suffix List and RFC 6265) treat `localhost` as a top-level domain, similar to `.com`. Setting `Domain=.localhost` is rejected — the browser either drops the cookie or downgrades it to host-only (scoped to exactly `localhost`).

This means logging in on `localhost:8080` does **not** make the cookie available on `assistum.localhost:8080`. The auth middleware sees no cookie on subdomain requests and returns 401.

This only affects local development (`--http` mode). Production deployments with a proper domain or sslip.io are not affected.

### Browser behaviour summary

| Access URL | Cookie Domain | Subdomain cookie sharing | Works? |
|---|---|---|---|
| `example.com` | `.example.com` | Yes | Yes |
| `91.98.144.204` | (host-only, no Domain) | No subdomains exist | N/A |
| `localhost` | `.localhost` (rejected by browser) | No | No |
| `91.98.144.204.sslip.io` | `.91.98.144.204.sslip.io` | Yes | Yes |

## The sslip.io solution

[sslip.io](https://sslip.io) is a free DNS service that maps hostnames to embedded IP addresses:

```
91.98.144.204.sslip.io        → 91.98.144.204
assistum.91.98.144.204.sslip.io → 91.98.144.204
*.91.98.144.204.sslip.io      → 91.98.144.204
```

No DNS configuration or account needed. Using an sslip.io hostname as `APPX_HOST` provides:

1. **Valid subdomain routing** — `assistum.91.98.144.204.sslip.io` is a real hostname that resolves correctly
2. **Cookie sharing** — `Domain=.91.98.144.204.sslip.io` is a multi-label domain that browsers accept
3. **Wildcard TLS** — the cert includes `*.91.98.144.204.sslip.io` as a SAN

### Setup

Edit `/etc/appx/appx.env`:

```bash
APPX_HOST=91.98.144.204.sslip.io
```

Delete old certs and restart:

```bash
sudo rm /var/lib/appx/.appx-internals/{cert,key}.pem
sudo systemctl restart appx
```

### Trade-offs

- **Bare IP stops working**: `https://91.98.144.204` returns "unknown host" because `baseDomain` is now the sslip.io hostname. Access via `https://91.98.144.204.sslip.io` instead.
- **Depends on sslip.io DNS**: if sslip.io is down, DNS resolution fails. For production, use a proper domain with `APPX_DOMAIN` and Let's Encrypt.
- **Self-signed cert**: browsers still show a warning. Accept once for the base domain; the wildcard SAN covers all subdomains.

## Comparison of deployment modes

| Mode | `APPX_HOST` | Dashboard URL | Project URL | Cookie sharing |
|---|---|---|---|---|
| Local dev | (default `localhost`) | `localhost:8080` | `assistum.localhost:8080` | Broken (browser rejects `.localhost` cookies) |
| Remote, IP only | `91.98.144.204` | `https://91.98.144.204` | Not possible (no subdomains on IPs) | N/A |
| Remote, sslip.io | `91.98.144.204.sslip.io` | `https://91.98.144.204.sslip.io` | `https://assistum.91.98.144.204.sslip.io` | Works |
| Remote, own domain | `APPX_DOMAIN=app.example.com` | `https://app.example.com` | `https://assistum.app.example.com` | Works |
