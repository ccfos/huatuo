---
title: Autotracing
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

## Overview

**AutoTracing** is an intelligent diagnostic feature of the Huatuo kernel monitoring system.

When the system experiences specific performance anomalies or sudden resource spikes, AutoTracing is **automatically triggered**. It captures detailed on-site information in real time, including flame graphs, process context, call stacks, and resource status. This helps operations and development teams quickly locate and analyze issues without manual intervention.

This feature is built on **eBPF** technology, offering low overhead and high real-time performance. It is suitable for anomaly diagnosis in both physical machines and container environments.

## Supported Types

The current version supports the following five types of automatic tracing:

| Tracing Name | Core Function                                                | Use Cases                                                    |
| ------------ | ------------------------------------------------------------ | ------------------------------------------------------------ |
| cpusys       | Detects sudden increases in physical machine CPU sys (kernel) usage, automatically generates flame graphs and provides process context information | Resolves business jitter and latency issues caused by abnormal system load |
| cpuidle      | Detects abnormal decreases in container CPU idle rate, automatically generates flame graphs and provides process context information | Addresses abnormal container CPU usage and helps analyze process hotspots |
| dload        | Detects sudden increases in container loadavg, automatically captures call information of D-state processes inside the container | Solves issues caused by sudden spikes in D-state processes, resource unavailability, or long-held locks |
| memburst     | Detects sudden memory allocation bursts on physical machines, automatically captures process memory usage status | Handles scenarios with large amounts of memory allocation in a short time, which may trigger direct reclaim or OOM |
| iotracing    | Detects abnormal disk IO latency on physical machines, automatically captures related processes, containers, disks, and file information | Resolves application request delays or system performance jitter caused by saturated disk IO bandwidth or sudden increases in disk access |

## Features

- **Intelligent Triggering**: Automatically detects anomalies based on preset thresholds, eliminating the need for manual configuration.
- **Rich Diagnostic Information**: Automatically collects key data such as flame graphs, call stacks, process/container context, and resource usage details each time it is triggered.
- **Low Overhead Design**: Uses eBPF technology to perform targeted collection only when anomalies occur, resulting in extremely low overhead during normal operation.
- **Unified Output**: All tracing data is reported in a standardized format, facilitating querying, analysis, and integration with alerting systems.

## Recommendations

- **cpusys** and **cpuidle** are ideal for quickly locating CPU-related performance jitter.
- **dload** is particularly useful for issues caused by "pseudo-dead" or stalled processes due to D-state processes.
- **memburst** helps detect potential memory pressure in advance and prevents OOM occurrences.
- **iotracing** is the preferred tool for troubleshooting disk IO bottlenecks.

With the AutoTracing feature, Huatuo enables an automated closed loop from anomaly detection to on-site preservation, significantly improving problem diagnosis efficiency.
