# 硬件根因模式库

## ras

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| MCE 可纠正错误 | event=MCE + type=Corrected | 内存 ECC 偶发错误，关注趋势 |
| MCE 不可纠正 | event=MCE + type=Uncorrected* | 内存条故障，需更换 |
| EDAC 错误 | event=EDAC | 内存控制器报告多比特错误 |
| PCIe AER | event=AER | PCIe 设备/链路错误（网卡/GPU/NVMe） |
| ACPI/GHES | event=NON_STANDARD | 固件报告的硬件错误 |
| MCE Threshold | event=MCE_THRESHOLD | MCE 阈值中断风暴，可能预示硬件即将故障 |

## lacp

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| 单侧链路 down | content 中 slave 状态 down | 物理链路/网卡故障 |
| LACP 协商失败 | content 中 state 反复变化 | 交换机配置不匹配/对端不支持 LACP |
| 全部 slave down | content 中所有 slave down | 交换机端口故障/线缆问题 |

## netdev_events

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| 链路抖动 | linkstatus 频繁 up/down | 网线松动/光模块故障/交换机端口问题 |
| 永久 down | linkstatus=down 不恢复 | 网卡硬件故障/线缆断开 |
| 协商降速 | link up 但速率下降 | 自动协商失败/线缆质量差 |
