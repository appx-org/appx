# Security Guide

Deployment and operational security practices for appx.

## Cloud Firewall (Required)

Appx serves everything through a single HTTPS port (default 443). Agent-built apps run as native processes on ports 10000-10999 on the host. These apps are **not** network-isolated — if they bind to `0.0.0.0`, they are reachable on every interface.

**Always configure a cloud firewall that only allows inbound traffic to the ports appx needs.** This ensures all app traffic is forced through appx's authenticated reverse proxy.

### Recommended inbound rules

| Protocol | Port | Purpose |
|----------|------|---------|
| TCP | 22 | SSH access |
| TCP | 443 | Appx (HTTPS + subdomain proxy) |
| ICMP | — | Ping (optional, useful for diagnostics) |

**Do not open ports 10000-10999.** Even though appx proxies to these ports internally via `127.0.0.1`, the agent-built dev servers may bind to all interfaces (`0.0.0.0`) depending on the framework defaults. The cloud firewall blocks external access regardless of how the app binds.

This applies to all major cloud providers:
- **DigitalOcean:** Networking > Firewalls
- **AWS:** Security Groups on the EC2 instance
- **GCP:** VPC firewall rules
- **Hetzner:** Firewalls in Cloud Console

### Why not just bind to localhost?

The `AGENTS.md` scaffold instructs agents to use the assigned port, but the AI agent ultimately decides how to start the dev server. Some frameworks default to `0.0.0.0` (Express, Flask), others to `127.0.0.1` (Vite, Next.js). The cloud firewall is the reliable control — it works regardless of what the agent does.

### Defense in depth

Phase 6 plans to add host-level iptables/nftables rules that block external access to 10000-10999 as a second layer, so the system is safe even without a cloud firewall. Until then, the cloud firewall is the primary control.

## Initial Password

On first run, appx generates a random 32-character password and writes it to `data/initial_password` (permissions 0600). **Delete this file after logging in.** The password is also printed to stderr on startup.

## HSTS and Shared Domains

The HSTS header includes `includeSubDomains` with a 2-year max-age. When deploying with `--domain example.com`, this forces all subdomains of `example.com` to HTTPS in browsers that visit appx. Do not point appx at a shared domain that also hosts HTTP services on subdomains.

## TLS Modes

| Mode | Flag | Certificate | Use case |
|------|------|-------------|----------|
| Self-signed | (default) | Auto-generated ECDSA P-256, 365-day, auto-renewed | Personal/LAN use |
| Let's Encrypt | `--domain` + `CLOUDFLARE_API_TOKEN` | Automatic via CertMagic + Cloudflare DNS-01 | Public deployment |
| HTTP (dev only) | `--http` | None | Local development only, binds to 127.0.0.1 |

All TLS modes enforce TLS 1.3 minimum.
