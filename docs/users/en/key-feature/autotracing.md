---
title: Autotracing
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

HUATUO currently supports automatic tracing for the following metrics:

| Tracing Name        | Core Function               | Scenario                                  |
| --------------------| --------------------------- |------------------------------------------ |
| cpusys              | Host sys surge detection      | Service glitches caused by abnormal system load            |
| cpuidle             | Container CPU idle drop detection, providing call stacks, flame graphs, process context info, etc. | Abnormal container CPU usage, helping identify process hotspots |
| dload               | Tracks container loadavg and process states, automatically captures D-state process call info in containers | System D-state surges are often related to unavailable resources or long-held locks; R-state process surges often indicate poor business logic design |
| waitrate            | Container resource contention detection; provides info on contending containers during scheduling conflicts | Container contention can cause service glitches; existing metrics lack specific contending container details; waitrate tracing provides this info for mixed-deployment resource isolation reference |
| memburst            | Records context info during sudden memory allocations | Detects short-term, large memory allocation events on the host, which may trigger direct reclaim or OOM |
| iotracing           | Detects abnormal host disk I/O latency. Outputs context info like accessed filenames/paths, disk devices, inode numbers, containers, etc. | Frequent disk I/O bandwidth saturation or access surges leading to application request latency or system performance jitter |
