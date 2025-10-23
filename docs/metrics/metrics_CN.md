简体中文 | [English](./metrics.md)

该文档汇总了当前 v1.0 版本支持的所有的指标，涉及CPU，内存，网络，IO。

|子系统|指标|描述|单位|统计纬度|指标来源|
|-------|-------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------|-----|-------------------------------------------------------------------|
|cpu|cpu_util_sys|cpu 系统态利用率|%|宿主|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_util_usr|cpu 用户态利用率|%|宿主|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_util_total|容器 cpu 总利用率|%|宿主|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_util_container_sys|容器 cpu 系统态利用率|%|容器|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_util_container_usr|容器 cpu 用户态利用率|%|容器|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_util_container_total|容器 cpu 总利用率|%|容器|基于 cgroup cpuacct.stat 和 cpuacct.usage 计算|
|cpu|cpu_stat_container_burst_time|累计墙时（以纳秒为单位），周期内突发超出配额的时间|纳秒(ns)|容器|基于 cpu.stat 读取|
|cpu|cpu_stat_container_nr_bursts|周期内突发次数|计数|容器|基于 cpu.stat 读取|
|cpu|cpu_stat_container_nr_throttled|cgroup 被 throttled/limited 的次数|计数|容器|基于 cpu.stat 读取|
|cpu|cpu_stat_container_exter_wait_rate|容器外进程导致的等待率|%|容器|基于 cpu.stat 读取的 throttled_time hierarchy_wait_sum inner_wait_sum 计算|
|cpu|cpu_stat_container_inner_wait_rate|容器内部进程导致的等待率|%|容器|基于 cpu.stat 读取的 throttled_time hierarchy_wait_sum inner_wait_sum 计算|
|cpu|cpu_stat_container_throttle_wait_rate|容器被限制而引起的等待率|%|容器|基于 cpu.stat 读取的 throttled_time hierarchy_wait_sum inner_wait_sum 计算|
|cpu|cpu_stat_container_wait_rate|总的等待率: exter_wait_rate + inner_wait_rate + throttle_wait_rate|%|容器|基于 cpu.stat 读取的 throttled_time hierarchy_wait_sum inner_wait_sum 计算|
|cpu|loadavg_container_container_nr_running|容器中运行的任务数量|计数|容器|从内核通过 netlink 获取|
|cpu|loadavg_container_container_nr_uninterruptible|容器中不可中断任务的数量|计数|容器|从内核通过 netlink 获取|
|cpu|loadavg_load1|系统过去 1 分钟的平均负载|计数|宿主|procfs|
|cpu|loadavg_load5|系统过去 5 分钟的平均负载|计数|宿主|procfs|
|cpu|loadavg_load15|系统过去 15 分钟的平均负载|计数|宿主|procfs|
|cpu|softirq_latency|在不同时间域发生的 NET_RX/NET_TX 中断延迟次数：<br>0~10 us<br>100us ~ 1ms<br>10us ~ 100us<br>1ms ~ inf|计数|宿主|BPF 软中断埋点统计|
|cpu|runqlat_container_nlat_01|容器中进程调度延迟在 0~10 毫秒内的次数|计数|容器|bpf 调度切换埋点统计|
|cpu|runqlat_container_nlat_02|容器中进程调度延迟在 10~20 毫秒之间的次数|计数|容器|bpf 调度切换埋点统计|
|cpu|runqlat_container_nlat_03|容器中进程调度延迟在 20~50 毫秒之间的次数|计数|容器|bpf 调度切换埋点统计|
|cpu|runqlat_container_nlat_04|容器中进程调度延迟超过 50 毫秒的次数|计数|容器|bpf 调度切换埋点统计|
|cpu|runqlat_g_nlat_01|宿主中进程调度延迟在范围内 0～10 毫秒的次数|计数|宿主|bpf 调度切换埋点统计|
|cpu|runqlat_g_nlat_02|宿主中进程调度延迟在范围内 10～20 毫秒的次数|计数|宿主|bpf 调度切换埋点统计|
|cpu|runqlat_g_nlat_03|宿主中进程调度延迟在范围内 20～50 毫秒的次数|计数|宿主|bpf 调度切换埋点统计|
|cpu|runqlat_g_nlat_04|宿主中进程调度延迟超过 50 毫秒的次数|计数|宿主|bpf 调度切换埋点统计|
|cpu|reschedipi_oversell_probability|vm 中 cpu 超卖检测|0-1|宿主|bpf 调度 ipi 埋点统计|
|memory|buddyinfo_blocks|内核伙伴系统内存分配|页计数|宿主|procfs|
|memory|memory_events_container_watermark_inc|内存水位计数|计数|容器|memory.events|
|memory|memory_events_container_watermark_dec|内存水位计数|计数|容器|memory.events|
|memory|memory_others_container_local_direct_reclaim_time|cgroup 中页分配速度|纳秒(ns)|容器|memory.local_direct_reclaim_time|
|memory|memory_others_container_directstall_time|直接回收时间|纳秒(ns)|容器|memory.directstall_stat|
|memory|memory_others_container_asyncreclaim_time|异步回收时间|纳秒(ns)|容器|memory.asynreclaim_stat|
|memory|memory_stat_container_writeback|匿名/文件 cache sync 到磁盘排队字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_unevictable|无法回收的内存（如 mlocked）|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_shmem|共享内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgsteal_kswapd|kswapd 和 cswapd 回收的内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgsteal_globalkswapd|由 kswapd 回收的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgsteal_globaldirect|过页面分配直接回收的内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgsteal_direct|页分配和 try_charge 期间直接回收的内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgsteal_cswapd|由 cswapd 回收的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgscan_kswapd|kswapd 和 cswapd 扫描的内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgscan_globalkswapd|kswapd 扫描的内存字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgscan_globaldirect|扫描内存中通过直接回收在页面分配期间的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgscan_direct|扫描内存的字节数，在页面分配和 try_charge 期间通过直接回收的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgscan_cswapd|由 cswapd 扫描内存的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgrefill|内存中扫描的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_pgdeactivate|内存中未激活的部分被添加到非活动列表中|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_inactive_file|文件内存中不活跃的 LRU 列表的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_inactive_anon|匿名和交换缓存内存中不活跃的 LRU 列表的字节数|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_dirty|等待写入磁盘的字节|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_active_file|活跃内存中文件内存的大小|字节(Bytes)|容器|memory.stat|
|memory|memory_stat_container_active_anon|活跃内存中匿名和交换内存的大小|字节(Bytes)|容器|memory.stat|
|memory|mountpoint_perm_ro|挂在点是否为只读|布尔(bool)|宿主|procfs|
|memory|vmstat_allocstall_normal|宿主在 normal 域直接回收|计数|宿主|/proc/vmstat|
|memory|vmstat_allocstall_movable|宿主在 movable 域直接回收|计数|宿主|/proc/vmstat|
|memory|vmstat_compact_stall|内存压缩计数|计数|宿主|/proc/vmstat|
|memory|vmstat_nr_active_anon|活跃的匿名页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_active_file|活跃的文件页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_boost_pages|kswapd boosting 页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_dirty|脏页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_free_pages|释放的页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_inactive_anon|非活跃的匿名页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_inactive_file|非活跃的文件页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_kswapd_boost|kswapd boosting 次数计数|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_mlock|锁定的页面数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_shmem|共享内存页面数|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_slab_reclaimable|可回收的 slab 页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_slab_unreclaimable|无法回收的 slab 页数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_unevictable|不可驱逐页面数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_nr_writeback|写入页面数|页计数|宿主|/proc/vmstat|
|memory|vmstat_numa_pages_migrated|NUMA 迁移中的页面数|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgdeactivate|页数被停用进入非活动 LRU|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgrefill|扫描的活跃 LRU 页面数|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgscan_direct|扫描的页数|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgscan_kswapd|扫描的页面数量，由 kswapd 回收的数量|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgsteal_direct|直接回收的页面|页计数|宿主|/proc/vmstat|
|memory|vmstat_pgsteal_kswapd|被 kswapd 回收的数量|页计数|宿主|/proc/vmstat|
|memory|hungtask_counter|hungtask 事件计数|计数|宿主|BPF 埋点统计|
|memory|oom_host_counter|oom 事件计数|计数|宿主|BPF 埋点统计|
|memory|oom_container_counter|oom 事件计数|计数|容器|BPF 埋点统计|
|memory|softlockup_counter|softlockup 事件计数|计数|宿主|BPF 埋点统计|
|memory|memory_free_compaction|内存压缩的速度|纳秒(ns)|宿主|bpf 埋点统计|
|memory|memory_free_allocstall|内存中主机直接回收速度|纳秒(ns)|宿主|bpf 埋点统计|
|memory|memory_cgroup_container_directstall|cgroup 尝试直接回收的计数|计数|容器|bpf 埋点统计|
|IO|iolatency_disk_d2c|磁盘访问时的 io 延迟统计，包括驱动程序和硬件组件消耗的时间|计数|宿主|bpf 埋点统计|
|IO|iolatency_disk_q2c|磁盘访问整个 I/O 生命周期时的 I/O 延迟统计|计数|宿主|bpf 埋点统计|
|IO|iolatency_container_d2c|磁盘访问时的 I/O 延迟统计，包括驱动程序和硬件组件消耗的时间|计数|容器|bpf 埋点统计|
|IO|iolatency_container_q2c|磁盘访问整个 I/O 生命周期时的 I/O 延迟统计|计数|容器|bpf 埋点统计|
|IO|iolatency_disk_flush|磁盘 RAID 设备刷新操作延迟统计|计数|宿主|bpf 埋点统计|
|IO|iolatency_container_flush|磁盘 RAID 设备上由容器引起的刷新操作延迟统计|计数|容器|bpf 埋点统计|
|IO|iolatency_disk_freeze|磁盘 freese 事件|计数|宿主|bpf 埋点统计|
|network|tcp_mem_limit_pages|系统 TCP 总内存大小限制|页计数|系统|procfs|
|network|tcp_mem_usage_bytes|系统使用的 TCP 内存总字节数|字节(Bytes)|系统|tcp_mem_usage_pages \* page_size|
|network|tcp_mem_usage_pages|系统使用的 TCP 内存总量|页计数|系统|procfs|
|network|tcp_mem_usage_percent|系统使用的 TCP 内存百分比（相对 TCP 内存总限制）|%|系统|tcp_mem_usage_pages / tcp_mem_limit_pages|
|network|arp_entries|arp 缓存条目数量|计数|宿主，容器|procfs|
|network|arp_total|总 arp 缓存条目数|计数|系统|procfs|
|network|qdisc_backlog|待发送的字节数|字节(Bytes)|宿主|netlink qdisc 统计|
|network|qdisc_bytes_total|已发送的字节数|字节(Bytes)|宿主|netlink qdisc 统计|
|network|qdisc_current_queue_length|排队等待发送的包数量|计数|宿主|netlink qdisc 统计|
|network|qdisc_drops_total|丢弃的数据包数量|计数|宿主|netlink qdisc 统计|
|network|qdisc_overlimits_total|排队数据包里超限的数量|计数|宿主|netlink qdisc 统计|
|network|qdisc_packets_total|已发送的包数量|计数|宿主|netlink qdisc 统计|
|network|qdisc_requeues_total|重新入队的数量|计数|宿主|netlink qdisc 统计|
|network|ethtool_hardware_rx_dropped_errors|接口接收丢包统计|计数|宿主|硬件驱动相关, 如 mlx, ixgbe, bnxt_en, etc.|
|network|netdev_receive_bytes_total|接口接收的字节数|字节(Bytes)|宿主，容器|procfs|
|network|netdev_receive_compressed_total|接口接收的压缩包数量|计数|宿主，容器|procfs|
|network|netdev_receive_dropped_total|接口接收丢弃的包数量|计数|宿主，容器|procfs|
|network|netdev_receive_errors_total|接口接收检测到错误的包数量|计数|宿主，容器|procfs|
|network|netdev_receive_fifo_total|接口接收 fifo 缓冲区错误数量|计数|宿主，容器|procfs|
|network|netdev_receive_frame_total|接口接收帧对齐错误|计数|宿主，容器|procfs|
|network|netdev_receive_multicast_total|多播数据包已接收的包数量，对于硬件接口，此统计通常在设备层计算（与 rx_packets 不同），因此可能包括未到达的数据包|计数|宿主，容器|procfs|
|network|netdev_receive_packets_total|接口接收到的有效数据包数量|计数|宿主，容器|procfs|
|network|netdev_transmit_bytes_total|接口发送的字节数|字节(Bytes)|宿主，容器|procfs|
|network|netdev_transmit_carrier_total|接口发送过程中由于载波丢失导致的帧传输错误数量|计数|宿主，容器|procfs|
|network|netdev_transmit_colls_total|接口发送碰撞计数|计数|宿主，容器|procfs|
|network|netdev_transmit_compressed_total|接口发送压缩数据包数量|计数|宿主，容器|procfs|
|network|netdev_transmit_dropped_total|数据包在传输过程中丢失的数量，如资源不足|计数|宿主，容器|procfs|
|network|netdev_transmit_errors_total|发送错误计数|计数|宿主，容器|procfs|
|network|netdev_transmit_fifo_total|帧传输错误数量|计数|宿主，容器|procfs|
|network|netdev_transmit_packets_total|发送数据包计数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_ArpFilter|因 ARP 过滤规则而被拒绝的 ARP 请求/响应包数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_BusyPollRxPackets|通过 busy polling​​ 机制接收到的网络数据包数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_DelayedACKLocked|由于用户态锁住了sock，而无法发送delayed ack的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_DelayedACKLost|当收到已确认的包时，它将被更新。延迟 ACK 丢失可能会引起这个问题，但其他原因也可能触发，例如网络中重复的包。|计数|宿主，容器|procfs|
|network|netstat_TcpExt_DelayedACKs|延迟的 ACK 定时器已过期。TCP 堆栈将发送一个纯 ACK 数据包并退出延迟 ACK 模式|计数|宿主，容器|procfs|
|network|netstat_TcpExt_EmbryonicRsts|收到初始 SYN_RECV 套接字的重置|计数|宿主，容器|procfs|
|network|netstat_TcpExt_IPReversePathFilter|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_ListenDrops|当内核收到客户端的 SYN 请求时，如果 TCP 接受队列已满，内核将丢弃 SYN 并将 TcpExtListenOverflows 加 1。同时，内核也会将 TcpExtListenDrops 加 1。当一个 TCP 套接字处于监听状态，且内核需要丢弃一个数据包时，内核会始终将 TcpExtListenDrops 加 1。因此，增加 TcpExtListenOverflows 会导致 TcpExtListenDrops 同时增加，但 TcpExtListenDrops 也会在没有 TcpExtListenOverflows 增加的情况下增加，例如内存分配失败也会导致 TcpExtListenDrops 增加。|计数|宿主，容器|procfs|
|network|netstat_TcpExt_ListenOverflows|当内核收到客户端的 SYN 请求时，如果 TCP 接受队列已满，内核将丢弃 SYN 并将 TcpExtListenOverflows 加 1。同时，内核也会将 TcpExtListenDrops 加 1。当一个 TCP 套接字处于监听状态，且内核需要丢弃一个数据包时，内核会始终将 TcpExtListenDrops 加 1。因此，增加 TcpExtListenOverflows 会导致 TcpExtListenDrops 同时增加，但 TcpExtListenDrops 也会在没有 TcpExtListenOverflows 增加的情况下增加，例如内存分配失败也会导致 TcpExtListenDrops 增加。|计数|宿主，容器|procfs|
|network|netstat_TcpExt_LockDroppedIcmps|由于套接字被锁定，ICMP 数据包被丢弃|计数|宿主，容器|procfs|
|network|netstat_TcpExt_OfoPruned|协议栈尝试在乱序队列中丢弃数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_OutOfWindowIcmps|ICMP 数据包因超出窗口而被丢弃|计数|宿主，容器|procfs|
|network|netstat_TcpExt_PAWSActive|数据包在 Syn-Sent 状态被 PAWS 丢弃|计数|宿主，容器|procfs|
|network|netstat_TcpExt_PAWSEstab|数据包在除 Syn-Sent 之外的所有状态下都会被 PAWS 丢弃|计数|宿主，容器|procfs|
|network|netstat_TcpExt_PFMemallocDrop|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_PruneCalled|协议栈尝试回收套接字内存。更新此计数器后，将尝试合并乱序队列和接收队列。如果内存仍然不足，将尝试丢弃乱序队列中的数据包（并更新 TcpExtOfoPruned 计数器）。|计数|宿主，容器|procfs|
|network|netstat_TcpExt_RcvPruned|在从顺序错误的队列中‘collapse’和丢弃数据包后，如果实际使用的内存仍然大于最大允许内存，则此计数器将被更新。这意味着‘prune’失败|计数|宿主，容器|procfs|
|network|netstat_TcpExt_SyncookiesFailed|MSS 从 SYN cookie 解码出来的无效。当这个计数器更新时，接收到的数据包不会被当作 SYN cookie 处理，并且 TcpExtSyncookiesRecv 计数器不会更新|计数|宿主，容器|procfs|
|network|netstat_TcpExt_SyncookiesRecv|接收了多少个 SYN cookies 的回复数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_SyncookiesSent|发送了多少个 SYN cookies|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedChallenge|ACK 为 challenge ACK 时，将跳过 ACK|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedFinWait2|ACK 在 Fin-Wait-2 状态被跳过，原因可能是 PAWS 检查失败或接收到的序列号超出窗口|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedPAWS|由于 PAWS（保护包装序列号）检查失败，ACK 被跳过|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedSeq|序列号超出窗口范围，时间戳通过 PAWS 检查，TCP 状态不是 Syn-Recv、Fin-Wait-2 和 Time-Wait|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedSynRecv|ACK 在 Syn-Recv 状态中被跳过。Syn-Recv 状态表示协议栈收到一个 SYN 并回复 SYN+ACK|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPACKSkippedTimeWait|CK 在 Time-Wait 状态中被跳过，原因可能是 PAWS 检查失败或接收到的序列号超出窗口|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortFailed|内核 TCP 层将在满足 RFC2525 2.17 节时发送 RST。如果在处理过程中发生内部错误，TcpExtTCPAbortFailed 将增加|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortOnClose|用户模式程序缓冲区中有数据时关闭的套接字数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortOnData|TCP 层有正在传输的数据，但需要关闭连接|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortOnLinger|当 TCP 连接进入 FIN_WAIT_2 状态时，内核不会等待来自另一侧的 fin 包，而是发送 RST 并立即删除套接字|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortOnMemory|当一个应用程序关闭 TCP 连接时，内核仍然需要跟踪该连接，让它完成 TCP 断开过程|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAbortOnTimeout|此计数器将在任何 TCP 计时器到期时增加。在这种情况下，内核不会发送 RST，而是放弃连接|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAckCompressed|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPAutoCorking|发送数据包时，TCP 层会尝试将小数据包合并成更大的一个|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPBacklogDrop|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPChallengeACK|challenge ack 发送的数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKIgnoredNoUndo|当 DSACK 块无效时，这两个计数器中的一个将被更新。哪个计数器将被更新取决于 TCP 套接字的 undo_marker 标志|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKIgnoredOld|当 DSACK 块无效时，这两个计数器中的一个将被更新。哪个计数器将被更新取决于 TCP 套接字的 undo_marker 标志|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKOfoRecv|收到一个 DSACK，表示收到一个顺序错误的重复数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKOfoSent|收到一个乱序的重复数据包，因此向发送者发送 DSACK|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKOldSent|收到一个已确认的重复数据包，因此向发送者发送 DSACK|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKRecv|收到一个 DSACK，表示收到了一个已确认的重复数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDSACKUndo|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDeferAcceptDrop|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDelivered|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPDeliveredCE|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenActive|当 TCP 栈在 SYN-SENT 状态接收到一个 ACK 包，并且 ACK 包确认了 SYN 包中的数据，理解 TFO cookie 已被对方接受，然后它更新这个计数器|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenActiveFail|Fast Open 失败|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenBlackhole|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenCookieReqd|客户端想要请求 TFO cookie 的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenListenOverflow|挂起的 Fast Open 请求数量大于 fastopenq->max_qlen 时，协议栈将拒绝 Fast Open 请求并更新此计数器|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenPassive|指示 TCP 堆栈接受 Fast Open 请求的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastOpenPassiveFail|协议栈拒绝 Fast Open 的次数，这是由于 TFO cookie 无效或 在创建套接字过程中发现错误所引起的|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFastRetrans|快速重传|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFromZeroWindowAdv|TCP 接收窗口设置为非零值|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPFullUndo|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHPAcks|如果数据包设置了 ACK 标志且没有数据，则是一个纯 ACK 数据包，如果内核在快速路径中处理它，TcpExtTCPHPAcks 将增加 1|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHPHits|如果 TCP 数据包包含数据（这意味着它不是一个纯 ACK 数据包），并且此数据包在快速路径中处理，TcpExtTCPHPHits 将增加 1|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHystartDelayCwnd|CWND 检测到的包延迟总和。将此值除以 TcpExtTCPHystartDelayDetect，即为通过包延迟检测到的平均 CWND|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHystartDelayDetect|检测到数据包延迟阈值次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHystartTrainCwnd|TCP Hystart 训练中使用的拥塞窗口大小，将此值除以 TcpExtTCPHystartTrainDetect 得到由 ACK 训练长度检测到的平均 CWND|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPHystartTrainDetect|TCP Hystart 训练检测的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPKeepAlive|此计数器指示已发送的保活数据包。默认情况下不会启用保活功能。用户空间程序可以通过设置 SO_KEEPALIVE 套接字选项来启用它。|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPLossFailures|丢失数据包而进行恢复失败的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPLossProbeRecovery|检测到丢失的数据包恢复的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPLossProbes|TCP 检测到丢失的数据包数量，通常用于检测网络拥塞或丢包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPLossUndo|TCP重传数据包成功到达目标端口，但之前已经由于超时或拥塞丢失，因此被视为“撤销”丢失的数据包数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPLostRetransmit|丢包重传个数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMD5Failure|校验错误|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMD5NotFound|校验错误|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMD5Unexpected|校验错误|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMTUPFail|使用 DSACK 无需慢启动即可恢复拥塞窗口|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMTUPSuccess|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMemoryPressures|到达 tcp 内存压力位 low 的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMemoryPressuresChrono|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPMinTTLDrop|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPOFODrop|TCP 层接收到一个乱序的数据包，但内存不足，因此丢弃它。此类数据包不会计入 TcpExtTCPOFOQueue 计数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPOFOMerge|接收到的顺序错误的包与上一个包有重叠。重叠部分将被丢弃。所有 TcpExtTCPOFOMerge 包也将计入 TcpExtTCPOFOQueue|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPOFOQueue|TCP 层接收到一个乱序的数据包，并且有足够的内存来排队它|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPOrigDataSent|发送原始数据（不包括重传但包括 SYN 中的数据）的包数量。此计数器与 TcpOutSegs 不同，因为 TcpOutSegs 还跟踪纯 ACK。TCPOrigDataSent 更有助于跟踪 TCP 重传率|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPPartialUndo|检测到一些错误的重传，在我们快速重传的同时，收到了部分确认，因此能够部分撤销我们的一些 CWND 减少|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPPureAcks|如果数据包设置了 ACK 标志且没有数据，则是一个纯 ACK 数据包，如果内核在快速路径中处理它，TcpExtTCPHPAcks 将增加 1，如果内核在慢速路径中处理它，TcpExtTCPPureAcks 将增加 1|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRcvCoalesce|当数据包被 TCP 层接收但未被应用程序读取时，TCP 层会尝试合并它们。这个计数器表示在这种情况下合并了多少个数据包。如果启用了 GRO，GRO 会合并大量数据包，这些数据包不会被计算到 TcpExtTCPRcvCoalesce 中|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRcvCollapsed|在“崩溃”过程中释放了多少个 skbs|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRenoFailures|TCP_CA_Disorder 阶段进入并经历 RTO 的重传失败次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRenoRecovery|当拥塞控制进入恢复状态时，如果使用 sack，TcpExtTCPSackRecovery 增加 1，如果不使用 sack，TcpExtTCPRenoRecovery 增加 1。这两个计数器意味着协议栈开始重传丢失的数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRenoRecoveryFail|进入恢复阶段并 RTO 的连接数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRenoReorder|重排序数据包被快速恢复检测到。只有在 SACK 被禁用时才会使用|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPReqQFullDoCookies|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPReqQFullDrop|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPRetransFail|尝试将重传数据包发送到下层，但下层返回错误|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSACKDiscard|有多少个 SACK 块无效。如果无效的 SACK 块是由 ACK 记录引起的，tcp 栈只会忽略它，而不会更新此计数器|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSACKReneging|一个数据包被 SACK 确认，但接收方已丢弃此数据包，因此发送方需要重传此数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSACKReorder|SACK 检测到的重排序数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSYNChallenge|响应 SYN 数据包发送的 Challenge ack 数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackFailures|TCP_CA_Disorder 阶段进入并经历 RTO 的重传失败次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackMerged|skb 已合并计数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackRecovery|当拥塞控制进入恢复状态时，如果使用 sack，TcpExtTCPSackRecovery 增加 1，如果不使用 sack，TcpExtTCPRenoRecovery 增加 1。这两个计数器意味着 TCP 栈开始重传丢失的数据包|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackRecoveryFail|SACK 恢复失败的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackShiftFallback|skb 应该被移动或合并，但由于某些原因，TCP 堆栈没有这样做|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSackShifted|skb 被移位|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSlowStartRetrans|重新传输一个数据包，拥塞控制状态为“丢失”|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSpuriousRTOs|虚假重传超时|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSpuriousRtxHostQueues|当 TCP 栈想要重传一个数据包，发现该数据包并未在网络中丢失，但数据包尚未发送，TCP 栈将放弃重传并更新此计数器。这可能会发生在数据包在 qdisc 或驱动程序队列中停留时间过长的情况下|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPSynRetrans|SYN 和 SYN/ACK 重传次数，将重传分解为 SYN、快速重传、超时重传等|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPTSReorder|tcp 栈在接收到时间截包而进行乱序包阀值调整的次数|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPTimeWaitOverflow|TIME_WAIT 状态的套接字因超出限制而无法分配的数量|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPTimeouts|TCP 超时事件|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPToZeroWindowAdv|TCP 接收窗口从非零值设置为零|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPWantZeroWindowAdv|根据当前内存使用情况，TCP 栈尝试将接收窗口设置为零。但接收窗口可能仍然是一个非零值|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPWinProbe|定期发送的 ACK 数据包数量，以确保打开窗口的反向 ACK 数据包没有丢失|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TCPWqueueTooBig|\-|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TW|TCP 套接字在快速计时器中完成 time wait 状态|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TWKilled|TCP 套接字在慢速计时器中完成 time wait 状态|计数|宿主，容器|procfs|
|network|netstat_TcpExt_TWRecycled|等待套接字通过时间戳回收|计数|宿主，容器|procfs|
|network|netstat_Tcp_ActiveOpens|TCP 层发送一个 SYN，进入 SYN-SENT 状态。每当 TcpActiveOpens 增加 1 时，TcpOutSegs 应该始终增加 1|计数|宿主，容器|procfs|
|network|netstat_Tcp_AttemptFails|TCP 连接从 SYN-SENT 状态或 SYN-RCVD 状态直接过渡到 CLOSED 状态次数，加上 TCP 连接从 SYN-RCVD 状态直接过渡到 LISTEN 状态次数|计数|宿主，容器|procfs|
|network|netstat_Tcp_CurrEstab|TCP 连接数，当前状态为 ESTABLISHED 或 CLOSE-WAIT|计数|宿主，容器|procfs|
|network|netstat_Tcp_EstabResets|TCP 连接从 ESTABLISHED 状态或 CLOSE-WAIT 状态直接过渡到 CLOSED 状态次数|计数|宿主，容器|procfs|
|network|netstat_Tcp_InCsumErrors|TCP 校验和错误|计数|宿主，容器|procfs|
|network|netstat_Tcp_InErrs|错误接收到的段总数（例如，错误的 TCP 校验和）|计数|宿主，容器|procfs|
|network|netstat_Tcp_InSegs|TCP 层接收到的数据包数量。如 RFC1213 所述，包括接收到的错误数据包，如校验和错误、无效 TCP 头等|计数|宿主，容器|procfs|
|network|netstat_Tcp_MaxConn|可以支持的总 TCP 连接数限制，在最大连接数动态的实体中，此对象应包含值-1|计数|宿主，容器|procfs|
|network|netstat_Tcp_OutRsts|TCP 段中包含 RST 标志的数量|计数|宿主，容器|procfs|
|network|netstat_Tcp_OutSegs|发送的总段数，包括当前连接上的段，但不包括仅包含重传字节的段|计数|宿主，容器|procfs|
|network|netstat_Tcp_PassiveOpens|TCP 连接从监听状态直接过渡到 SYN-RCVD 状态的次数|计数|宿主，容器|procfs|
|network|netstat_Tcp_RetransSegs|总重传段数 - 即包含一个或多个先前已传输字节的 TCP 段传输的数量|计数|宿主，容器|procfs|
|network|netstat_Tcp_RtoAlgorithm|The algorithm used to determine the timeout value used for retransmitting unacknowledged octets|计数|宿主，容器|procfs|
|network|netstat_Tcp_RtoMax|TCP 实现允许的重传超时最大值，以毫秒为单位|毫秒|宿主，容器|procfs|
|network|netstat_Tcp_RtoMin|TCP 实现允许的重传超时最小值，以毫秒为单位|毫秒|宿主，容器|procfs|
|network|sockstat_FRAG_inuse|\-|计数|宿主，容器|procfs|
|network|sockstat_FRAG_memory|\-|页计数|宿主，容器|procfs|
|network|sockstat_RAW_inuse|使用的 RAW 套接字数量|计数|宿主，容器|procfs|
|network|sockstat_TCP_alloc|TCP 已分配的套接字数量|计数|宿主，容器|procfs|
|network|sockstat_TCP_inuse|已建立的 TCP 套接字数量|计数|宿主，容器|procfs|
|network|sockstat_TCP_mem|系统使用的 TCP 内存总量|页计数|系统|procfs|
|network|sockstat_TCP_mem_bytes|系统使用的 TCP 内存总量|字节(Bytes)|系统|sockstat_TCP_mem \* page_size|
|network|sockstat_TCP_orphan|TCP 等待关闭的连接数|计数|宿主，容器|procfs|
|network|sockstat_TCP_tw|TCP 套接字终止数量|计数|宿主，容器|procfs|
|network|sockstat_UDPLITE_inuse|\-|计数|宿主，容器|procfs|
|network|sockstat_UDP_inuse|使用的 UDP 套接字数量|计数|宿主，容器|procfs|
|network|sockstat_UDP_mem|系统使用的 UDP 内存总量|页计数|系统|procfs|
|network|sockstat_UDP_mem_bytes|系统使用的 UDP 内存字节数总和|字节(Bytes)|系统|sockstat_UDP_mem \* page_size|
|network|sockstat_sockets_used|系统使用 socket 数量|计数|系统|procfs|
