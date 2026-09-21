# IO 监测

IO 监测包含磁盘性能指标、IO 延时与大小分布、blk-throttle 排队
等待，以及事件驱动的设备健康监测。`iotracing` 从 `/proc/diskstats` 计算
磁盘指标；`iolatency` 统计 block IO 生命周期；`blk_throtl` 统计 throttle 等待；
`iocost` 统计预算不足造成的等待；`io_health` 记录设备异常并采集健康证据。

## 配置

`io_health` 默认启用。关闭时向现有 `BlackList` 添加 `io_health` 并重启，
即可关闭健康事件、取证及对应指标，不影响 `iotracing`。

交付配置默认将 `iolatency` 放入全局 `BlackList`；从列表移除并重启后才会
加载 block IO 跟踪程序。

`blk_throtl` 默认启用并采集 blk-throttle 被限速的 IO 数量和平均等待时间。
仅可通过将 `blk_throtl` 加入全局 `BlackList` 并重启来关闭。

`iocost` 默认启用，采集因 IOCOST 预算不足而等待的 IO 数量和平均等待时间。将 `iocost` 加入全局 `BlackList` 并重启可关闭。

磁盘指标的统计周期由 Prometheus scrape 周期决定，不需要配置 iotracing
采样周期。将 `iotracing` 加入 `BlackList` 会同时关闭磁盘指标和自动诊断。

## 磁盘指标

每次 scrape 读取一次 diskstats，与上次成功读取的结果做差，并按实际时间
间隔输出区间平均值。首次读取只建立基线；读取失败不会推进基线。自动诊断
仍使用独立的 5 秒采样，不与 Prometheus 指标共享状态。

所有 `huatuo_bamai_iotracing_` 指标均为 Gauge，并带 `device` 标签：

| 指标后缀 | 含义 |
| --- | --- |
| `read_bytes_per_second`、`write_bytes_per_second` | 读写 BPS |
| `read_iops`、`write_iops` | 每秒读写操作数 |
| `read_await_milliseconds`、`write_await_milliseconds` | 读写平均完成时间（毫秒） |
| `io_utilization_percent` | 磁盘忙碌时间占比 |
| `average_queue_size` | 平均 I/O 队列长度 |

指标只覆盖整盘和支持的逻辑设备，不包含分区及 loop、ram、zram、fd 等伪
设备。

## IO 延时与大小分布

统计下发到 SCSI/SAS、NVMe 磁盘的读写 IO，覆盖 Q（入队）、G（request
起始时间）、D（下发）和 C（request 完成）。
C 使用 `block_rq_complete` 的完成字节数结算完整 bio；部分完成的 bio
保留到下一次完成。全部采集探针必须挂载成功；完成统计只处理
有本次入队记录的 bio。D→C 使用 request 的内核时间，Q 相关区间使用
bio 的入队时间。

request clone 撤销时，在 `blk_rq_unprep_clone` 释放 bio 前清理其起点记录，
已记录的入队样本保留，撤销不产生完成样本。正常完成后的空链直接返回。

A（remap）作为目标设备的 Q。同一 bio 在同一整盘上多次产生 Q/A 时，
入队大小只计一次，Q 相关延时从最后一次 Q/A 计时。

| 阶段 | 指标后缀 |
| --- | --- |
| Q→G | `q2g_seconds_bucket` |
| Q→D | `q2d_seconds_bucket` |
| D→C | `d2c_seconds_bucket` |

IO 大小统计：

| 采集点 | 指标后缀 | 含义 |
| --- | --- | --- |
| Q | `queued_io_size_bytes_bucket` | 入队 bio 大小 |
| C | `issued_io_size_bytes_bucket` | 已完成 request 最后一次下发的大小 |

宿主机指标使用 `huatuo_bamai_iolatency_` 前缀，并带 `device`、
`operation`（`read` 或 `write`）和 `le` 标签。容器指标使用
`huatuo_bamai_iolatency_container_` 前缀，并沿用标准容器标签。

这些指标是相邻两次成功采集之间的 Gauge bucket。首次采集只建立基线，
宿主机计数读取失败时保留 session 和基线；建立基线后新出现的宿主机统计项从零开始计算。
容器发现不影响启动或宿主机采集，无需运行 kubelet。容器查询、计数读取失败或标签无效时，
宿主机指标和基线正常推进，不输出对应容器指标；关联恢复后先建立容器基线，不补发缺失区间。
freeze 读取失败只缺少本轮 freeze 指标，不阻断直方图。
`le` 在单个样本内累计，`+Inf` 是该指标在区间内的样本
总数。不输出 `_sum` 或 `_count`。共享的宿主机指标
`huatuo_bamai_iolatency_interval_start_timestamp_seconds` 和
`huatuo_bamai_iolatency_interval_end_timestamp_seconds` 给出真实区间边界，
仅带 `region` 和 `host` 标签。

