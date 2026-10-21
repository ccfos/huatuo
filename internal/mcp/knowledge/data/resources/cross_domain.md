# 跨子系统关联

跨子系统排查线索，用于多维度关联分析。事件共现不证明因果关系，
需结合目标、时间线、指标和调用栈验证；不能仅凭组合名称认定根因。

## CPU ↔ Memory

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| cpusys + memburst | 内存回收可能推高 CPU sys | 核验回收指标与内核栈 |
| cpuidle + memburst | 容器内存压力可能增加 CPU 开销 | 核验容器内存限制、回收和 CPU 时间线 |

## CPU ↔ IO

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| cpusys + iotracing | IO 路径可能增加系统态开销 | 核验 IO 延迟与 CPU 热点 |
| dload + iotracing | IO 阻塞可能使进程进入 D 状态 | 核验阻塞栈、设备与进程是否对应 |

## CPU ↔ Network

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| cpusys + dropwatch | 网络处理压力可能增加 CPU sys 并伴随丢包 | 核验软中断指标、CPU 热点和丢包原因 |
| dropwatch + net_rx_latency | 丢包和接收延迟可能有共同瓶颈 | 核验接口、流量与时间线 |

## Memory ↔ IO

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| memburst + iotracing | 内存压力可能通过换页或回写影响 IO | 核验换页、回写与 IO 指标 |

## CPU ↔ Memory ↔ IO

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| dload + memburst | 直接内存回收可能阻塞进程 | 核验回收路径与 D 状态进程栈 |
| hungtask + dload | 持续 D 状态可能触发 hung task | 核验同一进程的阻塞时长与调用栈 |

## Network ↔ Hardware

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| netdev_txqueue_timeout + netdev_events | 链路变化可能伴随发送超时 | 核验网卡、驱动和链路时间线 |
| lacp + netdev_events | bond slave 可能发生链路变更 | 核验 bond 状态和成员接口 |
| netdev_events + dropwatch | 链路抖动可能导致丢包 | 核验接口与丢包原因 |

## CPU ↔ Memory (OOM)

| 关联 | 因果关系 | 触发排查方向 |
|------|---------|-------------|
| oom + memory_reclaim | 部分 OOM 场景伴随直接回收 | 核验 OOM 类型、内存限制与回收证据；缺少回收事件不排除 OOM |
| memburst + oom | 内存增长可能导致 OOM | 核验被杀进程、内存限制及时间线 |
