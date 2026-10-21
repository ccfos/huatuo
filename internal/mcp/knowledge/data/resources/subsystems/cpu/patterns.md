# CPU 根因模式库

## cpusys

| 现象 | Profiler 热点特征 | 典型原因 |
|------|-----------|---------|
| sync 风暴 | ksys_sync → iterate_supers → sync_inodes_sb + rwsem 等待 | 容器内 cron/应用频繁 fsync |
| 内存回收 | try_to_free_pages → shrink_node → shrink_slab | 内存压力大触发回收 |
| 调度器开销 | run_rebalance_domains → load_balance → find_busiest_group | NUMA 负载均衡频繁 |
| 锁争抢 | native_queued_spin_lock_slowpath 高频 | 多核竞争同一把锁 |
| 网络软中断 | net_rx_action → napi_poll | 大量网络包处理 |
| 设备驱动 | ixgbe_update_stats / 网卡轮询 | 密集统计或轮询占 CPU |

## cpuidle

| 现象 | Profiler 热点特征 | 典型原因 |
|------|-----------|---------|
| 应用计算密集 | 单一用户进程 self time > 80% | 死循环/压测/计算任务 |
| sync 风暴 | ksys_sync → iterate_supers | 容器内 fsync/sync 频繁 |
| 内存压力 | page_fault → alloc_pages → shrink_node | 容器内存不足触发回收 |
| 锁争抢 | spinlock_slowpath / rwsem_slowpath | 多线程并发竞争 |
| IO 阻塞 | io_schedule → blkdev_issue_flush | IO 等待转化为 CPU |
| noisy neighbor | 容器异常 + 宿主机 cpusys 事件 | 其他容器/进程抢占资源 |

## CPU 配额限流

| 现象 | 关键指标 | 典型原因 |
|------|---------|---------|
| CFS throttle | throttle_wait_rate 高 + nr_throttled > 0 | 容器 CPU 配额不足，频繁被 CFS 限流 |

## dload

| 现象 | 关键指标 | 典型原因 |
|------|---------|---------|
| IO 阻塞型 D 状态 | nr_uninterruptible 高 + iotracing 有事件 | 进程等 IO 完成（磁盘慢/IO hang） |
| 锁争抢型 D 状态 | nr_uninterruptible 高 + 无 IO 事件 | 进程等内核锁（mutex/rwsem） |
| 内存回收型 D 状态 | nr_uninterruptible 高 + memburst 有事件 | 直接内存回收阻塞进程 |

## softlockup

| 现象 | 关键指标 | 典型原因 |
|------|---------|---------|
| 内核死循环 | CPU 单核 softlockup_total 递增 | 驱动 bug 或内核 bug 导致不释放 CPU |
| 锁嵌套 | softlockup + spin_lock 特征 | 自旋锁嵌套/ABBA 死锁 |
