---
title: Event Diagnostics
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

## Overview

Event diagnostics records scheduling, memory, and network anomalies reported by the Linux kernel, along with process, container, and call stack context for later investigation. Collection primarily uses eBPF probes; network interface state changes are obtained through netlink.

| Problem | Events or observers |
| --- | --- |
| Scheduling stalls, soft lockups, or prolonged task blocking | `sched_tick`, `softlockup`, `hungtask` |
| OOM or blocking during direct memory reclaim | `memory_oom_kill`, `memory_reclaim_events` |
| Packet drops in the network stack or receive latency | `dropwatch`, `net_rx_latency` |
| Network interface state, LACP, or transmit queue timeouts | `netdev_events`, `netdev_bonding_lacp`, `netdev_txqueue_timeout` |
| TCP retransmissions | See [tcpshark](/docs/best-practice/tcpshark_en.md) |
| CPU, memory, or PCIe hardware errors | See [Hardware Error Diagnostics](/docs/key-feature/hardware-events_en.md) |

Events provide diagnostic clues but cannot independently establish a root cause. Probe support, filtering, and rate limits affect coverage; insufficient buffer space can also cause event loss.

## Configuration

Set thresholds and filters under `EventTracing` in `huatuo-bamai.conf`. For all fields, defaults, and units, see “Event tracing” in [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md). The global blacklist controls whether observers are enabled.

## Event records

Events are written to Elasticsearch or local files according to the storage configuration. Filter by `tracer_type=event`, event name, host or container, and time range to inspect process, call stack, and device information. Container fields are provided only when association succeeds.

For full JSON examples, field units, and compatibility notes, see [Event Fields](/docs/reference/instant-observability_en.md).

## CPU scheduling

### Scheduler tick interval

Observer: `sched_tick`.

When the interval between consecutive scheduler ticks reaches the threshold, the observer records the process and kernel stack. This helps investigate prolonged interrupt disabling, CPU stalls, or virtualization scheduling delays. Set the threshold above the normal tick period; normal NO_HZ tick suppression is filtered out.

The record captures the first tick after the delay ends. The process and call stack are not necessarily the cause of the delay. High CPU load or a softirq backlog does not itself imply tick delay.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#scheduling-tick-intervals).

### Soft lockups

Observer: `softlockup`.

The observer records process, CPU, and NMI backtrace information when the kernel reports a softlockup. This information alone cannot identify the process responsible. The kernel watchdog configuration determines the trigger timing.

The reporting interval for repeated events increases from 10 minutes to a maximum of 3 hours. Event counts continue to accumulate during this interval.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#soft-lockups).

### Blocked tasks

Observer: `hungtask`.

The observer detects system hungtask events and collects kernel stacks of processes in D state (uninterruptible sleep), along with CPU backtraces.

The reporting interval for repeated events increases from 10 minutes to a maximum of 3 hours, while event counts continue to accumulate. The observer is not registered if the kernel does not expose `hung_task_timeout_secs`. Setting this value to `0` disables kernel hungtask detection.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#hung-tasks).

## Memory

### OOM kills

Observer: `memory_oom_kill`.

The observer records the process that triggers the OOM Killer (`trigger`), the terminated process (`victim`), and available container, cgroup, and host memory information. The triggering process is not necessarily the main memory consumer; examine memory usage and limits together.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#oom-kills).

### Direct reclaim

Observer: `memory_reclaim_events`.

The observer records the duration of a single direct memory reclaim operation by a container thread. When `try_to_free_pages` returns, it reports process, thread, and container information if the call exceeded the threshold, which defaults to 900 milliseconds. Events not associated with a container are filtered out.

Use the registered name `memory_reclaim_events` in the blacklist. Saved records use `memory_reclaim` as `tracer_name`.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#direct-reclaim).

## Network

### Network stack packet drops

Observer: `dropwatch`.

The observer detects packet drops in the kernel network stack and records the kernel call stack, network five-tuple, TCP state, and related information at the time of the drop. An optional call stack filter can suppress noise patterns verified on the local host.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#protocol-stack-drops).

### Receive latency

Observer: `net_rx_latency`.

The observer measures TCP packet latency from the driver receive point to `__netif_receive_skb`, `tcp_v4_rcv`, and the user-space copy point. All three values start at the driver; they are not durations between adjacent observation points. Default thresholds are 5, 10, and 115 milliseconds, respectively. When a threshold is reached, the observer records endpoints, TCP state, sequence numbers, and latency values.

By default, the host network namespace is excluded and only container networks are observed.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#receive-latency).

### Network interface state

Observer: `netdev_events`.

The observer subscribes to kernel netlink RTM_NEWLINK messages to detect changes in interface link state flags, such as AdminDown and CarrierDown. It records the interface name, link state, MAC address, and driver information.

At startup, the observer scans the current state of interfaces configured in `EventTracing.Netdev.DeviceList` to establish a baseline. It reports only subsequent state changes.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#nic-state).

### LACP state

Observer: `netdev_bonding_lacp`.

The observer is enabled only when an IEEE 802.3ad bonding interface exists. On LACP state changes, it records the bonding interface mode, link state, and negotiation information to help investigate link aggregation anomalies.

Use the registered name `netdev_bonding_lacp` in the blacklist and `tracer_name=lacp` when querying records.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#lacp-state).

### Transmit queue timeouts

Observer: `netdev_txqueue_timeout`.

The observer detects network interface transmit queue timeouts and records the affected queue index, device name, and driver name. Use these records to investigate device, driver, or link problems on the transmit path.

For field details, see [Event Fields](/docs/reference/instant-observability_en.md#transmit-queue-timeouts).
