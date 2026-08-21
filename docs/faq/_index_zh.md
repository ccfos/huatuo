---
title: 常见问题
type: docs
weight: 9
---

## 指标

- **为什么 `memory_others_*`（如 `directstall_time`）指标没有数据？**

    `memory_others` 采集器读取的是滴滴云定制内核提供的 memory cgroup 扩展接口（`memory.directstall_stat`、`memory.asynreclaim_stat`、`memory.local_direct_reclaim_time`）。主线内核及常见发行版内核不提供这些接口，也没有可加载的内核模块能提供，因此在标准内核上这些指标不会输出，属预期行为。

    在标准内核上观测容器直接回收（direct reclaim）行为，请使用基于 eBPF 实现的 `memory_reclaim_container_directstall` 指标，详见「核心特性 / 内核全景观测」文档中的内存系统章节。
