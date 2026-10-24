#!/usr/bin/env python3

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit


class PprofHandler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 - required by BaseHTTPRequestHandler
        path = urlsplit(self.path).path
        if path == "/debug/pprof/heap":
            status, body = 200, b"heap profile"
        elif path == "/debug/pprof/fail":
            status, body = 502, b"diagnostic " * 1024
        elif path == "/debug/pprof/exact":
            status, body = 200, b"1234"
        elif path == "/debug/pprof/over":
            status, body = 200, b"12345"
        else:
            status, body = 404, b"not found"

        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        return


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port-file", required=True, type=Path)
    args = parser.parse_args()

    server = ThreadingHTTPServer(("127.0.0.1", 0), PprofHandler)
    args.port_file.write_text(str(server.server_port), encoding="ascii")
    server.serve_forever()


if __name__ == "__main__":
    main()
