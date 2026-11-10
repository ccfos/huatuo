---
title: Kernel-wide Insight
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

## Overview

HUATUO collects host and container metrics for CPU, memory, network, I/O, system events, and GPUs to help identify when an anomaly occurs, which resources are involved, and its scope.

| Problem | Key metrics |
| --- | --- |
| Busy CPUs or tasks waiting too long to be scheduled | Utilization, scheduling latency, throttling, and wait ratio |
| Memory pressure, reclaim stalls, or allocation failures | Memory state, reclaim duration, OOM, and free memory blocks |
| Reduced network throughput, packet drops, or connection anomalies | Network interface, transmit queue, TCP, and socket statistics |
| Slow disk responses | I/O latency distributions, throughput, and request duration |
| System stalls or prolonged task blocking | softlockup and hungtask event counts |
| GPU load, memory, or device state anomalies | Utilization, memory, temperature, power, and interconnect metrics |

## Configuration

Actual output depends on kernel interfaces, device drivers, collector switches, and filters. Some collectors are disabled by default in the repository configuration. For instructions on enabling them, see [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md).

Metrics are exposed through `/metrics`. For full names, units, labels, examples, and measurement semantics, see the [Metrics Reference](/docs/reference/metrics_en.md). A missing metric does not imply a zero value.

## CPU

Utilization measures how much CPU capacity is used; scheduling latency measures how long runnable tasks wait for a CPU. Combine these with available container CPU capacity, throttling, and wait ratio to distinguish high compute load, quota limits, and resource contention.

Host utilization is normalized by the number of online CPUs; container utilization is normalized by available CPU capacity. Host load averages and current container task counts use different observation windows and should not be compared directly.

For metrics and bucket ranges, see [CPU Metrics](/docs/reference/metrics_en.md#cpu).

## Memory

Memory state metrics distinguish anonymous and file memory, as well as activity states. Reclaim duration and event counts help investigate stalls caused by memory pressure. Free memory blocks are reported by NUMA node, zone, and order to help investigate contiguous memory allocation problems.

Fields and units vary by kernel and cgroup version. Interpret page counts, byte counts, and event counts separately; some metrics depend on custom kernel interfaces. For measurement conditions, see [Memory Metrics](/docs/reference/metrics_en.md#memory).

## Network

Network interface and transmit queue metrics show throughput, backlog, and packet drops. TCP, socket, connection tracking, and ARP statistics help analyze connections and protocol resource usage.

Transmit queue backlog is not queueing latency, and aggregate error counts may already include individual error categories. Check measurement semantics to avoid double counting. For metrics and their sources, see [Network Metrics](/docs/reference/metrics_en.md#network).

## I/O

`iotracing` provides disk throughput, IOPS, mean request duration, utilization, and queue length. `iolatency` measures Q2G, Q2D, and D2C latency and IO size distributions; `blk_throtl` and `iocost` report completed queue waits. `io_health` records Block, NVMe, SCSI, and MD health events and collects read-only device evidence. XFS metrics add filesystem log and allocation state.

Latency, size, and wait metrics describe individual collection intervals. Device changes restart collection, and transition data may be omitted. For names, units, labels, startup requirements, and measurement semantics, see [I/O Metrics](/docs/reference/metrics_en.md#io).

## System events

softlockup and hungtask counters track soft lockups and blocked tasks reported by the kernel. Count changes help identify the affected time range. For process and call stack information, see [Event Diagnostics](/docs/key-feature/instant-observability_en.md). For metric names, see [System Event Metrics](/docs/reference/metrics_en.md#system-events).

## GPU

### MetaX

The collector gathers GPU utilization, memory, temperature, power, PCIe, MetaXLink, ECC, and related metrics. Before enabling it, meet the device and SDK requirements and check the `metax_gpu` blacklist setting.

For the metric list, see [MetaX Metrics](/docs/reference/metrics_en.md#metax).

### MThreads

The collector gathers GPU, memory, power, PCIe, MTLink, and related metrics. It requires the MTML management library. After enabling the collector, use `MetricCollector.Mthreads` to control metric groups.

Interpret device specifications separately from current throughput. For dependencies and the metric list, see [MThreads Metrics](/docs/reference/metrics_en.md#mthreads).

### Ascend

Details omitted.
