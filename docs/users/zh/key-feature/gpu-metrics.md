---
title: GPU 指标说明
type: docs
description:
author: HUATUO Team
date: 2026-02-25
weight: 4
---

当前版本支持的 GPU 平台:
- MetaX

|子系统|指标|描述|单位|统计纬度|指标来源|
|---|----|---|---|---|---|
|gpu|metax_gpu_sdk_info|GPU SDK 信息|-|version|sml.GetSDKVersion|
|gpu|metax_gpu_driver_info|GPU 驱动信息|-|version|sml.GetGPUVersion with driver unit|
|gpu|metax_gpu_info|GPU 基本信息|-|gpu, model, uuid, bios_version, bdf, mode, die_count|sml.GetGPUInfo|
|gpu|metax_gpu_board_power_watts|GPU 板级功耗|瓦特（W）|gpu|sml.ListGPUBoardWayElectricInfos|
|gpu|metax_gpu_pcie_link_speed_gt_per_second|GPU PCIe 当前链路速率|千兆次传输每秒（GT/s）|gpu|sml.GetGPUPcieLinkInfo|
|gpu|metax_gpu_pcie_link_width_lanes|GPU PCIe 当前链路宽度|链路宽度（通道数）|gpu|sml.GetGPUPcieLinkInfo|
|gpu|metax_gpu_pcie_receive_bytes_per_second|GPU PCIe 接收吞吐率|字节数/秒|gpu|sml.GetGPUPcieThroughputInfo|
|gpu|metax_gpu_pcie_transmit_bytes_per_second|GPU PCIe 发送吞吐率|字节数/秒|gpu|sml.GetGPUPcieThroughputInfo|
|gpu|metax_gpu_metaxlink_link_speed_gt_per_second|GPU MetaXLink 当前链路速率|千兆次传输每秒（GT/s）|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|gpu|metax_gpu_metaxlink_link_width_lanes|GPU MetaXLink 当前链路宽度|链路宽度（通道数）|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|gpu|metax_gpu_metaxlink_receive_bytes_per_second|GPU MetaXLink 接收吞吐率|字节数/秒|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|gpu|metax_gpu_metaxlink_transmit_bytes_per_second|GPU MetaXLink 发送吞吐率|字节数/秒|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|gpu|metax_gpu_metaxlink_receive_bytes_total|GPU MetaXLink 接收数据总量|字节数|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|gpu|metax_gpu_metaxlink_transmit_bytes_total|GPU MetaXLink 发送数据总量|字节数|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|gpu|metax_gpu_metaxlink_aer_errors_total|GPU MetaXLink AER 错误次数|计数|gpu, metaxlink, error_type|sml.ListGPUMetaXLinkAerErrorsInfos|
|gpu|metax_gpu_status|GPU 状态（0 表示正常，其它值表示异常，具体含义需参考文档）|-|gpu, die|sml.GetDieStatus|
|gpu|metax_gpu_temperature_celsius|GPU 温度|摄氏度|gpu, die|sml.GetDieTemperature|
|gpu|metax_gpu_utilization_percent|GPU 利用率（0–100）|%|gpu, die, ip|sml.GetDieUtilization|
|gpu|metax_gpu_memory_total_bytes|显存总容量|字节数|gpu, die|sml.GetDieMemoryInfo|
|gpu|metax_gpu_memory_used_bytes|已使用显存容量|字节数|gpu, die|sml.GetDieMemoryInfo|
|gpu|metax_gpu_clock_mhz|GPU 时钟频率|兆赫兹（MHz）|gpu, die, ip|sml.ListDieClocks|
|gpu|metax_gpu_clocks_throttling|GPU 时钟降频原因|-|gpu, die, reason|sml.GetDieClocksThrottleStatus|
|gpu|metax_gpu_dpm_performance_level|GPU DPM 性能等级|-|gpu, die, ip|sml.GetDieDPMPerformanceLevel|
|gpu|metax_gpu_ecc_memory_errors_total|GPU ECC 内存错误次数|计数|gpu, die, memory_type, error_type|sml.GetDieECCMemoryInfo|
|gpu|metax_gpu_ecc_memory_retired_pages_total|GPU ECC 内存退役页数|计数|gpu, die|sml.GetDieECCMemoryInfo|
