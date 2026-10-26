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

"""Record SuperMonitor.waitrate and chart CPU scheduling contention data."""

import argparse
import csv
import html
import json
import math
import os
import re
import signal
import threading
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Dict, List, NamedTuple, Optional, Tuple


CGROUP_ROOT = Path("/sys/fs/cgroup/cpu,cpuacct")
DEFAULT_MAX_BUCKETS = 4000
UTC_PLUS_8 = timezone(timedelta(hours=8))
UNIX_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)

SCHED_BLAME_REQUIRED_COLUMNS = {
    "time",
    "target_dense_id",
    "target_container_id",
    "target_container_name",
    "target_css_id",
    "external_sample_valid",
    "internal_sample_valid",
    "current_external_contention_percent",
    "current_internal_contention_percent",
    "historical_p99_percent",
    "external_anomaly_k",
    "external_contention_threshold_percent",
    "history_samples",
    "ready",
    "target_runtime_ns",
    "internal_contention_ns",
    "external_contention_ns",
    "throttled_time_ns",
    "emitted",
    "supermonitor_waitrate_percent",
}

SUPER_MONITOR_CSV_HEADER = [
    "timestamp",
    "timestamp_unix_ns",
    "container_id",
    "cgroup_path",
    "sample_valid",
    "supermonitor_waitrate_percent",
    "delta_hierarchy_wait_ns",
    "delta_inner_wait_ns",
    "delta_throttle_wait_ns",
    "delta_external_wait_ns",
    "delta_cpu_usage_ns",
    "delta_total_runnable_demand_ns",
    "invalid_reason",
]
SUPER_MONITOR_REQUIRED_COLUMNS = set(SUPER_MONITOR_CSV_HEADER)


class SuperMonitorCounters(NamedTuple):
    hierarchy_wait: int
    inner_wait: int
    throttle_wait: int
    cpu_usage: int


class SuperMonitorSample(NamedTuple):
    timestamp_ns: int
    timestamp: str
    waitrate_percent: Optional[float]
    hierarchy_wait: int
    inner_wait: int
    throttle_wait: int
    external_wait: int
    cpu_usage: int
    total_demand: int
    invalid_reason: str


class ChartPoint(NamedTuple):
    timestamp_ns: int
    timestamp: str
    current_percent: Optional[float]
    internal_percent: Optional[float]
    p99_percent: Optional[float]
    threshold_percent: Optional[float]
    supermonitor_percent: Optional[float]
    emitted: bool
    details: Tuple[object, ...]


class Bucket:
    """Retain line shape, invalid gaps, and emitted markers."""

    def __init__(self) -> None:
        self.first = None  # type: Optional[ChartPoint]
        self.last = None  # type: Optional[ChartPoint]
        self.minimum = None  # type: Optional[ChartPoint]
        self.maximum = None  # type: Optional[ChartPoint]
        self.invalid = None  # type: Optional[ChartPoint]
        self.internal_minimum = None  # type: Optional[ChartPoint]
        self.internal_maximum = None  # type: Optional[ChartPoint]
        self.internal_invalid = None  # type: Optional[ChartPoint]
        self.supermonitor_minimum = None  # type: Optional[ChartPoint]
        self.supermonitor_maximum = None  # type: Optional[ChartPoint]
        self.supermonitor_invalid = None  # type: Optional[ChartPoint]
        self.first_emitted = None  # type: Optional[ChartPoint]
        self.last_emitted = None  # type: Optional[ChartPoint]

    def add(self, point: ChartPoint) -> None:
        if self.first is None:
            self.first = point
        self.last = point
        if point.emitted:
            if self.first_emitted is None:
                self.first_emitted = point
            self.last_emitted = point
        if point.internal_percent is None:
            if self.internal_invalid is None:
                self.internal_invalid = point
        else:
            if (
                self.internal_minimum is None
                or point.internal_percent < self.internal_minimum.internal_percent
            ):
                self.internal_minimum = point
            if (
                self.internal_maximum is None
                or point.internal_percent > self.internal_maximum.internal_percent
            ):
                self.internal_maximum = point
        if point.supermonitor_percent is None:
            if self.supermonitor_invalid is None:
                self.supermonitor_invalid = point
        else:
            if (
                self.supermonitor_minimum is None
                or point.supermonitor_percent < self.supermonitor_minimum.supermonitor_percent
            ):
                self.supermonitor_minimum = point
            if (
                self.supermonitor_maximum is None
                or point.supermonitor_percent > self.supermonitor_maximum.supermonitor_percent
            ):
                self.supermonitor_maximum = point
        if point.current_percent is None:
            if self.invalid is None:
                self.invalid = point
            return
        if (
            self.minimum is None
            or point.current_percent < self.minimum.current_percent
        ):
            self.minimum = point
        if (
            self.maximum is None
            or point.current_percent > self.maximum.current_percent
        ):
            self.maximum = point

    def points(self) -> List[ChartPoint]:
        candidates = (
            self.first,
            self.minimum,
            self.maximum,
            self.invalid,
            self.internal_minimum,
            self.internal_maximum,
            self.internal_invalid,
            self.supermonitor_minimum,
            self.supermonitor_maximum,
            self.supermonitor_invalid,
            self.first_emitted,
            self.last_emitted,
            self.last,
        )
        unique = {
            point.timestamp_ns: point
            for point in candidates
            if point is not None
        }
        return sorted(unique.values(), key=lambda point: point.timestamp_ns)


