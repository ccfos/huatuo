---
title: profiler CLI Parameters
type: docs
author: HUATUO Team
date: 2026-10-21
weight: 6
---

This reference lists `profiler` parameters, defaults, and applicability. See [CLI Collection](/docs/best-practice/continuous-profiling_en.md#cli-collection) for prerequisites and examples, and [Output Formats](/docs/best-practice/continuous-profiling_en.md#output-formats) to choose a format.

Both `--output-path` and `--output-format` must be explicit. The declared default for `--output-format` is `collapsed`, but validation does not allow the flag to be omitted.

## General

| Parameter | Default | Applies To | Description |
| --- | --- | --- | --- |
| `--type`, `-t` | None | All | Observation type: `cpu` or `memory`; required |
| `--language`, `-l` | None | All | Target language: `c`, `c++`, `go`, `java`, or `python`; required |
| `--pid`, `-p` | None | All | Target PID; Java and Python accept comma-separated PIDs, native collection accepts at most one |
| `--container-id` | None | All | Target container ID; mutually exclusive with `--pid` |
| `--duration`, `-d` | `10` | All | Total collection duration in seconds, minimum 1 |
| `--aggr-interval` | `10` | All | Aggregation interval in seconds; cannot exceed duration |
| `--freq`, `-F` | `99` | CPU | Samples per second; Java maximum: 1000 |
| `--output-path` | Required | All | Local file, directory, or `stdout`; Unix socket for `remote-store`. End new directory paths with `/` |
| `--output-format` | `collapsed` | All | `collapsed`, `flamegraph`, `svg`, `json`, or `remote-store`; must be explicit together with `--output-path` |
| `--max-concurrent-procs` | `0` | Java, Python | Maximum concurrent collection child processes; `0` is unlimited |
| `--tool-path` | None | Java, Python | Shared root directory for external tools; required |
| `--binary-match-path` | None | Java, Python | Match target container processes by executable path |
| `--huatuo-api-address` | `127.0.0.1:19704` | Container targets | HUATUO API address for resolving container metadata |
| `--task-id` | Empty; generated internally for local output | All; required for `remote-store` | Stable collection task ID shared by toolstream and remote storage |
| `--enable-pprof` | `false` | The tool itself | Serve this process's Go runtime profiles on `127.0.0.1:6000` |
| `--version-format` | `text` | Version queries | Format for `--version`: `text`, `json`, or `short` |
| `--help`, `-h` | - | All | Show command help |
| `--version`, `-v` | - | All | Show version and build information |

These pprof endpoints describe the profiler process's own Go runtime, not the `--pid` target or Java/Python collector subprocesses. The listener is now restricted to loopback rather than all interfaces; an occupied port fails startup.

## Collection

| Parameter | Default | Applies To | Description |
| --- | --- | --- | --- |
| `--memory-mode` | None | Native and Java memory | Memory measurement; required with `--type memory` |
| `--cpuid` | All CPUs | Native CPU | CPU list or ranges; off-CPU filtering uses the CPU where the task switches out |
| `--cpu-mode` | `oncpu` | Native CPU | `oncpu` samples by frequency; `offcpu` attributes blocked and runnable scheduling delays |
| `--require-hardware-pmu` | `false` | Native on-CPU | Require hardware PMU sampling; fail if unavailable without falling back to software CPU clock |
| `--offcpu-phase` | `all` | Native off-CPU | Accumulate `all`, `blocked`, or `runqueue` time |
| `--offcpu-min-duration-us` | `1000` | Native off-CPU | Discard phases shorter than this duration in microseconds |
| `--offcpu-stats` | `false` | Native off-CPU | Collect BPF diagnostic statistics; adds overhead to error and cleanup paths |
| `--thread-group` | `false` | Native | Also collect other threads in the target PID's thread group |
| `--physical-memory-probability` | `100` | Native physical memory | Physical-memory event sampling probability from 1 to 100 |
| `--log-bpf-debug` | `false` | Native | Emit BPF debug events; not recommended for routine collection |

When the hardware PMU event or sampling is unsupported, the command exits with code `1` and identifies the error on stderr with `Error: hardware_pmu_unavailable:`. Permission and other startup errors do not use this marker.

## Logging

| Parameter | Default | Description |
| --- | --- | --- |
| `--log-level` | `error` | `trace`, `debug`, `info`, `warn`, or `error` |
| `--log-file` | `stdout` | Log file path or `stdout` |
| `--log-size` | `100` | Log rotation size in MB; `0` disables rotation. File output only |
| `--verbose` | `false` | Equivalent to `--log-level debug --log-file stdout`, overriding explicit log settings |
