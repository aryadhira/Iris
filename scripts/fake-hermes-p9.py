#!/usr/bin/env python3
"""A tiny fake Hermes api_server for the P9 browser acceptance.

It exists so the acceptance run never sends a single request to the live
gateway on :8642. Only the three endpoints irisd needs for a room and one turn
are implemented:

    POST   /api/sessions                    -> {"session":{"id":"api_fake_p9"}}
    DELETE /api/sessions/<id>               -> 200
    POST   /api/sessions/<id>/chat/stream   -> a small SSE reply

The reply deliberately carries a STATUS line and a standalone -- so the browser
assertions in scripts/browser-p9.mjs exercise the real cleaner end to end.
"""
from __future__ import annotations

import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # keep the acceptance output readable
        sys.stderr.write("fake-hermes: " + (fmt % args) + "\n")

    def _json(self, status: int, body: object) -> None:
        data = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        if length:
            self.rfile.read(length)

        if self.path == "/api/sessions":
            self._json(200, {"session": {"id": "api_fake_p9"}})
            return

        if self.path.endswith("/chat/stream"):
            reply = (
                "Balasan palsu untuk P9 -- tanpa sentuhan gateway asli.\n\n"
                "STATUS: DONE — selesai"
            )
            frames = [
                ("run.started", {"run_id": "run_fake_p9", "session_id": "api_fake_p9"}),
                ("assistant.delta", {"run_id": "run_fake_p9", "delta": reply}),
                ("assistant.completed", {"run_id": "run_fake_p9", "content": reply}),
            ]
            body = b"".join(
                f"event: {name}\ndata: {json.dumps(payload)}\n\n".encode("utf-8")
                for name, payload in frames
            )
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            self.wfile.flush()
            return

        self._json(404, {"error": "not found"})

    def do_DELETE(self):
        if self.path.startswith("/api/sessions/"):
            self._json(200, {"ok": True})
            return
        self._json(404, {"error": "not found"})


def main() -> int:
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8659
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"fake-hermes listening on 127.0.0.1:{port}", flush=True)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
