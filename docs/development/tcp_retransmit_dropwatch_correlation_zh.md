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
| `software` | 找到满足全部严格条件且尚未被消费的 host software drop。 |
| `hardware` | 找到满足相同严格条件的 devlink DROP trap 记录。 |
| `unknown` | 未找到严格匹配，或匹配记录的来源枚举未知；`correlation_reason` 明确区分已匹配与未匹配终态。 |

no-match 不能证明问题位于网络或硬件。drop 可能发生在采集启动前、另一个
network namespace，或记录虽送达但缺少 TCP 匹配字段。

shutdown 使用同一个时间点处理等待项：已到期项为 `warmup` 或 `wait_timeout`，未到期项为
`interrupted`。这些未匹配结果均输出 `drop_location=unknown` 和独立诊断信息。

## 2. 三种运行场景

1. `tcpshark --bpf-path <tcp_retransmit.o>` 只采集并直接输出重传。
2. `tcpshark --with-dropwatch --bpf-path-dir <dir>` 在一个进程内加载
   `tcp_retransmit.o` 与 `net_dropwatch.o`，统一持有两条 perf 输入、timer、输出和关闭。
3. huatuo-bamai 的 standalone dropwatch 仍由 `cmd/dropwatch` 独立运行，只输出
   raw `DropWatchTracing`，与 embedded source 不共享状态。

三种 retransmit hook 在两种模式下都使用 synthetic L3 filter。关联模式只使用
`EventTracing.TCPRetransmit.Filter`；空值规范化为 `tcp`，同一表达式传给两个
BPF 对象。依赖 Ethernet 地址、无法在 synthetic L3 输入上等价执行的表达式会在
启动前被拒绝。`EventTracing.Dropwatch.Filter` 只控制 standalone dropwatch。

两个命令都使用 `HardwareAuto` 自动检测 devlink trap 支持。tracepoint 不可用时
输出 warning 并继续软件采集；可用时只接收驱动上报的 DROP trap。
`--device` / `--device-excluded` 与独立 dropwatch 同名、互斥，只过滤 embedded
source 的软件及硬件记录，要求 `--with-dropwatch`。重传输入仍使用共同的 L3 filter。

丢包来源和原因由 `internal/dropwatch.ResolveMetadata` 统一解析。软件 reason
从每会话一次加载的 BTF 表查找；未知值为十进制数字，旧内核不支持为
`NOT_SUPPORTED`。硬件 reason 和 group 分别来自 trap 名称与分组，不查软件
reason 表。reader 保留独立的来源和 reason 字符串，不借用可复用的 ABI buffer。
成功匹配后输出 `drop_source`、`drop_reason`、`drop_reason_group`；no-match
省略这些字段，输出唯一关联结束原因和独立诊断。`drop_location` 在匹配时直接使用
`drop_source`，未匹配时为 `unknown`；它表示关联分类，不同于独立 dropwatch
的内核地址。未知来源不根据 reason 或 stack 推断。
硬件记录缺少 namespace 或 TCP 匹配字段时无法建立严格匹配。

## 3. 两条时间约束

dropwatch 与 retransmit 使用不同 perf reader，事件还可能来自不同 CPU，因此
用户态接收顺序不等于内核发生顺序：

```text
CPU 2: drop       monotonic_ns=180，暂留在 dropwatch ring
CPU 0: retransmit monotonic_ns=200，先到达用户态
用户态: retransmit(200) -> drop(180)
```

实现使用两个不同维度的窗口：

```go
retransmitRetentionDuration = 100 * time.Millisecond
maxDropToRetransmitAge     = time.Second
```

- 100ms 是用户态送达乱序预算。retransmit 先到时进入等待队列，到期仍未匹配则
  输出 `unknown`。处理任一新事件前先结算已经到期的记录；timer 只负责唤醒，
  不决定 deadline 语义。
- 1s 是因果候选年龄，使用 BPF `kernel_observed_ns` 判断。候选必须满足：

