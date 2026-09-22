---
title: CPU Frequency Policy Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

`cpufreq` 暴露 CPU 调频策略，便于将异常的频率上限或 governor 设置与 CPU 饱和、
业务延迟关联分析。多个 CPU 共享的策略只读取和导出一次。

指标前缀为 `huatuo_bamai_cpufreq_`，类型均为 gauge，标签为 `host`、`region`、
`policy`（数字策略 ID，不一定等于 CPU ID）。

| 后缀 | 含义 |
| --- | --- |
| `scaling_current_hertz` | 调频驱动报告的当前频率 |
| `scaling_minimum_hertz` | 策略允许的最低频率 |
| `scaling_maximum_hertz` | 策略允许的最高频率 |
| `hardware_minimum_hertz` | 硬件支持的最低频率 |
| `hardware_maximum_hertz` | 硬件支持的最高频率 |
| `bios_limit_hertz` | 固件频率上限，若内核提供 |
| `info` | 恒为 1，附加 `governor`、`driver` 标签 |

```promql
# 策略上限低于硬件最高频率
huatuo_bamai_cpufreq_scaling_maximum_hertz
< huatuo_bamai_cpufreq_hardware_maximum_hertz
```

较低的上限可能是有意配置，应结合 governor 和业务负载判断。
`scaling_current_hertz` 可能表示请求频率，不一定等于瞬时物理时钟，详见
[内核调频说明](https://docs.kernel.org/admin-guide/pm/cpufreq.html)。

默认启用，可在 `BlackList` 中加入 `cpufreq` 关闭。每次采集重新发现
`/sys/devices/system/cpu/cpufreq/policy*`，兼容 CPU 热插拔。
缺失属性不产生零值；不支持 CPUFreq 的主机不产生指标。
读取失败时保留其余有效指标并报告采集错误。每个策略最多读取八个小文件、产生七条时间序列；
实际读取延迟取决于调频驱动。
