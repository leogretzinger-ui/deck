#!/usr/bin/env python3
"""Isolated in-memory MCP relay for the live a2a names acceptance fixture.

Binds loopback, prints READY <port>, and accepts only presence/key writes.
It uses no credentials or persistent store. SIGTERM discards its state.
"""

from __future__ import annotations

import argparse
import json
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE_MAX = 500


class Relay:
    """In-memory events plus the filters a2a's folds read through."""

    def __init__(self) -> None:
        self.events: list[dict] = []
        self.n = 0
        self.lock = threading.Lock()

    def append(self, args: dict) -> dict:
        if args.get("room") not in ("presence", "keys"):
            raise ToolError("the fixture relay accepts only presence and key events")
        with self.lock:
            self.n += 1
            event = {
                "id": f"evt_{self.n:06d}",
                "seq": self.n,
                "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "type": args["type"],
                "stream": args["stream"],
                "room": args["room"],
                "from_agent": args["from_agent"],
                "to_agent": args.get("to_agent"),
                "correlation_id": args.get("correlation_id"),
                "status": args.get("status"),
                "body": args.get("body", ""),
                "metadata": args.get("metadata") or {},
            }
            self.events.append(event)
            return dict(event)

    def list(self, args: dict) -> list[dict]:
        with self.lock:
            snapshot = list(self.events)
        out = []
        after = args.get("since_event_id")
        for e in snapshot:
            if after and e["id"] <= after:
                continue
            if args.get("stream") and e["stream"] != args["stream"]:
                continue
            if args.get("room") and e["room"] != args["room"]:
                continue
            if args.get("type") and e["type"] != args["type"]:
                continue
            if args.get("correlation_id") and e["correlation_id"] != args["correlation_id"]:
                continue
            if args.get("to_agent") and e["to_agent"] not in (args["to_agent"], "*"):
                continue
            if args.get("since_created_at") and e["created_at"] < args["since_created_at"]:
                continue
            out.append(dict(e))
        if not after and args.get("order") != "asc":
            out.reverse()  # MemPalace 3.10.0 lists newest first without a cursor unless asked
        return out[: min(args.get("limit", 50), PAGE_MAX)]


class ToolError(Exception):
    pass


RELAY = Relay()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args) -> None:  # silence per-request noise
        pass

    def do_POST(self) -> None:
        try:
            length = int(self.headers.get("Content-Length", "0"))
            request = json.loads(self.rfile.read(length) or b"{}")
            tool = request.get("params", {}).get("name", "")
            arguments = request.get("params", {}).get("arguments") or {}
            if tool == "mempalace_event_append":
                result = {"event": RELAY.append(arguments)}
            elif tool == "mempalace_event_list":
                result = {"events": RELAY.list(arguments)}
            else:
                self._reply(request.get("id"), error=f"unknown tool {tool!r}")
                return
            self._reply(request.get("id"), result=result)
        except ToolError as exc:
            self._reply(request.get("id"), error=str(exc))
        except Exception as exc:  # a malformed body must not kill the server
            self._reply(request.get("id"), error=f"relay fixture: {exc}")

    def _reply(self, request_id, result: dict | None = None, error: str | None = None) -> None:
        document = {"jsonrpc": "2.0", "id": request_id}
        if error is not None:
            document["error"] = {"code": -32000, "message": error}
        else:
            # MCP tools/call shape: the payload rides as text content, which
            # is exactly what a2a._rpc decodes.
            document["result"] = {"content": [{"type": "text", "text": json.dumps(result or {})}]}
        body = json.dumps(document).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=0)
    options = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", options.port), Handler)
    # shutdown() blocks until serve_forever returns, so it must run on its
    # own thread: a signal handler shares the main thread with
    # serve_forever, and calling it there deadlocks the stop.
    def stop() -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, lambda *_: stop())
    signal.signal(signal.SIGINT, lambda *_: stop())
    print(f"READY {server.server_address[1]}", flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()


if __name__ == "__main__":
    sys.exit(main())