```text
drop.kernel_observed_ns <= retransmit.kernel_observed_ns
retransmit.kernel_observed_ns - drop.kernel_observed_ns <= 1s
```

`observed_timestamp` 是用户态墙上时间，受调度和系统时间调整影响，不参与匹配。
固定 1s 是当前支持合同，不等于 Linux 所有 RTO 的理论上限；窗口外结果保持
`unknown`。

## 4. 有界状态

等待中的 retransmit 与尚未匹配的 drop 共用包内泛型存储 `store[T]`，分别持有
独立实例。容器只负责容量、TTL、flow 索引和删除；严格匹配与结果定型由关联器负责。

```go
type storeEntry[T any] struct {
    value    T
    flow     *flowKey
    deadline time.Time
    sequence uint64
    node     *list.Element
}

type store[T any] struct {
    byDeadline   list.List
    byFlow       map[flowKey][]*storeEntry[T]
    capacity     int
    ttl          time.Duration
    nextSequence uint64
}
```

| 实例 | 存储值 | 容量 | TTL |
| --- | --- | --- | --- |
| `retransmitStore` | `waitingRetransmit`，内联在 entry 中 | 1024 | 100ms |
| `dropStore` | `*dropEvent` | 4096 | 1s + 100ms |

`waitingRetransmit` 只包含原始事件、匹配字段和跨 namespace 候选标记；
不再持有链表节点、deadline 或独立 ID。drop 也无需额外的缓存包装类型。
链表的 `Element.Value` 与 flow 桶引用同一个 `*storeEntry[T]`，entry 的 `node`
指回所属链表元素。entry 引用业务数据中不再修改的 flow，避免重复保存地址。
删除会同时解除两个索引的引用，并清空 flow 切片尾部；
最后一个条目移除后删除空桶。

flow 索引按两个 `AddrPort` 的规范顺序建键，正反向只占一个桶；原始方向仍保留
在业务数据中，供严格匹配使用。namespace 不进入索引键，保证跨 namespace
的相似候选仍能设置诊断标记。匹配扫描与切片删除为 O(k)，k 是该 flow 的候选数；
定位最早 deadline 与链表摘除为 O(1)。

每个实例的 TTL 固定，关联循环传入单调不减的处理时间，因此链表插入顺序也是
deadline 顺序。链表用于到期清理和容量淘汰，不向业务暴露 FIFO 消费接口。
等待容量满时，最早 deadline 的记录以 `queue_full`
定型；drop 容量满时直接淘汰最早 deadline 的候选。因果判断仍使用 `kernel_observed_ns`。

两个 reader 通过无缓冲 channel 交付事件，容器只由单个关联循环访问，无锁、
无独立清理 goroutine。一个 timer 始终取两个实例的最早 deadline，因此仅有
drop 缓存、没有等待重传时也会按期清理。每次输入和 timer 唤醒均先清理到期项，
再匹配、检查容量；`now >= deadline` 即为过期。100ms 是匹配截止时间，
同步输出的阻塞仍可能延迟实际清理和结果送达。

## 5. 严格匹配

正向 segment 候选要求：

- 相同 network namespace、地址族和方向四元组；
- drop 不晚于 retransmit，且时间差不超过 1s；
- TCP SYN/data/FIN sequence range 与重传 range 重叠；
- RST 和不受支持的重传类型不产生正向匹配。

反向 ACK 候选使用反向四元组，并要求 ACK 覆盖重传 sequence end。SYN 与
SYN-ACK 使用各自更严格的 ACK/SYN 条件。

