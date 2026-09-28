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
| BPF C | 名称体现内核原始语义和单位 | `tgid`、`kernel_observed_ns` |
| Go | MixedCaps；缩写保持全大写 | `TGID`、`KernelObservedNS` |
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
| 内核观测原始时间 | `CLOCK_MONOTONIC` 纳秒读数，不含系统挂起时间 | `kernel_observed_ns` | `KernelObservedNS` | 不序列化，仅用于内部关联 |
| 内核观测时间 | 原始单调时间转换后的 UTC 时间 | 不适用 | `KernelObservedTimestamp` | `kernel_observed_timestamp` |
| 用户态观测时间 | 事件生产者在用户态观测事件的 UTC 时间 | 不适用 | `ObservedTimestamp` | `observed_timestamp` |
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

## 观测时间与兼容性

`kernel_observed_timestamp`、`observed_timestamp` 和 `uploaded_timestamp`
分别表示内核观测、用户态观测和存储写入时刻。内核观测表示 hook 执行时刻，
不保证等同于物理硬件错误或网络故障开始的时刻。

Document 顶层保存 UTC 时间，不保存 `kernel_observed_ns`。
没有内核时间的生产者及旧文档省略 `kernel_observed_timestamp`，不得用
用户态时间或写入时间补填。历史 RAS 文档的 `observed_timestamp` 曾表示
内核观测时刻，不能据此推算历史用户态观测时刻。

tcpshark 和 dropwatch 的 JSON/文本输出使用 `kernel_observed_timestamp` 表示内核观测时间。
工具和 Agent 应一起升级；旧文档保持原样，不自动回填或重写。
单调时钟与 UTC 的转换要求相同主机、同一次启动及主机时间命名空间。
跨越校时跳变或系统挂起的历史事件，仅凭单调时间不能精确恢复 UTC。

UTC 转换缓存单调时钟到实时钟的偏移：距上次成功采样满 1 小时后，由下一次
调用刷新，普通调用不延长缓存期限。采样失败返回错误，下次调用重试。
系统校时跳变或挂起不会提前触发刷新，偏移会在缓存到期后的下一次调用更新。
