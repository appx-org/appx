# Egress iptables Rules (Phase 6 Installer)

## Overview

The egress CONNECT proxy (Phase 5 Step 6) provides cooperative enforcement via
`HTTPS_PROXY`. A compromised agent can bypass the proxy using raw TCP connections.
iptables rules provide kernel-level enforcement as a second defence layer.

## Design

All outbound traffic from the `opencode` UID is blocked except to localhost.
The appx server runs as the `appx` user and is NOT affected.

## Rules

The Phase 6 installer applies these rules:

```bash
OPENCODE_UID=$(id -u opencode)

# Allow loopback (proxy lives at 127.0.0.1:9080)
iptables -A OUTPUT -o lo -m owner --uid-owner $OPENCODE_UID -j ACCEPT

# Allow established connections
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -m state --state ESTABLISHED,RELATED -j ACCEPT

# Block everything else from opencode user
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -j REJECT --reject-with icmp-port-unreachable
```

## Verification

```bash
# Should succeed (goes through proxy to allowed destination)
sudo -u opencode curl -x http://127.0.0.1:9080 https://api.anthropic.com/health

# Should fail (direct — blocked by iptables)
sudo -u opencode curl --noproxy '*' https://api.anthropic.com/health

# appx user is unaffected
sudo -u appx curl https://api.anthropic.com/health
```

## Persistence

Persist rules across reboots using `iptables-save`/`iptables-restore` or
the distribution's equivalent (e.g. `netfilter-persistent` on Debian/Ubuntu).
