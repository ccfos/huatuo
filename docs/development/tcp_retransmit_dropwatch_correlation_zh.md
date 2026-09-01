---
title: TCP retransmit 与 dropwatch 关联的难点
type: docs
author: HUATUO Team
date: 2026-08-21
weight: 7
---

本文说明 local correlation 的证据边界与实现。两条事件流没有共同事件 ID，
关联只能根据 namespace、四元组、TCP sequence/ACK 和单调时间作保守推断。

## 1. 结果语义

| 结果 | 含义 |
| --- | --- |
| `host_software` | 找到满足全部严格条件且尚未被消费的 host software drop。 |
| `unknown` | 没有严格正向证据；`correlation_reasons` 说明限制。 |

no-match 不能证明问题位于网络或硬件。drop 可能发生在采集启动前、另一个
network namespace，或记录虽送达但缺少 TCP 匹配字段。

shutdown 时仍在等待的 retransmit 不形成关联结果，而是保持关联字段为空并原样
交付；它既不是 `host_software`，也不会被改写为 `unknown`。

## 2. 三种运行场景

1. `tcpshark --bpf-path <tcp_retransmit.o>` 只采集并直接输出重传。
2. `tcpshark --with-dropwatch --bpf-path-dir <dir>` 在一个进程内加载
   `tcp_retransmit.o` 与 `dropwatch.o`，统一持有两条 perf 输入、timer、输出和关闭。
3. huatuo-bamai 的 standalone dropwatch 仍由 `cmd/dropwatch` 独立运行，只输出
   raw `DropWatchTracing`，与 embedded source 不共享状态。

三种 retransmit hook 在两种模式下都使用 synthetic L3 filter。关联模式只使用
`EventTracing.TCPRetransmit.Filter`；空值规范化为 `tcp`，同一表达式传给两个
BPF 对象。依赖 Ethernet 地址、无法在 synthetic L3 输入上等价执行的表达式会在
启动前被拒绝。`EventTracing.Dropwatch.Filter` 只控制 standalone dropwatch。

## 3. 两条时间约束

dropwatch 与 retransmit 使用不同 perf reader，事件还可能来自不同 CPU，因此
用户态接收顺序不等于内核发生顺序：

```text
CPU 2: drop       ktime=180，暂留在 dropwatch ring
CPU 0: retransmit ktime=200，先到达用户态
用户态: retransmit(200) -> drop(180)
```

实现使用两个不同维度的窗口：

```go
retransmitDropWaitDuration = 100 * time.Millisecond
maxDropToRetransmitAge     = time.Second
```

- 100ms 是用户态送达乱序预算。retransmit 先到时进入等待队列，到期仍未匹配则
  输出 `unknown`。处理任一新事件前先结算已经到期的记录；timer 只负责唤醒，
  不决定 deadline 语义。
- 1s 是因果候选年龄，使用 BPF `ktime_ns` 判断。候选必须满足：

```text
drop.ktime_ns <= retransmit.ktime_ns
retransmit.ktime_ns - drop.ktime_ns <= 1s
```

`observed_timestamp` 是用户态墙上时间，受调度和系统时间调整影响，不参与匹配。
固定 1s 是当前支持合同，不等于 Linux 所有 RTO 的理论上限；窗口外结果保持
`unknown`。

## 4. 有界状态

等待中的 retransmit 使用：

```go
type retransmitWaitQueue struct {
    capacity   int
    byDeadline list.List
    byFlow     map[flowKey][]*waitingRetransmit
}
```

`byDeadline.Front()` 是最早到期记录，`byFlow` 只扫描相关正向或反向 flow。
容量为 1024，满时最早 deadline 的记录以
`retransmit_wait_capacity_exceeded` 定案。

尚未匹配的 drop 使用：

```go
type dropwatchCandidates struct {
    capacity int
    byAge    list.List
    byFlow   map[flowKey][]*dropCandidate
}
```

`byAge` 按用户态接收顺序支持 O(1) 容量淘汰和过期清理；`byFlow` 定位候选。
容量为 4096。容量顺序不用于因果判断，因果判断始终使用 `ktime_ns`。

## 5. 严格匹配

正向 segment 候选要求：

- 相同 network namespace、地址族和方向四元组；
- drop 不晚于 retransmit，且时间差不超过 1s；
- TCP SYN/data/FIN sequence range 与重传 range 重叠；
- RST 和不受支持的重传类型不产生正向匹配。

反向 ACK 候选使用反向四元组，并要求 ACK 覆盖重传 sequence end。SYN 与
SYN-ACK 使用各自更严格的 ACK/SYN 条件。

