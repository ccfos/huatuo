---
title: 硬件故障诊断
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

## 概述

HUATUO 通过内核 RAS 事件记录 CPU、内存和 PCIe 错误，提供错误计数及结构化详情。可结合设备位置、错误趋势和业务异常分析故障；单次错误或计数增长不能直接证明设备即将失效。

## 采集范围

| 来源 | `tracer_data.event` | 主要信息 |
| --- | --- | --- |
| MCE | `MCE` | CPU 机器检查寄存器、CPU 与 Bank 编号 |
| EDAC | `EDAC` | 内存错误、控制器及内存位置标签 |
| ACPI GHES | `NON_STANDARD` | 平台非标准错误记录和 FRU 信息 |
| PCIe AER | `AER` | PCIe 设备、错误类型及 TLP 头部 |
| MCE 阈值中断 | `MCE_THRESHOLD` | 中断向量和 CPU 编号 |

实际采集范围取决于硬件、固件、内核 tracepoint 及探针加载结果。当前启动检查要求存在 `ras` tracepoint 目录和 `mce/mce_record`；缺失时不会启动 RAS 观测器。虽然解码器定义了 ARM GHES 记录，仍不能据此认定 ARM 平台已可用。

内核事件经 eBPF 缓冲区传递给用户态，再解析为计数指标和事件记录：

![硬件错误采集与存储流程](/docs/img/hardware-errors-huatuo-framework.png)

## 配置

确认全局黑名单未禁用 `ras`，并按需要启用 Elasticsearch 或本地文件存储。配置说明见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)的“硬件错误（RAS）”章节。

`EventTracing.Ras.MceThrBackoff` 控制 MCE 阈值中断事件的保存间隔，内置默认值为 1800 秒。冷却期间仍累计接收到的 THR 事件数，因此计数增长量不一定等于已保存的记录数。

## 结果

### 错误计数

`huatuo_bamai_ras_hw_err_total` 按来源累计接收到的 RAS 事件数，用于查看错误趋势。它不是 EDAC 记录中 `err_count` 的累加值，也不等于已保存的记录数。

### 事件记录

事件包含观测时间、设备、错误来源、严重级别和来源详情。以下为省略部分字段的示例：

```json
{
    "tracer_name": "ras",
    "tracer_type": "event",
    "tracer_data": {
        "dev": "MEM",
        "event": "EDAC",
        "type": "Corrected",
        "info": "{\"err_count\":1,\"label\":\"CPU0_DIMM_A1\"}"
    }
}
```

按 `tracer_name=ras`、设备和时间范围查询。`info` 是 JSON 字符串，需再次解析，其字段随错误来源变化。结合设备拓扑和错误趋势判断影响，单条记录不能直接证明设备损坏。

### 严重级别

| 类型 | 含义 |
| --- | --- |
| `Corrected` | 已纠正 |
| `UncorrectedRecoverable` | 未纠正、可恢复类别 |
| `UncorrectedDeferred` | 未纠正、延迟处理类别 |
| `UncorrectedFatal` | 未纠正、致命类别 |
| `Info` | 信息记录 |
| `unknown` | 无法映射严重级别 |

严重级别用于分类，不等同于恢复成功、业务无影响或必须重启的判断。寄存器、原始编码映射及各来源字段见[硬件事件字段](/docs/reference/hardware-events_zh.md)。
