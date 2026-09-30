---
title: Bonding Redundancy Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

bond 在成员链路故障后可能仍保持可用，但已失去冗余。`bonding` 采集器读取主机
sysfs 中的 bonding 状态，让这类故障在业务完全断网前可被发现。

| 指标 | 类型 | 附加标签 |
| --- | --- | --- |
| `huatuo_bamai_bonding_slaves` | gauge | `master` |
| `huatuo_bamai_bonding_slaves_up` | gauge | `master` |
| `huatuo_bamai_bonding_slave_up` | gauge，0 或 1 | `master`、`slave` |
| `huatuo_bamai_bonding_slave_link_failures_total` | counter | `master`、`slave` |

所有指标均带 `host`、`region` 标签。以下条件可用于发现链路冗余下降：

```promql
huatuo_bamai_bonding_slaves_up < huatuo_bamai_bonding_slaves
```

配置适当的持续时间，避免短暂切换误报。
`increase(huatuo_bamai_bonding_slave_link_failures_total[15m])` 可定位频繁抖动的链路；
重新加入 bond 后计数可能重置。

默认启用，可通过 `BlackList` 中的 `bonding` 关闭。每次采集重新读取 bond 和成员列表，
无 bond 的主机不产生指标。若成员在采集中消失，不将未知状态当作故障，而是暂不导出该 bond 的
`slaves_up`；其他 bond 的有效数据保留。

链路状态来自 bonding 驱动的监测，包含备用链路，不代表 LACP 转发资格。
需按 [Linux bonding 指南](https://docs.kernel.org/networking/bonding.html) 配置链路监测。
每个 bond 读取一次成员列表，每个成员读取两个小文件。
