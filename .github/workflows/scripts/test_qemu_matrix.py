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

import copy
import importlib.util
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
            outputs = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertIn("ubuntu26.04.amd64-", outputs["fast-image"])
            matrix = json.loads(outputs["matrix"])
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


spec = importlib.util.spec_from_file_location(
    "fast_pass", Path(__file__).with_name("fast-pass.py"))
fast_pass = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fast_pass)


class FastPassTest(unittest.TestCase):
    def setUp(self):
        self.tests = [f"integration/test_{index:02}.sh" for index in range(25)]
        self.tests += [f"e2e/test_{index:02}.sh" for index in range(11)]
        self.timings = {test: 10 for test in self.tests}
        self.plan = fast_pass.make_plan(self.tests, self.timings, "revision", "image")
        self.shard = next(shard for shard in self.plan["shards"] if shard["suite"] == "integration")
        self.report = {
            "suite": "integration", "status": "completed", "exit_code": 0,
            "runs_per_test": 1,
            "test_executions": [self.execution(test) for test in self.shard["tests"]],
        }

    @staticmethod
    def execution(test):
        return {"test_name": Path(test).name, "run_index": 1, "result": "passed",
                "exit_code": 0, "reason": None, "started_at": "start", "finished_at": "end"}

    def test_full_plan_is_deterministic_and_complete(self):
        self.assertEqual(len(self.plan["shards"]), 14)
        fast_pass.verify_plan(self.plan, self.tests, "revision", "image")
        self.assertEqual(self.plan, fast_pass.make_plan(
            list(reversed(self.tests)), self.timings, "revision", "image") | {"inventory": self.tests})
        self.assertTrue(all(shard["kubernetes"] for shard in self.plan["shards"] if shard["suite"] == "e2e"))

    def test_longest_tests_are_distributed_first(self):
        timings = {test: (1000 if index < 10 else 1) for index, test in enumerate(self.tests)}
        plan = fast_pass.make_plan(self.tests, timings, "revision", "image")
        loads = [shard["seconds"] for shard in plan["shards"] if shard["suite"] == "integration"]
        self.assertLessEqual(max(loads) - min(loads), 1)

    def test_new_tests_get_full_setup_and_fallback_duration(self):
        plan = fast_pass.make_plan(["integration/test_new.sh"], {}, "revision", "image")
        self.assertTrue(plan["shards"][1]["kubernetes"])
        self.assertEqual(plan["shards"][1]["seconds"], 120)

    def test_go_fixture_shards_restore_cache(self):
        tests = ["integration/test_io_kernel.sh", "integration/test_profiler_config.sh",
                 "e2e/test_autotracing_memory_threshold_snapshot_lifecycle.sh"]
        plan = fast_pass.make_plan(tests, {}, "revision", "image")
        for shard in plan["shards"]:
            expected = shard["suite"] == "unit" or any(
                test in fast_pass.GO_FIXTURE_TESTS for test in shard["tests"])
            self.assertEqual(shard["go_cache"], expected)
        self.assertTrue(plan["shards"][0]["go_cache"])

    def test_exact_diagnostic_selection(self):
        for selection in ("unit", "integration-01", self.tests[0]):
            plan = fast_pass.make_plan(self.tests, self.timings, "revision", "image", selection)
            self.assertEqual(len(plan["shards"]), 1)
            fast_pass.verify_plan(plan, self.tests, "revision", "image")
        with self.assertRaises(ValueError):
            fast_pass.make_plan(self.tests, self.timings, "revision", "image", "unknown")

    def test_partial_runs_require_manual_fast_stage(self):
        script = Path(__file__).with_name("fast-pass.py")
        for event, stage, expected_code in (
            ("pull_request", "fast", 1), ("push", "fast", 1),
            ("workflow_dispatch", "full", 1), ("workflow_dispatch", "fast", 0),
        ):
            with self.subTest(event=event, stage=stage), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["python3", str(script), "plan", "--selection", "unit", "--plan", str(root / "plan.json")],
                    env={**os.environ, "GITHUB_EVENT_NAME": event, "CI_STAGE": stage,
                         "VM_IMAGE_REF": "image", "GITHUB_OUTPUT": str(root / "output"),
                         "GITHUB_STEP_SUMMARY": str(root / "summary")},
                    capture_output=True, text=True,
                )
                self.assertEqual(result.returncode, expected_code, result.stderr)
                if expected_code == 0:
                    plan = json.loads((root / "plan.json").read_text())
                    self.assertEqual([shard["id"] for shard in plan["shards"]], ["unit"])
                else:
                    self.assertIn("manual stage=fast", result.stderr)
                    self.assertFalse((root / "plan.json").exists())

    def test_missing_duplicate_and_misassigned_tests_fail(self):
        for change in ("missing", "duplicate", "wrong_suite", "hidden_in_unit"):
            plan = copy.deepcopy(self.plan)
            if change == "missing":
                plan["shards"][1]["tests"].pop()
            elif change == "duplicate":
                plan["shards"][1]["tests"].append(self.tests[0])
            elif change == "wrong_suite":
                plan["shards"][1]["suite"] = "e2e"
            else:
                plan["shards"][0]["tests"].append(plan["shards"][1]["tests"].pop())
            with self.subTest(change=change), self.assertRaises(ValueError):
                fast_pass.verify_plan(plan, self.tests, "revision", "image")
        with self.assertRaises(ValueError):
            fast_pass.verify_plan(self.plan, self.tests, "other-commit", "image")
        with self.assertRaises(ValueError):
            fast_pass.verify_plan(self.plan, self.tests, "revision", "other-image")

    def test_complete_report_passes(self):
        fast_pass.verify_report(self.shard, self.report)

    def test_aborted_missing_duplicate_and_unstarted_results_fail(self):
        for change in ("aborted", "missing", "duplicate", "unstarted", "failed", "prerequisite_skip"):
            report = copy.deepcopy(self.report)
            case = report["test_executions"][0]
            if change == "aborted":
                report["status"] = "aborted"
            elif change == "missing":
                report["test_executions"].pop()
            elif change == "duplicate":
                report["test_executions"].append(copy.deepcopy(case))
            elif change == "unstarted":
                case["started_at"] = None
            elif change == "failed":
                case.update(result="failed", exit_code=1)
            else:
                case.update(result="skipped", exit_code=77, reason="command is not installed: gcc")
            with self.subTest(change=change), self.assertRaises(ValueError):
                fast_pass.verify_report(self.shard, report)

    def test_platform_skip_requires_exact_test_and_reason(self):
        cases = (
            ("e2e/test_autotracing_dload.sh",
             "dload unsupported-mode behavior verified; functional/ES cases require Legacy cgroup (actual=unified)"),
            ("integration/test_sched_blame.sh", "SchedBlame requires Linux 4.18 through 6.5"),
            ("integration/test_dropwatch_netdevsim.sh", "netdevsim registered no drop trap"),
            ("integration/test_profiler_python_exceptions.sh",
             "py-spy probe for Python 3.14.4 exited 1: Error: Unsupported version of Python: 3.14.4\n"
             "\nCaused by:\n    unsupported Python version; unsupported interpreter"),
        )
        for test, reason in cases:
            with self.subTest(test=test):
                suite = test.split("/")[0]
                shard = {"id": "selected-test", "suite": suite, "tests": [test]}
                report = copy.deepcopy(self.report)
                report["suite"] = suite
                case = self.execution(test)
                case.update(result="skipped", exit_code=77, reason=reason)
                report["test_executions"] = [case]
                fast_pass.verify_report(shard, report)
                for invalid_reason in ("command is not installed: gcc",
                                       "test exited with code 77 without a skip reason"):
                    case["reason"] = invalid_reason
                    with self.assertRaises(ValueError):
                        fast_pass.verify_report(shard, report)

    def test_results_require_receipts_and_use_latest_attempt(self):
        plan = fast_pass.make_plan(self.tests, self.timings, "revision", "image", "integration-01")
        shard = plan["shards"][0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                fast_pass.verify_results(plan, root)
            for attempt, code in ((1, 1), (2, 0)):
                result = root / f"ubuntu-fast-result-integration-01-{attempt}"
                (result / "report").mkdir(parents=True)
                receipt = {"revision": "revision", "image": "image", "shard": shard, "exit_code": code}
                (result / "receipt.json").write_text(json.dumps(receipt))
                (result / "report/test_run_results.json").write_text(json.dumps(self.report))
            fast_pass.verify_results(plan, root)
            receipt["image"] = "wrong-image"
            (result / "receipt.json").write_text(json.dumps(receipt))
            with self.assertRaises(ValueError):
                fast_pass.verify_results(plan, root)


if __name__ == "__main__":
    unittest.main()
