#!/usr/bin/env python3
"""Tiny Web Push endpoint for acceptance tests. Writes one JSON line per POST.

It does not decrypt: the proof is that a real aes128gcm POST arrived, with the
VAPID authorization header, not that this script can read the payload.
"""

import argparse
import base64
import json
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    out = None

    def log_message(self, *_args):
        return

    def do_GET(self):
        body = b'{"ok":true}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0") or 0)
        body = self.rfile.read(length) if length else b""
        record = {
            "at": self.date_time_string(),
            "method": "POST",
            "path": self.path,
            "headers": {key: value for key, value in self.headers.items()},
            "body_b64": base64.b64encode(body).decode("ascii"),
            "bytes": len(body),
        }
        with LOCK, open(self.out, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(record) + "\n")
            handle.flush()
        self.send_response(201)
        self.send_header("Content-Length", "0")
        self.end_headers()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    Handler.out = args.out
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    print("ready", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
