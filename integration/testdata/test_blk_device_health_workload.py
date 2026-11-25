#!/usr/bin/env python3

# Copyright 2026 The HuaTuo Authors
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

# Generate sealed-memfd and bounded SG_IO inputs; Shell owns assertions.
import ctypes
import fcntl
import json
import os
from pathlib import Path
import sys
import time


def memfd(arguments):
    path_file, seal_request, sealed_file, stop_file = map(Path, arguments)
    if hasattr(os, "memfd_create"):
        fd = os.memfd_create(
            "huatuo-io-health",
            getattr(os, "MFD_ALLOW_SEALING", 2),
        )
    else:
        libc = ctypes.CDLL(None, use_errno=True)
        libc.memfd_create.argtypes = [ctypes.c_char_p, ctypes.c_uint]
        libc.memfd_create.restype = ctypes.c_int
        fd = libc.memfd_create(b"huatuo-io-health", 2)
        if fd < 0:
            errno = ctypes.get_errno()
            raise OSError(errno, os.strerror(errno))
    os.ftruncate(fd, 8 * 1024 * 1024)
    path_file.write_text(f"/proc/{os.getpid()}/fd/{fd}\n", encoding="ascii")

    while not seal_request.exists():
        if stop_file.exists():
            sys.exit(0)
        time.sleep(0.05)

    fcntl.fcntl(
        fd,
        getattr(fcntl, "F_ADD_SEALS", 1033),
        getattr(fcntl, "F_SEAL_WRITE", 0x0008),
    )
    sealed_file.touch()
    while not stop_file.exists():
        time.sleep(0.05)


def scsi_read(arguments):
    class Request(ctypes.Structure):
        _fields_ = [
            ("interface_id", ctypes.c_int), ("dxfer_direction", ctypes.c_int),
            ("cmd_len", ctypes.c_ubyte), ("mx_sb_len", ctypes.c_ubyte),
            ("iovec_count", ctypes.c_ushort), ("dxfer_len", ctypes.c_uint),
            ("dxferp", ctypes.c_void_p), ("cmdp", ctypes.c_void_p), ("sbp", ctypes.c_void_p),
            ("timeout", ctypes.c_uint), ("flags", ctypes.c_uint), ("pack_id", ctypes.c_int),
            ("usr_ptr", ctypes.c_void_p), ("status", ctypes.c_ubyte),
            ("masked_status", ctypes.c_ubyte), ("msg_status", ctypes.c_ubyte),
            ("sb_len_wr", ctypes.c_ubyte), ("host_status", ctypes.c_ushort),
            ("driver_status", ctypes.c_ushort), ("resid", ctypes.c_int),
            ("duration", ctypes.c_uint), ("info", ctypes.c_uint),
        ]


    command = (ctypes.c_ubyte * 10)(0x28, 0, 0, 0, 0, 0, 0, 0, 1, 0)
    sense = ctypes.create_string_buffer(64)
    data = ctypes.create_string_buffer(int(arguments[1]))
    request = Request()
    request.interface_id = ord("S")
    request.dxfer_direction = -3
    request.cmd_len = len(command)
    request.mx_sb_len = len(sense)
    request.dxfer_len = len(data)
    request.dxferp = ctypes.addressof(data)
    request.cmdp = ctypes.addressof(command)
    request.sbp = ctypes.addressof(sense)
    request.timeout = 1000
    libc = ctypes.CDLL(None, use_errno=True)
    libc.ioctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_void_p]
    libc.ioctl.restype = ctypes.c_int
    started = time.monotonic()
    fd = os.open(arguments[0], os.O_RDONLY | os.O_NONBLOCK)
    try:
        result = libc.ioctl(fd, 0x2285, ctypes.byref(request))
    finally:
        os.close(fd)
    print(json.dumps({"result": result, "errno": ctypes.get_errno(),
                      "status": request.status, "host_status": request.host_status,
                      "driver_status": request.driver_status,
                      "seconds": time.monotonic() - started}), flush=True)


if __name__ == "__main__":
    if len(sys.argv) == 6 and sys.argv[1] == "memfd":
        memfd(sys.argv[2:])
    elif len(sys.argv) == 4 and sys.argv[1] == "scsi-read":
        scsi_read(sys.argv[2:])
    else:
        sys.exit("usage: test_blk_device_health_workload.py memfd PATH SEAL SEALED STOP | scsi-read DEVICE BLOCK_SIZE")