def is_true(value: str) -> bool:
    return value.lower() == "true"


def optional_float(value: str) -> Optional[float]:
    return float(value) if value else None


def parse_rfc3339_ns(value: str) -> int:
    # datetime handles timezone offsets, but stores only microsecond precision.
    match = re.fullmatch(r"(.{19})(?:\.(\d{1,9}))?([+-]\d{2}:\d{2})", value)
    if match is None:
        raise ValueError(f"invalid RFC3339 timestamp with numeric offset: {value!r}")
    seconds, fraction, offset = match.groups()
    delta = datetime.fromisoformat(seconds + offset) - UNIX_EPOCH
    return (
        (delta.days * 86400 + delta.seconds) * 1_000_000_000
        + int((fraction or "0").ljust(9, "0"))
    )


def chart_timestamp(timestamp_ns: int) -> str:
    seconds, nanoseconds = divmod(timestamp_ns, 1_000_000_000)
    local_time = datetime.fromtimestamp(seconds, UTC_PLUS_8)
    return f"{local_time:%Y-%m-%dT%H:%M:%S}.{nanoseconds:09d}+08:00"


def csv_columns(path: Path) -> set:
    with path.open(newline="", encoding="utf-8") as stream:
        return set(csv.DictReader(stream).fieldnames or [])


def require_columns(path: Path, required: set) -> None:
    missing = required.difference(csv_columns(path))
    if missing:
        raise ValueError(
            f"{path}: missing columns: {', '.join(sorted(missing))}"
        )


def detect_schema(path: Path) -> str:
    columns = csv_columns(path)
    is_sched_blame = SCHED_BLAME_REQUIRED_COLUMNS.issubset(columns)
    is_supermonitor = SUPER_MONITOR_REQUIRED_COLUMNS.issubset(columns)
    if is_sched_blame and not is_supermonitor:
        return "sched-blame"
    if is_supermonitor and not is_sched_blame:
        return "supermonitor"
    if is_sched_blame and is_supermonitor:
        raise ValueError(f"{path}: CSV schema is ambiguous")
    raise ValueError(f"{path}: unsupported CSV schema")


def sched_blame_display_name(row: Dict[str, str]) -> str:
    name = row["target_container_name"]
    container_id = row["target_container_id"]
    return f"{name} ({container_id[:13]})"


def sched_blame_point(row: Dict[str, str]) -> ChartPoint:
    external_valid = is_true(row["external_sample_valid"])
    internal_valid = is_true(row["internal_sample_valid"])
    ready = is_true(row["ready"])
    return ChartPoint(
        timestamp_ns=parse_rfc3339_ns(row["time"]),
        timestamp=row["time"],
        current_percent=optional_float(
            row["current_external_contention_percent"]
        )
        if external_valid
        else None,
        internal_percent=optional_float(
            row["current_internal_contention_percent"]
        )
        if internal_valid
        else None,
        p99_percent=optional_float(row["historical_p99_percent"])
        if ready
        else None,
        threshold_percent=optional_float(
            row["external_contention_threshold_percent"]
        )
        if ready
        else None,
        supermonitor_percent=optional_float(row["supermonitor_waitrate_percent"]),
        emitted=is_true(row["emitted"]),
        details=(
            int(row["target_runtime_ns"]),
            int(row["external_contention_ns"]),
            int(row["internal_contention_ns"]),
            int(row["throttled_time_ns"]),
        ),
    )


def scan_sched_blame(
    path: Path,
    container_id: Optional[str] = None,
) -> Tuple[int, int, Dict[str, str]]:
    require_columns(path, SCHED_BLAME_REQUIRED_COLUMNS)
    minimum = None  # type: Optional[int]
    maximum = None  # type: Optional[int]
    labels = {}  # type: Dict[str, str]
    with path.open(newline="", encoding="utf-8") as stream:
        for row in csv.DictReader(stream):
            if container_id is not None and row["target_container_id"] != container_id:
                continue
            timestamp_ns = parse_rfc3339_ns(row["time"])
            minimum = timestamp_ns if minimum is None else min(
                minimum,
                timestamp_ns,
            )
            maximum = timestamp_ns if maximum is None else max(
                maximum,
                timestamp_ns,
            )
            labels[row["target_container_id"]] = sched_blame_display_name(row)
    if minimum is None or maximum is None:
        if container_id is None:
            raise ValueError(f"{path}: contains no SchedBlame samples")
        raise ValueError(
            f"{path}: contains no SchedBlame samples for container "
            f"{container_id}"
        )
    return minimum, maximum, labels


