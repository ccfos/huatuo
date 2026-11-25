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

"""Tests for sched_contention.py."""

import argparse
import contextlib
import csv
import io
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


sys.path.insert(0, str(Path(__file__).parent))
import sched_contention as contention  # noqa: E402


SCHED_BLAME_HEADER = [
    "time",
    "target_dense_id",
    "target_container_id",
    "target_container_name",
    "target_css_id",
    "external_sample_valid",
    "internal_sample_valid",
    "supermonitor_waitrate_percent",
    "current_external_contention_percent",
    "current_internal_contention_percent",
    "historical_p99_percent",
    "external_anomaly_k",
    "external_contention_threshold_percent",
    "history_samples",
    "ready",
    "emitted",
    "target_runtime_ns",
    "internal_contention_ns",
    "external_contention_ns",
    "throttled_time_ns",
]


class SchedContentionTest(unittest.TestCase):
    def write_sched_blame_csv(
        self,
        path: Path,
        container_id: str,
    ) -> None:
        with path.open("w", newline="", encoding="utf-8") as stream:
            writer = csv.writer(stream)
            writer.writerow(SCHED_BLAME_HEADER)
            writer.writerow(
                [
                    "2026-07-29T17:00:01+08:00",
                    "0",
                    container_id,
                    "workload-a",
                    "10",
                    "true",
                    "true",
                    "43.75",
                    "25",
                    "50",
                    "10",
                    "2",
                    "20",
                    "60",
                    "true",
                    "true",
                    "300",
                    "150",
                    "100",
                    "25",
                ]
            )
            writer.writerow(
                [
                    "2026-07-29T17:00:02+08:00",
                    "0",
                    container_id,
                    "workload-a",
                    "10",
                    "false",
                    "false",
                    "",
                    "",
                    "",
                    "10",
                    "2",
                    "20",
                    "61",
                    "true",
                    "false",
                    "0",
                    "0",
                    "0",
                    "100",
                ]
            )

    def write_supermonitor_csv(
        self,
        path: Path,
        container_id: str,
    ) -> None:
        with path.open("w", newline="", encoding="utf-8") as stream:
            writer = csv.writer(stream)
            writer.writerow(contention.SUPER_MONITOR_CSV_HEADER)
            writer.writerow(
                [
                    "2026-07-29T17:00:01+08:00",
                    str(contention.parse_rfc3339_ns("2026-07-29T17:00:01+08:00")),
                    container_id,
                    f"/sys/fs/cgroup/cpu,cpuacct/{container_id}",
                    "true",
                    "43.75",
                    "100",
                    "20",
                    "10",
                    "70",
                    "60",
                    "160",
                    "",
                ]
            )
            writer.writerow(
                [
                    "2026-07-29T17:00:02+08:00",
                    str(contention.parse_rfc3339_ns("2026-07-29T17:00:02+08:00")),
                    container_id,
                    f"/sys/fs/cgroup/cpu,cpuacct/{container_id}",
                    "false",
                    "",
                    "0",
                    "0",
                    "0",
                    "0",
                    "0",
                    "0",
                    "no_runnable_demand",
                ]
            )

    def test_calculate_supermonitor_sample(self) -> None:
        previous = contention.SuperMonitorCounters(100, 20, 10, 1_000)
        current = contention.SuperMonitorCounters(200, 40, 20, 1_060)
        sample = contention.calculate_supermonitor_sample(
            1,
            "2026-07-29T17:00:01+08:00",
            previous,
            current,
        )
        self.assertEqual(43.75, sample.waitrate_percent)
        self.assertEqual(70, sample.external_wait)
        self.assertEqual(160, sample.total_demand)

    def test_parse_rfc3339_ns(self) -> None:
        for timestamp, expected in (
            ("1970-01-01T08:00:01+08:00", 1_000_000_000),
            ("1970-01-01T00:00:01.1+00:00", 1_100_000_000),
            ("1970-01-01T08:00:01.123456789+08:00", 1_123_456_789),
            ("1969-12-31T23:59:59.999999999+00:00", -1),
        ):
            with self.subTest(timestamp=timestamp):
                self.assertEqual(expected, contention.parse_rfc3339_ns(timestamp))
        with self.assertRaises(ValueError):
            contention.parse_rfc3339_ns("2026-01-01T00:00:00")
        self.assertEqual(
            "1970-01-01T08:00:01.123456789+08:00",
            contention.chart_timestamp(1_123_456_789),
        )

    def test_ratio_validity_and_baseline_are_independent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            csv_path = Path(directory) / "sched.csv"
            self.write_sched_blame_csv(csv_path, "a" * 64)
            with csv_path.open(newline="", encoding="utf-8") as stream:
                valid, invalid = list(csv.DictReader(stream))

            point = contention.sched_blame_point(valid)
            self.assertEqual(25, point.current_percent)
            self.assertEqual(50, point.internal_percent)
            zero = contention.sched_blame_point({
                **valid,
                "current_external_contention_percent": "0",
                "current_internal_contention_percent": "0",
            })
            self.assertEqual(0, zero.current_percent)
            self.assertEqual(0, zero.internal_percent)
            point = contention.sched_blame_point(invalid)
            self.assertIsNone(point.current_percent)
            self.assertIsNone(point.internal_percent)
            self.assertEqual(10, point.p99_percent)
            self.assertEqual(20, point.threshold_percent)

            valid.update(
                target_runtime_ns="0",
                internal_sample_valid="false",
                current_external_contention_percent="100",
                current_internal_contention_percent="",
            )
            point = contention.sched_blame_point(valid)
            self.assertEqual(100, point.current_percent)
            self.assertIsNone(point.internal_percent)

    def test_reused_dense_slot_keeps_container_series_separate(self) -> None:
        first_id, second_id = "a" * 64, "b" * 64
        with tempfile.TemporaryDirectory() as directory:
            csv_path = Path(directory) / "sched.csv"
            self.write_sched_blame_csv(csv_path, first_id)
            with csv_path.open(newline="", encoding="utf-8") as stream:
                rows = list(csv.DictReader(stream))
            rows[1]["target_container_id"] = second_id
            with csv_path.open("w", newline="", encoding="utf-8") as stream:
                writer = csv.DictWriter(stream, fieldnames=SCHED_BLAME_HEADER)
                writer.writeheader()
                writer.writerows(rows)
            _labels, series = contention.load_sched_blame_series(csv_path, 10)
            self.assertEqual({first_id, second_id}, set(series))

    def test_invalid_supermonitor_samples(self) -> None:
        previous = contention.SuperMonitorCounters(100, 20, 10, 1_000)
        reset = contention.calculate_supermonitor_sample(
            1,
            "2026-07-29T17:00:01+08:00",
            previous,
            contention.SuperMonitorCounters(99, 20, 10, 1_000),
        )
        self.assertIsNone(reset.waitrate_percent)
        self.assertEqual("hierarchy_wait_decreased", reset.invalid_reason)

        inconsistent = contention.calculate_supermonitor_sample(
            2,
            "2026-07-29T17:00:02+08:00",
            previous,
            contention.SuperMonitorCounters(120, 50, 20, 1_010),
        )
        self.assertIsNone(inconsistent.waitrate_percent)
        self.assertEqual(
            "hierarchy_less_than_inner_plus_throttle",
            inconsistent.invalid_reason,
        )

        first_read = contention.calculate_supermonitor_sample(
            3, "2026-07-29T17:00:03+08:00", None, previous
        )
        self.assertIsNone(first_read.waitrate_percent)
        self.assertEqual("first_read", first_read.invalid_reason)

    def test_resolve_container_cgroup(self) -> None:
        first_id = "a" * 64
        second_id = "a" * 13 + "b" * 51
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for container_id in (first_id, second_id):
                cgroup = root / "kubepods" / container_id
                cgroup.mkdir(parents=True)
                (cgroup / "cpu.stat").write_text("", encoding="utf-8")
                (cgroup / "cpuacct.usage").write_text("0", encoding="utf-8")

            resolved_id, resolved_path = contention.resolve_container_cgroup(
                "a" * 14,
                root,
            )
            self.assertEqual(first_id, resolved_id)
            self.assertEqual(first_id, resolved_path.name)

            with self.assertRaisesRegex(ValueError, "ambiguous"):
                contention.resolve_container_cgroup("a" * 13, root)
            with self.assertRaisesRegex(ValueError, "not found"):
                contention.resolve_container_cgroup("f" * 13, root)

    def test_schema_detection_and_reversed_comparison(self) -> None:
        container_id = "d" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sched_csv = root / "sched.csv"
            supermonitor_csv = root / "supermonitor.csv"
            html_path = root / "comparison.html"
            self.write_sched_blame_csv(sched_csv, container_id)
            self.write_supermonitor_csv(supermonitor_csv, container_id)

            self.assertEqual(
                "sched-blame",
                contention.detect_schema(sched_csv),
            )
            self.assertEqual("supermonitor", contention.detect_schema(supermonitor_csv))
            self.assertEqual(
                (sched_csv, supermonitor_csv),
                contention.comparison_inputs(supermonitor_csv, sched_csv),
            )

            contention.generate_comparison(
                supermonitor_csv,
                sched_csv,
                html_path,
                100,
                False,
                True,
            )
            document = html_path.read_text(encoding="utf-8")
            self.assertIn("SchedBlame external", document)
            self.assertIn("SchedBlame internal", document)
            self.assertIn("SuperMonitor.waitrate", document)
            self.assertIn(container_id[:13], document)

    def test_chart_auto_detection(self) -> None:
        container_id = "e" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sched_csv = root / "sched.csv"
            supermonitor_csv = root / "supermonitor.csv"
            sched_html = root / "sched.html"
            supermonitor_html = root / "supermonitor.html"
            self.write_sched_blame_csv(sched_csv, container_id)
            self.write_supermonitor_csv(supermonitor_csv, container_id)

            contention.generate_chart(
                sched_csv,
                sched_html,
                100,
                False,
                True,
            )
            contention.generate_chart(
                supermonitor_csv,
                supermonitor_html,
                100,
                False,
                True,
            )
            self.assertIn(
                "SchedBlame contention",
                sched_html.read_text(encoding="utf-8"),
            )
            self.assertIn(
                "SuperMonitor.waitrate",
                sched_html.read_text(encoding="utf-8"),
            )
            self.assertIn(
                '"supermonitor_percent":[43.75,null]',
                sched_html.read_text(encoding="utf-8"),
            )
            self.assertIn(
                '"internal_percent":[50.0,null]',
                sched_html.read_text(encoding="utf-8"),
            )
            self.assertIn(
                "SuperMonitor waitrate",
                supermonitor_html.read_text(encoding="utf-8"),
            )

    def test_bucket_preserves_emitted_sample(self) -> None:
        bucket = contention.Bucket()
        for timestamp, current, emitted in (
            (1, 10.0, False),
            (2, 20.0, True),
            (3, 30.0, False),
        ):
            bucket.add(
                contention.ChartPoint(
                    timestamp,
                    str(timestamp),
                    current,
                    None,
                    None,
                    None,
                    None,
                    emitted,
                    (),
                )
            )
        timestamps = [point.timestamp_ns for point in bucket.points()]
        self.assertIn(2, timestamps)

    def test_bucket_preserves_supermonitor_spike(self) -> None:
        bucket = contention.Bucket()
        for timestamp, current, supermonitor in (
            (1, 10.0, 10.0),
            (2, 20.0, 90.0),
            (3, 30.0, 20.0),
        ):
            bucket.add(
                contention.ChartPoint(
                    timestamp,
                    str(timestamp),
                    current,
                    None,
                    None,
                    None,
                    supermonitor,
                    False,
                    (),
                )
            )
        timestamps = [point.timestamp_ns for point in bucket.points()]
        self.assertIn(2, timestamps)

    def test_bucket_preserves_internal_spike_and_gap(self) -> None:
        bucket = contention.Bucket()
        for timestamp, internal in enumerate((10, 90, None, 20), start=1):
            bucket.add(contention.ChartPoint(
                timestamp_ns=timestamp,
                timestamp=str(timestamp),
                current_percent=10,
                internal_percent=internal,
                p99_percent=None,
                threshold_percent=None,
                supermonitor_percent=None,
                emitted=False,
                details=(),
            ))
        retained = {point.timestamp_ns: point for point in bucket.points()}
        self.assertEqual(90, retained[2].internal_percent)
        self.assertIsNone(retained[3].internal_percent)

    def test_cli_chart_and_compare(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sched_csv = root / "sched.csv"
            supermonitor_csv = root / "supermonitor.csv"
            html_path = root / "chart.html"
            self.write_sched_blame_csv(sched_csv, "a" * 64)
            self.write_supermonitor_csv(supermonitor_csv, "a" * 64)
            for arguments in (
                ["chart", str(sched_csv)],
                ["compare", str(supermonitor_csv), str(sched_csv)],
            ):
                result = subprocess.run(
                    [sys.executable, "-B", contention.__file__, *arguments,
                     "--html", str(html_path), "--connect-gaps", "-q"],
                    capture_output=True, text=True, check=False,
                )
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertEqual("", result.stdout)
                document = html_path.read_text(encoding="utf-8")
                self.assertIn("SchedBlame internal", document)
                self.assertIn("connectgaps: true", document)
                self.assertIn("Time (UTC+8)", document)

    def test_compare_rejects_unmatched_container(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sched_csv, supermonitor_csv = root / "sched.csv", root / "monitor.csv"
            self.write_sched_blame_csv(sched_csv, "a" * 64)
            self.write_supermonitor_csv(supermonitor_csv, "b" * 64)
            with self.assertRaisesRegex(ValueError, "no SchedBlame samples"):
                contention.generate_comparison(
                    sched_csv, supermonitor_csv, root / "chart.html", 10, False, True
                )

    def test_record_missing_counters_outputs_na(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            container_id = "c" * 64
            args = argparse.Namespace(
                container_id=container_id, csv=root / "monitor.csv",
                html=root / "monitor.html", interval=0.001, duration=0.001,
                quiet=False,
            )
            terminal = io.StringIO()
            with patch.object(
                contention, "resolve_container_cgroup", return_value=(container_id, root)
            ), patch.object(
                contention, "read_supermonitor_counters",
                side_effect=ValueError("missing fields: hierarchy_wait_sum"),
            ), patch.object(contention.signal, "signal"), contextlib.redirect_stdout(terminal):
                contention.record_supermonitor(args)
            self.assertIn("SuperMonitor.waitrate=N/A", terminal.getvalue())
            with args.csv.open(newline="", encoding="utf-8") as stream:
                rows = list(csv.DictReader(stream))
            self.assertEqual(1, len(rows))
            self.assertEqual("false", rows[0]["sample_valid"])
            self.assertEqual("", rows[0]["supermonitor_waitrate_percent"])
            self.assertIn("missing fields", rows[0]["invalid_reason"])
            self.assertTrue(args.html.is_file())


if __name__ == "__main__":
    unittest.main()
