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

import multiprocessing
import sys


def child_hot_method():
    value = 1
    while True:
        value = (value * 33 + 17) % 1_000_003


def parent_hot_method(child_pid_file):
    child = multiprocessing.Process(target=child_hot_method, daemon=True)
    child.start()
    with open(child_pid_file, "w", encoding="utf-8") as output:
        output.write(str(child.pid))
        output.flush()

    value = 1
    while True:
        value = (value * 31 + 19) % 1_000_003


def independent_hot_method():
    value = 1
    while True:
        value = (value * 29 + 23) % 1_000_003


if __name__ == "__main__":
    mode = sys.argv[1]
    if mode == "parent":
        parent_hot_method(sys.argv[2])
    elif mode == "independent":
        independent_hot_method()
    else:
        raise SystemExit(f"unsupported mode: {mode}")
