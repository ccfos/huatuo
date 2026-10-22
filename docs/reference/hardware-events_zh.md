---
title: 硬件事件字段
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

本文说明 RAS 指标、事件结构、严重级别和来源字段。采集范围及启用方式见[硬件故障诊断](/docs/key-feature/hardware-events_zh.md)。

`EventTracing.Ras.MceThrBackoff` 控制阈值中断事件的保存间隔。冷却期间仍累计事件数，因此计数增长量不一定等于已保存记录数。

## 结果

### 错误计数

`huatuo_bamai_ras_hw_err_total` 为接收到的 RAS 事件累计数，按 `type` 区分 `mce`、`edac`、`acpi`、`aer`、`thr` 和 `arm_ghes`。标签存在不代表当前平台支持对应探针。

```prometheus
huatuo_bamai_ras_hw_err_total{hostname="node-01",region="region-01",type="edac"} 12
```

以上为示例值。计数单位是事件，并非 EDAC 记录中 `err_count` 的累加值。网卡硬件丢包及 PFC 指标见[内核全景观测](/docs/key-feature/kernel-wide-insight_zh.md)，网卡状态变化见[异常事件诊断](/docs/key-feature/instant-observability_zh.md)。

### 事件记录

按 `tracer_name=ras` 和 `tracer_type=event` 查询。下面以 EDAC 为例，省略部分字段：

