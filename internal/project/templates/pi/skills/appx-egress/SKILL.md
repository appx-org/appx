---
name: appx-egress
description: Use when a command or package manager needs external network access and Appx reports that the destination is not in the egress allowlist.
---

# appx-egress

Request narrowly-scoped outbound network access through Appx's local egress
approval flow.

## Usage

```bash
python3 .pi/skills/appx-egress/request_egress.py <host> <port> "<reason>"
```

Examples:

```bash
python3 .pi/skills/appx-egress/request_egress.py registry.npmjs.org 443 "install Vite dependency"
python3 .pi/skills/appx-egress/request_egress.py api.github.com 443 "fetch GitHub release metadata"
```

Only request hosts that are strictly required for the user's task. If the user
denies or the request times out, explain the blocker and stop instead of trying
to bypass Appx's egress policy.
