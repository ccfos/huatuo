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

import gc
import sys
class MemsnapshotPayload:
    __slots__ = ("payload",)
objects = []
pressure = None
while True:
    command = sys.stdin.read(1)
    if command == "a":
        objects = [MemsnapshotPayload() for _ in range(20000)]
        for obj in objects:
            obj.payload = list(range(64))
        gc.collect()
        print("object_bytes=" + str(sys.getsizeof(objects[0])), flush=True)
        print("ready", flush=True)
    elif command == "p":
        pressure = bytearray(80 * 1024 * 1024)
        print("pressure ready", flush=True)
    elif command == "q" or not command:
        break