Grafana 直接将 Gauge bucket 展示为 Heatmap，并在详情中显示区间起止时间、
时长、总量和互斥桶。查询不需要 `rate()` 或 `increase()`。原有
`huatuo_bamai_iolatency_blkdisk_freeze` Counter 保持不变。

被 merge 的 bio 通过 request 的 bio 列表在 C 点结算。G 使用内核保存的
`request.start_time_ns`，D 使用最后一次下发的 `request.io_start_time_ns`。
Q→D 包含重排等待，D→C 从最后一次下发计时。启动时检查各目标盘的
`iostats` 和请求时间统计，任一未开启则初始化失败并说明设备。NVMe multipath 统计实际下发请求的路径盘，跳过上层 head。每轮采集采用 Update 进入时已确认的磁盘配置，期间发现的变化在后台复查后对后续采集生效，切换附近一个周期允许少记或不准。仅暂停不满足条件的磁盘；恢复配置后重新统计，其他磁盘不受影响。
零字节 pure flush 被过滤，带数据的 read/write 请求正常统计。

启动依赖可读的内核 BTF 和完整的 `bpf_htab` 结构。采用 `bpf_mem_alloc`
时停止 IOLatency；BTF 读取、解析或结构查找失败时，记录具体原因并停止。
其他采集模块不受此检查影响。

单次最多遍历 512 个 bio，起点表最多保存 10,240 条记录。
map 写入或删除遇到 `-EBUSY` 时跳过该操作并继续采集。
容器计数分配或清理失败不停止 Host，未归属样本仍包含在宿主机总量中。
遍历超限或 bio 起点记账错误会停止采集并记录原因，排查后重启 Huatuo 恢复。

允许漏事件留下部分阶段样本和起点记录，后续 Q/A 重新初始化记录。
缺少起点或时间倒置的区间被忽略；地址复用仍可能产生正值错样。
启动时发现软件 blk-crypto fallback 已初始化，会记录 warning 并跳过本特性。
用户态负载用例及误差边界见 [采集方案](iolatency-design.md)。

request 最终完成时记录一次下发大小，设备和操作类型取自 request。
容器归属取本次完成的首个剩余 bio 的 blkcg；
4.18 内核可能合并不同 blkcg 的 bio，因此容器
`issued` 分布为近似值，宿主机分布不受影响。

## blk-throttle 排队等待

`blk_throtl` 统计 IO 因 blk-throttle 限速而排队的数量和平均等待时间，
用于观察限速对磁盘及容器 IO 的影响。每个 IO 结束排队后计数一次，
等待时间从首次排队算起。

| 指标 | 含义 |
| --- | --- |
| `huatuo_bamai_blk_throtl_delayed_io_count` | 相邻两次成功采集间完成等待的 IO 数量 |
| `huatuo_bamai_blk_throtl_average_wait_milliseconds` | 这些 IO 的平均排队时间，单位毫秒 |

指标按 `device` 和 `operation`（`read` 或 `write`）区分，`scope=host`
表示宿主机总量，`scope=other` 表示其中无法归属容器的部分。

两项均为区间 Gauge，查询时直接使用指标值。

`blk_throtl` 因错误停止采集时，请查看日志排查原因，解决后重启 Huatuo。
临时采集失败会自动重试，缺失的数据不会补发。

容器发现不影响启动或宿主机采集，无需运行 kubelet。容器查询失败或标签无效时，
未归属的数据进入 `other`，统计基线正常推进；关联恢复后只统计当前区间，不补发之前的数据。

## IOCOST 等待统计

等待时间从 IO 开始等待算到最终被唤醒，计入等待结束所在的采集区间。

| 指标 | 含义 |
| --- | --- |
| `huatuo_bamai_iocost_waitq_io_count` | 本轮完成等待的读写 IO 数量 |
| `huatuo_bamai_iocost_average_wait_milliseconds` | 这些 IO 的平均等待时间，单位毫秒 |
| `huatuo_bamai_iocost_container_waitq_io_count` | 容器本轮完成等待的读写 IO 数量 |
| `huatuo_bamai_iocost_container_average_wait_milliseconds` | 容器 IO 的平均等待时间，单位毫秒 |

指标按 `device`（设备号 `major:minor`）和 `operation`（`read`、`write`）分组。`scope=host` 表示设备总量，包含所有容器；`scope=other` 表示未归属到容器的部分。

