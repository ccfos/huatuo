---
title: Hardware Errors
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

### Architecture

The huatuo supports detection of various hardware faults, including:

- CPU, L1/L2/L3 Cache, TLB
- Memory, ECC
- PCIe
- Network Interface Card Link
- PFC / RDMA
- ACPI
- GPU MetaX

Overall Architecture of HUATUO

![](/docs/img/hardware-errors-huatuo-framework.png)

The huatuo is built on Linux kernel MCE (Machine Check Exception) and RAS (Reliability, Availability, and Serviceability) mechanisms. It uses eBPF to capture critical hardware events and retrieve device information.
The Linux kernel RAS framework has been continuously evolving since kernel 2.6, gradually adding more tracepoints. This lightweight, event-driven approach covers most high-frequency hardware fault scenarios. In addition, HUATUO supports monitoring of PFC/RDMA congestion as well as physical link status of network interfaces.

![](/docs/img/hardware-errors-ras.jpg)

### Hardware Event Metrics

The huatuo can capture hardware events through eBPF and obtain key information such as:

- fault type
- device id
- error details
- timestamp
- and other related data

#### Network Link Faults
Fault information is stored both locally on the server where the HUATUO component is deployed (under huatuo-local/netdev_event) and in the configured Elasticsearch service.

The local file data format:
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

linkstatus possible values:
```bash
linkstatus_adminup — Administrator enabled the interface (e.g. ip link set dev eth0 up)
linkstatus_admindown — Administrator disabled the interface (e.g. ip link set dev eth0 down)
linkstatus_carrierup — Physical link restored
linkstatus_carrierdown — Physical link failure
```

NIC Packet Loss:

```bash
huatuo_bamai_buddyinfo_blocks{host="hostname",region="xxx",device="eth0",driver="ixgbe"} 0
```

NIC RDMA PFC Congestion Metrics:
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

Linux Kernel RAS Hardware Fault:

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
