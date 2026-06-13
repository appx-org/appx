#!/usr/bin/env bash
# deploy/tools-install.sh — install build and runtime tools system-wide.
#
# Must be run as root. Safe to run multiple times (idempotent).
# Installs everything to /usr/local/bin so the appx user has access.
#
# Deploy is CONTAINER MODE ONLY (Stage 4): agent-server + Pi run INSIDE the
# appx-managed outer container, so this script does NOT install Pi or
# agent-server on the host. Instead it builds (or pulls) the outer image. The
# outer image's Dockerfile is multi-stage and compiles agent-server in a node:22
# stage, so building it on the box needs docker + the agent-server source, not
# host Node.
#
# Tools installed:
#   - Go          (version pinned to go.mod — builds the appx binary)
#   - Task        (taskfile.dev build runner — builds the appx binary)
#   - Node.js 24  (via nvm, pinned to major version — builds the appx web UI)
#   - Claude Code (Claude CLI for the terminal feature in user shells)
#   - uv          (Python version/package manager for the terminal feature)
#   - the outer builder image (built from the agent-server checkout, tag-pinned)
#
# Supported platforms: Ubuntu/Debian (amd64, arm64).

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Detect architecture.
ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
case "$ARCH" in
  amd64) GO_ARCH="amd64" ;;
  arm64) GO_ARCH="arm64" ;;
  *) echo "ERROR: unsupported architecture: $ARCH"; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# Task (taskfile.dev build runner)
# ---------------------------------------------------------------------------

if command -v task >/dev/null 2>&1; then
  echo "task already installed: $(task --version 2>/dev/null)"
else
  echo "installing task..."
  curl -1sLf 'https://dl.cloudsmith.io/public/task/task/setup.deb.sh' | bash
  apt-get install -y task
  echo "task installed: $(task --version 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# Go
# ---------------------------------------------------------------------------

# Read required version from go.mod; fall back to a known-good default.
GO_VERSION="1.24.2"
if [ -f "$REPO_DIR/go.mod" ]; then
  _GO_MOD_VER=$(grep '^go ' "$REPO_DIR/go.mod" | awk '{print $2}')
  if [ -n "$_GO_MOD_VER" ]; then
    GO_VERSION="$_GO_MOD_VER"
  fi
fi

if command -v go >/dev/null 2>&1; then
  echo "go already installed: $(go version 2>/dev/null)"
else
  echo "installing Go ${GO_VERSION}..."
  TMP_GO=$(mktemp)
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o "$TMP_GO"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$TMP_GO"
  rm -f "$TMP_GO"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  echo "go installed: $(go version 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# Node.js (via nvm, pinned to major version 24)
#
# nvm is installed to /usr/local/nvm (system-wide). Binaries are symlinked
# to /usr/local/bin so all users have access without sourcing nvm manually.
# ---------------------------------------------------------------------------

NODE_MAJOR=24
NVM_DIR="/usr/local/nvm"
NVM_VERSION="v0.40.1"

CURRENT_NODE_MAJOR=$(/usr/local/bin/node --version 2>/dev/null | sed 's/^v//' | cut -d. -f1 || echo "0")
if [ "$CURRENT_NODE_MAJOR" = "$NODE_MAJOR" ]; then
  echo "node $NODE_MAJOR already installed: $(/usr/local/bin/node --version)"
else
  echo "installing Node.js $NODE_MAJOR via nvm..."

  # Install nvm to system-wide location if not present.
  if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    mkdir -p "$NVM_DIR"
    export NVM_DIR
    # PROFILE=/dev/null prevents nvm from modifying any shell profile file.
    curl -o- "https://raw.githubusercontent.com/nvm-sh/nvm/${NVM_VERSION}/install.sh" | \
      PROFILE=/dev/null bash
  fi

  # Source nvm for this script session.
  export NVM_DIR
  # shellcheck source=/dev/null
  . "$NVM_DIR/nvm.sh"

  # Install the pinned major version and symlink binaries system-wide.
  nvm install "$NODE_MAJOR"
  for bin in node npm npx; do
    ln -sf "$(nvm which $NODE_MAJOR | xargs dirname)/$bin" "/usr/local/bin/$bin"
  done

  echo "node installed: $(/usr/local/bin/node --version)"
fi

# Resolve the nvm bin directory where npm install -g puts binaries.
# Follow the /usr/local/bin/node symlink back to the nvm versioned directory.
NODE_BIN_DIR="$(dirname "$(readlink -f /usr/local/bin/node)")"