多个 drop 候选先选最大的 `drop.kernel_observed_ns`；时间相同时选较大的插入序号。
drop 后到时，选择插入序号最小的严格匹配重传，同时扫描其余候选以记录各重传的
namespace 匹配状态。严格匹配后立即从 deadline 和 flow 两个索引删除，只能消费一次。
正向 segment 和反向 ACK 共用 flow 索引，先检查 namespace，再检查报文和时间条件。
同一 flow 下出现相同 namespace 的 drop 时，将 `netNamespace` 置为 true；
即使时间或 sequence 不满足严格匹配条件，该标记也会保留，后续不匹配候选不会清除它。
严格匹配的结果必定为 true。无候选、只有其他 namespace 或 namespace 无法比较时，
该值为 false，表示未观测到匹配，不能据此断定 namespace 不同。
输出通过 `NetNamespace` 保留该状态，JSON 和文本字段为
`matched_net_namespace`，false 时省略。

## 6. 互斥的关联终态

`types.CorrelationReason` 为 string 类型；每次关联只输出一个 `correlation_reason`：

| Go 常量 | JSON 值 | 含义 |
|---------|---------|------|
| `CorrelationMatched` | `matched` | 严格匹配成功，包括来源为 unknown 的 drop。 |
| `CorrelationUnsupported` | `unsupported` | 当前规则无法处理该重传，不进入等待队列。 |
| `CorrelationWarmup` | `warmup` | 等待到期仍未匹配，重传发生时间早于 source ready。 |
| `CorrelationWaitTimeout` | `wait_timeout` | 等待到期仍未匹配，重传发生时间等于或晚于 source ready。 |
| `CorrelationQueueFull` | `queue_full` | 该重传因等待队列已满，在到期前被淘汰。 |
| `CorrelationInterrupted` | `interrupted` | 关联循环退出，未到期的等待被提前中断。 |

各结束分支直接赋值 `correlationResult.reason`。输出侧使用该枚举判定结果，并校验
`matched` 必须带 drop，其他五种终态不能带 drop；空值、未知值或矛盾组合返回错误。
关闭关联时省略输出字段；启用关联后所有已定型事件都包含一个有效终态。
单值字段替代原原因数组，工具端和接收端同步迁移。

等待到期时，按重传的内核时间选择终态：早于 source ready 时为 `warmup`，
等于或晚于 ready 时为 `wait_timeout`，包括 ready 后不足 1s 的情况。
drop 到重传的 1s 上限只用于候选匹配，不参与预热判定。用户态处理延迟
不会改变分类。该判定仅在到期分支执行，严格匹配、
不支持、队列淘汰和提前中断保留各自原因。预热不阻止匹配，也不提前结束等待。

`matched_net_namespace` 保留为独立诊断标记：同一 flow 下观测到相同 namespace
的 drop，独立于报文和时间条件；不能替代 `correlation_reason=matched`。

该标记可与限流、丢失计数并存，不改变唯一终态。无法规范化的 drop 与 drop
缓存容量淘汰不会直接结束重传等待，也不会生成重传级原因。

## 7. Perf 状态

状态由三个独立通道组成，各报各的、互不求和：

- `perf_lost`：累计 per-CPU `bpf_perf_out_dropwatch` map（公共 perf output
  计数设施 `bpf_perf_output.h`），BPF 在 `bpf_perf_event_output` 负返回时
  +1（如当前 CPU 未 attach reader）。
- `lost_samples`：用户态从 `PERF_RECORD_LOST` 累计的 ring buffer 溢出数，只在
  reader 运行期间可见，处理丢失记录后即可通过 ReadStatus 查询。
- `rate_limited`：限流状态 map `bpf_rlimit_dropwatch` 的 `total_missed`。

`ReadStatus` 成功时设置 `HasMapCounters=true`，JSON 为
`map_counters_available=true`。出错时该标记为 false，`PerfLost`、`RateLimited`
为零，表示不可用而非没有丢失或限流；`LostSamples` 仍有效，输出保留该快照。
这些值为实例累计计数，map 和 reader 分别采样，不能推断某条重传未匹配的根因。

用户态汇总所有 CPU 的 perf_lost，每次返回当前快照，不检查计数回退或 uint64 加法溢出。该状态只说明
证据完整性，不会把 no-match 提升为确定性网络分类。旧的 active epoch、
旧的双 slot、inflight、frontier 和 drain 水位机制已删除。

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

