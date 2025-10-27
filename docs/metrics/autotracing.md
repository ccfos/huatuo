English | [简体中文](./autotracing_CN.md)

### Overview
HUATUO currently supports automatic tracing for the following metrics:

| Tracing Name        | Core Function               | Scenario                                  |
| --------------------| --------------------------- |------------------------------------------ |
| cpusys              | Host sys surge detection      | Service glitches caused by abnormal system load            |
| cpuidle             | Container CPU idle drop detection, providing call stacks, flame graphs, process context info, etc. | Abnormal container CPU usage, helping identify process hotspots |
| dload               | Tracks container loadavg and process states, automatically captures D-state process call info in containers | System D-state surges are often related to unavailable resources or long-held locks; R-state process surges often indicate poor business logic design |
| waitrate            | Container resource contention detection; provides info on contending containers during scheduling conflicts | Container contention can cause service glitches; existing metrics lack specific contending container details; waitrate tracing provides this info for mixed-deployment resource isolation reference |
| memburst            | Records context info during sudden memory allocations | Detects short-term, large memory allocation events on the host, which may trigger direct reclaim or OOM |
| iotracing           | Detects abnormal host disk I/O latency. Outputs context info like accessed filenames/paths, disk devices, inode numbers, containers, etc. | Frequent disk I/O bandwidth saturation or access surges leading to application request latency or system performance jitter |

### CPUSYS
System mode CPU time reflects kernel execution overhead, including system calls, interrupt handling, kernel thread scheduling, memory management, lock contention, etc. Abnormal increases in this metric typically indicate kernel-level performance bottlenecks: frequent system calls, hardware device exceptions, lock contention, or memory reclaim pressure (e.g., kswapd direct reclaim).

When cpusys detects an anomaly in this metric, it automatically captures system call stacks and generates flame graphs to help identify the root cause. It considers both sustained high CPU Sys usage and sudden Sys spikes, with trigger conditions including:
- CPU Sys usage > Threshold A
- CPU Sys usage increase over a unit time > Threshold B

### CPUIDLE
In K8S container environments, a sudden drop in CPU idle time (i.e., the proportion of time the CPU is idle) usually indicates that processes within the container are excessively consuming CPU resources, potentially causing business latency, scheduling contention, or even overall system performance degradation.

cpuidle automatically triggers the capture of call stacks to generate flame graphs. Trigger conditions:
- CPU Sys usage > Threshold A
- CPU User usage > Threshold B && CPU User usage increase over unit time > Threshold C
- CPU Usage > Threshold D && CPU Usage increase over unit time > Threshold E

### DLOAD
The D state is a special process state where a process is blocked waiting for kernel or hardware resources. Unlike normal sleep (S state), D-state processes cannot be forcibly terminated (even with SIGKILL) and do not respond to interrupt signals. This state typically occurs during I/O operations (e.g., direct disk read/write) or hardware driver failures. System D-state surges often relate to unavailable resources or long-held locks, while runnable process surges often indicate poor business logic design. dload uses netlink to obtain the count of running + uninterruptible processes in a container, calculates the D-state process contribution to the load over the past 1 minute via a sliding window algorithm. When the smoothed D-state process load value exceeds the threshold, it triggers the collection of container runtime status and D-state process information.

### MemBurst
memburst detects short-term, large memory allocation events on the host. Sudden memory allocations may trigger direct reclaim or even OOM, so context information is recorded when such allocations occur.

### IOTracing
When I/O bandwidth is saturated or disk access surges suddenly, the system may experience increased request latency, performance jitter, or even overall instability due to I/O resource contention.

iotracing outputs context information—such as accessed filenames/paths, disk devices, inode numbers, and container names—during periods of high host disk load or abnormal I/O latency.
