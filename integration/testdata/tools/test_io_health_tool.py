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

# Supply controlled SCSI responses or record real private NVMe health commands.
import json
import os
from pathlib import Path
import subprocess
import sys
import time

if Path(sys.argv[0]).name == "nvme":
    directory = Path(os.environ["HUATUO_IO_EVIDENCE_CASE"])
    target = (directory / "target").read_text().strip()
    nqn = (directory / "nqn").read_text().strip()
    executable = (directory / "nvme-command").read_text().strip()
    timeout = (directory / "timeout-command").read_text().strip()
    arguments = sys.argv[1:]


    def record(phase, **fields):
        with (directory / "commands.jsonl").open("a") as output:
            output.write(json.dumps({"phase": phase, "pid": os.getpid(), "args": arguments,
                                     "monotonic": time.monotonic(), **fields}) + "\n")


    record("attempt")
    if not arguments or arguments[0] not in ("smart-log", "error-log"):
        raise RuntimeError(f"unexpected NVMe evidence command: {arguments}")
    devices = [arg for arg in arguments if arg.startswith("/dev/")]
    if devices != [f"/dev/{target}"]:
        raise RuntimeError(f"unexpected NVMe evidence target: {devices}")
    if Path(f"/sys/class/nvme/{target}/subsysnqn").read_text().strip() != nqn:
        raise RuntimeError("NVMe controller no longer belongs to this test")

    # Journal the group before spawning: cancellation may kill this wrapper
    # before it can reap the real command. Shell cleanup owns the whole group.
    if os.getpgrp() != os.getpid():
        os.setsid()
    record("start", pgid=os.getpgrp())
    try:
        result = subprocess.run([timeout, "--foreground", "--kill-after=1s", "8s", executable, *arguments],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=10, check=False)
    except subprocess.TimeoutExpired as error:
        record("end", timeout=True)
        print(f"private nvme-cli command did not finish: {error}", file=sys.stderr)
        sys.exit(1)
    output_name = f"{os.getpid()}-{arguments[0]}.json"
    (directory / output_name).write_bytes(result.stdout)
    (directory / f"{os.getpid()}-{arguments[0]}.err").write_bytes(result.stderr)
    record("end", output=output_name, returncode=result.returncode)
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    sys.exit(result.returncode if result.returncode >= 0 else 1)

elif Path(sys.argv[0]).name == "smartctl":
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

else:
    raise RuntimeError(f"unexpected evidence tool name: {Path(sys.argv[0]).name}")
