## Egress Access

When a command fails with "destination not in allowlist" or a similar proxy/network
error for an external host, request access by running:

    curl -s -X POST http://127.0.0.1:9081/egress/request \
      -H 'Content-Type: application/json' \
      -d '{"host":"<hostname>","port":<port>,"reason":"<brief description of why this host is needed>"}'

This blocks until the user approves or denies (up to 60 seconds). If approved,
retry the original command. If denied or timed out, inform the user and stop.

Do not request access to hosts that are not strictly necessary for the task.