def load_sched_blame_series(
    path: Path,
    max_buckets: int,
    container_id: Optional[str] = None,
) -> Tuple[Dict[str, str], Dict[str, List[ChartPoint]]]:
    minimum, maximum, labels = scan_sched_blame(path, container_id)
    bucket_width = max(
        1,
        math.ceil((maximum - minimum + 1) / max_buckets),
    )
    buckets = {}  # type: Dict[str, Dict[int, Bucket]]
    with path.open(newline="", encoding="utf-8") as stream:
        for row in csv.DictReader(stream):
            if container_id is not None and row["target_container_id"] != container_id:
                continue
            point = sched_blame_point(row)
            key = row["target_container_id"]
            index = (point.timestamp_ns - minimum) // bucket_width
            buckets.setdefault(key, {}).setdefault(index, Bucket()).add(point)
    series = {}  # type: Dict[str, List[ChartPoint]]
    for key, series_buckets in buckets.items():
        series[key] = [
            point
            for index in sorted(series_buckets)
            for point in series_buckets[index].points()
        ]
    return labels, series


def supermonitor_point(row: Dict[str, str]) -> ChartPoint:
    valid = is_true(row["sample_valid"])
    return ChartPoint(
        timestamp_ns=int(row["timestamp_unix_ns"]),
        timestamp=row["timestamp"],
        current_percent=optional_float(row["supermonitor_waitrate_percent"])
        if valid
        else None,
        internal_percent=None,
        p99_percent=None,
        threshold_percent=None,
        supermonitor_percent=None,
        emitted=False,
        details=(
            int(row["delta_external_wait_ns"]),
            int(row["delta_cpu_usage_ns"]),
            int(row["delta_inner_wait_ns"]),
            int(row["delta_throttle_wait_ns"]),
            int(row["delta_hierarchy_wait_ns"]),
            int(row["delta_total_runnable_demand_ns"]),
            row["invalid_reason"],
        ),
    )


def scan_supermonitor(path: Path) -> Tuple[int, int, str, str]:
    require_columns(path, SUPER_MONITOR_REQUIRED_COLUMNS)
    minimum = None  # type: Optional[int]
    maximum = None  # type: Optional[int]
    container_ids = set()
    cgroup_paths = set()
    with path.open(newline="", encoding="utf-8") as stream:
        for row in csv.DictReader(stream):
            timestamp_ns = int(row["timestamp_unix_ns"])
            minimum = timestamp_ns if minimum is None else min(
                minimum,
                timestamp_ns,
            )
            maximum = timestamp_ns if maximum is None else max(
                maximum,
                timestamp_ns,
            )
            container_ids.add(row["container_id"])
            cgroup_paths.add(row["cgroup_path"])
    if minimum is None or maximum is None:
        raise ValueError(f"{path}: contains no SuperMonitor samples")
    if len(container_ids) != 1 or "" in container_ids:
        raise ValueError(f"{path}: must contain one nonempty container ID")
    if len(cgroup_paths) != 1 or "" in cgroup_paths:
        raise ValueError(f"{path}: must contain one nonempty cgroup path")
    return minimum, maximum, container_ids.pop(), cgroup_paths.pop()


def load_supermonitor_series(
    path: Path,
    max_buckets: int,
) -> Tuple[Dict[str, str], Dict[str, List[ChartPoint]], str]:
    minimum, maximum, container_id, _cgroup_path = scan_supermonitor(path)
    bucket_width = max(
        1,
        math.ceil((maximum - minimum + 1) / max_buckets),
    )
    buckets = {}  # type: Dict[int, Bucket]
    with path.open(newline="", encoding="utf-8") as stream:
        for row in csv.DictReader(stream):
            point = supermonitor_point(row)
            index = (point.timestamp_ns - minimum) // bucket_width
            buckets.setdefault(index, Bucket()).add(point)
    points = [
        point
        for index in sorted(buckets)
        for point in buckets[index].points()
    ]
    labels = {container_id: f"SuperMonitor.waitrate ({container_id[:13]})"}
    return labels, {container_id: points}, container_id


