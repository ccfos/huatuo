---
title: Continuous Profiling
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

## Overview

Continuous profiling attributes CPU time, wait time, and memory allocation during collection to call stacks, helping identify which code paths consume resources. Combined with resource metrics, it helps narrow an investigation from an affected node or container to functions that need inspection.

Profiling runs for a specified duration and produces periodic windows or a single result, depending on the collection mode. Manage node and container tasks through the API, or run `profiler` directly on the target node.

## Use cases

| Problem | Profiling mode | Diagnostic information |
| --- | --- | --- |
| CPU usage rises and hotspots need investigation | on-CPU | CPU time consumed by each call stack |
| Requests slow down without high CPU usage | off-CPU | Call paths associated with blocking and scheduling waits |
| Frequent memory allocation requires investigation | Native memory or Java object allocation | Allocation volume and allocation call stacks |
| Memory usage keeps growing and live allocations need inspection | Live physical pages or live Java objects | Live allocation volume and allocation paths within the collection scope |

Hotspots and live allocation volume provide diagnostic clues; they do not independently prove a performance bottleneck or memory leak. Interpret them alongside workload, collection scope, and time window.

## Collection capabilities

### CPU

| Mode | Supported languages | Measurements |
| --- | --- | --- |
| on-CPU | C, C++, Go, Java, Python | Call stacks sampled at the configured frequency and converted to CPU time |
| off-CPU | C, C++, Go | Blocking and scheduling wait time after a thread leaves the CPU |

Native collection uses eBPF; Java uses async-profiler; Python uses py-spy. Off-CPU collection is event-driven and distinguishes blocking from run queue waits. It does not measure end-to-end application request latency.

### Memory

| Mode | Supported languages | Measurements |
| --- | --- | --- |
| `virtual_alloc` | C, C++, Go | Virtual address space allocation volume |
| `physical_alloc` | C, C++, Go | Physical memory allocated within the collection window |
| `physical_usage` | C, C++, Go | Bytes in live physical pages tracked during this collection |
| `object_alloc` | Java | Object allocation volume and allocation call stacks |
| `object_usage` | Java, Python | JVM live objects, or retained Python-process allocation bytes and their allocation stacks |

Python memory collection uses the prebuilt Memray runtime for CPython 3.7–3.14 on Linux glibc, x86-64 or AArch64. It reports allocations made after attachment that remain live when collection ends. It excludes earlier allocations and measures allocation bytes rather than RSS. The running profiler requires `gdb` in `PATH` and permission to attach through `ptrace`; seccomp and Yama `ptrace_scope` must allow attachment. Python-only, native, and hybrid stack views are available through the CLI.

Python frames use function names only, including in hybrid views. Filenames
and allocation lines are not shown; same-named functions in different files
or different allocation lines can be combined when rendered call chains match.

Only one Memray session may profile a Python process at a time; a competing request fails without interrupting the active capture. The source runtime tree must contain only regular files and directories, not symlinks. In the container's destination staging path, relative symlinks that stay within the target root are supported; absolute symlinks and links escaping that root are rejected.

Before reusing a staged container runtime, profiler verifies its file names and
contents against the selected source bundle. Generated `__pycache__` directories
are excluded because Python can regenerate bytecode during normal use. A missing
or changed runtime file fails the attachment; remove the reported stale cache
directory inside the target container and retry. Existing cached files are not
automatically overwritten. This check does not prevent changes after validation.

A Python memory result is published only after the runtime sends its stream trailer. Cancellation allows two seconds to drain pending records; a forced socket close or EOF without a trailer fails the capture instead of publishing an incomplete retained-memory result. The target also bounds the initial data-connection wait to ten seconds and aborts it when cancellation is requested.

`physical_usage` excludes pages allocated before collection and does not scale values by the inverse sampling probability. It cannot represent a process's full RSS. Page-free detection and other limitations still require validation and improvement; see [Profiling in Practice](/docs/best-practice/continuous-profiling_en.md#live-physical-memory).

## Collection methods

| Method | Scope | Dependencies |
| --- | --- | --- |
| Profiling API | Remotely create, query, and stop node or container tasks | huatuo-apiserver and huatuo-bamai; querying results requires Elasticsearch |
| `profiler` CLI | Collect data for a target process on a node; native CPU collection also supports host or container scope | A Linux collection environment and the relevant tools; local output requires neither the API service nor Elasticsearch |

Supported languages, modes, and target scopes depend on the current version. Native collection also requires the necessary kernel capabilities and sampling permissions.

## Results

### Flame graphs

A flame graph shows call paths and their cumulative values. Width represents CPU time, wait time, or bytes in the selected memory dimension; the vertical axis represents call depth. Horizontal position does not indicate chronological order. Inspect wider frames to identify sources of resource consumption.

![Host CPU flame graph and function statistics](/docs/img/continuous-profiling-grafana-host.png)

View results in Grafana or export them locally as SVG, folded stacks, or JSON for offline analysis and further processing.

### Collection windows

Native CPU, native memory, and Java collection process data periodically; Python produces one result when collection ends. Window timestamps describe reception and aggregation intervals, not exact execution times for individual samples.

Live physical page usage uses the latest snapshot and must not be summed across windows. When comparing results, keep the target, mode, time range, and workload comparable.

### Completeness

Task status, result publication, and sample loss statistics together determine whether results are usable. A failed task may retain some data already written. A flame graph does not prove that the task completed successfully, and empty results do not prove that no resources were consumed.

For deployment steps, task APIs, CLI examples, and result validation, see [Profiling in Practice](/docs/best-practice/continuous-profiling_en.md).
