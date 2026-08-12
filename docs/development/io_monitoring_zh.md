# IO 监测

IO 监测包含磁盘性能指标和事件驱动的设备健康监测。`iotracing` 从
`/proc/diskstats` 计算磁盘指标；`io_health` 记录 block、NVMe、SCSI 和
MD 异常，并在能够唯一定位设备时采集健康证据。

## 配置

`io_health` 默认启用。关闭时向现有 `BlackList` 添加 `io_health` 并重启，
即可关闭健康事件、取证及对应指标，不影响 `iotracing`。

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