def series_chart_data(
    labels: Dict[str, str],
    series: Dict[str, List[ChartPoint]],
    kind: str,
) -> List[Dict[str, object]]:
    result = []
    for key in sorted(series, key=lambda item: labels[item].lower()):
        points = series[key]
        result.append(
            {
                "kind": kind,
                "key": key,
                "label": labels[key],
                "x": [chart_timestamp(point.timestamp_ns) for point in points],
                "current_percent": [
                    point.current_percent for point in points
                ],
                "internal_percent": [point.internal_percent for point in points],
                "p99_percent": [point.p99_percent for point in points],
                "threshold_percent": [
                    point.threshold_percent for point in points
                ],
                "supermonitor_percent": [
                    point.supermonitor_percent for point in points
                ],
                "emitted": [
                    point.current_percent if point.emitted else None
                    for point in points
                ],
                "details": [point.details for point in points],
            }
        )
    return result


def chart_document(
    title: str,
    source: str,
    data: List[Dict[str, object]],
    connect_gaps: bool,
) -> str:
    encoded = json.dumps(data, separators=(",", ":")).replace("</", "<\\/")
    connect_gaps_js = str(connect_gaps).lower()
    escaped_title = html.escape(title)
    escaped_source = html.escape(source)
    hide_selector = "hidden" if len(data) == 1 else ""
    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{escaped_title}</title>
  <script src="https://cdn.plot.ly/plotly-2.35.2.min.js"></script>
  <style>
    body {{ margin: 0; color: #1f2937; background: #f7f7f8;
      font: 14px system-ui, sans-serif; }}
    header {{ display: flex; align-items: center; gap: 16px; padding: 14px 18px;
      border-bottom: 1px solid #d8dadd; background: white; }}
    h1 {{ margin: 0; font-size: 18px; font-weight: 600; }}
    select {{ min-width: 320px; max-width: 50vw; padding: 6px 8px; }}
    .hidden {{ display: none; }}
    #source {{ color: #60646c; overflow: hidden; text-overflow: ellipsis;
      white-space: nowrap; }}
    #meta {{ margin-left: auto; color: #60646c; white-space: nowrap; }}
    #chart {{ width: 100%; height: calc(100vh - 62px); }}
  </style>
</head>
<body>
  <header>
    <h1>{escaped_title}</h1>
    <select id="target" class="{hide_selector}" aria-label="Target"></select>
    <span id="source">{escaped_source}</span>
    <span id="meta"></span>
  </header>
  <div id="chart"></div>
  <script>
    const series = {encoded};
    const target = document.getElementById("target");
    const meta = document.getElementById("meta");
    for (let i = 0; i < series.length; i++) {{
      const option = document.createElement("option");
      option.value = String(i);
      option.textContent = series[i].label;
      target.appendChild(option);
    }}
    function schedBlameTraces(item) {{
      const common = {{ x: item.x, type: "scattergl", mode: "lines",
        connectgaps: {connect_gaps_js},
        hovertemplate: "%{{x}}<br>%{{y:.4f}}%<extra></extra>" }};
      return [
        {{ ...common, y: item.current_percent, name: "SchedBlame external",
          line: {{ color: "#1769aa", width: 1.5 }} }},
        {{ ...common, y: item.internal_percent, name: "SchedBlame internal",
          line: {{ color: "#a23b72", width: 1.5 }} }},
        {{ ...common, y: item.p99_percent, name: "Historical P99",
          line: {{ color: "#16865c", width: 1.2 }} }},
        {{ ...common, y: item.threshold_percent, name: "Anomaly threshold",
          line: {{ color: "#bd5b00", width: 1.2, dash: "dot" }} }},
        {{ ...common, y: item.supermonitor_percent, name: "SuperMonitor.waitrate",
          line: {{ color: "#c62828", width: 1.5 }} }},
        {{ x: item.x, y: item.emitted, type: "scattergl", mode: "markers",
          name: "Emitted event", marker: {{ color: "#6a1b9a", size: 6 }},
          hovertemplate:
            "%{{x}}<br>%{{y:.4f}}%<extra>Emitted event</extra>" }}
      ];
    }}
    function supermonitorTraces(item) {{
      return [{{
        x: item.x, y: item.current_percent, customdata: item.details,
        type: "scattergl", mode: "lines", name: "SuperMonitor.waitrate",
        connectgaps: {connect_gaps_js},
        line: {{ color: "#1769aa", width: 1.5 }},
        hovertemplate:
          "%{{x}}<br>waitrate=%{{y:.4f}}%" +
          "<br>external=%{{customdata[0]}} ns" +
          "<br>usage=%{{customdata[1]}} ns" +
          "<br>inner=%{{customdata[2]}} ns" +
          "<br>throttle=%{{customdata[3]}} ns" +
          "<br>hierarchy=%{{customdata[4]}} ns" +
          "<br>demand=%{{customdata[5]}} ns" +
          "<br>invalid=%{{customdata[6]}}<extra></extra>"
      }}];
    }}
    function draw() {{
      const item = series[Number(target.value || 0)];
      if (!item) return;
      const traces = item.kind === "sched-blame"
        ? schedBlameTraces(item) : supermonitorTraces(item);
      Plotly.react("chart", traces, {{
        margin: {{ l: 70, r: 28, t: 26, b: 54 }},
        paper_bgcolor: "#f7f7f8", plot_bgcolor: "white",
        hovermode: "x unified", legend: {{ orientation: "h", y: 1.08 }},
        xaxis: {{ title: "Time (UTC+8)", rangeslider: {{ visible: true }} }},
        yaxis: {{ title: "Contention (%)", tickformat: ".0f",
          ticksuffix: "%", rangemode: "tozero" }},
        uirevision: item.key
      }}, {{ responsive: true, displaylogo: false }});
      meta.textContent = item.x.length.toLocaleString() + " plotted points";
    }}
    target.addEventListener("change", draw);
    draw();
  </script>
