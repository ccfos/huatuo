# Native Profiler 进程树跟踪

Native CPU 与内存分析可以跟踪目标进程在分析启动后创建的进程和线程：

```bash
profiler --type cpu --language c --pid 1234 --duration 60 \
  --follow-forks --fork-max-procs 4096 --fork-rate 1000 --fork-burst 2000
```

`--follow-forks` 默认关闭，启用时必须指定 PID。它覆盖 native CPU、虚拟
内存分配、物理内存分配和物理内存用量模式。原始进程退出后，profiler
仍运行到配置的 duration，存活子进程会继续产生样本。

跟踪由事件驱动：eBPF 程序监听 `sched_process_fork`、
`sched_process_exec` 和 `sched_process_exit`，不会周期性扫描 `/proc`。
分析启动前已经属于根进程的工作线程可通过线程组 ID 识别；启动前已经
存在的其他后代不会回填。非 leader 线程调用 `exec` 并接管 leader PID
时，跟踪条目会迁移到新 PID，同时保留原进程树谱系。

## 防护与覆盖率

- `--fork-max-procs` 限制同时跟踪的后代 PID/TID 数。根进程不占名额。
  默认 `4096`，最大 `65536`。
- `--fork-rate` 限制所有 CPU 共用的一秒时间桶内的后代创建事件数。
  默认 `1000`；设为 `0` 可关闭这项速率限制。
- `--fork-burst` 为每个时间桶增加突发额度，默认 `2000`。

PID map 使用非预分配存储，并在加载 CollectionSpec 时改写容量：未启用
跟踪时只保留 1 个条目，启用时精确等于 `--fork-max-procs`。进程/线程退出
时会立即删除自己的条目；父进程退出不会删除仍存活子进程的独立条目。

profiler 停止时会输出汇总，包括当前条目数、接纳数、退出清理数、重复
事件数、map 更新失败数、容量拒绝数、速率拒绝数、exec 迁移数和最深代数。若日志出现
`health=limited` 警告，表示防护机制拒绝过事件，结果中的进程树覆盖可能
不完整。