容器发现不影响 IOCOST 启动或宿主机采集，无需运行 kubelet。容器查询失败或标签无效时，未归属的数据进入 `other`，统计基线正常推进；容器关联恢复后只统计当前区间，不补发之前的数据。

使用时注意：

- 采集失败后，首次成功采集只恢复统计起点，下一次成功采集恢复输出。
- cgroup 或设备释放时，尚未上报的数据可能丢失；设备号变化时，该设备可能缺少一轮数据。
- 单个 CPU 上的同一统计项在两次成功采集间完成不少于 67,108,864 次等待，或累计等待达到约 274.9 万秒时，结果可能偏小。例如 10,240 个 IO 均等待 270 秒后在同一区间结束，会超过等待时间的容量。

需要提供 IOCOST 函数及 BPF/BTF、kprobe 能力的非 PREEMPT_RT Linux x86_64 内核。

## 健康事件

| 事件类型 | 触发条件 |
| --- | --- |
| `block_error` | 普通 block 请求完成时返回错误 |
| `nvme_timeout` | NVMe 请求超时 |
| `nvme_reset` | NVMe controller reset；只记录事件，不执行取证命令 |
| `nvme_state_change` | NVMe controller 状态发生变化 |
| `scsi_timeout` | SCSI command 超时 |
| `scsi_dispatch_error` | SCSI command 派发失败 |
| `md_sync_action` | MD 同步动作发生变化 |
| `md_degraded` | MD 不可用成员数发生变化 |
| `md_member_state` | MD 成员状态发生变化或成员消失 |

`block_error`、NVMe timeout、SCSI 异常，以及进入 `faulty`、`blocked`、
`write_error`、`removed` 的 MD 成员会触发健康取证。目标唯一时，NVMe
设备执行只读 `nvme-cli` 命令，SCSI 设备执行只读 `smartctl` 命令；目标
无法唯一确定时仍保存事件，但不扫描其他设备。MD 监测只读取
`/proc/mdstat` 和 sysfs，不执行 `mdadm`。

`huatuo_bamai_io_health_` Counter 记录进程启动后观察到的事件：

| 指标后缀 | 标签 | 含义 |
| --- | --- | --- |
| `block_errors_total` | device、operation、status | block 层错误次数 |
| `nvme_timeouts_total` | device | NVMe timeout 次数 |
| `nvme_resets_total` | device | NVMe reset 次数 |
| `scsi_timeouts_total` | device | SCSI timeout 次数 |
| `scsi_dispatch_errors_total` | device、status | SCSI 派发失败次数 |
| `collection_errors_total` | device、reason | 健康取证失败次数 |
| `event_persistence_failures_total` | reason | 事件持久化被丢弃或结果未确认的次数 |

MD 和 NVMe controller 状态变化只保存为事件，不导出当前状态 Gauge。

进程内事件 Counter 在事件到达时更新，不依赖存储速度。事件文档通过容量为
1024 的有界队列串行保存，单次保存设置 5 秒超时。存储未及时返回时记录超时，
待这次保存返回后继续消费；取消时退出，不等待阻塞的存储。取证命令使用另一条容量为
1024 的串行队列；同一设备代际的请求处于执行中或 60 秒冷却期时会被合并，
设备名复用后的新代际不继承旧代际状态。任一队列满、存储不响应、停止期间未完成
等情况不会阻塞 BPF、MD 或取证来源，但相应文档可能被丢弃或结果无法确认，并通过
`event_persistence_failures_total` 的 `reason` 标签计数。

## 事件内容

队列接受的健康事件按独立文档保存，不适用或未取得的可选字段会被省略：

| 字段 | 含义 |
| --- | --- |
| `type` | 事件类型 |
| `device` | 整盘设备名或 NVMe controller 名 |
| `array`、`member` | MD 阵列和成员名 |
| `old_state`、`new_state` | MD 状态变化；NVMe 只输出新状态 |
| `new_state_raw` | NVMe controller 的原始状态枚举 |
| `operation`、`io_error_status`、`sector` | block 操作、错误和请求起始 sector |
| `collection_status` | `ok`、`partial`、`unsupported`、`timeout` 或 `error` |
| `nvme`、`scsi` | 事件触发后采集到的协议健康证据 |

NVMe 证据包括 `critical_warning`、`media_errors_total`，以及最多 8 条包含
status code、NSID 和 LBA 的 error log。SCSI 证据包括总体健康结论、
ASC/ASCQ、温度、缺陷数、读写校验错误计数，以及 pending defect 数量和
最多 8 个样本 LBA。