</body>
</html>
"""


def comparison_document(
    container_id: str,
    sched_item: Dict[str, object],
    supermonitor_item: Dict[str, object],
    sched_path: Path,
    supermonitor_path: Path,
    connect_gaps: bool,
) -> str:
    data = {"sched": sched_item, "supermonitor": supermonitor_item}
    encoded = json.dumps(data, separators=(",", ":")).replace("</", "<\\/")
    connect_gaps_js = str(connect_gaps).lower()
    short_id = html.escape(container_id[:13])
    sources = html.escape(f"{sched_path} | {supermonitor_path}")
    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Scheduling contention comparison</title>
  <script src="https://cdn.plot.ly/plotly-2.35.2.min.js"></script>
  <style>
    body {{ margin: 0; color: #1f2937; background: #f7f7f8;
      font: 14px system-ui, sans-serif; }}
    header {{ display: flex; align-items: center; gap: 16px; padding: 14px 18px;
      border-bottom: 1px solid #d8dadd; background: white; }}
    h1 {{ margin: 0; font-size: 18px; font-weight: 600; }}
    #source {{ margin-left: auto; color: #60646c; overflow: hidden;
      text-overflow: ellipsis; white-space: nowrap; }}
    #chart {{ width: 100%; height: calc(100vh - 62px); }}
  </style>
</head>
<body>
  <header>
    <h1>Scheduling contention: {short_id}</h1>
    <span id="source">{sources}</span>
  </header>
  <div id="chart"></div>
  <script>
    const data = {encoded};
    const sched = data.sched;
    const supermonitor = data.supermonitor;
    const traces = [
      {{ x: sched.x, y: sched.current_percent, type: "scattergl",
        mode: "lines", name: "SchedBlame external",
        connectgaps: {connect_gaps_js},
        line: {{ color: "#1769aa", width: 1.5 }},
        hovertemplate: "%{{x}}<br>%{{y:.4f}}%<extra>SchedBlame</extra>" }},
      {{ x: sched.x, y: sched.internal_percent, type: "scattergl",
        mode: "lines", name: "SchedBlame internal",
        connectgaps: {connect_gaps_js},
        line: {{ color: "#a23b72", width: 1.5 }},
        hovertemplate: "%{{x}}<br>%{{y:.4f}}%<extra>SchedBlame internal</extra>" }},
      {{ x: supermonitor.x, y: supermonitor.current_percent, customdata: supermonitor.details,
        type: "scattergl", mode: "lines", name: "SuperMonitor.waitrate",
        connectgaps: {connect_gaps_js},
        line: {{ color: "#c62828", width: 1.5 }},
        hovertemplate:
          "%{{x}}<br>%{{y:.4f}}%" +
          "<br>external=%{{customdata[0]}} ns" +
          "<br>usage=%{{customdata[1]}} ns" +
          "<br>inner=%{{customdata[2]}} ns" +
          "<br>throttle=%{{customdata[3]}} ns" +
          "<extra>SuperMonitor</extra>" }},
      {{ x: sched.x, y: sched.p99_percent, type: "scattergl",
        mode: "lines", name: "SchedBlame historical P99",
        visible: "legendonly", connectgaps: {connect_gaps_js},
        line: {{ color: "#16865c", width: 1.2 }} }},
      {{ x: sched.x, y: sched.threshold_percent, type: "scattergl",
        mode: "lines", name: "SchedBlame anomaly threshold",
        visible: "legendonly", connectgaps: {connect_gaps_js},
        line: {{ color: "#bd5b00", width: 1.2, dash: "dot" }} }},
      {{ x: sched.x, y: sched.emitted, type: "scattergl", mode: "markers",
        name: "SchedBlame emitted event",
        marker: {{ color: "#6a1b9a", size: 6 }} }}
    ];
    Plotly.newPlot("chart", traces, {{
      margin: {{ l: 70, r: 28, t: 26, b: 54 }},
      paper_bgcolor: "#f7f7f8", plot_bgcolor: "white",
      hovermode: "x unified", legend: {{ orientation: "h", y: 1.08 }},
      xaxis: {{ title: "Time (UTC+8)", rangeslider: {{ visible: true }} }},
      yaxis: {{ title: "Contention (%)", tickformat: ".0f",
        ticksuffix: "%", rangemode: "tozero" }}
    }}, {{ responsive: true, displaylogo: false }});
  </script>
</body>
</html>
"""