```json
{
    "hostname": "node-01",
    "observed_timestamp": "2026-05-11T08:58:47.329Z",
    "kernel_observed_timestamp": "2026-05-11T08:58:47.328548319Z",
    "tracer_name": "ras",
    "tracer_type": "event",
    "tracer_data": {
        "dev": "MEM",
        "event": "EDAC",
        "type": "Corrected",
        "info": "{\"err_count\":1,\"err_type\":\"Corrected\",\"err_msg\":\"memory read error\",\"label\":\"CPU_SrcID#0_Ha#0_Chan#0_DIMM#0\",\"mc_index\":0,\"addr\":7860269056,\"grain\":128}"
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `tracer_data.dev` | 设备或部件标识，如 `CPU/MEM`、`MEM`、`ACPI`、`PCIe 0000:03:00.0` |
| `tracer_data.event` | 错误来源，见[采集范围](/docs/key-feature/hardware-events_zh.md#采集范围) |
| `tracer_data.type` | 归一化后的严重级别，见下表 |
| `tracer_data.info` | 包含详细字段的 JSON **字符串**，需再次解析；结构取决于事件来源 |
| `observed_timestamp` | 用户态接收事件的时间 |
| `kernel_observed_timestamp` | 内核观测时间转换后的时间戳 |

### 严重级别

严重级别用于分类，不等同于恢复成功、业务无影响或必须重启的判断。

| `type` | 含义 | 当前映射 |
| --- | --- | --- |
| `Corrected` | 已纠正 | MCE 无 UC/DEFERRED 标志、EDAC 0、ACPI 1、AER 2、THR |
| `UncorrectedRecoverable` | 未纠正、可恢复类别 | MCE UC、EDAC 1、ACPI 2、AER 0 |
| `UncorrectedDeferred` | 未纠正、延迟处理类别 | MCE DEFERRED、EDAC 2 |
| `UncorrectedFatal` | 未纠正、致命类别 | EDAC 3、ACPI 3、AER 1 |
| `Info` | 信息记录 | EDAC 4、ACPI 0 |
| `unknown` | 无法映射严重级别 | 未识别的原始值或不携带严重级别的来源 |

MCE 同时设置 DEFERRED 和 UC 时，优先归为 `UncorrectedDeferred`。

## 事件详情

以下字段位于解析后的 `tracer_data.info` 中。

### MCE

寄存器与 Bank 的含义依 CPU 型号而异，诊断时需结合对应处理器手册。

| 字段 | 说明 |
| --- | --- |
| `mcg_cpu_cap` / `mcg_msr_status` | 全局能力寄存器 `MCG_CAP` 与状态寄存器 `MCG_STATUS` |
| `banks_msr_status` | Bank 状态寄存器 `MCi_STATUS`，包含错误代码及 UC、DEFERRED、ADDRV、MISCV 等标志 |
| `banks_msr_addr` | `MCi_ADDR` 错误地址；仅在 ADDRV 有效时解读 |
| `banks_msr_misc` | `MCi_MISC` 补充信息；仅在 MISCV 有效时解读 |
| `mca_synd_msr` / `mca_ipid_msr` | AMD 综合征与实例 ID 寄存器 |
| `instr_pointer` / `code_seg` | 指令指针与代码段；指令指针的有效性需结合 `MCG_STATUS` |
| `tsc_timestamp` | CPU 时间戳计数器值，不能直接作为 Unix 时间戳 |
| `walltime` | Unix 时间戳，单位为秒 |
| `cpu` / `cpuid` / `apicid` / `socketid` | 逻辑 CPU、CPUID、APIC ID 和 CPU 插槽编号 |
| `bank` | Machine Check Bank 编号；部件映射依平台而异 |
| `cpuvendor` | 内核记录的 CPU 厂商编码 |

### MCE 阈值中断

| 字段 | 说明 |
| --- | --- |
| `vector` | 中断向量 |
| `cpu` | 逻辑 CPU 编号 |

### EDAC

| 字段 | 说明 |
| --- | --- |
| `err_count` | 本条事件报告的错误次数 |
| `err_type` / `err_msg` | 归一化严重级别与错误描述 |
| `label` | 驱动报告的内存位置标签；实际插槽位置需结合机器拓扑确认 |
| `mc_index` | 内存控制器编号，从 0 开始 |
| `top_layer` / `mid_layer` / `low_layer` | 驱动定义的内存拓扑层次索引；`-1` 表示未指定 |
| `addr` | 内核报告的错误地址 |
| `grain` | 地址粒度，单位为字节 |
| `syndrome` | ECC 综合征值 |
| `driver` | 驱动提供的补充错误信息，不一定是驱动名称 |

### ACPI GHES

| 字段 | 说明 |
| --- | --- |
| `severity` | 原始严重级别值，当前映射见严重级别表 |
| `sec_type` | 错误段类型 GUID，表示为 16 字节十六进制字符串 |
| `fru_id` / `fru_text` | 可更换部件标识及描述，是否提供取决于固件 |
| `data_len` | 报告的数据长度，单位为字节 |
| `raw_data` | 十六进制转储；需结合平台和厂商格式解读 |

### PCIe AER

| 字段 | 说明 |
| --- | --- |
| `dev_name` | PCIe BDF 地址，如 `0000:03:00.0`（Domain:Bus:Device.Function） |
| `err_type` | `Corrected`、`UncorrectedRecoverable`、`UncorrectedFatal` 或 `unknown` |
| `err_reason` | AER 状态值对应的错误名称；当前按单一状态值匹配，未匹配时为 `unknown` |
| `tlp_header` | TLP 头部的四个十六进制字；头部不可用时为 `not available` |

错误名称描述上报的现象，不能直接推断为设备损坏。以下列出当前解析器识别的状态值。

#### 可纠正错误

| 状态值 | `err_reason` |
| --- | --- |
| `0x00000001` | Receiver Error |
| `0x00000040` | Bad TLP |
| `0x00000080` | Bad DLLP |
| `0x00000100` | REPLAY_NUM Rollover |
| `0x00001000` | Replay Timer Timeout |
| `0x00002000` | Advisory Non-Fatal Error |
| `0x00004000` | Corrected Internal Error |
| `0x00008000` | Header Log Overflow |

#### 不可纠正错误

| 状态值 | `err_reason` |
| --- | --- |
| `0x00000001` | Undefined |
| `0x00000010` | Data Link Protocol Error |
| `0x00000020` | Surprise Down Error |
| `0x00001000` | Poisoned TLP |
| `0x00002000` | Flow Control Protocol Error |
| `0x00004000` | Completion Timeout |
| `0x00008000` | Completer Abort |
| `0x00010000` | Unexpected Completion |
| `0x00020000` | Receiver Overflow |
| `0x00040000` | Malformed TLP |
| `0x00080000` | ECRC Error |
| `0x00100000` | Unsupported Request Error |
| `0x00200000` | ACS Violation |
| `0x00400000` | Uncorrectable Internal Error |
| `0x00800000` | MC Blocked TLP |
| `0x01000000` | AtomicOp Egress Blocked |
| `0x02000000` | TLP Prefix Blocked Error |

### ARM GHES

`ARM_GHES` 记录的严重级别为 `unknown`；平台可用性限制见[采集范围](/docs/key-feature/hardware-events_zh.md#采集范围)。

| 字段 | 说明 |
| --- | --- |
| `mpidr` / `midr` | 十六进制处理器标识；`mpidr=0x0` 也可能表示内核未提供该值 |
| `running_state` | `running`、`stopped` 或 `N/A` |
| `psci_state` | 停止状态下的十六进制 PSCI 状态；不适用时为 `N/A` |
| `affinity_level` | 十进制亲和层级；未提供时为 `N/A` |
