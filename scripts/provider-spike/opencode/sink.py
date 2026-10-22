#!/usr/bin/env python3
"""One-shot-per-run HTTP hook sink for the opencode identity spike.

Binds 127.0.0.1 on an ephemeral port, prints "PORT <n>" on stdout, then
appends every POST body to the file named by argv[1]. Runs until killed.
Bodies contain only the hook payload (session id, cwd, source) — never
prompt or transcript content.
"""
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

out = sys.argv[1]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        with open(out, "ab") as f:
            f.write(b"%s %s\n" % (self.headers.get("X-Noisedeck-Hook-Token", "-").encode(), body))
        self.send_response(200)
        self.end_headers()

    def log_message(self, *a):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
print("PORT", srv.server_address[1], flush=True)
srv.serve_forever()
