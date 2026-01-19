---
title: Collection Framework
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

HuaTuo framework provides three data collection modes: `autotracing`, `event`, and `metrics`, covering different monitoring scenarios, helping users gain comprehensive insights into system performance.

### Collection Mode Comparison
| Mode            | Type           | Trigger Condition | Data Output      | Use Case       |
|-----------------|----------------|-------------------|------------------|----------------|
| **Autotracing** | Event-driven   | Triggered on system anomalies | ES + Local Storage, Prometheus (optional) | Non-routine operations, triggered on anomalies |
| **Event**       | Event-driven   | Continuously running, triggered on preset thresholds | ES + Local Storage, Prometheus (optional) | Continuous operations, directly dump context |
| **Metrics**     | Metric collection | Passive collection | Prometheus format | Monitoring system metrics |

### Autotracing
- **Type**: Event-driven (tracing).
- **Function**: Automatically tracks system anomalies and dump context when anomalies occur.
- **Features**:
    - When a system anomaly occurs, `autotracing` is triggered automatically to dump relevant context.
    - Data is stored to ES in real-time and stored locally for subsequent analysis and troubleshooting. It can also be monitored in Prometheus format for statistics and alerts.
    - Suitable for scenarios with high performance overhead, such as triggering captures when metrics exceed a threshold or rise too quickly.
- **Integrated Features**: CPU anomaly tracking (cpu idle), D-state tracking (dload), container contention (waitrate), memory burst allocation (memburst), disk anomaly tracking (iotracer).

### Event
- **Type**: Event-driven (tracing).
- **Function**: Continuously operates within the system context, directly dump context when preset thresholds are met.
- **Features**:
    - Unlike `autotracing`, `event` continuously operates within the system context, rather than being triggered by anomalies.
    - Data is also stored to ES and locally, and can be monitored in Prometheus format.
    - Suitable for continuous monitoring and real-time analysis, enabling timely detection of abnormal behaviors. The performance impact of `event` collection is negligible.
- **Integrated Features**: Soft interrupt anomalies (softirq), memory allocation anomalies (oom), soft lockups (softlockup), D-state processes (hungtask), memory reclamation (memreclaim), packet droped abnormal (dropwatch), network ingress latency (net_rx_latency).

### Metrics
- **Type**: Metric collection.
- **Function**: Collects performance metrics from subsystems.
- **Features**:
    - Metric data can be sourced from regular procfs collection or derived from `tracing` (autotracing, event) data.
    - Outputs in Prometheus format for easy integration into Prometheus monitoring systems.
    - Unlike `tracing` data, `metrics` primarily focus on system performance metrics such as CPU usage, memory usage, and network traffic, etc.
    - Suitable for monitoring system performance metrics, supporting real-time analysis and long-term trend observation.
- **Integrated Features**: CPU (sys, usr, util, load, nr_running, etc.), memory (vmstat, memory_stat, directreclaim, asyncreclaim, etc.), IO (d2c, q2c, freeze, flush, etc.), network (arp, socket mem, qdisc, netstat, netdev, sockstat, etc.).

### Multiple Purpose of Tracing Mode
Both `autotracing` and `event` belong to the **tracing** collection mode, offering the following dual purposes:
1. **Real-time storage to ES and local storage**: For tracing and analyzing anomalies, helping users quickly identify root causes.
2. **Output in Prometheus format**: As metric data integrated into Prometheus monitoring systems, providing comprehensive system monitoring capabilities.

By flexibly combining these three modes, users can comprehensively monitor system performance, capturing both contextual information during anomalies and continuous performance metrics to meet various monitoring needs.
