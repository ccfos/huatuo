#!/usr/bin/env python3
# Copyright 2026 The HuaTuo Authors
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

"""Accept a toolstream connection, optionally recording decoded wire frames."""

import argparse
import json
import pathlib
import signal
import socket
import subprocess


def record_frames(connection, args):
    decoder = subprocess.Popen(
        ["capnp", "convert", "--short", "-I" + args.schema_import,
         "binary:json", args.schema, "Message"],
        stdin=connection, stdout=subprocess.PIPE, universal_newlines=True,
    )
    try:
        with open(args.output, "w") as output:
            for line in decoder.stdout:
                message = json.loads(line)
                if "connect" in message:
                    frame = {"connect": message["connect"]}
                else:
                    chunk = message["chunk"]
                    payload = bytes(chunk.get("data", []))
                    frame = {"end": chunk.get("end", False)}
                    if payload:
                        frame["event"] = json.loads(payload)
                output.write(json.dumps(frame) + "\n")
                output.flush()
        decoder.wait(timeout=5)
        if decoder.returncode:
            raise RuntimeError("capnp receiver failed with exit " + str(decoder.returncode))
    finally:
        if decoder.poll() is None:
            decoder.terminate()
        try:
            decoder.wait(timeout=5)
        except subprocess.TimeoutExpired:
            decoder.kill()
            decoder.wait()


def stop(_signal, _frame):
    raise SystemExit(0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--socket", required=True)
    parser.add_argument("--connected", required=True)
    parser.add_argument("--output")
    parser.add_argument("--schema")
    parser.add_argument("--schema-import")
    args = parser.parse_args()
    if args.output and not (args.schema and args.schema_import):
        parser.error("recording requires --schema and --schema-import")
    signal.signal(signal.SIGTERM, stop)

    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
        listener.bind(args.socket)
        listener.listen(1)
        connection, _ = listener.accept()
        with connection:
            pathlib.Path(args.connected).touch()
            if args.output:
                record_frames(connection, args)
            else:
                while True:
                    signal.pause()


if __name__ == "__main__":
    main()
