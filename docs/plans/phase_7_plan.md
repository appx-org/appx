# Phase 7 Plan: Hosted Service — Digital Fortress Model

**Date:** 2026-04-06
**Status:** Draft
**Scope:** Multi-tenant hosted offering where each user gets a dedicated server

---

## Vision: The Digital Fortress

Every user gets their own private server — not a slice of shared infrastructure, but a complete isolated machine that holds everything:

- Their AI coding agent projects
- Their deployed web apps
- Their data and persistent storage
- Their AI models (local LLMs via Ollama, not just API calls)
- Their terminal access

This server is their **digital fortress**: nothing shared with other users, no neighbours competing for resources, no data commingling. The user's server is the user's world.

Appx runs on this server and manages everything — exactly as it does today for self-hosters. Phase 7 adds the control plane that provisions and manages these servers automatically, so users never have to touch a terminal or configure DNS.

---

## Architecture

```
User signs up at appx.app
         │
         ▼
Control Plane (thin service)
  ├── Provision VM (Hetzner API)        ← ~30 seconds
  ├── Run install.sh on new server      ← ~2 minutes
  ├── Configure DNS: user.appx.app      ← ~30 seconds (Cloudflare API)
  └── Email: "Your fortress is ready"
         │
         ▼
user.appx.app (dedicated VM)
  └── appx server (Phase 1-6)
        ├── OpenCode agent containers
        ├── User app containers
        ├── Terminal WebSocket
        └── All data on local disk
```

The control plane does three things: provision servers, manage DNS, handle billing. It never touches the data inside a user's server.

---

## Why Dedicated Servers

**vs. shared infrastructure (Option B from original plan):**

| | Dedicated (this plan) | Shared multi-tenant |
|--|--|--|
| Code changes to appx | None | Significant (user scoping everywhere) |
| Data isolation | Hardware-level | Row-level security |
| Local LLM support | ✓ (full RAM available) | ✗ (resource contention) |
| GDPR / data deletion | Delete the VM | Complex data scrubbing |
| Server sizing | Per-user upgrade | Shared pool |
| Security blast radius | User's server only | All users |
| Time to ship | Weeks | Months |

The "no code changes to appx" point is decisive. Everything built in Phases 1–6 runs identically on a dedicated VM. The installer (`install.sh` from Phase 6) is literally the provisioning script.

---

## The Control Plane

A separate lightweight service (not part of the appx binary). Responsible for:

### 1. User Signup + Provisioning

```
POST /signup {email, password, username}
  → validate username availability
  → create user record (control plane DB)
  → provision VM via cloud provider API
  → SSH: run install.sh on new server
  → configure DNS: username.appx.app → server IP
  → send welcome email with login URL
  → return {url: "https://username.appx.app"}
```

Provisioning takes ~3 minutes total. The user sees a "setting up your fortress" progress screen.

### 2. Server Hibernation (idle cost management)

A dedicated €4/month Hetzner CX22 costs money even when idle. Hibernation solves this:

- **Detect idle**: no HTTP requests and no running containers for 30 minutes
- **Hibernate**: snapshot the server disk → delete the VM → record "hibernated" state
- **Wake on request**: first request to `username.appx.app` triggers VM restore → ~60s cold start → redirect user to loading screen → forward request when ready

Hetzner charges for snapshots at ~€0.01/GB/month. A typical appx install is ~20GB. Hibernated users cost €0.20/month instead of €4/month.

Active users (containers running) are never hibernated.

### 3. DNS Management

Cloudflare API:
- `username.appx.app A <server IP>` on provision
- Update record on server IP change (e.g. after restore from snapshot, IP may change)
- Delete record on account deletion

Wildcard `*.appx.app` is a single Cloudflare record pointing to the control plane's load balancer, which routes to individual servers. Per-user A records are not needed if the control plane proxies (but direct A records are simpler and avoid a hop).

### 4. Bring-Your-Own-Domain

User points `myapps.example.com CNAME username.appx.app`. Control plane:
- Detects the custom domain (via SNI or explicit registration)
- Runs `./appx --domain myapps.example.com` on the user's server (restarts appx with new flag)
- CertMagic issues a cert for the custom domain

