---
title: GPU Metrics
type: docs
description:
author: HUATUO Team
date: 2026-02-25
weight: 4
---

Supported GPU Platforms (Current Version):
- MetaX

|Subsystem|Metric|Description|Unit|Dimensions|Source|
|---|----|---|---|---|---|
|gpu|metax_gpu_sdk_info|GPU SDK info.|-|version|sml.GetSDKVersion|
|gpu|metax_gpu_driver_info|GPU driver info.|-|version|sml.GetGPUVersion with driver unit|
|gpu|metax_gpu_info|GPU info.|-|gpu, model, uuid, bios_version, bdf, mode, die_count|sml.GetGPUInfo|
|gpu|metax_gpu_board_power_watts|GPU board power.|W|gpu|sml.ListGPUBoardWayElectricInfos|
|gpu|metax_gpu_pcie_link_speed_gt_per_second|GPU PCIe current link speed.|GT/s|gpu|sml.GetGPUPcieLinkInfo|
|gpu|metax_gpu_pcie_link_width_lanes|GPU PCIe current link width.|lanes|gpu|sml.GetGPUPcieLinkInfo|
|gpu|metax_gpu_pcie_receive_bytes_per_second|GPU PCIe receive throughput.|B/s|gpu|sml.GetGPUPcieThroughputInfo|
|gpu|metax_gpu_pcie_transmit_bytes_per_second|GPU PCIe transmit throughput.|B/s|gpu|sml.GetGPUPcieThroughputInfo|
|gpu|metax_gpu_metaxlink_link_speed_gt_per_second|GPU MetaXLink current link speed.|GT/s|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|gpu|metax_gpu_metaxlink_link_width_lanes|GPU MetaXLink current link width.|lanes|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|gpu|metax_gpu_metaxlink_receive_bytes_per_second|GPU MetaXLink receive throughput.|B/s|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|gpu|metax_gpu_metaxlink_transmit_bytes_per_second|GPU MetaXLink transmit throughput.|B/s|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|gpu|metax_gpu_metaxlink_receive_bytes_total|GPU MetaXLink receive data size.|bytes|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|gpu|metax_gpu_metaxlink_transmit_bytes_total|GPU MetaXLink transmit data size.|bytes|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|gpu|metax_gpu_metaxlink_aer_errors_total|GPU MetaXLink AER errors count.|count|gpu, metaxlink, error_type|sml.ListGPUMetaXLinkAerErrorsInfos|
|gpu|metax_gpu_status|GPU status, 0 means normal, other values means abnormal. Check the documentation to see the exceptions corresponding to each value.|-|gpu, die|sml.GetDieStatus|
|gpu|metax_gpu_temperature_celsius|GPU temperature.|°C|gpu, die|sml.GetDieTemperature|
|gpu|metax_gpu_utilization_percent|GPU utilization, ranging from 0 to 100.|%|gpu, die, ip|sml.GetDieUtilization|
|gpu|metax_gpu_memory_total_bytes|Total vram.|bytes|gpu, die|sml.GetDieMemoryInfo|
|gpu|metax_gpu_memory_used_bytes|Used vram.|bytes|gpu, die|sml.GetDieMemoryInfo|
|gpu|metax_gpu_clock_mhz|GPU clock.|MHz|gpu, die, ip|sml.ListDieClocks|
|gpu|metax_gpu_clocks_throttling|Reason(s) for GPU clocks throttling.|-|gpu, die, reason|sml.GetDieClocksThrottleStatus|
|gpu|metax_gpu_dpm_performance_level|GPU DPM performance level.|-|gpu, die, ip|sml.GetDieDPMPerformanceLevel|
|gpu|metax_gpu_ecc_memory_errors_total|GPU ECC memory errors count.|count|gpu, die, memory_type, error_type|sml.GetDieECCMemoryInfo|
|gpu|metax_gpu_ecc_memory_retired_pages_total|GPU ECC memory retired pages count.|count|gpu, die|sml.GetDieECCMemoryInfo|
