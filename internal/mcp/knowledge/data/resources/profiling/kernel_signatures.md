# 内核函数签名库

常见内核函数在自动 profiler 中的含义速查表。按子系统分类，供分析热点时参考。

## 内存管理

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `shrink_node` | 内存回收核心函数，扫描 LRU 回收页面 | 内存压力，结合 memburst 排查 |
| `shrink_slab` | 回收 slab 缓存（dentry/inode cache） | 文件系统缓存过多 |
| `shrink_lruvec` | 按 LRU 链表回收页面 | 内存不足 |
| `try_to_free_pages` | 直接内存回收入口 | 内存严重不足，可能触发 OOM |
| `alloc_pages` | 页面分配 | 频繁分配，看是否触发回收 |
| `page_fault` | 缺页异常 | 首次访问内存/写时复制/换入 |
| `do_wp_page` | 写时复制 | 大量 fork 后写操作 |
| `compact_zone` | 内存规整（碎片整理） | 内存碎片严重 |
| `kswapd` | 后台内存回收线程 | kswapd 频繁 = 持续内存压力 |
| `free_one_page` | 释放单个页面 | 正常回收路径 |
| `__isolate_lru_page` | 从 LRU 隔离页面准备回收 | 回收活跃 |

## IO 子系统

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `io_schedule` | 进程因 IO 阻塞睡眠 | IO 等待，结合 iotracing 排查 |
| `blkdev_issue_flush` | 发起块设备 flush | fsync 频繁 |
| `submit_bio` | 提交块 IO 请求 | IO 提交路径 |
| `wait_on_page_bit` | 等待页面 IO 完成 | IO 延迟高 |
| `filemap_fdatawait_range` | 等待脏页写回完成 | fsync/sync 等待 |
| `sync_inodes_sb` | 同步超级块的所有 inode | sync 系统调用 |
| `ksys_sync` | sync 系统调用入口 | 频繁 sync 操作 |
| `iterate_supers` | 遍历所有超级块 | sync 全局操作 |
| `ext4_sync_fs` | ext4 文件系统同步 | fsync 文件系统开销 |
| `xfs_sync_fs` | xfs 文件系统同步 | fsync 文件系统开销 |
| `blk_mq_run_hw_queue` | 运行块设备硬件队列 | IO 调度路径 |
| `__blockdev_direct_IO` | 直接 IO | 绕过 page cache 的 IO |

## 调度器

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `schedule` | 主调度函数，进程让出 CPU | 频繁 schedule = 上下文切换过多 |
| `scheduler_tick` | 时钟中断调度检查 | 正常路径 |
| `run_rebalance_domains` | 负载均衡 | NUMA 负载均衡频繁 |
| `load_balance` | 执行负载均衡 | CPU 间任务迁移 |
| `find_busiest_group` | 找最忙的 CPU 调度组 | 负载均衡开销 |
| `select_task_rq` | 为任务选择 CPU | NUMA/Wake-up 调度 |
| `try_to_wake_up` | 唤醒进程 | 大量唤醒/等待 |
| `wakeup_preempt` | 抢占当前进程 | 调度抢占频繁 |

## 锁

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `native_queued_spin_lock_slowpath` | 自旋锁慢路径（竞争失败后自旋） | 多核争抢同一自旋锁 |
| `__rwsem_down_read_slowpath` | 读写信号量读慢路径 | 读端竞争 |
| `__rwsem_down_write_slowpath` | 读写信号量写慢路径 | 写端阻塞 |
| `mutex_lock_slowpath` | 互斥锁慢路径（获取失败后睡眠） | 互斥锁竞争 |
| `rwsem_optimistic_spin` | 读写信号量乐观自旋 | 尝试自旋而非睡眠 |
| `osq_lock` | 乐观自旋队列锁 | 排队自旋 |
| `futex_lock_pi` | PI futex 锁 | 用户态锁（pthread_mutex） |

## 网络协议栈

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `net_rx_action` | 网络接收软中断处理 | 入向流量处理 |
| `net_tx_action` | 网络发送软中断处理 | 出向流量处理 |
| `napi_poll` | NAPI 轮询收包 | 网络包处理 |
| `__do_softirq` | 软中断处理入口 | 软中断开销 |
| `netif_receive_skb` | 内核接收 skb | 协议栈入口 |
| `tcp_v4_rcv` | TCP v4 接收路径 | TCP 处理 |
| `ip_rcv` | IP 层接收 | IP 处理 |
| `ip_output` | IP 层发送 | IP 发送 |
| `dev_queue_xmit` | 网卡发送队列 | 网络发送 |
| `skb_copy_datagram_iovec` | 拷贝 skb 数据到用户空间 | 大量数据拷贝 |
| `copyin_user` | 从用户空间拷贝 | 网络数据读取 |
| `ixgbe_update_stats` / `i40e_update_stats` | 网卡驱动统计更新 | 驱动统计开销 |

## 文件系统

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `vfs_read` | 虚拟文件系统读 | 文件读取 |
| `vfs_write` | 虚拟文件系统写 | 文件写入 |
| `ext4_file_write_iter` | ext4 文件写入 | ext4 写开销 |
| `generic_file_read_iter` | 通用文件读 | page cache 读 |
| `generic_perform_write` | 通用文件写 | page cache 写 |
| `filemap_map_pages` | 文件映射页 | mmap 读 |
| `do_page_fault` | 缺页处理 | 文件/匿名页缺页 |

## 容器 / Cgroup

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `try_charge` | cgroup 内存配额检查 | 容器内存接近 limit |
| `mem_cgroup_try_charge` | memory cgroup 充值 | 容器内存分配 |
| `css_task_iter_start` / `css_task_iter_next` | cgroup 任务遍历 | cgroup 操作开销 |
| `cgroup_rstat_flush` | cgroup 统计刷新 | cgroup 统计开销 |

## 其他

| 函数 | 含义 | 出现时的排查方向 |
|------|------|-----------------|
| `copy_page` / `copy_user_page` | 页面拷贝 | fork/写时复制开销 |
| `clear_page` / `clear_user_page` | 页面清零 | 大页分配/安全清零 |
| `acct_update_integrals` | 进程统计更新 | 统计开销（可忽略） |
| `update_wall_time` | 更新系统时间 | 时钟相关 |
| `tick_sched_timer` | 时钟定时器 | 正常系统 tick |
