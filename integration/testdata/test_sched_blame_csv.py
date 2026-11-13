#!/usr/bin/env python3
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

"""Validate live SchedBlame CSV intervals without assuming an exact ratio."""

import argparse
import csv
import datetime
import io
import math
from pathlib import Path
import re


MIN_SAMPLES = 5
SETTLE_SECONDS = 2


def read_samples(path):
    if not path.exists():
        return []
    text = path.read_text()
    # The command may be appending the last row while a readiness poll reads it.
    complete = text[: text.rfind("\n") + 1]
    return list(csv.DictReader(io.StringIO(complete)))


def sample_timestamp(value):
    # Keep Go's nanosecond fraction separate from datetime's microseconds.
    match = re.fullmatch(r"(.{19})(?:\.(\d{1,9}))?([+-]\d{2}:\d{2})", value)
    if match is None:
        raise ValueError(f"invalid sample timestamp: {value!r}")
    seconds, fraction, offset = match.groups()
    sample_time = datetime.datetime.strptime(
        seconds + offset.replace(":", ""), "%Y-%m-%dT%H:%M:%S%z")
    return (sample_time.timestamp()
            + int((fraction or "0").ljust(9, "0")) / 1_000_000_000)


def interval_samples(samples, container_id, start, end=math.inf):
    return [
        row for row in samples
        if row["target_container_id"] == container_id
        and start + SETTLE_SECONDS <= sample_timestamp(row["time"]) < end
        and row["external_sample_valid"] == "true"
        and int(row["target_runtime_ns"]) > 0
    ]


def require(condition, message):
    # Python removes assert statements under -O or PYTHONOPTIMIZE.
    if not condition:
        raise SystemExit(f"SchedBlame CSV verification failed: {message}")


def verify(args):
    samples = read_samples(args.csv)
    with args.phases.open(newline="") as file:
        phases = list(csv.DictReader(file, delimiter="\t"))
    expected_phases = [
        "baseline", "competing", "recovered", "replacement"
    ]
    actual_phases = [phase["phase"] for phase in phases]
    require(actual_phases == expected_phases,
            f"collection phases: expected {expected_phases}, got {actual_phases}")
    initial_ids = {phase["container_id"] for phase in phases[:3]}
    require(len(initial_ids) == 1,
            f"target changed before recreation: got {sorted(initial_ids)}")
    ratios = {}
    for phase in phases:
        rows = interval_samples(samples, phase["container_id"],
                                float(phase["start"]), float(phase["end"]))
        require(len(rows) >= MIN_SAMPLES,
                f"{phase['phase']}: expected at least {MIN_SAMPLES} valid samples, got {len(rows)}")
        require(len({row["time"] for row in rows}) == len(rows),
                f"{phase['phase']}: duplicate target samples")
        runtime = external = 0
        for row in rows:
            context = f"{phase['phase']} at {row['time']}"
            require(row["target_container_name"] == args.target,
                    f"{context}: expected target {args.target!r}, got {row['target_container_name']!r}")
            require(row["target_dense_id"] == "0",
                    f"{context}: expected highlight dense ID 0, got {row['target_dense_id']!r}")
            require(0 <= int(row["target_css_id"]) < 4096,
                    f"{context}: expected CSS ID in [0, 4096), got {row['target_css_id']!r}")
            run_ns = int(row["target_runtime_ns"])
            external_ns = int(row["external_contention_ns"])
            internal_ns = int(row["internal_contention_ns"])
            require(external_ns >= 0 and 0 <= internal_ns <= run_ns,
                    f"{context}: invalid accounting: runtime={run_ns}, external={external_ns}, internal={internal_ns}")
            require(row["internal_sample_valid"] == "true",
                    f"{context}: expected valid internal sample, got {row['internal_sample_valid']!r}")
            expected_external = 100 * external_ns / (run_ns + external_ns)
            expected_internal = 100 * internal_ns / run_ns
            require(math.isclose(float(row["current_external_contention_percent"]),
                                 expected_external, abs_tol=1e-6),
                    f"{context}: expected external percent {expected_external}, got {row['current_external_contention_percent']}")
            require(math.isclose(float(row["current_internal_contention_percent"]),
                                 expected_internal, abs_tol=1e-6),
                    f"{context}: expected internal percent {expected_internal}, got {row['current_internal_contention_percent']}")
            runtime += run_ns
            external += external_ns
        ratios[phase["phase"]] = 100 * external / (runtime + external)
        print(f"{phase['phase']}: samples={len(rows)} external={ratios[phase['phase']]:.2f}%")

    # A broad phase difference tolerates host noise and batch-window boundaries.
    require(ratios["competing"] >= ratios["baseline"] + 5,
            f"expected competing percent >= baseline + 5, got competing={ratios['competing']:.2f}, baseline={ratios['baseline']:.2f}")
    require(ratios["competing"] >= ratios["recovered"] + 5,
            f"expected competing percent >= recovered + 5, got competing={ratios['competing']:.2f}, recovered={ratios['recovered']:.2f}")
    old_id, new_id = phases[0]["container_id"], phases[-1]["container_id"]
    require(old_id != new_id,
            f"target recreation did not replace container {old_id}")
    new_rows = [row for row in samples if row["target_container_id"] == new_id]
    require(new_rows and new_rows[0]["history_samples"] == "0",
            f"expected replacement history 0, got {new_rows[0]['history_samples'] if new_rows else 'no samples'}")
    for row in samples:
        if row["target_container_name"] == args.target and (
            sample_timestamp(row["time"]) >= float(phases[-1]["start"])
        ):
            require(row["target_container_id"] == new_id,
                    f"expected replacement container {new_id}, got {row['target_container_id']} at {row['time']}")

    log = args.log.read_text()
    batches = [int(value) for value in re.findall(r"\bslice_batch_records=(\d+)", log)]
    require(any(batches), f"expected positive slice_batch_records, got {batches}")
    for name in ("perf_submission_failures", "perf_submission_failed_slices",
                 "perf_records_lost", "throttle_event_submission_failures"):
        values = re.findall(rf"\b{name}=(\d+)", log)
        require(values and all(int(value) == 0 for value in values),
                f"expected {name}=0 in every transport interval, got {values}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command")
    commands.required = True
    ready = commands.add_parser("ready")
    ready.add_argument("csv", type=Path)
    ready.add_argument("container_id")
    ready.add_argument("start", type=float)
    check = commands.add_parser("verify")
    check.add_argument("csv", type=Path)
    check.add_argument("phases", type=Path)
    check.add_argument("log", type=Path)
    check.add_argument("target")
    args = parser.parse_args()
    if args.command == "ready":
        rows = interval_samples(read_samples(args.csv), args.container_id, args.start)
        return 0 if len({row["time"] for row in rows}) >= MIN_SAMPLES else 1
    verify(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