### 5. Billing

**Private beta**: invite-only, no billing. Collect usage data.

**Usage metrics from day one:**
- Server uptime hours (active vs hibernated)
- Number of projects created
- Container compute hours
- Storage used (disk)

**Pricing model (suggested):**
- **Free tier**: 1 project, server hibernates after 30 min idle, cold start on next visit
- **Standard** (~€10/month): dedicated CX22 (2 vCPU, 4GB RAM, 40GB SSD), always-on option, up to 10 projects
- **Pro** (~€25/month): CX32 (4 vCPU, 8GB RAM, 80GB SSD), Ollama with 7B local models, unlimited projects
- **GPU** (pricing TBD): GPU instance for larger models

Users pay slightly above cloud costs. Transparent: "your server costs €4, we charge €10 for the service."

---

## What Each User Gets

On a standard Hetzner CX22 (€4/month raw cost):

```
username.appx.app
├── Appx dashboard          — manage projects, settings, API keys
├── OpenCode agent UI       — AI coding assistant (per project)
├── Terminal access         — SSH into any project container
├── /apps/:name/*           — reverse proxy to deployed apps
├── 40GB persistent disk    — data survives container restarts
├── Docker isolation        — each project in its own container
└── Anthropic API (default) — bring your own API key
```

On a larger instance (CX32, 8GB RAM):
```
└── Ollama                  — run 7B local models without an API key
                              (no per-token cost, fully private)
```

---

## Server Specification (Default)

Hetzner CX22 (suggested default):
- 2 vCPU AMD (shared)
- 4 GB RAM
- 40 GB NVMe SSD
- 20 TB traffic
- €4.15/month in EU

Suitable for 2-3 concurrent OpenCode sessions (each uses ~500MB RAM). Upgrade to CX32 (8GB) for local LLMs or heavy parallel workloads.

---

## What Doesn't Change in Appx

Everything in the appx binary (Phases 1–6) runs identically. The dedicated server model is purely an operational wrapper. Specific things that already work:

- Path-based routing + Service Worker → agent UI works with trusted cert
- Port publishing → containers reachable on all platforms
- `--domain username.appx.app` → CertMagic + Cloudflare DNS-01
- Session cookies, bearer tokens, terminal WebSocket
- Container security hardening

The only appx change Phase 7 might add: an **idle detection hook** that the control plane can call to know when to hibernate. A simple `GET /api/health/activity` endpoint returning last-activity timestamp.

---

## Prerequisites

- **Phase 5** — Dockerfile pinning, container secret files (not env vars). Required before running AI agents for untrusted users.
- **Phase 6** — `install.sh` (this is what provisions each server), bearer token auth (mobile access for hosted users), HTTP dev mode for localhost.

---

## Control Plane Tech Stack

The control plane is small and separate from appx:
- Go (consistent with appx, single binary)
- PostgreSQL (multi-tenant control data: users, servers, billing)
- Hetzner Cloud API (server provisioning)
- Cloudflare API (DNS)
- Stripe API (billing, when ready)
- Resend / Postmark (transactional email)

This is a separate repository and deployment from appx.

---

## Open Questions

1. **Hibernation latency**: 60-second cold start on free tier — acceptable? Could show a "waking up your fortress" screen. Users who want instant access upgrade to always-on.

2. **Data backup**: automatic daily snapshots? User-triggered exports? Important for fortress narrative — users should feel their data is safe.

3. **Multi-region**: deploy servers in the user's preferred region (EU, US, Asia) for latency and data residency. Hetzner has Nuremberg, Helsinki, Ashburn, Singapore.

4. **Ollama integration**: should appx orchestrate Ollama startup (like it orchestrates OpenCode)? Makes local models a first-class feature: project settings could show "use local llama3:8b" as an option.

5. **Username permanence**: if a user deletes their account, their username should be reserved (not reassigned) to prevent phishing/impersonation.

6. **Shared projects (future)**: a team of two people could share one fortress. Multi-user within a single server is simpler than cross-server collaboration. Phase 8 territory.
