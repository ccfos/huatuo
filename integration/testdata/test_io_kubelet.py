#!/usr/bin/env python3

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Supply kubelet HTTP metadata for real containers without replacing Huatuo's
# discovery code or the container runtime's process and cgroup records.

import argparse
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import ssl


def main():
    parser = argparse.ArgumentParser(description="IO test kubelet HTTP fixture")
    parser.add_argument("--pods-file", type=Path, required=True)
    parser.add_argument("--port-file", type=Path, required=True)
    parser.add_argument("--cert-file", type=Path, required=True)
    parser.add_argument("--key-file", type=Path, required=True)
    args = parser.parse_args()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/pods":
                payload = json.loads(args.pods_file.read_text())
            elif self.path == "/configz":
                payload = {"kubeletconfig": {"cgroupDriver": "cgroupfs"}}
            else:
                self.send_error(404)
                return
            body = json.dumps(payload).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    with HTTPServer(("127.0.0.1", 0), Handler) as server:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(args.cert_file, args.key_file)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        args.port_file.write_text(str(server.server_port))
        server.serve_forever()


if __name__ == "__main__":
    main()
