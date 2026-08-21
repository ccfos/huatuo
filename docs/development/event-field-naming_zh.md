---
title: 事件字段规范
type: docs
author: HUATUO Team
date: 2026-08-19
weight: 31
---

事件字段必须先统一语义，再按各层的惯例命名。相同名称不得表示不同概念。

## 分层规则

| 层 | 规则 | 示例 |
| --- | --- | --- |
| BPF C | 名称体现内核原始语义和单位 | `tgid`、`ktime_ns` |
| Go | MixedCaps；缩写保持全大写 | `TGID`、`KtimeNS` |
| JSON | 使用面向用户的统计语义 | `pid`、`observed_timestamp` |
| CLI 参数 | 小写短横线 | `--pid`、`--cpuid` |
| CLI 文本和表头 | 使用标准大写缩写 | `PID`、`CPU`、`COMM` |

## CPU 和任务

| 概念 | 定义 | BPF C | Go | JSON | CLI |
| --- | --- | --- | --- | --- | --- |
| 逻辑 CPU 编号 | `bpf_get_smp_processor_id()` 返回的编号 | `cpu` | `CPU` | `cpu` | `--cpu`、`CPU` |
| 进程 ID | 用户态进程标识，即 Linux TGID | `tgid` | 原始事件用 `TGID`，输出用 `PID` | `pid` | `--pid`、`PID` |
| 线程 ID | Linux task PID | `tid` | `TID` | `tid` | `--tid`、`TID` |
| 父进程 ID | 父进程 TGID | `parent_tgid` | `ParentTGID` 或输出层 `ParentPID` | `parent_pid` | `PPID` |
| task comm | 内核 task 的 `comm`，可能是线程名 | `comm` | `Comm` | `comm` | `COMM` |

## 时间和数值单位

| 概念 | 定义 | BPF C | Go | JSON |
| --- | --- | --- | --- | --- |
| BPF 单调时间 | `bpf_ktime_get_ns()`| `ktime_ns` | `KtimeNS` | 仅诊断场景使用 `ktime_ns` |
| 观测时间 | 用户态规范化后的 UTC 时间 | 不适用 | `ObservedTimestamp` | `observed_timestamp` |
| Unix 纳秒时间 | Unix 纳秒时间 | `timestamp_ns` | `TimestampNS` | `timestamp_ns` |
| 纳秒时长 | 两个时间点的差值 | `<name>_ns` | `<Name>NS` | `<name>_ns` |
| 纳秒阈值 | 触发条件对应的时长 | `<name>_threshold_ns` | `<Name>ThresholdNS` | `<name>_threshold_ns` |
| 计数 | 无单位的累计值 | `<name>_count` | `<Name>Count` | `<name>_count` |
| 字节数 | 数据大小，不是位数 | `<name>_bytes` | `<Name>Bytes` | `<name>_bytes` |

## 容器、cgroup 和网络命名空间

| 概念 | BPF C | Go | JSON | CLI |
| --- | --- | --- | --- | --- |
| 容器 ID | `container_id` | `ContainerID` | `container_id` | `--container-id`、`CONTAINER_ID` |
| cgroup ID | `cgroup_id` | `CgroupID` | `cgroup_id` | `CGROUP_ID` |
| cgroup CSS 地址 | `<subsystem>_css_addr` | `<Subsystem>CSSAddr` | `<subsystem>_css_addr` | `<SUBSYSTEM>_CSS_ADDR` |
| 网络命名空间 inode | `netns_inum` | `NetNamespaceInum` | `net_namespace_inum` | `NETNS_INUM` |
| 网络命名空间 cookie | `netns_cookie` | `NetNamespaceCookie` | `net_namespace_cookie` | `NETNS_COOKIE` |
| 网络设备索引 | `ifindex` 或 `<name>_ifindex` | `Ifindex` 或 `<Name>Ifindex` | `ifindex` 或 `<name>_ifindex` | `IFINDEX` |

内核术语 `inum` 和 `ifindex` 在 Go 中保持一个单词，不拆成 `INum` 或
`IfIndex`。面向用户的 Go 和 JSON 字段将 `netns` 展开为
`NetNamespace` 和 `net_namespace`。
