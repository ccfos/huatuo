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

"""Skip expensive CI only when every changed path is documentation."""

import json
import os
import subprocess


def requires_tests(event_name, event):
    if event_name == "pull_request":
        pr = event["pull_request"]
        revision = f'{pr["base"]["sha"]}...{pr["head"]["sha"]}'
    elif event_name == "push":
        base = event.get("before", "")
        if not base or set(base) == {"0"}:
            return True
        revision = f'{base}..{event["after"]}'
    else:
        return True

    try:
        # Disable rename detection so moving code into docs still runs tests.
        paths = subprocess.check_output(
            ["git", "diff", "--no-renames", "--name-only", "-z", revision, "--"]
        ).split(b"\0")
    except subprocess.CalledProcessError:
        # Missing history must never silently disable validation.
        print("Could not compare revisions; running tests.")
        return True

    return any(
        not (path.startswith(b"docs/") or path.endswith(b".md"))
        for path in paths
        if path
    )


if __name__ == "__main__":
    with open(os.environ["GITHUB_EVENT_PATH"], encoding="utf-8") as stream:
        event = json.load(stream)
    run_tests = requires_tests(os.environ["GITHUB_EVENT_NAME"], event)
    value = str(run_tests).lower()
    print(f"run-tests={value}")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
        stream.write(f"run-tests={value}\n")
