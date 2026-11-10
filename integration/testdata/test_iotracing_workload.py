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

# Generate bounded file and block IO; Shell owns product assertions and cleanup.

import argparse
import json
import mmap
import os
from pathlib import Path
import signal
import time

running = True


def stop(signum, frame):
    global running
    running = False


def block_read(args):
    device, ready = args.device, args.ready
    fd = os.open(device, os.O_RDONLY | os.O_CLOEXEC)
    page_size = os.sysconf("SC_PAGE_SIZE")
    offset = (os.lseek(fd, 0, os.SEEK_END) // page_size - 1) * page_size
    published = False
    try:
        while True:
            os.posix_fadvise(fd, offset, page_size, os.POSIX_FADV_DONTNEED)
            if len(os.pread(fd, page_size, offset)) != page_size:
                raise OSError("short buffered block-device read")
            if not published:
                Path(ready).write_text(str(os.getpid()), encoding="ascii")
                published = True
            time.sleep(0.01)
    finally:
        os.close(fd)


def files(args):
    directory, name, ready, budget = args.directory, args.name, args.ready, args.budget
    requested_batch = 0
    published_batch = -1

    def request_batch(signum, frame):
        nonlocal requested_batch
        requested_batch += 1

    def publish(record):
        temporary = Path(ready + ".next")
        temporary.write_text(json.dumps(record) + "\n")
        temporary.replace(ready)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGUSR1, request_batch)
    files = [os.open(f"{directory}/{name}-{i}", os.O_CREAT | os.O_RDWR | os.O_SYNC, 0o600)
             for i in range(3)]
    direct_fd = os.open(f"{directory}/{name}-direct", os.O_CREAT | os.O_RDWR | os.O_DIRECT, 0o600)
    aligned = mmap.mmap(-1, 4096, flags=mmap.MAP_PRIVATE | mmap.MAP_ANONYMOUS)
    shared_fd = os.open(f"{directory}/{name}-shared", os.O_CREAT | os.O_RDWR, 0o600)
    os.ftruncate(shared_fd, mmap.PAGESIZE)
    deadline = time.monotonic() + float(budget)
    try:
        while running and time.monotonic() < deadline:
            batch = requested_batch
            report = batch > published_batch
            receipt = {"pid": os.getpid(), "batch": batch, "phase": "running",
                       "started_ns": int(time.monotonic() * 1000000000)}
            if report and batch:
                publish(receipt)
            read_bytes = 0
            sync_write_bytes = 0
            for i, fd in enumerate(files):
                size = 4096 * (i + 1)
                if os.pwrite(fd, b"x" * size, 0) != size:
                    raise RuntimeError("short synchronous write")
                if os.pread(fd, size, 0) != b"x" * size:
                    raise RuntimeError("short or incorrect synchronous file read")
                read_bytes += size
                sync_write_bytes += size
            if os.pwrite(direct_fd, aligned, 0) != len(aligned):
                raise RuntimeError("short Direct IO write")
            # Each iteration needs a fresh shared write fault.
            with mmap.mmap(shared_fd, mmap.PAGESIZE, flags=mmap.MAP_SHARED) as shared:
                shared[0] = 1
                shared.flush()
            if report:
                # The first round initializes files; later receipts cover a full
                # iteration beginning after the Shell's capture marker.
                receipt.update({"phase": "completed" if batch else "ready",
                                "finished_ns": int(time.monotonic() * 1000000000),
                                "read_bytes": read_bytes, "sync_write_bytes": sync_write_bytes,
                                "direct_write_bytes": len(aligned), "shared_write_bytes": 1})
                publish(receipt)
                published_batch = batch
    finally:
        os.close(shared_fd)
        aligned.close()
        os.close(direct_fd)
        for fd in files:
            os.close(fd)


def content(args):
    path, ready = args.path, args.ready
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_SYNC, 0o600)
    deadline = time.monotonic() + 90
    try:
        payload = b"x" * 65536
        while running and time.monotonic() < deadline:
            if os.pwrite(fd, payload, 0) != len(payload):
                raise RuntimeError("short synchronous content write")
            if not Path(ready).exists():
                Path(ready).write_text(str(os.fstat(fd).st_ino))
            time.sleep(0.01)
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description="File and buffered block IO workloads")
    commands = parser.add_subparsers(dest="command")
    block = commands.add_parser("block-read")
    block.add_argument("device")
    block.add_argument("ready")
    block.set_defaults(workload=block_read)
    file_load = commands.add_parser("files")
    file_load.add_argument("directory")
    file_load.add_argument("name")
    file_load.add_argument("ready")
    file_load.add_argument("budget", type=float)
    file_load.set_defaults(workload=files)
    content_load = commands.add_parser("content")
    content_load.add_argument("path")
    content_load.add_argument("ready")
    content_load.set_defaults(workload=content)
    args = parser.parse_args()
    if args.command is None:
        parser.error("a workload is required")
    args.workload(args)


if __name__ == "__main__":
    main()
