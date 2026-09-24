---
title: IPv6 Socket Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

`sockstat` 采集器为主机和容器同时读取 `/proc/<init-pid>/net/sockstat` 与
`sockstat6`，补齐 IPv6 socket 用量监控，可用于发现 IPv6 连接或分片队列持续增长。

| 指标后缀 | 含义 |
| --- | --- |
| `TCP6_inuse` | IPv6 TCP socket 数量 |
| `UDP6_inuse` | IPv6 UDP socket 数量 |
| `UDPLITE6_inuse` | IPv6 UDP-Lite socket 数量 |
| `RAW6_inuse` | IPv6 raw socket 数量 |
| `FRAG6_inuse` | IPv6 分片队列数量 |
| `FRAG6_memory` | IPv6 分片队列占用字节数 |

主机指标前缀为 `huatuo_bamai_sockstat_`，容器指标前缀为
`huatuo_bamai_sockstat_container_`，沿用现有容器标签。所有指标均为 gauge，
仅导出内核实际提供的字段。例如：

```promql
huatuo_bamai_sockstat_container_TCP6_inuse{container_host="checkout-api"}
```

默认启用，除非 `BlackList` 包含 `sockstat`。未提供 `sockstat6` 的内核继续正常采集 IPv4。
每个网络命名空间每次采集增加一次小文件读取。IPv6 文件不单独提供 TCP/UDP 内存页计数；
原有主机 `TCP_mem_bytes` 和 `UDP_mem_bytes` 保留内核全局统计语义。
参见[内核实现](https://github.com/torvalds/linux/blob/master/net/ipv6/proc.c)。
