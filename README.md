# Appx

Agentic Application Proxy — self-hostable tool to build and host personal apps with AI agents powered by [OpenCode](https://github.com/anomalyco/opencode).

## What it does

Appx is a management shell for running OpenCode agents on a remote server. It provides authentication, TLS termination, a web dashboard, and a reverse proxy — so you can manage projects, chat with agents, and access agent-built apps from a browser over HTTPS.

## Architecture

```
Browser
  └── HTTPS (single port)
        ├── /              React SPA (embedded in binary)
        ├── /api/*         REST API (auth, projects, settings)
        ├── /api/opencode/* Reverse proxy → OpenCode server
        └── <project>.<domain>   Reverse proxy → agent-built apps
```

Everything is a single Go binary. The React frontend is compiled and embedded at build time. State lives in a SQLite database on disk.

OpenCode runs as a **separate process** on `localhost:4096` and handles all AI agent work (sessions, tool execution, file editing, terminal). Appx proxies requests to it and adds auth + TLS on top.

**Auth model**: single user, password login, session cookie. On first run a random password is generated and printed to stdout.

**TLS**: self-signed ECDSA P-256 certificate auto-generated on first run, auto-renewed 7 days before expiry. For production, use `-domain` with `CLOUDFLARE_API_TOKEN` for Let's Encrypt certificates.

## Prerequisites

- Linux host (Ubuntu/Debian, amd64 or arm64)
- `git` — installed manually before bootstrap
- Go, Node.js, Task, and all agent tools — installed automatically by bootstrap

## Self-Hosting

### Initial setup

```bash
sudo apt-get install -y git

# Use the SSH URL if the repo is private (use deploy key)
git clone https://github.com/neuromaxer/appx.git /srv/appx
cd /srv/appx
sudo ./deploy/bootstrap.sh
```

On first run, bootstrap prompts for server configuration:

```
Server hostname [138.x.x.x.sslip.io]:
Data directory [/var/lib/appx]: /mnt/vol/appx-data
Port [443]:
```

Press Enter to accept defaults. The hostname defaults to `<your-ip>.sslip.io` which provides free wildcard DNS — this enables subdomain routing for agent-built apps (e.g. `https://myapp.138.x.x.x.sslip.io`). You can also use your own domain here.

If you want to use a persistent volume for storage (e.g. Hetzner Cloud Volumes), mount it first (Volumes -> Show configuration in Hetzner console) and enter the mount path as the data directory.

The config is saved to `/etc/appx/appx.env` and reused on subsequent runs. To change it later: `sudo nano /etc/appx/appx.env && sudo systemctl restart appx`.

Bootstrap then creates OS users with proper isolation, installs tools (Node.js, OpenCode, Claude Code, uv), sets up systemd services, starts everything, and runs a verification suite.

During Opencode installation you might be prompted "opencode is installed to /usr/local/bin/opencode and may be managed by a package manager". Select `Install anyways? Yes`

On first run, a random password is written to `{data-dir}/initial_password`. Delete the file after saving your password.

Bootstrap installs these tools system-wide so agents can use them in the terminal or via agent:

- **Task** — [taskfile.dev](https://taskfile.dev) build runner
- **Go** — compiled from the version in `go.mod`
- **Node.js 24 / npm** — JavaScript/TypeScript projects (installed via nvm, pinned to major version 24)
- **uv** — Python version and package management (self-update: `uv self update`)
- **OpenCode** — AI agent backend (pinned version in `deploy/opencode-version`)
- **Claude Code** — Claude CLI for terminal use (self-update: `sudo npm update -g @anthropic-ai/claude-code`)

### Updating appx

After pushing a new release:

```bash
cd /srv/appx
task server:deploy
```

Pulls latest code, rebuilds, installs the binary, updates OpenCode to the pinned version, and restarts both services.

### Updating OpenCode version

Edit `deploy/opencode-version` to the new version, then:

```bash
cd /srv/appx
task server:deploy
```

### Updating Claude Code

```bash
sudo npm install -g @anthropic-ai/claude-code
```

No service restart needed — it's a CLI tool.

### Verify installation

```bash
sudo ./deploy/verify-installation.sh
```

Checks users, permissions, isolation, tools, service files, and runtime. Exits 0 only if everything is correct.

### Troubleshoot

```bash
journalctl -u appx -f          # appx logs
journalctl -u opencode -f      # opencode logs
```

### Deploy scripts

| File / Script                   | When             | What                                                       |
| ------------------------------- | ---------------- | ---------------------------------------------------------- |
| `deploy/bootstrap.sh`           | Day 1            | Full setup: users, dirs, tools, build, start, verify       |
| `deploy/system-setup.sh`        | Infra changes    | Users, groups, directories, service files, opencode config |
| `deploy/tools-install.sh`       | Tool updates     | Go, Node.js 24, OpenCode (pinned), Claude Code, uv         |
| `deploy/opencode.json`          | Model changes    | Default OpenCode model config (copied to opencode home)    |
| `deploy/opencode-version`       | Version pin      | Pinned OpenCode version installed by tools-install         |
| `deploy/verify-installation.sh` | After any change | Full system verification                                   |

## Local development

OpenCode must be running before starting appx:

```bash
opencode serve --hostname 127.0.0.1 --port 4096
```

Then start appx with `--host 127.0.0.1.sslip.io` so that subdomain routing and session cookies work correctly across project subdomains. Plain `localhost` has inconsistent cookie-sharing behaviour for subdomains across browsers.

```bash
task local
```

Access the dashboard at `http://127.0.0.1.sslip.io:8080`. Project subdomains are at `http://<project>.127.0.0.1.sslip.io:8080`.

For any change: edit → `task local` (Ctrl-C the running process first). There is no hot-reload dev server — appx embeds the compiled frontend at build time, so the local dev setup is identical to what runs on the server.

[sslip.io](https://sslip.io) is public DNS — `anything.127.0.0.1.sslip.io` resolves to `127.0.0.1` with no setup required.

## Persistent storage

All state lives in the data directory (configured during bootstrap, default `/var/lib/appx`):

| Contents                      | Path                      | Access    |
| ----------------------------- | ------------------------- | --------- |
| SQLite DB, TLS certs, secrets | `{data}/.appx-internals/` | appx only |
| Project directories           | `{data}/projects/`        | shared    |

To use a mounted volume, specify the path when bootstrap prompts for "Data directory". Bootstrap automatically creates the subdirectories with correct permissions.

## Subdomain routing without a domain (sslip.io)

Subdomain routing (e.g. `assistum.<base>`) requires a real domain name — bare IPs don't work because `assistum.91.98.144.204` isn't a valid hostname. [sslip.io](https://sslip.io) provides free wildcard DNS: `anything.IP.sslip.io` resolves to the embedded IP automatically.

Edit `/etc/appx/appx.env` and set `APPX_HOST` to the sslip.io hostname:

```bash
APPX_HOST=91.98.144.204.sslip.io
```

Delete old TLS certs so they regenerate with the wildcard SAN, then restart:

```bash
sudo rm /var/lib/appx/.appx-internals/{cert,key}.pem
sudo systemctl restart appx
```

This gives you:

- `https://91.98.144.204.sslip.io` — dashboard
- `https://assistum.91.98.144.204.sslip.io` — project subdomain
- Session cookie shared across all subdomains via `Domain=.91.98.144.204.sslip.io`

Note: the bare IP (`https://91.98.144.204`) will stop serving the dashboard. Access via the sslip.io hostname instead.

See [docs/security/certificate_and_sslip.md](docs/security/certificate_and_sslip.md) for the full analysis of certificate generation, cookie scoping, and browser behaviour.

## Automatic TLS via Let's Encrypt

Uncomment and fill in the two variables in `/etc/appx/appx.env`:

```bash
APPX_DOMAIN=app.yourdomain.com
CLOUDFLARE_API_TOKEN=your_token_here
```

Then restart: `sudo systemctl restart appx`.

Appx requests certificates for `app.yourdomain.com` and `*.app.yourdomain.com` via Cloudflare DNS-01 challenge. No port 80 required.

Requirements:

- Cloudflare API token with **Zone > DNS > Edit** permissions
- Domain managed by Cloudflare DNS

## User isolation

Bootstrap creates two OS users with a shared `projects` group:

```
appx      — runs the appx server, owns DB and TLS certs
opencode  — runs OpenCode, cannot access appx data
projects  — shared group, both users read/write project directories
```

Directory permissions prevent OpenCode (and any agent it spawns) from accessing the appx database, TLS keys, or binary. Project directories use setgid so files created by either user are accessible to both.

## Development

```bash
task local          # Build and run appx in HTTP dev mode (127.0.0.1.sslip.io)
task test           # Run all Go tests
task server:bootstrap   # First-time server setup
task server:deploy      # Pull, build, install, restart
task server:verify      # Post-deploy verification
```

See [CLAUDE.md](CLAUDE.md) for architecture details and development conventions.

## Caveats

- **Self-signed TLS (default).** Browsers show a security warning. Use `-domain` for automatic Let's Encrypt.
- **Single-user only.** One password, one session store. Designed for personal use.
- **Port 443 requires root.** Use `-port 8443` or grant `CAP_NET_BIND_SERVICE` (bootstrap handles this).
