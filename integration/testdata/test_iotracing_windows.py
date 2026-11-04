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

# Supply commanded diskstats snapshots and record their delivery boundaries.
# Scenario decisions and observations of Huatuo belong to the Shell test.

import argparse
import ctypes
import errno
import json
import os
from pathlib import Path
import signal
import struct
import time


class DiskstatsSupplier:
    def __init__(self, root, workspace):
        self.root = root
        self.workspace = workspace
        self.diskstats = root / "proc/diskstats"
        self.receipts = (workspace / "diskstats-reads.jsonl").open("w")
        self.counters = {"vda": [100, 100, 100, 100], "vdb": [100, 100, 100, 100]}
        self.command = None
        self.is_pending = False
        self.has_supplied = False
        self.is_regular = False
        self.closed_count = 0
        self.unmatched_closes = 0
        self.overflow_count = 0
        self.published_ns = 0
        self.next_update = 0
        self.libc = ctypes.CDLL(None, use_errno=True)
        self.watch = self.libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
        if self.watch < 0:
            raise OSError(ctypes.get_errno(), "inotify_init1")
        os.mkfifo(self.diskstats, 0o600)
        # IN_CLOSE_NOWRITE distinguishes completed reads from fixture writes;
        # watching the directory also follows atomic diskstats replacement.
        self.directory_watch = self.libc.inotify_add_watch(self.watch, os.fsencode(self.diskstats.parent), 0x10)
        self.fifo_watch = self.libc.inotify_add_watch(self.watch, os.fsencode(self.diskstats), 0x10)
        if self.directory_watch < 0 or self.fifo_watch < 0:
            raise OSError(ctypes.get_errno(), "inotify_add_watch")
        (root / "supplier.ready").touch()

    def receipt(self, action, **values):
        self.receipts.write(json.dumps({
            "monotonic_ns": int(time.monotonic() * 1e9),
            "action": action,
            "id": self.command["id"] if self.command else 0,
            **values,
        }) + "\n")
        self.receipts.flush()

    def save_state(self):
        path = self.root / "supplier-state.next"
        path.write_text(json.dumps({
            "id": self.command["id"] if self.command else 0,
            "closed_count": self.closed_count,
            "has_supplied": self.has_supplied,
            "published_ns": self.published_ns,
            "unmatched_closes": self.unmatched_closes,
            "overflow_count": self.overflow_count,
        }))
        path.replace(self.root / "supplier-state.json")

    def consume_closes(self):
        try:
            data = os.read(self.watch, 4096)
        except BlockingIOError:
            return
        offset = 0
        while offset < len(data):
            watch, mask, _, length = struct.unpack_from("iIII", data, offset)
            filename = data[offset + 16:offset + 16 + length].split(b"\0", 1)[0]
            offset += 16 + length
            if mask & 0x4000:
                self.overflow_count += 1
                self.receipt("notification_overflow")
            if not mask & 0x10:
                continue
            if self.is_regular:
                if watch != self.directory_watch or filename != b"diskstats":
                    continue
            elif watch != self.fifo_watch:
                continue
            if not self.is_regular and not self.is_pending:
                self.unmatched_closes += 1
            self.is_pending = False
            self.closed_count += 1
            self.receipt("reader_closed", regular=self.is_regular)
        self.save_state()

    def load_command(self):
        if self.is_pending:
            return
        try:
            command = json.loads((self.root / "supplier-command.json").read_text())
        except FileNotFoundError:
            return
        if self.command and command["id"] == self.command["id"]:
            return
        self.command = command
        self.has_supplied = False
        self.closed_count = 0
        self.published_ns = 0
        self.receipt("command", command=command)
        self.save_state()

    def snapshot(self):
        lines = []
        for disk, minor in (("vda", 0), ("vdb", 16)):
            read, write = self.command[disk]
            counters = self.counters[disk]
            counters[0] += 10
            counters[1] += 10 * read
            counters[2] += 10
            counters[3] += 10 * write
            lines.append(f"8 {minor} {disk} {counters[0]} 0 {counters[0] * 8} {counters[1]} "
                         f"{counters[2]} 0 {counters[2] * 8} {counters[3]} 0 0 0 0 0 0 0 0 0\n")
        return "".join(lines).encode()

    def publish_regular(self):
        payload = self.snapshot()
        temporary = self.diskstats.with_suffix(".next")
        temporary.write_bytes(payload)
        temporary.replace(self.diskstats)
        if not self.published_ns:
            self.published_ns = int(time.monotonic() * 1e9)
        self.next_update = time.monotonic() + 1
        self.receipt("published", payload=payload.decode())
        self.save_state()
        return payload

    def supply(self):
        if not self.command:
            return
        if self.command["mode"] == "regular":
            if not self.is_regular:
                # A reader can resolve the FIFO before replacement. Supply that
                # reader one final complete snapshot without blocking rename.
                fd = os.open(self.diskstats, os.O_RDWR | os.O_NONBLOCK)
                try:
                    payload = self.publish_regular()
                    if os.write(fd, payload) != len(payload):
                        raise OSError("short final FIFO snapshot write")
                finally:
                    os.close(fd)
                self.is_regular = True
                self.receipt("switched_to_regular")
            elif not self.published_ns or time.monotonic() >= self.next_update:
                self.publish_regular()
            return
        if self.is_regular:
            temporary = self.diskstats.with_suffix(".next")
            os.mkfifo(temporary, 0o600)
            self.fifo_watch = self.libc.inotify_add_watch(self.watch, os.fsencode(temporary), 0x10)
            if self.fifo_watch < 0:
                raise OSError(ctypes.get_errno(), "inotify_add_watch")
            temporary.replace(self.diskstats)
            # Watch this inode so a late close of the replaced regular file
            # cannot acknowledge delivery of the new FIFO command.
            self.is_regular = False
            self.receipt("switched_to_fifo")
        if self.is_pending or (self.has_supplied and not self.command["repeat"]):
            return
        try:
            fd = os.open(self.diskstats, os.O_WRONLY | os.O_NONBLOCK)
        except OSError as error:
            if error.errno == errno.ENXIO:
                return
            raise
        payload = self.snapshot()
        try:
            if os.write(fd, payload) != len(payload):
                raise OSError("short FIFO snapshot write")
            # Commit delivery before EOF lets the reader start a capture;
            # its asynchronous close notification may be processed later.
            self.is_pending = True
            self.has_supplied = True
            self.receipt("supplied", payload=payload.decode())
            self.save_state()
        finally:
            os.close(fd)

    def run(self):
        try:
            while True:
                self.consume_closes()
                self.load_command()
                self.supply()
                time.sleep(0.02)
        finally:
            os.close(self.watch)
            self.receipts.close()


def interrupted(signum, frame):
    raise SystemExit(128 + signum)


def main():
    parser = argparse.ArgumentParser(description="commanded diskstats input supplier")
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--workspace", type=Path, required=True)
    args = parser.parse_args()
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    DiskstatsSupplier(args.root, args.workspace).run()


if __name__ == "__main__":
    main()
