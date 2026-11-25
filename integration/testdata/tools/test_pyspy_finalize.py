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

"""py-spy stand-in that writes its final sample only after SIGINT."""

import os
from pathlib import Path
import signal
import sys

pid = sys.argv[sys.argv.index("-p") + 1]
output = sys.argv[sys.argv.index("-o") + 1]


def finish(_signum, _frame):
    if pid == os.environ.get("PROFILER_FINALIZE_FAIL_PID"):
        sys.exit(7)
    Path(output).write_text("final_worker (app.py:10) 3\n")
    sys.exit(0)


signal.signal(signal.SIGINT, finish)
Path(os.environ["PROFILER_FINALIZE_READY_DIR"], pid).touch()
while True:
    signal.pause()
