# 内存根因模式库

## memburst

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| 单进程泄漏 | 单个进程 RSS 持续增长 | 应用内存未释放 |
| 多进程上涨 | 多个进程 RSS 同时增长 | 流量突增 |
| 缓存泄漏 | Page cache 占比高 | 大量文件读写不关闭 fd |
| OOM 扩散 | OOM kill 后其他进程跟着涨 | 内存不足换页频繁 |
| JVM 堆外泄漏 | Java RSS 远大于堆配置 | 堆外内存分配后未释放 |

## oom

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| 单次 OOM | oom_container_total 突增 1 | 单进程超 cgroup 内存限制 |
| 连续 OOM | oom_container_total 持续增长 | 内存泄漏或配额严重不足 |
| 宿主机 OOM | oom_host_total 增长 | 宿主机内存耗尽 |

## memory_reclaim

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| 直接回收延迟 | deltatime 高 + 容器性能下降 | cgroup 内存接近 limit，频繁直接回收 |
| kswapd 回收不足 | pgscan_kswapd 高但 mem 仍紧张 | 后台回收跟不上分配速度 |

## hungtask

| 现象 | 特征 | 典型原因 |
|------|------|---------|
| IO hung task | hungtask + iotracing | NFS/磁盘 IO 永久阻塞 |
| 锁 hung task | hungtask 无 IO 事件 | 内核锁死锁 |
