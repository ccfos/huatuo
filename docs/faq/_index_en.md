---
title: FAQ
type: docs
weight: 9
---

## Metrics

### Why do the `memory_others_*` metrics (e.g. `directstall_time`) have no data?

The `memory_others` collector reads memory cgroup extension interfaces provided by the Didi Cloud custom kernel (`memory.directstall_stat`, `memory.asynreclaim_stat`, `memory.local_direct_reclaim_time`). Mainline and common distribution kernels do not expose these interfaces, and no loadable kernel module provides them, so these metrics are simply not emitted on standard kernels — this is expected behavior.

To observe container direct reclaim behavior on standard kernels, use the eBPF-based `memory_reclaim_container_directstall` metric instead; see the Memory System section in "Key Features / Kernel-Wide Insight".
