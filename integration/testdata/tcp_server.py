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

import argparse
import socket
import time


CHUNK_SIZE = 64 * 1024


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="single-connection TCP test server")
    parser.add_argument("--listen-address", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--payload-bytes", type=int, default=0)
    parser.add_argument("--send-delay", type=float, default=0)
    return parser.parse_args()


def serve(args: argparse.Namespace) -> None:
    family = socket.AF_INET6 if ":" in args.listen_address else socket.AF_INET
    with socket.socket(family, socket.SOCK_STREAM) as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((args.listen_address, args.port))
        listener.listen(1)

        connection, _ = listener.accept()
        with connection:
            if args.send_delay > 0:
                time.sleep(args.send_delay)

            remaining = args.payload_bytes
            chunk = bytes(CHUNK_SIZE)
            try:
                while remaining > 0:
                    send_size = min(remaining, CHUNK_SIZE)
                    connection.sendall(chunk[:send_size])
                    remaining -= send_size

                if args.payload_bytes == 0:
                    while connection.recv(CHUNK_SIZE):
                        pass
            except (BrokenPipeError, ConnectionResetError):
                pass


if __name__ == "__main__":
    serve(parse_args())