def write_document(
    path: Path,
    document: str,
    description: str,
    quiet: bool,
) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(document, encoding="utf-8")
    if not quiet:
        print(f"wrote {path} ({description})")


def generate_chart(
    csv_path: Path,
    html_path: Path,
    max_buckets: int,
    connect_gaps: bool,
    quiet: bool,
) -> None:
    if max_buckets < 1:
        raise ValueError("--max-buckets must be at least 1")
    schema = detect_schema(csv_path)
    if schema == "sched-blame":
        labels, series = load_sched_blame_series(csv_path, max_buckets)
        title = "SchedBlame contention"
    else:
        labels, series, _container_id = load_supermonitor_series(
            csv_path,
            max_buckets,
        )
        title = "SuperMonitor waitrate"
    data = series_chart_data(labels, series, schema)
    write_document(
        html_path,
        chart_document(title, str(csv_path), data, connect_gaps),
        f"{schema}, {len(data)} series",
        quiet,
    )


def comparison_inputs(first: Path, second: Path) -> Tuple[Path, Path]:
    first_schema = detect_schema(first)
    second_schema = detect_schema(second)
    if first_schema == "sched-blame" and second_schema == "supermonitor":
        return first, second
    if first_schema == "supermonitor" and second_schema == "sched-blame":
        return second, first
    raise ValueError(
        "compare requires one SchedBlame CSV and one SuperMonitor CSV"
    )


def generate_comparison(
    first: Path,
    second: Path,
    html_path: Path,
    max_buckets: int,
    connect_gaps: bool,
    quiet: bool,
) -> None:
    if max_buckets < 1:
        raise ValueError("--max-buckets must be at least 1")
    sched_path, supermonitor_path = comparison_inputs(first, second)
    supermonitor_labels, supermonitor_series, container_id = load_supermonitor_series(
        supermonitor_path,
        max_buckets,
    )
    sched_labels, sched_series = load_sched_blame_series(
        sched_path,
        max_buckets,
        container_id,
    )
    sched_data = series_chart_data(
        sched_labels,
        sched_series,
        "sched-blame",
    )
    supermonitor_data = series_chart_data(supermonitor_labels, supermonitor_series, "supermonitor")
    if len(sched_data) != 1 or len(supermonitor_data) != 1:
        raise ValueError("comparison inputs did not resolve to one series")
    write_document(
        html_path,
        comparison_document(
            container_id,
            sched_data[0],
            supermonitor_data[0],
            sched_path,
            supermonitor_path,
            connect_gaps,
        ),
        f"container {container_id}",
        quiet,
    )


def resolve_container_cgroup(
    selector: str,
    root: Path = CGROUP_ROOT,
) -> Tuple[str, Path]:
    container_id = selector.strip().lower()
    if not container_id or len(container_id) > 64 or not re.fullmatch(
        r"[0-9a-f]+",
        container_id,
    ):
        raise ValueError("container ID must be a hexadecimal ID or prefix")
    if not root.is_dir():
        raise ValueError(f"cgroup root does not exist: {root}")

    candidates = []
    for directory, _children, files in os.walk(str(root)):
        path = Path(directory)
        name = path.name.lower()
        if not re.fullmatch(r"[0-9a-f]{64}", name):
            continue
        if not name.startswith(container_id):
            continue
        if "cpu.stat" not in files or "cpuacct.usage" not in files:
            continue
        candidates.append(path)

    if not candidates:
        raise ValueError(f"container cgroup not found: {selector}")
    if len(candidates) != 1:
        matches = ", ".join(path.name for path in candidates[:5])
        raise ValueError(
            f"container ID prefix is ambiguous: {selector}: {matches}"
        )
    path = candidates[0]
    return path.name.lower(), path


