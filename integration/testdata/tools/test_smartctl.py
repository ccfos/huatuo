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

# Mock smartctl JSON, exit codes and completion timing for evidence cases.
import json
import os
from pathlib import Path
import sys
import time


directory = Path(os.environ["HUATUO_IO_EVIDENCE_CASE"])
mode = (directory / "mode").read_text().strip()
target = (directory / "target").read_text().strip()


def record(phase):
    with (directory / "commands.jsonl").open("a") as output:
        output.write(json.dumps({"phase": phase, "mode": mode, "pid": os.getpid(),
                                 "args": sys.argv[1:], "monotonic": time.monotonic()}) + "\n")


record("start")
exit_status = 0
try:
    if not sys.argv[1:] or sys.argv[-1] != f"/dev/{target}":
        raise RuntimeError(f"unexpected evidence target: {sys.argv[1:]}")
    if mode == "timeout":
        time.sleep(30)
        raise RuntimeError("evidence command exceeded its expected cancellation window")
    if mode == "gate":
        while not (directory / "release").exists():
            time.sleep(0.02)
    if mode == "exec":
        print("controlled smartctl execution failure", file=sys.stderr)
        exit_status = 2
    elif mode == "partial":
        print(json.dumps({"smart_status": {"passed": False},
                          "scsi_grown_defect_list": "invalid",
                          "temperature": {"current": "invalid", "drive_trip": 65}}))
    else:
        print(json.dumps({
            "smartctl": {"exit_status": 8},
            "smart_status": {"passed": False, "scsi": {"asc": 0, "ascq": 0}},
            "temperature": {"current": 0, "drive_trip": 0},
            "scsi_grown_defect_list": 0,
            "scsi_error_counter_log": {"read": {
                "errors_corrected_by_eccdelayed": 0,
                "errors_corrected_by_rereads_rewrites": 0,
                "gigabytes_processed": "0.000",
                "total_uncorrected_errors": 0,
            }},
            "scsi_pending_defects": {"count": 0, "table": []},
        }))
        exit_status = 8
finally:
    record("end")
sys.exit(exit_status)
