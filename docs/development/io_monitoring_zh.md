# IO 监测

## 实现边界

`iotracing` 是 `/proc/diskstats` 的唯一读取者。它持续计算相邻两次
成功读取之间的 delta，并将同一个最新完整窗口用于 Prometheus 和
自动诊断。诊断子进程运行时采样不会停止，也不会建立历史队列或第二个
reader。

`io_health` 记录 block、NVMe、SCSI 异常以及 NVMe、MD 状态变化事件。
健康命令只在事件能唯一定位到一个 NVMe 或 SCSI 设备时执行；NVMe
reset 只记录事件和 Counter。
MD 生产代码只读取 `/proc/mdstat` 和 sysfs，不调用 `mdadm`。该模块不
导出最近一次取证或设备当前状态。

## 配置

```toml
[AutoTracing.IOTracing]
    Interval = 5

[MetricCollector.IOHealth]
    Enabled = true
```

`Interval` 是 diskstats 采样周期，单位为秒。将 `iotracing` 加入
`BlackList` 会同时关闭 diskstats 指标和自动诊断。

## 采集字段与触发条件

`iotracing` 使用设备名、major、minor 识别同一块整盘；read/write I/O
次数计算 IOPS 和 await，sector 数按 512 字节计算 BPS，read/write ticks
计算 await，I/O ticks 和 weighted I/O ticks 分别计算 util 和平均队列
长度。计算使用相邻样本的实际间隔；分区及 loop、ram、zram、fd 等伪
设备不进入结果。

`io_health` 只抓取定位和分类事件需要的字段：

| 事件 | 触发条件 | 抓取字段及用途 |
| --- | --- | --- |
| `block_error` | 优先使用 `block_rq_error`；Linux 4.18–5.15 在该 tracepoint 不存在时从 `block_rq_complete` 筛选负 errno 的普通数据请求 | dev 定位整盘，sector 记录起始位置，errno 和 operation 分类错误 |
| `nvme_timeout` | 进入 `nvme_timeout` | request 对应的 dev 用于定位整盘 |
| `nvme_reset` | 进入 `nvme_reset_ctrl` | controller 映射为 `nvmeN`；只记录，不执行 `nvme-cli` |
| `nvme_state_change` | `nvme_change_ctrl_state` 返回状态已改变 | controller 定位设备，raw state 保留内核枚举值 |
| `scsi_timeout` | 发出 `scsi_dispatch_cmd_timeout` | host、channel、id、lun 通过 sysfs 定位磁盘 |
| `scsi_dispatch_error` | 发出 `scsi_dispatch_cmd_error` | HCTL 定位磁盘，return status 分类失败 |
| `md_sync_action` | 初始扫描后 `sync_action` 变化 | array 和新旧值记录同步动作变化 |
| `md_degraded` | 初始扫描后 `degraded` 变化 | array 和新旧值记录不可用成员数变化 |
| `md_member_state` | 初始扫描后成员 state 变化或成员消失 | array、member 和新旧值记录成员变化 |

HCTL、`dev_t`、controller 指针、`/proc/mdstat` 的 active 阵列和 sysfs
`level` 仅用于解析或重建拓扑，不作为公共字段输出。NVMe 和 MD 状态事件
也包含恢复、进入 `idle` 等正常方向的变化。

`block_error`、NVMe timeout、SCSI 异常和进入 `faulty`、`blocked`、
`write_error`、`removed` 的 MD 成员会尝试取证。只有唯一定位到 NVMe 或
SCSI 设备时才执行命令，其他事件仍会保存。

`iotracing` 自动诊断与健康事件相互独立。阈值必须连续两个完整窗口都
超过：非 NVMe 设备可由 util 触发；NVMe 要求 util 与对应方向 BPS 同时
超限；read/write await 也可以分别触发。

## 输出字段

### Prometheus 指标

`huatuo_bamai_iotracing_` 下的指标都带 `device` 标签：

- `read_bytes_per_second`、`write_bytes_per_second`：读写 BPS。
- `read_iops`、`write_iops`：每秒读写操作数。
- `read_await_seconds`、`write_await_seconds`：读写平均完成时间。
- `io_utilization_percent`：磁盘忙碌时间占比。
- `average_queue_size`：平均 I/O 队列长度。

scrape 只读取 sampler 发布的不可变快照，不会再次读取 diskstats。

`huatuo_bamai_io_health_` 输出进程启动后累计的 Counter：

| 指标后缀 | 标签 | 含义 |
| --- | --- | --- |
| `block_errors_total` | device、operation、status | block 层错误次数 |
| `nvme_timeouts_total` | device | NVMe timeout 次数 |
| `nvme_resets_total` | device | NVMe controller reset 次数 |
| `scsi_timeouts_total` | device | SCSI command timeout 次数 |
| `scsi_dispatch_errors_total` | device、status | SCSI command 派发失败次数 |
| `collection_errors_total` | device、reason | 事件触发的健康取证失败次数 |

