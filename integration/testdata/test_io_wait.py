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

# Generate direct IO and record application progress independently of Huatuo.
# The Shell cases own metric assertions and interpret the timing receipts.

import argparse
import json
import mmap
import os
from pathlib import Path
import sys
import time


def workload(args):
    flags = os.O_DIRECT | (os.O_RDONLY if args.operation == "read" else os.O_WRONLY)
    descriptor = os.open(args.device, flags)
    try:
        with mmap.mmap(-1, 4096) as buffer:
            buffer[:] = bytes(4096)
            Path(args.cgroup, "cgroup.procs").write_text(str(os.getpid()))
            started = time.monotonic()
            Path(str(args.output) + ".started").write_text(str(os.getpid()))
            for completed in range(1, args.requests + 1):
                os.lseek(descriptor, 0, os.SEEK_SET)
                size = os.readv(descriptor, [buffer]) if args.operation == "read" else os.write(descriptor, buffer)
                if size != 4096:
                    raise ValueError("short direct IO: {} bytes".format(size))
                if args.progress:
                    temporary = args.progress.with_suffix(".next")
                    temporary.write_text(json.dumps({"pid": os.getpid(), "completed": completed,
                                                     "requests": args.requests}) + "\n")
                    temporary.replace(args.progress)
            if args.operation == "write":
                os.fsync(descriptor)
            result = {"requests": args.requests, "operation": args.operation,
                      "seconds": time.monotonic() - started}
            args.output.write_text(json.dumps(result) + "\n")
    finally:
        os.close(descriptor)


def main():
    parser = argparse.ArgumentParser(description="Direct IO workload and completion receipts")
    commands = parser.add_subparsers(dest="command")
    load = commands.add_parser("workload")
    load.add_argument("--device", required=True)
    load.add_argument("--cgroup", required=True)
    load.add_argument("--operation", choices=("read", "write"), required=True)
    load.add_argument("--requests", type=int, default=32)
    load.add_argument("--output", type=Path, required=True)
    load.add_argument("--progress", type=Path)
    args = parser.parse_args()
    if args.command != "workload":
        parser.error("workload is required")
    try:
        workload(args)
    except (OSError, ValueError) as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