`tcpshark` 使用一个 `errgroup.WithContext` 管理 retransmit rate-limit reader、
retransmit reader、embedded dropwatch reader 和关联循环。所有命令 worker 共享同一个
`groupCtx`；任一 worker 返回错误后取消其余 worker，`Wait` 返回首个 worker
错误。资源关闭错误由各 owner 使用 `errors.Join` 合并，不再为 shutdown 维护私有
错误聚合 group。

`internal/dropwatch.Tracer` 持有 dropwatch 的 object、reader 和限流告警 worker。
实例 context 派生自 `groupCtx`；限流告警失败会取消实例读取，具体错误由 `Close`
返回。`ReadInto` 在读取前检查关闭和取消原因；进入底层读取后原样返回读取结果，
不再以随后发生的关闭或取消覆盖结果。`ReadStatus` 在 context 取消后仍可读取最终计数。
`Close` 先取消实例、
detach 并关闭事件与告警 reader，再等待告警 worker 退出，最后释放 object；
关闭告警 reader 会唤醒空闲读取，无需等待轮询期限。
状态类型为 `types.DropwatchStatus`，输出字段为 `drop_perf_status`，包含 map 计数可用性标记。

正常结束或 worker 失败时：

1. 取消共享 `groupCtx`；
2. 两个 `ReadInto` reader 和关联循环退出；
3. 不再读取 dropwatch perf ring 中尚未交给关联器的记录；
4. 调用 `drainRetransmits(now)`，以同一时间先结算到期项，再清空未到期项，
   按 deadline 顺序写入 output；
5. 等全部 worker 退出后，关闭 dropwatch 与 retransmit Tracer，收集内部告警
   worker 和资源释放错误；
6. 最后结束 socket output；调用者传入的 io.Writer 不由会话关闭。

shutdown 已到期项输出 `warmup` 或 `wait_timeout`，未到期项输出 `interrupted`，均保留
`drop_location=unknown`、诊断标记和 perf 状态。已匹配或已淘汰的事件不会重复定型。
尾部 drop 仍可能丢失，因此原本可以匹配的重传也可能被提前结束。

## 9. 文件职责

```text
cmd/tcpshark/
├── main.go                         # 信号、版本、CLI 入口
├── cli.go                          # 参数校验、路径与 filter 规范化
├── run.go                          # 全局 BPF 生命周期、超时、功能分派
└── retransmit/                     # 重传功能包
    ├── config.go                   # Config、RunConfig
    ├── run.go                      # Run：资源组合、执行和清理
    ├── tracer.go                   # Open、ReadInto、Close 和限流 worker
    ├── bpf_load.go                 # loadBPF、attachBPF 和探针选择
    ├── event_reader.go             # 双流读取、转换和 channel 交付
    ├── correlation_loop.go         # select、timer、退出结算
    ├── correlation.go              # 关联状态机、过期结算
    ├── correlation_match.go        # 候选选择、消费与 namespace 匹配状态
    ├── match.go                    # 匹配类型、namespace/flow/sequence/ktime
    ├── store.go                    # 条目存储、flow 索引、容量与过期管理
    ├── event.go                    # 两类 ABI record 转换
    ├── classify.go                 # 重传分类
    ├── correlation_output.go       # 状态补全、符号解析、关联输出字段
    └── output.go                   # text、JSON、socket 输出
```

顶层通过 `retransmit.Run(ctx, *RunConfig)` 运行重传功能，不操作 ABI record、
channel、关联 timer 或状态统计。CLI 先校验标量及参数组合，再编译检查 filter。
设置非空 `--output-storage` 时不校验 `--output` 的值；若显式指定后者，
向 stderr 提示其被忽略。未设置 storage 时仅接受 json 或 text。
RunConfig.Tracing 是采集配置，
Dropwatch 为 nil 时直接读取并输出；非 nil 时使用两个读取 worker 和单个关联
循环。关联能力是重传功能的组合，不是通用采集框架。

