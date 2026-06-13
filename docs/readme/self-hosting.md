# Self-Hosting

Deploy is **container-mode only**: appx runs as the `appx` systemd service and
creates/supervises the agent-server **outer container** (one unprivileged
container holding agent-server + rootless podman). There is no host `appx-agent`
user, no `agent-server.service`, and no host install of Pi/agent-server.

## Prerequisites

Installed manually **before** bootstrap (bootstrap does not install these):

- **Linux host** (Ubuntu **24.04 LTS** recommended — the prod target; amd64 or arm64). 26.04 works with one workaround (see [Known gotchas](#known-gotchas)).
- **`git`**
- **Rootful Docker**, installed and running. The outer runtime *must* be rootful host Docker (rootless docker breaks the nested rootless-podman setup).
- The sibling repos **`agent-server`** (the outer image is built from it) and **`agent-client`** (the web UI SDK) checked out **next to** `appx` under a common parent, and `agent-client`'s deps installed once.
- **Open port 443** in the firewall / cloud security group.

Everything else (Go, Node.js 24, Task, and the `builder-outer` image) is
installed/built automatically by bootstrap.

## Initial setup

```bash
# 1. Prerequisites bootstrap does NOT install: git + rootful Docker.
sudo apt-get update
sudo apt-get install -y git docker.io
sudo systemctl enable --now docker

# 2. Check out the three repos side-by-side under one parent (e.g. /srv).
#    Use SSH URLs + a deploy key if the repos are private.
cd /srv
git clone https://github.com/neuromaxer/appx.git
git clone https://github.com/appx-org/agent-server.git   # outer image is built from this
git clone https://github.com/appx-org/agent-client.git   # web UI SDK (file: dependency)

# 3. REQUIRED once: install agent-client deps, or the appx web build fails (TS errors).
cd /srv/agent-client && npm install

# 4. Run bootstrap from inside the appx checkout.
cd /srv/appx
sudo ./deploy/bootstrap.sh
```

The layout the deploy scripts expect:

```text
/srv/
├── appx/          ← run bootstrap from here
├── agent-server/  ← tools-install auto-detects ../agent-server and builds builder-outer
└── agent-client/  ← appx web build links ../../agent-client
```

On first run, bootstrap prompts for server configuration:

```
Server hostname [138.x.x.x.sslip.io]:
Data directory [/var/lib/appx]: /mnt/vol/appx-data
Port [443]: # you must open chosen port in your server firewall
```

Press Enter to accept defaults. The hostname defaults to `<your-ip>.sslip.io`
which provides free wildcard DNS — this enables subdomain routing for
agent-built apps (e.g. `https://myapp.138.x.x.x.sslip.io`). You can also use your
own domain here. For a persistent volume, mount it first and enter the mount path
as the data directory.

The config is saved to `/etc/appx/appx.env` and reused on subsequent runs. To
change it later: `sudo nano /etc/appx/appx.env && sudo systemctl restart appx`.

Bootstrap then creates the `appx` OS user, installs the build toolchain (Go,
Node.js, Task) and **builds the `builder-outer` image** from the sibling
`agent-server` checkout (its multi-stage Dockerfile compiles agent-server in a
`node:22` stage, so the box needs docker + the source, not host Node), installs
the `appx` systemd service, starts it, and runs a verification suite. agent-server
inside the container runs with `NODE_USE_ENV_PROXY=1` + `HTTPS_PROXY` pointed at
appx's egress proxy on the docker bridge gateway, so provider traffic goes through
the Appx egress allowlist.

**Provider credentials.** Configure them in the **Settings UI** after first
login — Anthropic and most providers are stored in the agent's Pi credential
storage (persisted in the `builder-workspace` volume), just like any other key.
Only credentials the Settings UI can't carry (e.g. Amazon Bedrock — an upstream
Pi gap) need the service-env path — see [Known gotchas](#known-gotchas).

After bootstrap finishes, grab the generated password and log in:

```bash
sudo cat {data directory path from bootstrap}/.appx-internals/initial_password   # delete after saving
```

Visit `https://<host>` (self-signed cert by default → browser warning; for a
trusted cert see [Networking & TLS](./networking-and-tls.md)). Open **Settings**
to configure your model-provider credentials and models, then create a project.

## Known gotchas

- **Amazon Bedrock (or any non-Anthropic provider).** The bootstrap prompt only covers `ANTHROPIC_API_KEY`. For Bedrock, after bootstrap put the creds in `secrets.env`, list the var names in `APPX_AGENT_ENV_PASSTHROUGH`, and **recreate** the outer container (passthrough vars are injected at container *create* time, so a plain restart won't pick them up):

  ```bash
  sudo tee /etc/appx/secrets.env >/dev/null <<'EOF'
  AWS_BEARER_TOKEN_BEDROCK=<your-token>
  AWS_REGION=eu-central-1
  EOF
  sudo chown root:root /etc/appx/secrets.env && sudo chmod 600 /etc/appx/secrets.env

  sudo sed -i 's/^# APPX_AGENT_ENV_PASSTHROUGH=.*/APPX_AGENT_ENV_PASSTHROUGH=AWS_BEARER_TOKEN_BEDROCK,AWS_REGION/' /etc/appx/appx.env

  sudo systemctl stop appx && docker rm -f builder-outer && sudo systemctl start appx
  ```

  `bedrock-runtime.*.amazonaws.com:443` is already in the egress allowlist. (Note: setting a Bedrock key via the Settings UI does **not** work yet — an upstream Pi gap; the env var is the supported path.)

- **Ubuntu 26.04 only:** `apt-get install -y task` fails (the cloudsmith repo has no 26.04 release), which aborts `tools-install.sh`. Pre-install Task, then re-run bootstrap (no-op on 24.04):

  ```bash
  sudo sh -c 'curl -1sLf https://taskfile.dev/install.sh | sh -s -- -d -b /usr/local/bin'
  ```

- **docker-group timing.** bootstrap adds `appx` to the `docker` group; the *service* inherits it on its next start (bootstrap handles that). A human shell needs a re-login to use docker without sudo.

- **Pushing a new image or new passthrough env needs a recreate, not just a restart.** A plain `systemctl restart appx` only re-attaches to the running container. To pick up a rebuilt `builder-outer` image or changed `APPX_AGENT_ENV_PASSTHROUGH`, recreate it (the `builder-workspace` + `builder-podman-storage` volumes — i.e. projects, sessions, inner images — survive):

  ```bash
  sudo systemctl stop appx && docker rm -f builder-outer && sudo systemctl start appx
  ```

## Updating appx

After pushing a new release:

```bash
cd /srv/appx
task server:deploy
```

Pulls latest code, rebuilds the appx binary, installs it, rebuilds the outer
image from the `agent-server` checkout, and restarts `appx` (which re-attaches to
the outer container — see the recreate note above to force a fresh image/env).

## Updating the agent (Pi / agent-server)

Pi and agent-server run **inside** the outer image, so updating them means
rebuilding that image from the sibling `agent-server` checkout — `task
server:deploy` does this. If the image's pinned ports/tag change, appx refuses to
silently recreate a running container (it would kill running apps); recreate
explicitly with `--recreate-agent-container` (or
`APPX_RECREATE_AGENT_CONTAINER=true`), or the stop/rm/start sequence above.

## Verify installation

```bash
sudo ./deploy/verify-installation.sh
```

Container-mode checks: the `appx` unit is active and ordered after docker, the
outer container is healthy with the proven security flags + loopback-only
publishes + `RestartPolicy=unless-stopped`, no secret leaked into the journal,
and no host-mode artifacts remain (if a provider cred is supplied via the env
path, it also checks that it's reachable inside the container). Exits 0 only if
everything passes.

## Troubleshoot

```bash
journalctl -u appx -f            # appx logs (incl. container supervision)
docker logs -f builder-outer     # agent-server (inside the outer container)
```

## Deploy scripts

| File / Script                   | When             | What                                                       |
| ------------------------------- | ---------------- | ---------------------------------------------------------- |
| `deploy/bootstrap.sh`           | Day 1            | Full setup: user, dirs, tools, outer image, build, start, verify |
| `deploy/system-setup.sh`        | Infra changes    | appx user, projects group, dirs, seccomp, docker group, unit |
| `deploy/tools-install.sh`       | Tool updates     | Go, Node.js 24, Task, + builds the outer image             |
| `deploy/appx.service`           | systemd unit     | `appx` unit (container mode; ordered after `docker.service`) |
| `deploy/builder-container/`     | security boundary | seccomp profile installed to `/etc/appx/seccomp-builder.json` |
| `deploy/verify-installation.sh` | After any change | Full system verification                                   |
| `deploy/teardown.sh`            | Uninstall & cleanup | Reverse everything created by bootstrap.sh                        |
