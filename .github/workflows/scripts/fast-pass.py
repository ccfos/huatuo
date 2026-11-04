#!/usr/bin/env python3
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

"""Create deterministic Ubuntu test shards and verify their complete reports."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[3]
TIMINGS = Path(__file__).with_name("fast-pass-timings.json")
# These are platform limitations of the pinned Ubuntu VM, not missing setup.
ALLOWED_SKIPS = {
    "e2e/test_autotracing_dload.sh": r"dload unsupported-mode behavior verified; functional/ES cases require Legacy cgroup \(actual=unified\)",
    "integration/test_basic_dockerfile_check.sh": r"docker build does not support --call; upgrade Docker Buildx",
    "integration/test_dropwatch_netdevsim.sh": r"devlink/devlink_trap_report tracepoint is not available|netdevsim kernel module is unavailable|netdevsim registered no drop trap",
    "integration/test_profiler_native_cpu_hardware_pmu.sh": r"hardware CPU cycles sampling unavailable \(see .+\)",
    "integration/test_profiler_python_cpu_multi_pid.sh": r"py-spy does not support Python 3\.14\..+",
    "integration/test_profiler_python_exceptions.sh": r"py-spy probe for Python 3\.14\.[^ ]+ exited 1: [\s\S]+; unsupported interpreter",
    "integration/test_sched_blame.sh": r"SchedBlame requires Linux 4\.18 through 6\.5",
}


# These cases compile Go fixtures or binaries inside the test VM.
GO_FIXTURE_TESTS = {
    "integration/test_autotracing_memburst.sh",
    "integration/test_basic_cgroup_css.sh",
    "integration/test_basic_cgroup_subsys_id.sh",
    "integration/test_basic_storage_elasticsearch_values.sh",
    "integration/test_io_kernel.sh",
    "integration/test_iocost.sh",
    "e2e/test_autotracing_memburst.sh",
    "e2e/test_autotracing_memory_threshold_snapshot_lifecycle.sh",
    "e2e/test_autotracing_memory_threshold_snapshot_runtimes.sh",
}


def inventory(root=ROOT):
    return sorted(str(path.relative_to(root)) for suite in ("integration", "e2e")
                  for path in (root / suite).glob("test_*.sh"))


def make_plan(tests, timings, revision, image, selection=""):
    shards = [{"id": "unit", "suite": "unit", "tests": [], "seconds": 0,
               "kubernetes": False, "go_cache": True}]
    for suite, count in (("integration", 10), ("e2e", 3)):
        suite_tests = [test for test in tests if test.startswith(suite + "/")]
        suite_shards = [{"id": f"{suite}-{index + 1:02}", "suite": suite,
                         "tests": [], "seconds": 0, "kubernetes": suite == "e2e", "go_cache": False}
                        for index in range(min(count, len(suite_tests)))]
        for test in sorted(suite_tests, key=lambda name: (-timings.get(name, 120), name)):
            shard = min(suite_shards, key=lambda item: (item["seconds"], item["id"]))
            shard["tests"].append(test)
            shard["go_cache"] |= test in GO_FIXTURE_TESTS
            shard["seconds"] += max(1, timings.get(test, 120))
            # New tests default to full setup until reviewed for this image.
            shard["kubernetes"] |= (test not in timings or test in {
                "integration/test_metrics_memory_container.sh",
                "integration/test_sched_blame.sh",
            })
        shards.extend(suite_shards)
    if selection:
        if selection in tests:
            suite = selection.split("/")[0]
            shards = [{"id": "selected-test", "suite": suite, "tests": [selection],
                       "seconds": timings.get(selection, 120), "kubernetes": True,
                       "go_cache": selection in GO_FIXTURE_TESTS}]
        else:
            shards = [shard for shard in shards if shard["id"] == selection]
            if not shards:
                raise ValueError(f"unknown test or shard: {selection}")
    return {"revision": revision, "image": image, "selection": selection,
            "inventory": tests, "shards": shards}


def verify_plan(plan, tests, revision, image):
    if plan["revision"] != revision or plan["image"] != image:
        raise ValueError("plan revision/image does not match this run")
    if plan["inventory"] != tests:
        raise ValueError("test inventory changed since planning")
    ids = [shard["id"] for shard in plan["shards"]]
    if not ids or len(set(ids)) != len(ids):
        raise ValueError("empty or duplicate shards")
    for shard in plan["shards"]:
        if shard["suite"] == "unit":
            if shard["id"] != "unit" or shard["tests"]:
                raise ValueError("invalid unit shard")
        elif (shard["suite"] not in ("integration", "e2e") or not shard["tests"]
              or any(test.split("/")[0] != shard["suite"] for test in shard["tests"])):
            raise ValueError("invalid suite or test assignment")
    assigned = [test for shard in plan["shards"] for test in shard["tests"]]
    if not plan["selection"]:
        if Counter(assigned) != Counter(tests) or ids.count("unit") != 1:
            raise ValueError("full plan must cover every test exactly once, plus unit tests")
    elif len(ids) != 1 or any(test not in tests for test in assigned):
        raise ValueError("invalid diagnostic selection")


def verify_report(shard, report):
    if (report["suite"] != shard["suite"] or report["status"] != "completed"
            or report["exit_code"] != 0 or report["runs_per_test"] != 1):
        raise ValueError(f"{shard['id']}: unsuccessful or incomplete report")
    executions = report["test_executions"]
    expected = Counter(Path(test).name for test in shard["tests"])
    if Counter(case["test_name"] for case in executions) != expected:
        raise ValueError(f"{shard['id']}: missing, duplicate, or extra test executions")
    for case in executions:
        test = f"{shard['suite']}/{case['test_name']}"
        if case["run_index"] != 1 or not case["started_at"] or not case["finished_at"]:
            raise ValueError(f"{test}: test did not finish")
        if case["result"] == "passed" and case["exit_code"] == 0:
            continue
        pattern = ALLOWED_SKIPS.get(test)
        if (case["result"] == "skipped" and case["exit_code"] == 77 and pattern
                and re.fullmatch(pattern, case["reason"] or "")):
            print(f"Platform skip: {test}: {case['reason']}")
            continue
        raise ValueError(f"{test}: {case['result']}: {case['reason']}")


def verify_results(plan, results):
    for shard in plan["shards"]:
        # A rerun retains successful earlier jobs. Prefer the latest attempt.
        receipts = list(results.glob(f"ubuntu-fast-result-{shard['id']}-*/receipt.json"))
        if not receipts:
            raise ValueError(f"{shard['id']}: no result artifact")
        receipt_path = max(receipts, key=lambda path: int(path.parent.name.rsplit("-", 1)[1]))
        receipt = json.loads(receipt_path.read_text())
        if any(receipt[key] != plan[key] for key in ("revision", "image")):
            raise ValueError(f"{shard['id']}: result revision/image mismatch")
        if receipt["shard"] != shard or receipt["exit_code"] != 0:
            raise ValueError(f"{shard['id']}: VM command failed or shard changed")
        if shard["suite"] != "unit":
            verify_report(shard, json.loads((receipt_path.parent / "report/test_run_results.json").read_text()))
    print(f"Verified {len(plan['shards'])} shards ({len(plan['inventory'])} discovered tests)")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("plan", "verify"))
    parser.add_argument("--selection", default="")
    parser.add_argument("--plan", type=Path, default=Path("fast-plan/plan.json"))
    parser.add_argument("--results", type=Path, default=Path("fast-results"))
    args = parser.parse_args()
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    image = os.environ["VM_IMAGE_REF"]
    if args.mode == "plan":
        if args.selection and not (os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch"
                                   and os.environ["CI_STAGE"] == "fast"):
            raise ValueError("test/shard selection requires a manual stage=fast run")
        timing_data = json.loads(TIMINGS.read_text())
        plan = make_plan(inventory(), timing_data["seconds"], revision, image, args.selection)
        args.plan.parent.mkdir(parents=True, exist_ok=True)
        args.plan.write_text(json.dumps(plan, indent=2) + "\n")
        for shard in plan["shards"]:
            (args.plan.parent / (shard["id"] + ".txt")).write_text(
                "".join(Path(test).name + "\n" for test in shard["tests"]))
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write("matrix=" + json.dumps({"include": plan["shards"]}) + "\n")
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write("## Ubuntu fast pass\n\n")
            if args.selection:
                summary.write("Diagnostic subset only; the OS matrix is disabled.\n\n")
            summary.write("| Shard | Tests | Estimated seconds | Kubernetes |\n|---|---:|---:|---|\n")
            for shard in plan["shards"]:
                summary.write(f"| {shard['id']} | {len(shard['tests'])} | {shard['seconds']} | {shard['kubernetes']} |\n")
    plan = json.loads(args.plan.read_text())
    verify_plan(plan, inventory(), revision, image)
    if args.mode == "verify":
        verify_results(plan, args.results)


if __name__ == "__main__":
    main()
