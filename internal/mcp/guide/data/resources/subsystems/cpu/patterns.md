# CPU root-cause patterns

## cpusys
| Symptom | Profiler signature | Typical cause |
|---------|--------------------|---------------|
| sync storm | ksys_sync → iterate_supers → sync_inodes_sb, with rwsem waits | Frequent fsync/sync calls from container cron jobs or applications |
| Memory reclaim | try_to_free_pages → shrink_node → shrink_slab | Memory pressure triggers reclaim |
| Scheduler overhead | run_rebalance_domains → load_balance → find_busiest_group | Frequent NUMA load balancing |
| Lock contention | Frequent native_queued_spin_lock_slowpath | Multiple CPUs contend for the same lock |
| Network softirq load | net_rx_action → napi_poll | High packet-processing load |
| Device-driver overhead | ixgbe_update_stats / NIC polling | Intensive statistics collection or polling consumes CPU |

## cpuidle
| Symptom | Profiler signature | Typical cause |
|---------|--------------------|---------------|
| Compute-intensive application | One user process has self time > 80% | Busy loop, load test, or computation |
| sync storm | ksys_sync → iterate_supers | Frequent fsync/sync in a container |
| Memory pressure | page_fault → alloc_pages → shrink_node | Container memory shortage triggers reclaim |
| Lock contention | spinlock_slowpath / rwsem_slowpath | Concurrent threads contend for locks |
| I/O blocking | io_schedule → blkdev_issue_flush | I/O-related paths; validate waits separately from on-CPU time |
| Noisy neighbor | Container anomaly with host cpusys events | Other containers or processes compete for resources |

## CPU quota throttling
| Symptom | Key metrics | Typical cause |
|---------|-------------|---------------|
| CFS throttling | High throttle_wait_rate and nr_throttled > 0 | Insufficient container CPU quota causes frequent CFS throttling |

## dload
| Symptom | Key metrics | Typical cause |
|---------|-------------|---------------|
| I/O-blocked D-state tasks | High nr_uninterruptible with iotracing events | Tasks wait for I/O completion (slow disk or hung I/O) |
| Lock-blocked D-state tasks | High nr_uninterruptible without I/O events | Tasks may wait for kernel mutexes/rwsems; verify stacks |
| Reclaim-blocked D-state tasks | High nr_uninterruptible with memburst events | Direct reclaim blocks tasks |

## softlockup
| Symptom | Key metrics | Typical cause |
|---------|-------------|---------------|
| Kernel busy loop | softlockup_total increases for one CPU | Driver or kernel bug prevents the CPU from scheduling other tasks |
| Lock nesting | softlockup with spin_lock signatures | Nested spinlocks or ABBA deadlock |
