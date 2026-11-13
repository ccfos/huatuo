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

import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


class MatrixTest(unittest.TestCase):
    def matrix(self, event, selection):
        workflow = Path(__file__).parents[1] / "os-distro-qemu-test-reusable.yml"
        # Execute the workflow's actual matrix script to avoid a duplicate policy.
        script = workflow.read_text().split("        run: |\n", 1)[1]
        script = textwrap.dedent(script.split("\n  # start VMs", 1)[0])
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            result = subprocess.run(
                ["bash", "-e", "-o", "pipefail", "-c", script],
                env={**os.environ, "EVENT_NAME": event, "INPUT_OS": selection,
                     "GITHUB_OUTPUT": str(output)},
                capture_output=True, text=True,
            )
            if result.returncode:
                return result, []
            matrix = json.loads(output.read_text().removeprefix("matrix="))
            return result, [entry["os"] for entry in matrix["include"]]

    def test_pr_representative_guests(self):
        result, guests = self.matrix("pull_request", "all")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(set(guests), {
            "rocky8", "openeuler22.03", "openeuler24.03", "ubuntu26.04", "ubuntu20.04"
        })
        self.assertEqual(len(guests), 5)

    def test_main_full_matrix(self):
        result, guests = self.matrix("push", "rocky8")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(guests), 13)
        self.assertIn("ubuntu20.04", guests)
        self.assertIn("debian12", guests)

    def test_manual_selection(self):
        result, guests = self.matrix("workflow_dispatch", " rocky8, ubuntu20.04 ")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(set(guests), {"rocky8", "ubuntu20.04"})

    def test_manual_all(self):
        result, guests = self.matrix("workflow_dispatch", "all")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(guests), 13)

    def test_invalid_manual_selection(self):
        for selection in ("   ", "unknown-distro"):
            with self.subTest(selection=selection):
                result, _ = self.matrix("workflow_dispatch", selection)
                self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
