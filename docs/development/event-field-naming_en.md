---
title: Event Field Naming
type: docs
author: HUATUO Team
date: 2026-08-19
weight: 31
---

Event fields must use consistent semantics before following the naming
conventions of each layer. The same name must not represent different concepts.

## Layer Rules

| Layer | Rule | Example |
| --- | --- | --- |
| BPF C | Names reflect the original kernel semantics and units | `tgid`, `kernel_observed_ns` |
| Go | MixedCaps; initialisms remain uppercase | `TGID`, `KernelObservedNS` |
| JSON | Use user-facing statistical semantics | `pid`, `observed_timestamp` |
| CLI flags | Lowercase kebab case | `--pid`, `--cpuid` |
| CLI text and headers | Use standard uppercase initialisms | `PID`, `CPU`, `COMM` |

## CPUs and Tasks

| Concept | Definition | BPF C | Go | JSON | CLI |
| --- | --- | --- | --- | --- | --- |
| Logical CPU number | Number returned by `bpf_get_smp_processor_id()` | `cpu` | `CPU` | `cpu` | `--cpu`, `CPU` |
| Process ID | Userspace process identifier, namely the Linux TGID | `tgid` | `TGID` in raw events; `PID` in output | `pid` | `--pid`, `PID` |
| Thread ID | Linux task PID | `tid` | `TID` | `tid` | `--tid`, `TID` |
| Parent process ID | Parent process TGID | `parent_tgid` | `ParentTGID`, or `ParentPID` in the output layer | `parent_pid` | `PPID` |
| Task comm | Kernel task `comm`, which may be a thread name | `comm` | `Comm` | `comm` | `COMM` |

## Time and Numeric Units

| Concept | Definition | BPF C | Go | JSON |
| --- | --- | --- | --- | --- |
| Raw kernel observation time | `CLOCK_MONOTONIC` nanoseconds, excluding system suspend | `kernel_observed_ns` | `KernelObservedNS` | Internal correlation only; not serialized |
| Kernel observation time | Raw monotonic timestamp converted to UTC | Not applicable | `KernelObservedTimestamp` | `kernel_observed_timestamp` |
| Userspace observation time | UTC time when the event producer observes the event in userspace | Not applicable | `ObservedTimestamp` | `observed_timestamp` |
| Unix nanosecond time | Unix time in nanoseconds | `timestamp_ns` | `TimestampNS` | `timestamp_ns` |
| Nanosecond duration | Difference between two time points | `<name>_ns` | `<Name>NS` | `<name>_ns` |
| Nanosecond threshold | Duration corresponding to a trigger condition | `<name>_threshold_ns` | `<Name>ThresholdNS` | `<name>_threshold_ns` |
| Count | Unitless cumulative value | `<name>_count` | `<Name>Count` | `<name>_count` |
| Bytes | Data size, not bit count | `<name>_bytes` | `<Name>Bytes` | `<name>_bytes` |

## Containers, Cgroups, and Network Namespaces

| Concept | BPF C | Go | JSON | CLI |
| --- | --- | --- | --- | --- |
| Container ID | `container_id` | `ContainerID` | `container_id` | `--container-id`, `CONTAINER_ID` |
| Cgroup ID | `cgroup_id` | `CgroupID` | `cgroup_id` | `CGROUP_ID` |
| Cgroup CSS address | `<subsystem>_css_addr` | `<Subsystem>CSSAddr` | `<subsystem>_css_addr` | `<SUBSYSTEM>_CSS_ADDR` |
| Network namespace inode | `netns_inum` | `NetNamespaceInum` | `net_namespace_inum` | `NETNS_INUM` |
| Network namespace cookie | `netns_cookie` | `NetNamespaceCookie` | `net_namespace_cookie` | `NETNS_COOKIE` |
| Network device index | `ifindex` or `<name>_ifindex` | `Ifindex` or `<Name>Ifindex` | `ifindex` or `<name>_ifindex` | `IFINDEX` |

Kernel terms `inum` and `ifindex` remain single words in Go. Do not split them
into `INum` or `IfIndex`. User-facing Go and JSON fields expand `netns` to
`NetNamespace` and `net_namespace`.

## Observation times and compatibility

`kernel_observed_timestamp`, `observed_timestamp`, and `uploaded_timestamp`
represent kernel observation, userspace observation, and storage write time.
Kernel observation means the hook execution time, which may differ from the
onset of a physical hardware error or network fault.

Document stores UTC timestamps at the top level and excludes
`kernel_observed_ns`. Producers without kernel timestamps and older
documents omit `kernel_observed_timestamp`; userspace or upload timestamps must
not fill it. Historical RAS documents used `observed_timestamp` for kernel
observation, so their userspace observation time cannot be reconstructed.

tcpshark and dropwatch use `kernel_observed_timestamp` for kernel observation
time in JSON and text output. Upgrade tools and Agent together. Existing
documents are not rewritten or backfilled. Conversion requires the same host,
boot, and host time namespace. Historical events spanning a realtime clock
step or system suspend cannot be mapped exactly to UTC from monotonic time alone.

UTC conversion caches the monotonic-to-realtime offset. The first call one hour
after the last successful sample refreshes it; ordinary calls do not extend
its lifetime. Sampling failures are returned and retried on the next call.
Realtime clock steps and system suspend do not trigger an early refresh; the
offset is updated on the first call after the cache expires.
