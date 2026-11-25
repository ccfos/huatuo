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

import ctypes
import gc
import re
import sys
class MemsnapshotPayload:
    __slots__ = ("payload",)
objects = []
pressure = None
# Establish version discoverability independently of the remote scanner.
with open("/proc/self/maps") as maps:
    version_available = hasattr(ctypes.pythonapi, "Py_Version") or any(
        re.search(r"/libpython3\.[0-9]+", line) for line in maps
    )
while True:
    command = sys.stdin.read(1)
    if command == "a":
        objects = [MemsnapshotPayload() for _ in range(20000)]
        for obj in objects:
            obj.payload = list(range(64))
        gc.collect()
        print("python_version=%d.%d.%d" % sys.version_info[:3], flush=True)
        print("python_version_available=" + str(int(version_available)), flush=True)
        print("object_bytes=" + str(sys.getsizeof(objects[0])), flush=True)
        print("ready", flush=True)
    elif command == "p":
        pressure = bytearray(80 * 1024 * 1024)
        print("pressure ready", flush=True)
    elif command == "q" or not command:
        break
