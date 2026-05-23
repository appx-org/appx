# Appx

Agentic Application Proxy — self-hostable tool to build and host personal apps with AI agents powered by Pi.

## What it does

Appx is a management shell for running coding agents on a remote server. It provides authentication, TLS termination, a web dashboard, and a reverse proxy — so you can manage projects, chat with agents, and access agent-built apps from a browser over HTTPS.

## Architecture

```
Browser
  └── HTTPS (single port)
        ├── /              React SPA (embedded in binary)
        ├── /api/*         REST API (auth, projects, settings)
        ├── /api/projects/:id/agent/* → Pi agent-server project session proxy
        ├── /api/agent/*    → Pi agent-server shared auth/model proxy
        └── <project>.<domain>   Reverse proxy → agent-built apps
```

Appx itself is a single Go binary. The React frontend is compiled and embedded
at build time. State lives in a SQLite database on disk.

Pi is installed as the default agent runtime. In Pi mode, systemd runs
`agent-server` on `localhost:4001` with `AGENT_SERVER_MODE=multi`, so Appx can
share credentials while keeping sessions scoped to one project.

**Auth model**: single user, password login, session cookie. On first run a random password is generated and printed to stdout.

**TLS**: self-signed ECDSA P-256 certificate auto-generated on first run, auto-renewed 7 days before expiry. For production, use `-domain` with `CLOUDFLARE_API_TOKEN` for Let's Encrypt certificates.

## Prerequisites

- Linux host (Ubuntu/Debian, amd64 or arm64)
- `git` — installed manually before bootstrap
- Go, Node.js, Task, and all agent tools — installed automatically by bootstrap

## Self-Hosting

### Private repo: deploy key setup

If the repo is private, set up a read-only deploy key on the server before cloning. This is a one-time step.

```bash
# Generate a deploy key (no passphrase — runs unattended on the server)
ssh-keygen -t ed25519 -f ~/.ssh/appx_deploy -N "" -C "appx-server-deploy"

# Print the public key — copy the output
cat ~/.ssh/appx_deploy.pub
```

On GitHub: **repo → Settings → Deploy keys → Add deploy key**. Paste the public key. Leave "Allow write access" unchecked — the server only needs to pull.

```bash
# Tell SSH to use this key for github.com
cat >> ~/.ssh/config << 'EOF'
Host github.com
  IdentityFile ~/.ssh/appx_deploy
  IdentitiesOnly yes
EOF
```

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

Bootstrap then creates OS users with proper isolation, installs tools (Node.js, Pi, Claude Code, uv, and agent-server), sets up systemd services, starts everything, and runs a verification suite. The Appx UI proxies project agent sessions to project-scoped `agent-server` runtimes and proxies provider-auth, subscription login, and custom-provider requests to shared Pi agent settings at `APPX_AGENT_SERVER_URL` (default `http://127.0.0.1:4001`). The Pi agent service runs with `NODE_USE_ENV_PROXY=1`, `HTTPS_PROXY=http://127.0.0.1:9080`, and `NO_PROXY=localhost,127.0.0.1`, so provider traffic goes through the Appx egress allowlist while local agent traffic stays on loopback.

On first run, a random password is written to `{data-dir}/initial_password`. Delete the file after saving your password.

Bootstrap installs these tools system-wide so agents can use them in the terminal or via agent:

- **Task** — [taskfile.dev](https://taskfile.dev) build runner
- **Go** — compiled from the version in `go.mod`
- **Node.js 24 / npm** — JavaScript/TypeScript projects (installed via nvm, pinned to major version 24)
- **uv** — Python version and package management (self-update: `uv self update`)
- **Pi** — AI coding agent CLI/SDK (pinned version in `deploy/pi-version`)
- **agent-server** — separate Appx org service that exposes Pi sessions over HTTP/SSE for the Agent tab
- **Claude Code** — Claude CLI for terminal use (self-update: `sudo npm update -g @anthropic-ai/claude-code`)

### Updating appx

After pushing a new release:

```bash
cd /srv/appx
task server:deploy
```

Pulls latest code, rebuilds, installs the binary, updates Pi/agent-server to the pinned versions, and restarts the needed services.

### Updating Pi version

Edit `deploy/pi-version` to the new version, then:

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
journalctl -u appx -f            # appx logs
journalctl -u agent-server -f    # Pi agent-server logs
```

### Deploy scripts

| File / Script                   | When             | What                                                       |
| ------------------------------- | ---------------- | ---------------------------------------------------------- |
| `deploy/bootstrap.sh`           | Day 1            | Full setup: users, dirs, tools, build, start, verify       |
| `deploy/system-setup.sh`        | Infra changes    | Users, groups, directories, service files, agent config    |
| `deploy/tools-install.sh`       | Tool updates     | Go, Node.js 24, Pi, agent-server, Claude Code, uv          |
| `deploy/agent-server.service`   | Pi backend       | Systemd unit for project-scoped Pi session service         |
| `deploy/pi-version`             | Version pin      | Pinned Pi version installed by tools-install               |
| `deploy/verify-installation.sh` | After any change | Full system verification                                   |

## Local development

Run the sibling `agent-server` in multi-project mode before starting appx:

```bash
cd ../agent-server
PROJECT_DIR=/path/to/appx-data/projects \
AGENT_SERVER_MODE=multi \
AGENT_SERVER_PORT=4001 \
npm run dev
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

Each new project is scaffolded with a project-local Pi harness under
`{data}/projects/<name>/.pi/`: an Appx-specific prompt, first-party guardrail
extension, egress skill helper, and `settings.json` for reviewed/pinned Pi
packages. Third-party Pi packages are not installed by default because they run
inside the agent process.

Pi credentials are configured from Settings. Built-in providers can use stored
API keys or Pi subscription auth where the provider supports it, and custom
providers such as LiteLLM are written to the agent service user's
`models.json` without exposing secret values back to the browser.

The Agent tab consumes Appx's project-scoped `/api/projects/{id}/agent/*`
proxy, not provider-specific OpenAI or Anthropic streams. `agent-server` turns
all supported Pi providers into the same session HTTP/SSE contract, so frontend
streaming code should handle Pi `message_update` events by `contentIndex` for
text and tool-call blocks. Pi extension UI requests, including Appx guardrail
approvals for risky commands, are delivered over the same session stream and
answered through the project-scoped agent proxy.

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
appx-agent — isolated agent user for Pi tooling, cannot access appx data
projects  — shared group, both users read/write project directories
```

Directory permissions prevent agent tooling from accessing the appx database, TLS keys, or binary. Project directories use setgid so files created by either user are accessible to both.

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
