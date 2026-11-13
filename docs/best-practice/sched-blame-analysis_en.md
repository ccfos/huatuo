---
title: SchedBlame Analysis
type: docs
description: "Chart and compare CPU scheduling contention measurements."
author: HUATUO Team
date: 2026-10-26
weight: 9
---

`integration/testdata/sched_contention.py` generates interactive HTML charts
from SchedBlame ratio CSV files. It can also record SuperMonitor waitrate from
cgroup v1 counters and compare that recording with the same container's
SchedBlame measurements.
The script uses only the Python 3 standard library. Open the generated HTML in
Chrome; the browser loads Plotly from its public CDN.

To capture data without starting the daemon, use the
[standalone SchedBlame command](sched-blame-standalone_en.md).

## Chart SchedBlame Ratios

Enable recording in the Huatuo configuration:

```toml
[AutoTracing.SchedBlame]
    ExternalRatioDebugFile = "/tmp/sched-blame-ratios.csv"
```

Generate a chart from the current CSV format:

```bash
python3 integration/testdata/sched_contention.py chart /tmp/sched-blame-ratios.csv \
    --html /tmp/sched-blame-ratios.html
```

The chart has a container selector and shows:

- External contention: `E / (R + E) * 100`.
- Internal contention: `I / R * 100`.
- Historical external P99 and its anomaly threshold.
- Emitted external anomaly markers.
- Aligned SuperMonitor waitrate when the highlighted container has valid samples.

`R` is target runtime, `E` is estimated external contention time, and `I` is
estimated internally contended runtime. Internal contention overlaps runtime.
Throttle duration does not enter either ratio. An external sample needs
`R + E > 0`; an internal sample needs `R > 0`. An unavailable ratio is a gap,
while a valid zero remains a zero. An available historical baseline remains
visible even when the current external sample is invalid.

Charts use UTC+8 and retain nanosecond timestamp precision in the embedded data.
Zoom, pan, use the time-range slider, or click legend entries to select lines.
To connect lines across unavailable samples:

```bash
python3 integration/testdata/sched_contention.py chart /tmp/sched-blame-ratios.csv \
    --html /tmp/sched-blame-ratios.html --connect-gaps
```

Large recordings are read in two passes and reduced into time buckets per
container. Each bucket retains its endpoints, external/internal/SuperMonitor
extrema, representative gaps, and its first and last emitted anomaly markers.
`--max-buckets` controls the number of buckets, not an exact point limit:

```bash
python3 integration/testdata/sched_contention.py chart /tmp/sched-blame-ratios.csv \
    --html /tmp/sched-blame-ratios.html --max-buckets 8000
```

The default is 4000 buckets. More buckets preserve more detail at the cost of
larger HTML and greater browser memory use. Container IDs identify series, so
reusing a dense target slot does not combine different containers.

## Record SuperMonitor Waitrate

This mode reads a container's `cpu.stat` and `cpuacct.usage` under
`/sys/fs/cgroup/cpu,cpuacct`. Supply a full container ID or a unique hexadecimal
prefix. The kernel must expose the custom `hierarchy_wait_sum`, `inner_wait_sum`,
and `throttle_wait_sum` counters for valid measurements.

```bash
python3 integration/testdata/sched_contention.py record-supermonitor CONTAINER_ID \
    --csv /tmp/supermonitor.csv --html /tmp/supermonitor.html \
    --duration 600
```

Sampling defaults to one second; `--interval` changes it. Omit `--duration` to
record until Ctrl-C. The HTML is generated when recording stops. Each sample
prints its UTC+8 timestamp and waitrate; `-q` suppresses normal terminal output.
Missing counters, read failures, counter resets, or zero demand produce `N/A`
and an empty CSV percentage, with the reason recorded in `invalid_reason`.
After a failed read, another successful baseline is required before a valid
delta can be calculated.

With deltas between consecutive reads, SuperMonitor uses:

```text
H  = delta hierarchy_wait_sum
I  = delta inner_wait_sum
TW = delta throttle_wait_sum
R  = delta cpuacct.usage
E  = H - I - TW
SuperMonitor.waitrate = E / (H + R) * 100
```

This has a different denominator and task weighting from SchedBlame, so the
comparison is a trend reference rather than an equality check.

## Compare Recordings

```bash
python3 integration/testdata/sched_contention.py compare \
    /tmp/sched-blame-ratios.csv /tmp/supermonitor.csv \
    --html /tmp/comparison.html
```

The two CSV arguments may appear in either order. The script identifies their
formats and selects the full container ID from the SuperMonitor recording.
The SchedBlame recording may contain other containers; only the matching
container is plotted. An unsupported format or missing matching container
produces an error. `--max-buckets`, `--connect-gaps`, and `-q` also apply here.

## Tests

```bash
python3 -B -m unittest discover -s integration/testdata \
    -p 'test_sched_contention.py' -v
```
