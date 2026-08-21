---
title: Change Log
type: docs
description:
author: HUATUO Team
date: 2026-08-13
weight: 50
---

#### Features

- Added a unified profiler for CPU and memory profiling of native applications, Java, and Python
- Added native Off-CPU profiling to distinguish blocking from run queue scheduling latency
- Added native virtual allocation, physical allocation, and physical usage profiling
- Added huatuo-apiserver and continuous profiling task APIs
- Added aggregation and persistence of continuous profiling data and a Pyroscope-compatible query API
- Added an interactive terminal flame graph to the perf tool
- Added a standalone dropwatch tool with pcap filtering, device filtering, and event rate limiting
- Added devlink hardware packet drop tracing and deduplication of software and hardware drops
- Added the tcpshark tool and TCP retransmission events with suspected packet drop correlation
- Added an Agent SSE API for real-time kernel event subscriptions
- Added Agent APIs for node task and tracer control
- Added Ascend NPU metrics for chips, HBM, PCIe, RoCE, and optical modules
- Added basic disk I/O metrics and I/O latency metrics for block devices and containers
- Added memory limits, usage, and system/container memory snapshots to OOM events
- Added ARM GHES processor error detection
- Added a CPU external wait ratio metric
- Added container filtering, combined issue rules, and delta thresholds to AutoTracing
- Added Elasticsearch 7/8, OpenSearch, and SQLite storage backends
- Added per-tracer local data logs with rotation support
- Added a Helm Chart, Prometheus Kubernetes service discovery, and deployment resource baselines
- Added Grafana dashboards for host/container continuous profiling and disk I/O
- Added version output to all major binaries and HTTP `/healthz` and `/version` endpoints
- Added a typed toolstream event channel for diagnostic subprocess output
- Added BPF ABI generation and BPF object lease management

#### Bug Fixes & Improvements

- Improved net_rx_latency compatibility with multiple skb timestamp layouts and non-ESTABLISHED TCP receive paths
- Improved structured RAS event output and kernel tracepoint capability detection
- Improved collector dependency checks to gracefully degrade when procfs or memcg capabilities are unavailable
- Improved AutoTracing trigger rules, iotracing collection limits, and result storage
- Improved asynchronous storage queue draining on shutdown and insert-only write semantics
- Improved compatibility with the Kubernetes Pod cgroup lifecycle
- Improved CPU event batch reads, perf reader lost-sample recovery, and kretprobe concurrency configuration
- Improved compatibility with ARM GHES, folios, skb timestamps, and tracefs kprobe symbols
- Improved and enriched documentation at https://docs.huatuo.tech/en/latest/
