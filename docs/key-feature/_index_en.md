---
title: Key Features
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 6
---

HUATUO provides resource metrics, event diagnostics, automatic tracing, and profiling. Choose a feature based on your troubleshooting goal:

| Goal | Documentation | Main results |
| --- | --- | --- |
| Check resource usage on nodes and in containers | [Kernel-wide Insight](/docs/key-feature/kernel-wide-insight_en.md) | CPU, memory, network, storage, and accelerator metrics |
| Investigate past system anomalies | [Event Diagnostics](/docs/key-feature/instant-observability_en.md) | Event times, process and container context, and call stacks |
| Capture diagnostics automatically when metrics indicate an anomaly | [Automatic Tracing](/docs/key-feature/autotracing_en.md) | Triggering metrics, CPU flame graphs, and I/O and memory diagnostics |
| Investigate CPU hotspots and memory allocation | [Continuous Profiling](/docs/key-feature/continuous-profiling_en.md) | CPU time, wait time, and memory allocation attributed to call stacks |
| Investigate CPU, memory, and PCIe hardware errors | [Hardware Error Diagnostics](/docs/key-feature/hardware-events_en.md) | RAS error counts, device identifiers, and error details |

Start with resource metrics to identify the time and scope of an anomaly, then examine events or profiling results. For collection switches, thresholds, and storage settings, see [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md). For task management and query settings, see [API Server Configuration](/docs/configuration/huatuo-apiserver-configuration_en.md).

For metrics, event fields, and raw codes, see the [Reference Manual](/docs/reference/_index_en.md).
