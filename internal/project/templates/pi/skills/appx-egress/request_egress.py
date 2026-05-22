#!/usr/bin/env python3
"""Request Appx egress access from the local agent-facing listener."""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request


def main() -> int:
    parser = argparse.ArgumentParser(description="Request Appx egress access")
    parser.add_argument("host", help="Destination hostname")
    parser.add_argument("port", type=int, help="Destination port")
    parser.add_argument("reason", help="Brief reason shown to the Appx user")
    parser.add_argument(
        "--url",
        default=os.environ.get("APPX_EGRESS_URL", "http://127.0.0.1:9081/egress/request"),
        help="Appx internal egress request URL",
    )
    args = parser.parse_args()

    payload = json.dumps(
        {"host": args.host, "port": args.port, "reason": args.reason}
    ).encode("utf-8")
    req = urllib.request.Request(
        args.url,
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=70) as res:
            body = json.loads(res.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        print(f"egress request failed: HTTP {exc.code} {exc.reason}", file=sys.stderr)
        return 2
    except Exception as exc:
        print(f"egress request failed: {exc}", file=sys.stderr)
        return 2

    if body.get("allowed"):
        print(f"approved: {args.host}:{args.port}")
        return 0
    if body.get("timeout"):
        print(f"timed out: {args.host}:{args.port}", file=sys.stderr)
        return 1
    print(f"denied: {args.host}:{args.port}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