Run 在 attach 探针之前建立输出，返回前完成全部会话 worker 等待、最终结算、
Tracer 关闭及输出结束。配置失败、取消与运行失败均释放已取得资源。
关闭时不保证排空内核缓冲区。普通模式不创建关联 channel 或 timer。

`retransmit.Tracer` 仍只拥有重传 BPF 对象、reader 和限流告警 worker；
事件转换、分类、关联及输出属于同包的功能会话，不进入 Tracer。
`Open` 失败会回滚全部已获取资源；会话 context 取消后仍需调用
`Close`。限流 worker 失败会取消采集，原始错误同时由 `Close` 保留。
`ReadInto` 已开始读取后直接保留底层结果，不再检查关闭或取消状态。
`Close` 可以打断读取，但调用者必须串行调用 `Close`。
进程级 `bpf.Init/Shutdown` 和运行超时由命令层统一管理。

关联器在处理每个输入和 timer 时统一清理过期候选，缓存不重复执行过期检查。
关联器返回匹配证据、单值终态和诊断标记，不修改输出字段；correlation_output.go 统一构造最终输出，
每个非空输出批次在入口读取一次 dropwatch 状态，空批次不读取；仅对匹配 drop 解析符号。
即使本批全部匹配也读取状态，读取失败时仍写出本批事件，再返回状态错误；
若写出失败则停止本批输出，并合并写出错误与状态错误。只有 no-match 事件携带
状态快照与诊断标记；读取失败也保留有效的 reader 计数，匹配事件不附加这些字段。
内部重传事件只保留 ABI record 和读取成功时的 observedAt；等待队列不保存
完整的展示对象。匹配直接从 ABI 地址构造 netip 地址，使用 ABI 事件枚举，
不经过字符串转换。IPv4-mapped IPv6 仍归一化为 IPv4。
单事件读取统一处理丢失重试和读取时间；普通模式复用事件并同步输出，
关联模式直接读取到独立事件后发送，避免覆盖等待中的数据。
run.go 统一创建 channel、启动读取 worker，并由发送方关闭 channel；
读取函数不启动 goroutine，不使用消费回调或借用事件合同。
最终输出时才构造 TCPRetransmitTracing；ObservedTimestamp 使用原读取时间，
不使用关联结束时间。ABI、SYNACK flags 补全和匹配规则保持不变。

`cmd/tcpshark/retransmit` 不提供 Go `internal` 导入限制；约定仅由 tcpshark
使用，不反向依赖命令层。两个命令共享的采集实现位于：

```text
internal/dropwatch/
├── config.go    # Config、HardwareMode 和配置校验
├── tracer.go    # Tracer API、newTracer 资源接管、回滚及 worker 生命周期
├── load.go      # loadBPF、attachBPF：加载、map/reader 准备及探针挂载
├── netdev.go    # 设备过滤与 map 配置
├── status.go    # ReadStatus：输出失败、ring 丢失和限流计数
├── reason.go    # LoadReasonNames：内核 BTF reason 表和数字回退
├── metadata.go  # ResolveMetadata：来源、软件 reason、硬件 trap/group
└── decode.go    # DecodePacket：ABI header 适配及 packet.Parse
```

`Open(ctx, *Config)` 返回具体的 `*Tracer`。每个实例只能有一个 `ReadInto`
调用者；状态读取和关闭允许并发，但同一实例的 `Close` 必须由资源所有者串行调用。
首次关闭返回清理错误；后续调用返回 nil，不重试清理，也不重复返回首次错误。
调用方拥有全局 BPF 初始化和关闭，且必须在最终
结算后关闭 Tracer。`DecodePacket` 的返回值不借用原始 record 内存。
符号化和 `DropWatchTracing` 展示转换仍位于 standalone 命令，TCP 关联直接保留
ABI 中的 KernelObservedNS、namespace、stack PC 和已解析的丢包来源及原因。没有新增通用 BPF 消费循环接口。
