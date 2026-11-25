---
title: FAQ
type: docs
weight: 9
---

## Metrics

- **Why are `memory_others_*` metrics, such as `directstall_time`, missing?**

    The `memory_others` collector reads memory cgroup extensions provided by DiDi Cloud's custom kernel: `memory.directstall_stat`, `memory.asynreclaim_stat`, and `memory.local_direct_reclaim_time`. Mainline and common distribution kernels do not provide these interfaces, and no loadable kernel module supplies them. These metrics are therefore expected to be absent on standard kernels.

    To observe container direct reclaim on a standard kernel, use the eBPF-based `memory_reclaim_container_directstall` metric. See [Memory Metrics](/docs/reference/metrics_en.md#memory).