def parse_cpu_stat(path: Path) -> Dict[str, int]:
    values = {}
    with path.open(encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, start=1):
            fields = line.split()
            if not fields:
                continue
            if len(fields) != 2:
                raise ValueError(
                    f"{path}:{line_number}: expected a name and integer value"
                )
            try:
                values[fields[0]] = int(fields[1])
            except ValueError as error:
                raise ValueError(
                    f"{path}:{line_number}: invalid integer {fields[1]!r}"
                ) from error
    required = {
        "hierarchy_wait_sum",
        "inner_wait_sum",
        "throttle_wait_sum",
    }
    missing = required.difference(values)
    if missing:
        raise ValueError(
            f"{path}: missing fields: {', '.join(sorted(missing))}"
        )
    return values


def read_integer(path: Path) -> int:
    value = path.read_text(encoding="utf-8").strip()
    try:
        return int(value)
    except ValueError as error:
        raise ValueError(f"{path}: invalid integer {value!r}") from error


def read_supermonitor_counters(cgroup_path: Path) -> SuperMonitorCounters:
    stat = parse_cpu_stat(cgroup_path / "cpu.stat")
    return SuperMonitorCounters(
        hierarchy_wait=stat["hierarchy_wait_sum"],
        inner_wait=stat["inner_wait_sum"],
        throttle_wait=stat["throttle_wait_sum"],
        cpu_usage=read_integer(cgroup_path / "cpuacct.usage"),
    )


def supermonitor_counter_deltas(
    previous: SuperMonitorCounters,
    current: SuperMonitorCounters,
) -> Tuple[Optional[SuperMonitorCounters], str]:
    names = SuperMonitorCounters._fields
    deltas = []
    for name, old_value, new_value in zip(names, previous, current):
        if new_value < old_value:
            return None, f"{name}_decreased"
        deltas.append(new_value - old_value)
    return SuperMonitorCounters(*deltas), ""


def unavailable_supermonitor_sample(
    timestamp_ns: int,
    timestamp: str,
    reason: str,
) -> SuperMonitorSample:
    return SuperMonitorSample(
        timestamp_ns, timestamp, None, 0, 0, 0, 0, 0, 0, reason
    )


def calculate_supermonitor_sample(
    timestamp_ns: int,
    timestamp: str,
    previous: Optional[SuperMonitorCounters],
    current: SuperMonitorCounters,
) -> SuperMonitorSample:
    if previous is None:
        return unavailable_supermonitor_sample(
            timestamp_ns, timestamp, "first_read"
        )
    deltas, invalid_reason = supermonitor_counter_deltas(previous, current)
    if deltas is None:
        return unavailable_supermonitor_sample(
            timestamp_ns, timestamp, invalid_reason
        )

    external_wait = deltas.hierarchy_wait - (
        deltas.inner_wait + deltas.throttle_wait
    )
    total_demand = deltas.hierarchy_wait + deltas.cpu_usage
    waitrate = None  # type: Optional[float]
    if external_wait < 0:
        invalid_reason = "hierarchy_less_than_inner_plus_throttle"
    elif total_demand == 0:
        invalid_reason = "no_runnable_demand"
    else:
        waitrate = external_wait / total_demand * 100.0

    return SuperMonitorSample(
        timestamp_ns,
        timestamp,
        waitrate,
        deltas.hierarchy_wait,
        deltas.inner_wait,
        deltas.throttle_wait,
        max(0, external_wait),
        deltas.cpu_usage,
        total_demand,
        invalid_reason,
    )


def supermonitor_sample_row(
    sample: SuperMonitorSample,
    container_id: str,
    cgroup_path: Path,
) -> List[str]:
    waitrate = (
        ""
        if sample.waitrate_percent is None
        else format(sample.waitrate_percent, ".12g")
    )
    return [
        sample.timestamp,
        str(sample.timestamp_ns),
        container_id,
        str(cgroup_path),
        str(sample.waitrate_percent is not None).lower(),
        waitrate,
        str(sample.hierarchy_wait),
        str(sample.inner_wait),
        str(sample.throttle_wait),
        str(sample.external_wait),
        str(sample.cpu_usage),
        str(sample.total_demand),
        sample.invalid_reason,
    ]


