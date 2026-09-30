---
title: CPU Frequency Policy Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

`cpufreq` exposes CPU frequency policy settings so a reduced frequency cap or
an unexpected governor can be correlated with CPU saturation and latency.
Policies shared by several CPUs are read and exported once.

Metrics use prefix `huatuo_bamai_cpufreq_`, type gauge, and labels `host`,
`region`, `policy` (the numeric policy ID, not necessarily a CPU ID).

| Suffix | Meaning |
| --- | --- |
| `scaling_current_hertz` | Frequency reported by the scaling driver |
| `scaling_minimum_hertz` | Configured lower frequency limit |
| `scaling_maximum_hertz` | Configured upper frequency limit |
| `hardware_minimum_hertz` | Hardware lower frequency bound |
| `hardware_maximum_hertz` | Hardware upper frequency bound |
| `bios_limit_hertz` | Firmware frequency cap, where available |
| `info` | Constant 1 with additional `governor` and `driver` labels |

```promql
# Policies with a configured cap below the hardware maximum
huatuo_bamai_cpufreq_scaling_maximum_hertz
< huatuo_bamai_cpufreq_hardware_maximum_hertz
```

A lower cap can be intentional; correlate it with the policy governor and
workload. `scaling_current_hertz` may reflect a requested frequency, not the
instantaneous physical clock. See [CPU performance scaling](https://docs.kernel.org/admin-guide/pm/cpufreq.html).

The collector is enabled unless `cpufreq` is in `BlackList`. It reads
`/sys/devices/system/cpu/cpufreq/policy*` on each scrape, handling CPU hotplug.
Unsupported attributes are omitted; hosts without CPUFreq emit no series.
A read failure preserves the remaining valid metrics and reports a scrape
error. At most eight small file reads and seven series are added per policy;
actual read latency depends on the scaling driver.
