# Local Development

Local development does **not** use the deploy scripts, systemd, or the outer
container. You run the sibling `agent-server` by hand and point appx at it with
`APPX_AGENT_SERVER_URL` (the appx binary keeps this host-mode runtime path for
local/macOS dev; only the *deployment* machinery is container-only).

## One-time: link the `agent-client` SDK locally

The Agent tab UI is provided by the `@appx-org/agent-client` package. Until that
package is published to GitHub Packages, `web/package.json` links it from a
**sibling checkout** via a `file:` dependency (`file:../../agent-client`), so the
`agent-client` repo must be cloned next to `appx` (both under the same parent):

```text
<parent>/
├── appx/         ← this repo
└── agent-client/ ← github.com/appx-org/agent-client
```

```bash
git clone https://github.com/appx-org/agent-client.git ../agent-client
# the package ships TypeScript source consumed directly by appx's Vite build,
# so its own deps must be installed once for the symlinked import to resolve
cd ../agent-client && npm install && cd -
```

`task web` / `task build` then follow the symlink and compile the SDK source as
part of the frontend bundle. Vite dedupes React (see `web/vite.config.ts`) so the
symlink can't pull a second React copy. When the package is published this
`file:` spec swaps back to a semver range and the clone step goes away.

## Run agent-server, then appx

Run the sibling `agent-server` with `WORKSPACE_DIR` pointed at the **same**
directory appx uses for projects (co-located dev), since agent-server owns the
project directories and appx's subdomain proxy/terminal read them from that
shared path:

```bash
cd ../agent-server
WORKSPACE_DIR=/path/to/appx-data/projects \
AGENT_SERVER_PORT=4001 \
npm run dev
```

Then start appx in HTTP dev mode. `task local` runs it with
`--host 127.0.0.1.sslip.io` (so subdomain routing and session cookies work across
project subdomains) against `APPX_AGENT_SERVER_URL` (default
`http://127.0.0.1:4001`). Plain `localhost` has inconsistent cookie-sharing
behaviour for subdomains across browsers.

```bash
task local
```

- Dashboard: `http://127.0.0.1.sslip.io:8080`
- Project subdomains: `http://<project>.127.0.0.1.sslip.io:8080`

For any change: edit → `task local` (Ctrl-C the running process first). There is
no hot-reload dev server — appx embeds the compiled frontend at build time, so
the local dev setup is identical to what runs on the server.

[sslip.io](https://sslip.io) is public DNS — `anything.127.0.0.1.sslip.io`
resolves to `127.0.0.1` with no setup required.

## Common tasks

```bash
task local              # Build and run appx in HTTP dev mode (127.0.0.1.sslip.io)
task test               # Run all Go tests
task server:bootstrap   # First-time server setup (production, container mode)
task server:deploy      # Pull, build, install, restart (production)
task server:verify      # Post-deploy verification (production)
```

See [CLAUDE.md](../../CLAUDE.md) for architecture details and development
conventions.
