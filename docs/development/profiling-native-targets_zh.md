# Native Profiling 目标选择器

Profiles API 支持以下可选的 native profiling 目标选择器：

| 字段 | 含义 |
| --- | --- |
| `container_id` | 通过解析后的 cgroup 元数据选择容器 |
| `pid` | 选择一个进程或线程 ID |
| `thread_group` | 将 `pid` 视为线程组成员，并按其 TGID 采集 |
| `cpu_ids` | 将 native CPU profiling 限制到指定 CPU |

`pid` 和 `container_id` 互斥。Native memory profiling 必须且只能指定其中
一个；native CPU profiling 可同时省略两者，从而进行主机级采集。

Java 和 Python profiling 同样必须且只能指定 `pid` 或 `container_id`。
CPU 选择和线程组扩展仍然只适用于 native profiler。

`thread_group` 必须与 `pid` 一起使用。Agent 会读取
`/proc/<pid>/status` 中的 `Tgid`，因此调用者可以传入非主线程 ID。不提供
单独的 process-group 选择器。

CPU ID 必须非负且不能重复。API 会排序以保证请求稳定，Agent 会根据目标
主机的 CPU 数量进行最终范围校验。

控制面会将 `container_id` 传递给 profiler。Profiler 使用容器对应的 cgroup
子系统元数据进行过滤，调用者不需要提供 cgroup 文件系统路径。
