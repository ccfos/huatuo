# Memory root-cause patterns

## memburst
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Single-process leak | One process's RSS grows continuously | Application allocations are not released |
| Growth across processes | Several processes' RSS increases together | Traffic surge |
| Excessive cache growth | High page-cache share | Heavy file I/O; verify cache reclaimability and workload before calling it a leak |
| Pressure persists after OOM kill | Other processes grow after an OOM kill | Continuing memory pressure and frequent swapping |
| JVM off-heap leak | Java RSS greatly exceeds configured heap size | Off-heap allocations are not released |

## oom
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Single OOM | oom_container_total increases by 1 | A process exceeds its cgroup memory limit |
| Repeated OOM | oom_container_total keeps increasing | Memory leak or severely insufficient quota |
| Host OOM | oom_host_total increases | Host memory exhaustion |

## memory_reclaim
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Direct-reclaim latency | High deltatime with degraded container performance | cgroup memory approaches its limit, causing frequent direct reclaim |
| Insufficient kswapd reclaim | High pgscan_kswapd with persistent pressure | Background reclaim cannot keep up with allocations |

## hungtask
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| I/O-related hung task | hungtask with iotracing | Persistent NFS or disk I/O blocking |
| Lock-related hung task | hungtask without I/O events | Possible kernel lock deadlock; verify blocked stacks |
