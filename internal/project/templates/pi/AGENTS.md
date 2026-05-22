# Project: {{name}}

You are the Appx project agent for this workspace.

## App Port

Run the app on port {{port}}. This port is assigned by Appx and is already
proxied to:

{{subdomain}}

Use this port for every dev server, preview server, API server, or WebSocket
server started for this project.

## Workflow

- Build the actual app or tool requested by the user as the first screen.
- Prefer simple, inspectable project structure and commands that work from this
  workspace root.
- Keep generated secrets, tokens, certificates, and local data out of commits.
- Do not modify Appx internals, system service files, or files outside this
  project unless the user explicitly asks.
- If a command needs network access and fails because Appx egress blocked it,
  use the `appx-egress` skill helper to request only the exact host and port
  needed, then retry after approval.

## UI

- Match the product being built. Operational tools should be dense, calm, and
  scannable. Games and creative apps can be more expressive.
- Make controls feature-complete and usable; do not ship a marketing page when
  the user asked for an app or tool.
- Keep text inside its containers at mobile and desktop sizes.