# ---------------------------------------------------------------------------
# Locate the agent-server checkout (used to build the outer image below).
# ---------------------------------------------------------------------------

AGENT_SERVER_DIR="${AGENT_SERVER_DIR:-}"
if [ -z "$AGENT_SERVER_DIR" ] && [ -d "$REPO_DIR/../agent-server" ]; then
  AGENT_SERVER_DIR="$(cd "$REPO_DIR/../agent-server" && pwd)"
fi

# ---------------------------------------------------------------------------
# Claude Code (self-update: sudo npm update -g @anthropic-ai/claude-code)
# ---------------------------------------------------------------------------

if command -v claude >/dev/null 2>&1; then
  echo "claude already installed: $(claude --version 2>/dev/null || echo 'unknown')"
else
  echo "installing claude..."
  npm install -g @anthropic-ai/claude-code
  ln -sf "$NODE_BIN_DIR/claude" /usr/local/bin/claude
  echo "claude installed"
fi

# ---------------------------------------------------------------------------
# uv (self-update: uv self update)
# ---------------------------------------------------------------------------

if [ -x /usr/local/bin/uv ]; then
  echo "uv already installed: $(/usr/local/bin/uv --version 2>/dev/null || echo 'unknown')"
else
  echo "installing uv..."
  curl -LsSf https://astral.sh/uv/install.sh | sh
  # Installer puts it in ~/.local/bin/ — copy to system path.
  for candidate in \
      /root/.local/bin/uv; do
    if [ -x "$candidate" ]; then
      install -m 755 "$candidate" /usr/local/bin/uv
      echo "copied uv → /usr/local/bin/uv"
      break
    fi
  done
fi

# ---------------------------------------------------------------------------
# Outer builder image — build from the agent-server checkout, or pull a pinned
# registry tag/digest. This is the only agent backend in container-mode deploy.
# ---------------------------------------------------------------------------

RUNTIME=""
command -v docker >/dev/null 2>&1 && RUNTIME="docker"
[ -z "$RUNTIME" ] && command -v podman >/dev/null 2>&1 && RUNTIME="podman"

# Pin the image. APPX_AGENT_IMAGE may be a local tag (built here) or a registry
# ref / digest to pull (e.g. registry.example.com/builder-outer@sha256:...).
APPX_AGENT_IMAGE="${APPX_AGENT_IMAGE:-builder-outer}"

if [ -z "$RUNTIME" ]; then
  echo "ERROR: no docker found — the outer runtime MUST be rootful host Docker." >&2
  echo "       Install it (apt-get install -y docker.io) and re-run." >&2
  exit 1
elif printf '%s' "$APPX_AGENT_IMAGE" | grep -q '/'; then
  # Looks like a registry reference → pull it (pinned by tag or digest).
  echo "pulling outer image: $APPX_AGENT_IMAGE"
  "$RUNTIME" pull "$APPX_AGENT_IMAGE"
elif [ -n "$AGENT_SERVER_DIR" ] && [ -f "$AGENT_SERVER_DIR/container/Dockerfile" ]; then
  echo "building outer image '$APPX_AGENT_IMAGE' from $AGENT_SERVER_DIR ..."
  "$RUNTIME" build -f "$AGENT_SERVER_DIR/container/Dockerfile" -t "$APPX_AGENT_IMAGE" "$AGENT_SERVER_DIR"
  echo "built outer image: $APPX_AGENT_IMAGE"
else
  echo "ERROR: APPX_AGENT_IMAGE='$APPX_AGENT_IMAGE' is a local tag but no agent-server checkout was found to build it." >&2
  echo "       Clone appx-org/agent-server next to appx, set AGENT_SERVER_DIR, or set APPX_AGENT_IMAGE to a pullable ref." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "Tools install complete."
echo ""
echo "  task:     $(task --version 2>/dev/null || echo 'not found')"
echo "  go:       $(go version 2>/dev/null || echo 'not found')"
echo "  node:     $(/usr/local/bin/node --version 2>/dev/null || echo 'not found')"
echo "  uv:       $(/usr/local/bin/uv --version 2>/dev/null || echo 'not found')"
echo "  claude:   $(claude --version 2>/dev/null || echo 'not found')"
echo "  outer image ($APPX_AGENT_IMAGE): $("$RUNTIME" image inspect "$APPX_AGENT_IMAGE" >/dev/null 2>&1 && echo present || echo 'not found')"
