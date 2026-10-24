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

"""External metadata/workload fixture; never imports or replaces agent code."""
import ctypes
import http.server
import json
import os
import pathlib
import signal
import socketserver
import ssl
import struct
import subprocess
import sys
import threading
import time


def serve(root):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.endswith('/info'):
                body = json.dumps({'DockerRootDir': str(root / 'docker')}).encode()
            elif self.path == '/configz':
                body = b'{"kubeletconfig":{"cgroupDriver":"cgroupfs"}}'
            elif self.path == '/pods':
                body = (root / 'pods.json').read_bytes()
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    class UnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
        daemon_threads = True

    docker = UnixServer(str(root / 'docker.sock'), Handler)
    kubelet = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(str(root / 'cert.pem'), str(root / 'key.pem'))
    kubelet.socket = context.wrap_socket(kubelet.socket, server_side=True)
    threading.Thread(target=docker.serve_forever, daemon=True).start()
    (root / 'port').write_text(str(kubelet.server_address[1]))
    kubelet.serve_forever()


def worker(fifo, ack):
    children = []

    def stop(*_):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGALRM, stop)
    signal.alarm(300)
    fd = os.open(str(fifo), os.O_RDWR)
    ack.write_text('ready')
    try:
        with os.fdopen(fd) as control:
            for command in control:
                for child in children:
                    child.terminate()
                    child.wait(timeout=5)
                children.clear()
                if command.strip() == 'busy':
                    for _ in range(2):
                        children.append(subprocess.Popen(
                            [sys.executable, '-c', 'while True: pass']))
                ack.write_text(command.strip())
    finally:
        for child in children:
            if child.poll() is None:
                child.terminate()
            child.wait(timeout=5)


def blocked(child_file):
    # CLONE_VFORK without CLONE_VM gives the child a private address space;
    # the parent waits in a killable D state until this bounded child exits.
    clone_nr = {'x86_64': 56, 'aarch64': 220}[os.uname().machine]
    libc = ctypes.CDLL(None, use_errno=True)
    parent = os.getpid()
    fd = os.open(str(child_file), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    child = libc.syscall(clone_nr, 0x4000 | signal.SIGCHLD, 0, 0, 0, 0)
    if child == -1:
        os.close(fd)
        raise OSError(ctypes.get_errno(), 'clone workload')
    if child == 0:
        libc.prctl(1, signal.SIGKILL)  # No orphan on parent cleanup/failure.
        if os.getppid() != parent:
            os._exit(0)
        os.write(fd, str(os.getpid()).encode())
        os.close(fd)
        time.sleep(120)
        os._exit(0)
    os.close(fd)
    os.waitpid(child, 0)


def kernfs_layout(layout, output):
    """Change only userspace CO-RE target shape, preserving kernel type IDs/offsets."""
    if layout not in ('scalar', 'union'):
        raise ValueError('expected scalar or union BTF layout')
    raw = pathlib.Path('/sys/kernel/btf/vmlinux').read_bytes()
    magic, version, flags, hdrlen, toff, tlen, soff, slen = struct.unpack_from('<HBBIIIII', raw)
    if magic != 0xeb9f or version != 1:
        raise ValueError('requires little-endian BTF version 1')
    types = bytearray(raw[hdrlen + toff:hdrlen + toff + tlen])
    strings = bytearray(raw[hdrlen + soff:hdrlen + soff + slen])

    def name(offset):
        return bytes(strings[offset:strings.index(0, offset)]).decode()

    records, offset, member_offset = [None], 0, None
    while offset < len(types):
        noff, info, size = struct.unpack_from('<III', types, offset)
        kind, count = (info >> 24) & 31, info & 65535
        lengths = {0: 0, 1: 4, 2: 0, 3: 12, 4: 12 * count, 5: 12 * count,
                   6: 8 * count, 7: 0, 8: 0, 9: 0, 10: 0, 11: 0, 12: 0,
                   13: 8 * count, 14: 4, 15: 12 * count, 16: 0, 17: 4,
                   18: 0, 19: 12 * count}
        if kind not in lengths:
            raise ValueError('unsupported BTF kind ' + str(kind))
        records.append((offset, kind, size))
        if kind == 4 and name(noff) == 'kernfs_node':
            for i in range(count):
                member = offset + 12 + i * 12
                if name(struct.unpack_from('<I', types, member)[0]) == 'id':
                    member_offset = member
        offset += 12 + lengths[kind]
    if offset != len(types) or member_offset is None:
        raise ValueError('invalid BTF or missing kernfs_node.id')
    scalar = struct.unpack_from('<I', types, member_offset + 4)[0]
    while records[scalar][1] in (8, 9, 10, 11, 18):
        scalar = records[scalar][2]
    pos, kind, size = records[scalar]
    if kind == 5:
        count = struct.unpack_from('<I', types, pos + 4)[0] & 65535
        for i in range(count):
            noff, tid, bits = struct.unpack_from('<III', types, pos + 12 + i * 12)
            if name(noff) == 'id' and bits == 0:
                scalar = tid
                break
    while records[scalar][1] in (8, 9, 10, 11, 18):
        scalar = records[scalar][2]
    if records[scalar][1:] != (1, 8):
        raise ValueError('kernfs ID is not a 64-bit integer')
    target = scalar
    if layout == 'union':
        union_name = len(strings)
        strings.extend(b'kernfs_node_id\0')
        field_name = len(strings)
        strings.extend(b'id\0')
        target = len(records)
        types.extend(struct.pack('<IIIIII', union_name, (5 << 24) | 1, 8,
                                 field_name, scalar, 0))
    struct.pack_into('<I', types, member_offset + 4, target)
    header = struct.pack('<HBBIIIII', magic, version, flags, 24, 0, len(types),
                         len(types), len(strings))
    output.write_bytes(header + types + strings)


def main():
    mode = sys.argv[1]
    if mode == 'kernfs-layout':
        kernfs_layout(sys.argv[2], pathlib.Path(sys.argv[3]))
    elif mode == 'serve':
        serve(pathlib.Path(sys.argv[2]))
    elif mode == 'blocked':
        blocked(pathlib.Path(sys.argv[2]))
    elif mode == 'worker':
        worker(pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3]))
    else:
        raise ValueError('unknown fixture mode: ' + mode)


if __name__ == '__main__':
    main()
