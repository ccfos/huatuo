# Kernel function signatures

Reference for common kernel functions seen in profiling data, grouped by
subsystem. Function presence suggests an investigation path, not a root cause.

## Memory management
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `shrink_node` | Core reclaim routine scanning LRU pages | Memory pressure; correlate with memburst |
| `shrink_slab` | Reclaim slab caches, including dentries and inodes | Excessive filesystem cache |
| `shrink_lruvec` | Reclaim pages from LRU lists | Memory shortage |
| `try_to_free_pages` | Direct-reclaim entry point | Severe pressure, possibly preceding OOM |
| `alloc_pages` | Page allocation | Frequent allocations; check whether they trigger reclaim |
| `page_fault` | Page-fault exception | First access, copy-on-write, or swap-in |
| `do_wp_page` | Write-protect fault handling, including copy-on-write | Writes after fork |
| `compact_zone` | Memory compaction | Severe fragmentation |
| `kswapd` | Background reclaim thread | Frequent activity suggests sustained pressure |
| `free_one_page` | Free pages to the buddy allocator | Normal page-freeing path |
| `__isolate_lru_page` | Isolate a page from the LRU for reclaim | Active reclaim |

## I/O
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `io_schedule` | Schedule while waiting for I/O | Correlate I/O waits with iotracing |
| `blkdev_issue_flush` | Issue a block-device cache flush | Frequent fsync operations |
| `submit_bio` | Submit a block I/O request | I/O submission path |
| `wait_on_page_bit` | Wait for a page flag, often I/O completion | Elevated I/O latency |
| `filemap_fdatawait_range` | Wait for writeback in a file range | fsync/sync waits |
| `sync_inodes_sb` | Synchronize inodes belonging to a superblock | sync system calls |
| `ksys_sync` | sync system-call entry point | Frequent sync operations |
| `iterate_supers` | Iterate over superblocks | Global sync operations |
| `ext4_sync_fs` | Synchronize an ext4 filesystem | Filesystem synchronization overhead |
| `xfs_sync_fs` | Synchronize an XFS filesystem | Filesystem synchronization overhead |
| `blk_mq_run_hw_queue` | Run a block-device hardware queue | I/O scheduling path |
| `__blockdev_direct_IO` | Direct I/O | I/O bypassing the page cache |

## Scheduler
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `schedule` | Main scheduling routine; the task yields the CPU | Frequent context switches |
| `scheduler_tick` | Per-tick scheduler accounting/checks | Normal timer path |
| `run_rebalance_domains` | Rebalance scheduler domains | Frequent NUMA load balancing |
| `load_balance` | Balance task load across CPUs | Task migration |
| `find_busiest_group` | Find the busiest scheduling group | Load-balancing overhead |
| `select_task_rq` | Select a CPU for a task | NUMA placement or wake-up scheduling |
| `try_to_wake_up` | Wake a task | Frequent wake-ups and waits |
| `wakeup_preempt` | Wake-up preemption check | Frequent scheduler preemption |

## Locking
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `native_queued_spin_lock_slowpath` | Contended queued-spinlock slow path | Multiple CPUs contend for one spinlock |
| `__rwsem_down_read_slowpath` | rwsem read-acquisition slow path | Reader contention |
| `__rwsem_down_write_slowpath` | rwsem write-acquisition slow path | Blocked writers |
| `mutex_lock_slowpath` | Contended mutex acquisition, possibly sleeping | Mutex contention |
| `rwsem_optimistic_spin` | Optimistic spinning for an rwsem | Spinning instead of immediately sleeping |
| `osq_lock` | Optimistic spin queue lock | Queued spinning |
| `futex_lock_pi` | Priority-inheritance futex acquisition | Userspace locking, such as pthread mutexes |

## Network stack
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `net_rx_action` | Network receive softirq handler | Incoming traffic processing |
| `net_tx_action` | Network transmit softirq handler | Outgoing traffic processing |
| `napi_poll` | Poll a NAPI instance | Packet processing |
| `__do_softirq` | Softirq dispatch | Softirq overhead |
| `netif_receive_skb` | Receive an skb into the network stack | Network-stack ingress |
| `tcp_v4_rcv` | IPv4 TCP receive path | TCP processing |
| `ip_rcv` | IPv4 receive path | IP processing |
| `ip_output` | IP output path | IP transmission |
| `dev_queue_xmit` | Queue a packet for transmission | Network transmission |
| `skb_copy_datagram_iovec` | Copy skb data to userspace | Data-copy overhead |
| `copyin_user` | Copy within user address space | Check the actual caller and architecture |
| `ixgbe_update_stats` / `i40e_update_stats` | Update NIC driver statistics | Driver statistics overhead |

## Filesystems
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `vfs_read` | VFS read operation | File reads |
| `vfs_write` | VFS write operation | File writes |
| `ext4_file_write_iter` | ext4 file write | ext4 write overhead |
| `generic_file_read_iter` | Generic file read | Page-cache reads |
| `generic_perform_write` | Generic buffered write | Page-cache writes |
| `filemap_map_pages` | Map file-backed pages | mmap reads |
| `do_page_fault` | Page-fault handling | File-backed or anonymous page faults |

## Containers / cgroups
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `try_charge` | Charge memory against cgroup accounting/limits | Container memory near its limit |
| `mem_cgroup_try_charge` | Attempt a memory-cgroup charge | Container memory allocations |
| `css_task_iter_start` / `css_task_iter_next` | Iterate over cgroup tasks | cgroup management overhead |
| `cgroup_rstat_flush` | Flush cgroup resource statistics | cgroup accounting overhead |

## Other functions
| Function | Meaning | Investigation |
|----------|---------|---------------|
| `copy_page` / `copy_user_page` | Copy a page | fork/copy-on-write overhead |
| `clear_page` / `clear_user_page` | Zero a page | Page allocation and secure initialization |
| `acct_update_integrals` | Update task accounting | Usually minor accounting overhead |
| `update_wall_time` | Update system timekeeping | Clock-related overhead |
| `tick_sched_timer` | Scheduler tick timer | Normal system tick |
