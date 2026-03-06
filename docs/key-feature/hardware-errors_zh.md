---
title: 硬件故障
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

### 架构介绍

HUATUO（华佗）支持各种硬件故障检查：
- CPU, L1/L2/L3 Cache, TLB
- Memory, ECC
- PCIe
- Network Interface Card Link 
- PFC/RDMA
- ACPI
- GPU MetaX


HUATUO（华佗）总体架构如下：

![](/docs/img/hardware-errors-huatuo-framework.png)

HUATUO 基于 Linux 内核 MCE 和 RAS 技术，通过 eBPF 捕获关键硬件事件，获取硬件设备信息。RAS 在 Linux 内核一直在不断演进发展，从内核 2.6 版本开始逐步的引入更多 tracepoint 点。这种轻量级，事件驱动的实现方式能够覆盖绝大多数高频硬件故障场景。此外 HUATUO 还支持 PFC/RDMA，网卡物理链路状态的检查。

![](/docs/img/hardware-errors-ras.jpg)

### 硬件指标事件

HUATUO 通过事件触发实时感知各硬件模块上报的故障信息：故障类型，设备标识，错误信息，时间戳等。

网卡故障，该故障信息被存储在部署华佗组件的服务器，huatuo-local/netdev_event，以及配置的 Elasticsearch 存储服务。其中本地存储的信息格式如下：
```bash
{
    "hostname": "your-host-name",
    "region": "xxx",
    "uploaded_time": "2026-03-05T18:28:39.153438921+08:00",
    "time": "2026-03-05 18:28:39.153 +0800",
    "tracer_name": "netdev_event",
    "tracer_time": "2026-03-05 18:28:39.153 +0800",
    "tracer_type": "auto",
    "tracer_data": {
        "ifname": "eth0",
        "index": 2,
        "linkstatus": "linkstatus_admindown",
        "mac": "5c:6f:11:11:11:11",
        "start": false
    }
}
```

linkstatus 数值类型还可能为：
```bash
linkstatus_adminup 管理员开启网卡，例如通过 ip link set dev eth0 up
linkstatus_admindown 管理员关闭网卡，例如通过 ip link set dev eth0 down
linkstatus_carrierup 物理链路恢复
linkstatus_carrierdown 物理链路故障
```

网卡故障，硬件丢包指标：
```bash
huatuo_bamai_buddyinfo_blocks{host="hostname",region="xxx",device="eth0",driver="ixgbe"} 0
```

网卡 RDMA PFC 网络拥塞：
```bash
# HELP huatuo_bamai_netdev_dcb_pfc_received_total count of the received pfc frames
# TYPE huatuo_bamai_netdev_dcb_pfc_received_total counter
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="0",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="1",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="2",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="3",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="4",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="5",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="6",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_received_total{device="enp6s0f0np0",host="hostname",prio="7",region="xxx"} 0
# HELP huatuo_bamai_netdev_dcb_pfc_send_total count of the sent pfc frames
# TYPE huatuo_bamai_netdev_dcb_pfc_send_total counter
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="0",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="1",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="2",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="3",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="4",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="5",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="6",region="xxx"} 0
huatuo_bamai_netdev_dcb_pfc_send_total{device="enp6s0f0np0",host="hostname",prio="7",region="xxx"} 0
```

Linux 内核 RAS 硬件故障指标：
```bash
huatuo_bamai_ras_hw_total{host="hostname",region="xxx"} 0
```

```bash
{
    "hostname": "your-host-name",
    "region": "nmg02",
    "uploaded_time": "2026-03-01T15:41:13.027353585+08:00",
    "time": "2026-03-01 15:41:13.027 +0800",
    "tracer_name": "ras",
    "tracer_time": "2026-03-01 15:41:13.027 +0800",
    "tracer_type": "auto",
    "tracer_data": {
        "dev": "MEM",
        "event": "EDAC",
        "type": "CORRECTED",
        "timestamp": 26870134986481080,
        "info": "1 CORRECTED err: memory read error on CPU_SrcID#0_MC#1_Chan#0_DIMM#0 (mc: 1 location:0:0:-1 address: 0x3ddc84140 grain:32 syndrome:0x0  err_code:0x0101:0x0090 ProcessorSocketId:0x0 MemoryControllerId:0x1 PhysicalRankId:0x0 Row:0x15da Column:0x100 Bank:0x3 BankGroup:0x1 retry_rd_err_log[0001a209 00000000 00800000 0440d001 000015da] correrrcnt[0001 0000 0000 0000 0000 0000 0000 0000])"
    }
}
```