多个候选先选最大的 `drop.ktime_ns`；时间相同时选较新的内部 ID。严格匹配后
立即从 age 和 flow 两个索引删除，只能消费一次。除 namespace 外均满足的候选
只记录 `cross_netns_candidate`，不会输出 `host_software`。

## 6. Unknown 原因

原因可以同时出现：

| 原因 | 含义 |
| --- | --- |
| `no_matching_drop` | 100ms 到期时没有严格候选。 |
| `startup_history_incomplete` | 重传距离 embedded source ready 不足 1s，或早于 ready。 |
| `cross_netns_candidate` | tuple、时间和 sequence 匹配，但 namespace 不同。 |
| `perf_events_lost` | embedded dropwatch 无法把部分事件写入 perf。 |
| `drop_rate_limited` | embedded dropwatch limiter 拒绝了部分事件。 |
| `retransmit_wait_capacity_exceeded` | 重传等待队列已满。 |
| `unsupported_retransmission` | 重传缺少严格匹配所需字段或类型。 |
| `dropwatch_perf_status_unavailable` | no-match 输出前无法读取最新 perf 状态。 |

无法规范化的 drop 与候选容量淘汰不再按重传维护 evidence 区间，也不产生专用
reason；没有找到严格匹配时仍以 `no_matching_drop` 输出 `unknown`。
公开 Go 常量 `CorrelationReasonDropwatchInputInactive` 仅为 source compatibility
保留并标记为 deprecated，tcpshark 不再生成 `dropwatch_input_inactive`。

## 7. Perf 状态

BPF 只保留一个累计 per-CPU `dropwatch_perf_stats` map：

```text
perf_lost
rate_limited
```

用户态汇总所有 CPU，并拒绝计数回退或加法溢出。该状态只说明证据完整性，
不会把 no-match 提升为确定性网络分类。旧的 active epoch、双 slot、inflight、
frontier 和 `DrainedThroughKtimeNS` 已删除。

关联侧根据 IPv4 total length 或 IPv6 payload length 计算 TCP sequence span。
GSO/offload 下 IP header 长度可能无法覆盖完整 skb；当前不据此扩大匹配范围。

dropwatch 热路径为：

```text
software: hardware marker lookup/delete -> device/pcap filter
          -> status map lookup -> bpf_ktime_get_ns() -> rate limit -> perf output
hardware: device/pcap filter -> status map lookup -> bpf_ktime_get_ns()
          -> rate limit -> perf output
```

因此 filter 拒绝的事件不支付状态 lookup、事件时间 helper 或计数更新成本；
software drop 会先消费可能存在的 hardware dedup marker，避免被拒绝的 kfree
路径遗留 marker。

## 8. Shutdown

`tcpshark` 使用一个 `errgroup.WithContext` 管理 rate-limit reader、retransmit
reader、embedded dropwatch reader 和关联循环。所有 worker 共享同一个
`groupCtx`；任一 worker 返回错误后取消其余 worker，`Wait` 返回首个 worker
错误。资源关闭错误由各 owner 使用 `errors.Join` 合并，不再为 shutdown 维护私有
错误聚合 group。

正常结束或 worker 失败时：

1. 取消共享 `groupCtx`；
2. 两个 `ReadInto` reader 和关联循环退出；
3. 不再读取 dropwatch perf ring 中尚未交给关联器的记录；
4. 按 deadline 顺序取出 waiting retransmit，并直接写入 output；
5. 等全部 worker 退出后，依次 detach embedded source、关闭 reader 和 object；
6. 最后关闭 output。

shutdown pending 不调用正常 no-match 定型路径，不读取 perf 状态，也不附加
`drop_location`、`correlation_reasons`、`dropwatch_perf_status` 或 `drop_stack`。
因此尾部 drop 可能丢失，原本可以匹配的重传也会以未附加关联结论的原始事件
交付。这是关闭边界上明确接受的取舍。

## 9. 文件职责

```text
cmd/tcpshark/
├── trace.go                         # 基础模式分流与 output owner
├── bpf_load.go                      # 两个 filtered BPF object 的共享加载
├── retransmit_drop_trace.go         # 双流事件循环、timer、shutdown
├── retransmit_drop_bpf.go           # embedded dropwatch source 与 perf 状态
├── retransmit_drop_correlator.go    # 双流协调与定案
├── retransmit_drop_event.go         # 匹配领域类型及输入规范化
├── retransmit_drop_event_cache.go   # drop 候选与待匹配重传缓存
├── retransmit_drop_record.go        # 两种 BPF ABI record 转换
├── tcp_retransmit_classify.go       # 与 dropwatch 无关的 TCP 分类
└── format.go                        # text/JSON 输出
```

实现位于唯一生产调用方 `cmd/tcpshark`；不再通过 `internal/netcorrelate` 暴露
一个仓库内没有第二个使用者的抽象。
