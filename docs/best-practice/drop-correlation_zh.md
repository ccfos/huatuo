# 网卡硬件丢包与 dropwatch 关联诊断

HUATUO 可以把低频采样的网卡硬件计数器与高频 `dropwatch` 事件放到同一时间窗口中分析，回答生产排障中最重要的问题：数据包最早在哪一层丢失——网卡硬件、驱动还是内核协议栈。

## 为什么需要关联

`dropwatch` 能看到内核释放 skb 的位置，但看不到包是否在进入内核前就被网卡丢弃。`ethtool -S` 和 `/sys/class/net/*/statistics` 能提供硬件/驱动累计计数，却不能指出对应的连接、容器和内核栈。单看任一来源都会产生盲区。

关联器采用以下流程：

1. `netdev_hw` 周期性读取 sysfs 与 ethtool 绝对计数器；
2. 驱动专有字段被归一化为 RX/TX dropped、missed、errors、no-buffer 和 timeout；
3. 相邻样本计算增量，计数器回退按重置处理，绝不进行无符号下溢计算；
4. `dropwatch` 事件进入有界等待队列，并记录设备、方向、原因、协议、容器和调用栈；
5. 硬件样本覆盖事件时间且对应方向有正增量时，事件标为 `hardware`；
6. 没有硬件证据时，根据驱动函数、drop reason 和协议栈函数标为 `driver`、`protocol_stack` 或 `unknown`；
7. 结果写入 `dropwatch_correlation` 事件流，并同步导出 Prometheus 指标。

## 配置

```toml
[EventTracing.Dropwatch]
    Filter = "tcp or udp"
    MaxEventsPerSecond = 100

[MetricCollector.NetdevHW]
    DeviceList = ["eth0", "eth1", "bond0"]
    CorrelationWindowSec = 15
    PendingEventLimit = 4096
    RecentIncidentLimit = 1024
    ReasonLabelLimit = 64
    EnableEthtoolStats = true
```

`CorrelationWindowSec` 应大于等于 `netdev_hw` 的采样间隔，并为调度抖动留出少量余量。窗口过短会把同一时段的事件判成无硬件证据；窗口过长会降低时间相关的可信度。

三个 limit 都是内存和标签基数保护：

- `PendingEventLimit` 达到上限时，最旧事件会立即按现有内核证据完成分类，并增加 `drop_correlation_pending_dropped_total`；
- `RecentIncidentLimit` 限制进程内保留的最近结果，持久化事件不受影响；
- `ReasonLabelLimit` 限制 Prometheus 的 `reason` 标签，超出的新原因合并为 `other`。

## 指标

启用 `netdev_hw` 后，以下指标位于 `huatuo_netdev_hw_` 命名空间：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `drop_correlation_classified_total` | counter | 按 device、direction、layer、reason 分类的事件总数 |
| `drop_correlation_hardware_delta` | gauge | 最近一次用于相关的归一化硬件计数器增量 |
| `drop_correlation_pending_events` | gauge | 等待硬件样本的 dropwatch 事件数 |
| `drop_correlation_pending_dropped_total` | counter | 因有界队列满而提前完成分类的事件数 |
| `drop_correlation_counter_resets_total` | counter | 已识别并排除的设备计数器重置次数 |
| `drop_correlation_degraded_samples_total` | counter | sysfs 或 ethtool 部分读取失败的样本数 |
| `drop_correlation_unmatched_hardware_total` | counter | 有硬件丢包增量但没有 dropwatch 事件的采样方向数 |

`unmatched_hardware_total` 增长通常意味着包在内核可见之前已丢失、dropwatch 被过滤/限流，或选择了错误的设备列表。

## 分层解释

- `hardware`：同设备、同方向、同采样区间出现硬件计数器正增量。这是时间相关，不是逐包唯一标识，因此输出置信度而不是宣称一一对应。
- `driver`：没有硬件增量，但调用栈或 reason 命中已知驱动/NAPI/qdisc/XDP 路径。
- `protocol_stack`：没有硬件增量，证据位于 IP/TCP/UDP/netfilter/bridge 等协议栈路径。
- `unknown`：事件缺少方向、设备、栈和有效 reason，无法可靠分层。

## Grafana

Docker Compose 部署会自动加载 `Network Drop Correlation` 面板。面板支持设备和方向过滤，并展示：

- 各层新增事件速率；
- 主要 drop reason；
- 最近硬件计数器增量；
- 等待队列、重置、降级样本和无匹配硬件证据。

建议同时查看原始 `dropwatch_correlation` 事件。每条事件包含 `evidence`、`confidence`、`hardware_counter_delta` 与 `correlation_lag_ms`，用于解释分类依据。

## 已知边界

1. NIC 计数器是区间累计值，无法证明某个具体 skb 与某个硬件计数器增量一一对应；
2. SR-IOV、bond、bridge 或隧道可能导致事件设备与物理设备不同，应把相关下层设备加入 `DeviceList`；
3. 部分驱动不提供细粒度 ethtool 字段，系统会退化到通用 sysfs 计数并标记样本质量；
4. 设备热插拔和驱动重载会使计数器回退，该区间只记 reset，不参与硬件层判断；
5. `dropwatch` 限流后，硬件丢包可能没有对应内核事件，此时以 unmatched 指标提醒，而不会生成虚构事件。

这些约束使关联结果可解释、可降级，并避免在证据不足时给出过度确定的根因。