def record_supermonitor(args: argparse.Namespace) -> None:
    if args.interval <= 0:
        raise ValueError("--interval must be greater than zero")
    if args.duration < 0:
        raise ValueError("--duration cannot be negative")

    container_id, cgroup_path = resolve_container_cgroup(args.container_id)
    try:
        previous = read_supermonitor_counters(cgroup_path)
    except (OSError, ValueError):
        previous = None
    args.csv.parent.mkdir(parents=True, exist_ok=True)
    stop = threading.Event()

    def request_stop(_signum: int, _frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGINT, request_stop)
    signal.signal(signal.SIGTERM, request_stop)

    sample_count = 0
    started_at = time.monotonic()
    next_sample_at = started_at + args.interval
    with args.csv.open("w", newline="", encoding="utf-8") as stream:
        writer = csv.writer(stream)
        writer.writerow(SUPER_MONITOR_CSV_HEADER)
        stream.flush()
        if not args.quiet:
            print(
                f"collecting container={container_id} cgroup={cgroup_path} "
                f"every {args.interval:g}s into {args.csv}; stop with Ctrl-C"
            )
        while not stop.is_set():
            if (
                args.duration > 0
                and next_sample_at > started_at + args.duration
            ):
                break
            if stop.wait(max(0.0, next_sample_at - time.monotonic())):
                break

            read_error = ""
            try:
                current = read_supermonitor_counters(cgroup_path)
            except (OSError, ValueError) as error:
                current = None
                read_error = str(error)
            timestamp_ns = time.time_ns()
            timestamp = chart_timestamp(timestamp_ns)
            if current is None:
                sample = unavailable_supermonitor_sample(
                    timestamp_ns, timestamp, read_error
                )
            else:
                sample = calculate_supermonitor_sample(
                    timestamp_ns, timestamp, previous, current
                )
            writer.writerow(
                supermonitor_sample_row(sample, container_id, cgroup_path)
            )
            stream.flush()
            if not args.quiet:
                if sample.waitrate_percent is None:
                    print(
                        f"{sample.timestamp} SuperMonitor.waitrate=N/A "
                        f"reason={sample.invalid_reason}",
                        flush=True,
                    )
                else:
                    print(
                        f"{sample.timestamp} "
                        f"SuperMonitor.waitrate={sample.waitrate_percent:.4f}%",
                        flush=True,
                    )
            previous = current
            sample_count += 1
            next_sample_at += args.interval
            finished_at = time.monotonic()
            if next_sample_at <= finished_at:
                missed = math.floor(
                    (finished_at - next_sample_at) / args.interval
                ) + 1
                next_sample_at += missed * args.interval

    if not args.quiet:
        print(f"collected {sample_count} samples in {args.csv}")
    if sample_count:
        generate_chart(
            args.csv,
            args.html,
            DEFAULT_MAX_BUCKETS,
            False,
            args.quiet,
        )


def add_quiet_option(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "-q",
        "--quiet",
        action="store_true",
        help="suppress normal terminal output",
    )


def add_chart_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--html",
        type=Path,
        required=True,
        help="output HTML path",
    )
    parser.add_argument(
        "--max-buckets",
        type=int,
        default=DEFAULT_MAX_BUCKETS,
        help="maximum time buckets before spike-preserving reduction",
    )
    parser.add_argument(
        "--connect-gaps",
        action="store_true",
        help="connect chart lines across invalid samples",
    )
    add_quiet_option(parser)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Record and chart CPU scheduling contention.",
    )
    subparsers = parser.add_subparsers(dest="command")

    record_parser = subparsers.add_parser(
        "record-supermonitor",
        help="record SuperMonitor.waitrate for one container",
    )
    record_parser.add_argument(
        "container_id",
        help="full container ID or unique hexadecimal prefix",
    )
    record_parser.add_argument(
        "--csv",
        type=Path,
        default=Path("supermonitor-waitrate.csv"),
        help="recording CSV path",
    )
    record_parser.add_argument(
        "--html",
        type=Path,
        default=Path("supermonitor-waitrate.html"),
        help="HTML generated when recording stops",
    )
    record_parser.add_argument(
        "--interval",
        type=float,
        default=1.0,
        help="sampling interval in seconds",
    )
    record_parser.add_argument(
        "--duration",
        type=float,
        default=0.0,
        help="recording duration in seconds; zero runs until interrupted",
    )
    add_quiet_option(record_parser)

    chart_parser = subparsers.add_parser(
        "chart",
        help="generate HTML from a SchedBlame or SuperMonitor CSV",
    )
    chart_parser.add_argument("csv", type=Path)
    add_chart_options(chart_parser)

    compare_parser = subparsers.add_parser(
        "compare",
        help="compare one SchedBlame CSV with one SuperMonitor CSV",
    )
    compare_parser.add_argument("first_csv", type=Path)
    compare_parser.add_argument("second_csv", type=Path)
    add_chart_options(compare_parser)

    args = parser.parse_args()
    if args.command is None:
        parser.error("a command is required")
    return args


def main() -> None:
    args = parse_args()
    try:
        if args.command == "record-supermonitor":
            record_supermonitor(args)
        elif args.command == "chart":
            generate_chart(
                args.csv,
                args.html,
                args.max_buckets,
                args.connect_gaps,
                args.quiet,
            )
        else:
            generate_comparison(
                args.first_csv,
                args.second_csv,
                args.html,
                args.max_buckets,
                args.connect_gaps,
                args.quiet,
            )
    except (OSError, ValueError) as error:
        raise SystemExit(f"sched_contention.py: {error}") from error


if __name__ == "__main__":
    main()
