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

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "ci_changes", Path(__file__).with_name("ci-changes.py")
)
ci_changes = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci_changes)


class ChangesTest(unittest.TestCase):
    def test_paths(self):
        cases = [
            (b"docs/guide.md\0docs/img/example.png\0", False),
            (b"README.md\0nested/README.md\0", False),
            (b"docs/guide.md\0core/example.go\0", True),
            (b"data/img/example.png\0", True),
            (b"docs-old/example.png\0", True),
            (b"docs/a\nfile.png\0", False),
            (b"core/a\nfile.go\0", True),
            (b"", False),
        ]
        for paths, expected in cases:
            with self.subTest(paths=paths), patch.object(
                subprocess, "check_output", return_value=paths
            ):
                self.assertEqual(
                    ci_changes.requires_tests("push", {"before": "abc", "after": "def"}),
                    expected,
                )

    def test_pr_uses_merge_base(self):
        with patch.object(subprocess, "check_output", return_value=b"README.md\0") as diff:
            self.assertFalse(ci_changes.requires_tests("pull_request", {
                "pull_request": {"base": {"sha": "abc"}, "head": {"sha": "def"}}
            }))
            self.assertEqual(diff.call_args.args[0], [
                "git", "diff", "--no-renames", "--name-only", "-z", "abc...def", "--"
            ])

    def test_push_uses_full_push_range(self):
        with patch.object(subprocess, "check_output", return_value=b"README.md\0") as diff:
            ci_changes.requires_tests("push", {"before": "abc", "after": "def"})
            self.assertIn("abc..def", diff.call_args.args[0])

    def test_missing_history_runs_tests(self):
        with patch.object(subprocess, "check_output", side_effect=subprocess.CalledProcessError(1, "git")):
            self.assertTrue(ci_changes.requires_tests("push", {"before": "abc", "after": "def"}))

    def test_manual_and_new_branch_run_tests(self):
        with patch.object(subprocess, "check_output") as diff:
            self.assertTrue(ci_changes.requires_tests("workflow_dispatch", {}))
            self.assertTrue(ci_changes.requires_tests("push", {"before": "0" * 40}))
            self.assertTrue(ci_changes.requires_tests("push", {}))
            diff.assert_not_called()

    def test_git_history_and_workflow_output(self):
        script = Path(__file__).with_name("ci-changes.py").resolve()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*args):
                return subprocess.check_output(
                    ["git", "-C", directory, *args], stderr=subprocess.PIPE
                ).decode().strip()

            def commit():
                git("add", ".")
                git("-c", "user.name=CI", "-c", "user.email=ci@example.com",
                    "commit", "-m", "test")
                return git("rev-parse", "HEAD")

            def classify(base, head):
                # Keep event/output files outside the repository under test.
                with tempfile.TemporaryDirectory() as state:
                    event_path = Path(state) / "event.json"
                    output_path = Path(state) / "output"
                    event_path.write_text(json.dumps({
                        "pull_request": {"base": {"sha": base}, "head": {"sha": head}}
                    }))
                    subprocess.run(
                        [sys.executable, "-B", str(script)], cwd=root, check=True,
                        stdout=subprocess.PIPE, env={
                            **os.environ, "GITHUB_EVENT_NAME": "pull_request",
                            "GITHUB_EVENT_PATH": str(event_path),
                            "GITHUB_OUTPUT": str(output_path),
                        },
                    )
                    return output_path.read_text().strip()

            git("init")
            (root / "README.md").write_text("initial")
            initial = commit()
            (root / "README.md").write_text("updated")
            docs = commit()
            self.assertEqual(classify(initial, docs), "run-tests=false")
            (root / "code.go").write_text("package example")
            code = commit()
            self.assertEqual(classify(initial, code), "run-tests=true")
            (root / "docs").mkdir()
            git("mv", "code.go", "docs/code.go")
            moved = commit()
            self.assertEqual(classify(code, moved), "run-tests=true")


if __name__ == "__main__":
    unittest.main()
