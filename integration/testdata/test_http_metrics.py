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

import http.server
import sys
import threading


class MetricsHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/stall":
            threading.Event().wait(2)
        failed = self.path == "/error"
        body = b'{"error":"metrics unavailable"}\n' if failed else b"test_counter 7\n"
        self.send_response(503 if failed else 200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except BrokenPipeError:
            pass


with http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), MetricsHandler) as server:
    print("ready", flush=True)
    server.serve_forever()
