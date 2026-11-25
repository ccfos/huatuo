---
title: profiler CLI Parameters
type: docs
author: HUATUO Team
date: 2026-10-21
weight: 6
---

This reference lists `profiler` parameters, defaults, and applicability. See [CLI Collection](/docs/best-practice/continuous-profiling_en.md#cli-collection) for prerequisites and examples, and [Output Formats](/docs/best-practice/continuous-profiling_en.md#output-formats) to choose a format.

Specify `--output-format` explicitly when collecting samples. You can omit `--output-path` with `--output-format=tui`. Other formats require an output path.

## General

| Parameter | Default | Applies To | Description |
| --- | --- | --- | --- |
| `--type`, `-t` | None | Collection | Observation type: `cpu` or `memory`; required |
| `--language`, `-l` | None | Collection | Target language: `c`, `c++`, `go`, `java`, or `python`; required |
| `--pid`, `-p` | None | Collection | Target PID; Java and Python accept comma-separated PIDs, native collection accepts at most one |
| `--container-id` | None | Collection | Target container ID; native CPU combines it with `--pid` as an AND filter using the host PID; otherwise mutually exclusive with `--pid` |
| `--duration`, `-d` | `10` | Collection | Total collection duration in seconds, minimum 1 |
| `--aggr-interval` | `10` | Collection | Aggregation interval in seconds; cannot exceed duration |
| `--freq`, `-F` | `99` | CPU | Samples per second; Java maximum: 1000 |
| `--output-path` | Optional for TUI | Collection | Local file, directory, or `stdout`. Use a Unix socket for `remote-store`. End new directory paths with `/`. Specify a path explicitly for other formats |
| `--output-format` | Required | Collection | `collapsed`, `flamegraph`, `svg`, `tui`, `json`, `speedscope`, `chrometrace` or `remote-store`. `chrometrace` requires a time-based profile |
| `--tui-report FILE` | None | Report replay | Open a saved terminal flame graph report |
| `--max-concurrent-procs` | `0` | Java, Python | Maximum concurrent collection child processes; `0` is unlimited |
| `--tool-path` | None | Java, Python | Shared root directory for external tools; required |
| `--binary-match-path` | None | Java, Python | Match target container processes by executable path |
| `--task-id` | Empty; generated internally for local output | Collection (required for `remote-store`) | Stable collection task ID shared by toolstream and remote storage |
| `--enable-pprof` | `false` | Collection | Serve this process's Go runtime profiles on `127.0.0.1:6000` |
| `--version-format` | `text` | Version queries | Format for `--version`: `text`, `json`, or `short` |
| `--help`, `-h` | - | All | Show command help |
| `--version`, `-v` | - | All | Show version and build information |

These pprof endpoints describe the profiler process's own Go runtime, not the `--pid` target or Java/Python collector subprocesses. The listener is now restricted to loopback rather than all interfaces; an occupied port fails startup.

## Terminal flame graph and replay

`--output-format=tui` opens the flame graph in an interactive terminal after collection. Press `v` to cycle through the flame graph, the full frame list, and Top Table. Press `r` to return to the root of the current flame graph. Top Table sorts by descending `Self` by default. Press `s` to switch to descending `Total`.

The flame graph starts with call stacks alone. Press `g` (Group by) in the graph or frame list to enter a comma-separated list of `cpu`, `comm`, and `pid` fields, then press Enter to apply it. For example, `cpu,comm` groups by CPU, then by task name within each CPU. A missing label appears as `field=?`. Press Ctrl+U then Enter to clear the grouping, or Esc to cancel editing.

Top Table covers the entire collection. `Self` counts weight at the stack leaf. `Total` counts weight for every occurrence of the function in a stack, including recursive calls.

Use `--output-path` to save a gob report when you exit with `q`. A file path replaces the existing result at that path. A directory gets an automatically named `profile_*.gob` file. Omit the path or use `stdout` or `-` for terminal viewing only.

```sh
profiler --tui-report p.gob
```

`--tui-report p.gob` opens the report at `p.gob` in an interactive terminal. Use this option on its own. Replay requires a HuaTuo gob report and identifies it by its file header, regardless of the filename extension. Open reports only from trusted sources.

## Collection

Native on-CPU and memory collection require Linux `MEMBARRIER_CMD_GLOBAL` support (`CONFIG_MEMBARRIER` enabled and `NO_HZ_FULL` inactive). Ring swaps depend on this command providing a sched-RCU grace period on Linux 4.18 and a unified RCU grace period on Linux 5.0 and later; expedited membarrier commands cannot replace it. An RCU stall can delay a swap and collection shutdown.


| Parameter | Default | Applies To | Description |
| --- | --- | --- | --- |
| `--memory-mode` | None | Native and Java memory | Memory measurement; required with `--type memory` |
| `--cpu-list` | All CPUs | Native CPU | CPU list or ranges; off-CPU filtering uses the CPU where the task switches out |
| `--cpu-mode` | `oncpu` | Native CPU | `oncpu` samples by frequency; `offcpu` attributes blocked and runnable scheduling delays |
| `--require-hardware-pmu` | `false` | Native on-CPU | Require hardware PMU sampling; fail if unavailable without falling back to software CPU clock |
| `--offcpu-phase` | `all` | Native off-CPU | Accumulate `all`, `blocked`, or `runqueue` time |
| `--offcpu-min-duration-us` | `1000` | Native off-CPU | Discard phases shorter than this duration in microseconds |
| `--offcpu-stats` | `false` | Native off-CPU | Collect BPF diagnostic statistics; adds overhead to error and cleanup paths |
| `--thread-group` | `false` | Native | Also collect other threads in the target PID's thread group |
| `--physical-memory-probability` | `100` | Native physical memory | Physical-memory event sampling probability from 1 to 100 |
| `--log-bpf-debug` | `false` | Native | Emit BPF debug events; not recommended for routine collection |

When the hardware PMU event or sampling is unsupported, the command exits with code `1` and identifies the error on stderr with `Error: hardware_pmu_unavailable:`. Permission and other startup errors do not use this marker.

`--cpu-list` replaces `--cpuid` without an alias. The IRQ tool also uses `--cpu-list`; its former `--target-cpu` option is removed. Upgrade calling scripts together with the tools.

## Logging

| Parameter | Default | Description |
| --- | --- | --- |
| `--log-level` | `error` | `trace`, `debug`, `info`, `warn`, or `error` |
| `--log-file` | `stdout` | Log file path or `stdout` |
| `--log-size` | `100` | Log rotation size in MB; `0` disables rotation. File output only |
| `--verbose` | `false` | Equivalent to `--log-level debug --log-file stdout`, overriding explicit log settings |