`reason` 当前包括 `target_unresolved`、`tool_unavailable`、
`target_unsupported`、`timeout`、`exec_error`、`output_too_large` 和
`parse_error`。

MD 和 NVMe controller 状态变化只保存为事件，不导出当前状态 Gauge。

### 健康事件

每个事件单独保存。事件时间由外层 tracing 记录提供，`IOHealthEvent`
本身按事件类型输出以下字段；不适用或未取得的可选字段会被省略：

| 字段 | 含义 |
| --- | --- |
| `type` | 上表中的事件类型 |
| `device` | 整盘设备名或 NVMe controller 名；无法定位时为 `unknown` |
| `array`、`member` | MD 阵列及成员名 |
| `old_state`、`new_state` | MD 的新旧值；NVMe 只输出翻译后的新状态 |
| `new_state_raw` | NVMe controller 的原始状态枚举，防止 backport 造成误译 |
| `operation`、`status`、`sector` | block 操作、归一化错误和请求起始 sector；sector 不含长度 |
| `collection_status` | 取证结果：`ok`、`partial`、`unsupported`、`timeout` 或 `error` |
| `nvme` | `nvme-cli` 返回并通过校验的 NVMe 白名单证据 |
| `scsi` | `smartctl` 返回并通过校验的 SCSI 白名单证据 |

NVMe 证据中，`critical_warning` 是健康警告位图，`media_errors_total` 是
介质或数据完整性错误累计值；最多 8 条 `error_log` 分别保留
`status_code_type`、`status_code`、`nsid` 和 `lba`。`error_count`、
`sqid` 只用于过滤，不输出。

SCSI 证据中，`smart_passed` 是总体健康结论；`information_exception`
保存 ASC/ASCQ；`temperature` 保存当前和告警温度；`grown_defect_count`
保存新增缺陷数；read/write/verify 分别保存 `gigabytes_processed`、
`delayed_corrections`、`reread_rewrite_corrections` 和
`uncorrected_errors`；`pending_defects` 保存总数及最多 8 个样本 LBA。
`smartctl.exit_status` 只用于区分命令失败和健康告警，不输出。

## 实机功能验证

以下测试必须显式开启。设备参数只能指向测试环境，禁止选择系统盘。

验证真实 diskstats 读取和指标转换：

```bash
TEST_INTEGRATION=true TEST_IOSTAT_DEVICE=sda \
go test ./core/autotracing -run '^TestIOTracingRealDiskstatsMetrics$' -v
```

验证生产代码实际执行 `nvme` 或 `smartctl` 只读命令：

```bash
TEST_INTEGRATION=true TEST_IO_CONFIRM_NON_SYSTEM=1 \
TEST_NVME_DEVICE=/dev/nvme0 \
go test ./core/metrics -run '^TestIOHealthRealCommandEvidence$' -v

TEST_INTEGRATION=true TEST_IO_CONFIRM_NON_SYSTEM=1 \
TEST_SCSI_DEVICE=/dev/sdb \
go test ./core/metrics -run '^TestIOHealthRealCommandEvidence$' -v
```

NVMe/SCSI 用例要求安装对应的 `nvme-cli`/`smartctl`，并具有目标设备的
读取权限，通常需使用 root。MD 和 block 用例必须使用 root。

验证真实 sysfs 中唯一 leaf 和多 leaf 的取证边界：

```bash
TEST_INTEGRATION=true TEST_IO_UNIQUE_DEVICE=sdb \
TEST_IO_MULTILEAF_DEVICE=md0 \
go test ./core/metrics -run '^TestIOHealthRealTargetResolution$' -v
```

验证临时 MD RAID 的状态和成员事件：

```bash
TEST_INTEGRATION=true \
go test ./internal/ioobserve/health \
  -run '^TestMDWatcherMonitorsMdadmLoopArray$' -v
```

该测试用 `mdadm` 和 loop 设备创建、销毁测试夹具；若 `raid1` 尚未加载，
测试会执行 `modprobe raid1`，且不会卸载该模块。生产代码不会执行这些
命令。真实 block error 链路需要先构建 `huatuo-bamai` 和
`_output/bpf/io_health.o`，再运行：

```bash
bash integration/run.sh test_io_health_event.sh
```

该用例优先测试 `block_rq_error`；Linux 4.18–5.15 会改测
`block_rq_complete`，Linux 5.16–5.17 因 ABI 不受支持而跳过。

性能测试只在代表性物理机执行，不在 oevm 虚拟机执行。
