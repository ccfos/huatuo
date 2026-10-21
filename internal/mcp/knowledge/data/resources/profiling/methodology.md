# 自动 Profiler 分析方法论

## 概述

HuaTuo 的火焰图/Profile 数据可以是 frame 数组，也可以是包含
`profile_type`、`profile` 的 pprof 对象。事件 adapter 将两者规范化为 frame 数据。
`has_profiler` 表示该 tracer 有可解析的火焰图/Profile 数据。通过 `query_profiles`
获取结构化热点分析，包括 top 函数、self/total 占比和调用路径。它不读取手动
continuous profiling 的 pprof 文档。

`self_time`、`total_time` 是沿用的 API 名称，不代表所有 Profile 的权重都是 CPU 时间。
解读 pprof 时必须先看 `summary.sample_type`、`sample_unit`：`cpu/nanoseconds` 表示
CPU 时间，IRQ 等计数型 Profile 表示事件次数。frame 数组若未提供类型和单位，须结合
数据源确认其含义，不能仅凭函数名或分析类型猜测单位。

## 分析类型

### self_time（自时间）

- **定义**：归属于函数自身的采样权重，不包含子调用的权重
- **适用场景**：定位自身权重最集中的函数
- **解读**：self_percent 高表示该函数自身占总权重的比例高；只有确认是 CPU 时间
  Profile 后，才能进一步讨论 CPU 消耗，不能把 IRQ 次数占比当作 CPU 时间占比。

### total_time（总时间）

- **定义**：函数及其子调用的 inclusive 采样权重
- **适用场景**：理解调用链路的权重分布
- **解读**：total_percent 高表示该函数及子调用累计权重占比高；它表示的是 CPU 时间、
  事件次数还是其他量，由 Profile 的采样类型和单位决定。

### 选择策略

| 场景 | 推荐类型 | 原因 |
|------|---------|------|
| 定位热点函数 | self_time | 找到自身采样权重最大的函数 |
| 理解调用链 | total_time | 看清哪条路径整体最贵 |
| 区分用户/内核 | self_time | 精确定位是哪个函数 |
| 判断模块开销 | total_time | 按 call stack 聚合 |

## 读取步骤

### Step 1: 确认数据有效性

检查返回的 `summary` 字段：
- `total_samples`：参与分析的总权重。pprof 对象须结合 `sample_type`、`sample_unit`
  解读：`cpu/nanoseconds` 是累计 CPU 时间，不是采样条数；只有计数权重才能应用
  “采样数过低”的数量阈值。
- 相邻多个异常的合并热点不是单次异常的热点。需要按事件定位时，分别以
  `query_events` 的 `source.record_id` 调用 `query_profiles(record_id=...,region=[...])`，
  且每次仍指定相同目标、tracer 和时间范围，再比较两次结果。
- `unique_functions`：不同函数数量
- `documents`：成功解析的自动 profiler 文档数
- `unsupported_documents`：无法解析为有效 frame 数组或 pprof 样本的文档数
- `payload_format`：当前格式为 `frame_data`
- `coverage`：返回的 top_n 函数 self 权重之和占总权重的百分比；不是主机 CPU 利用率，
  也不表示查询覆盖了全部事件或进程。

同时保留 `sources` 中的原生 `source`。它们标识参与聚合且成功
解析的自动 profiler 文档，可由调用方 Skill 用于定位原始 profile；MCP 不生成查看
链接。

### Step 2: 定位 self_time 热点

从 `top_functions` 中按 `self_percent` 降序看前 5 个：
- 是否有单一函数 self_percent > 50% → 该函数自身权重集中；仅在 CPU Profile 中
  将其作为计算热点线索，其他类型按对应单位解释。
- 是否有多个函数 self_percent 接近 → 分散型开销，需看调用栈
- 热点函数是内核函数还是用户态函数 → 决定排查方向

### Step 3: 分析调用栈

对每个热点函数查看 `call_paths[].path`：
- path 从当前热点函数沿调用者向根排列（index 0 是热点函数）。
- `total_time` 即使热点函数自身没有 self samples，仍返回其
  调用上下文及 inclusive `total_samples`；`self_time` 只返回有 self samples 的路径。
- 关注路径上的关键内核函数，对照 `huatuo://profiling/kernel-signatures` 查含义
- 多个热点函数共享同一段调用栈前缀 → 共同根因

### Step 4: 交叉验证

- profiler 热点 + ES 事件 key_fields → 互相印证
- profiler 热点 + Prometheus 指标趋势 → 确认是持续还是瞬时
- profiler 热点 + patterns 匹配 → 定位根因模式

## 常见 Profiler 热点模式

以下 CPU 开销判断仅适用于已确认的 CPU Profile。IRQ 计数等其他类型出现相同函数时，
只能按其采样类型和单位解释，不能直接套用 CPU 消耗结论。

### 计算密集型

```
特征：单一用户态函数 self_percent > 80%
call_stack：[app_func → libc → ...]
判定：应用代码本身消耗 CPU
```

### 内核开销型

```
特征：多个内核函数 self_percent 分散但合计高
call_stack：[kernel_func → syscall → user_func]
判定：应用通过 syscall 触发内核开销
排查方向：看 kernel_func 属于哪个子系统（内存/IO/网络/锁）
```

### 锁争抢型

```
特征：native_queued_spin_lock_slowpath 或 rwsem_slowpath 占比高
call_stack：[spin_lock_slowpath → caller → ...]
判定：多核竞争同一把锁
排查方向：看 caller 是谁，定位锁保护的资源
```

### IO 等待型

```
特征：io_schedule / blkdev_issue_flush / wait_on_page_bit 占比高
call_stack：[io_schedule → submit_bio → ...]
线索：样本涉及 IO 等待相关路径；CPU Profile 不能衡量实际阻塞时长
排查方向：结合 iotracing 事件和等待指标确认磁盘延迟
```

### 内存回收型

```
特征：shrink_node / shrink_slab / try_to_free_pages 占比高
call_stack：[shrink_node → try_to_free_pages → alloc_pages → page_fault]
判定：内存压力触发回收
排查方向：结合 memburst 事件确认内存不足
```

### 软中断型

```
特征：net_rx_action / napi_poll / net_tx_action 占比高
call_stack：[napi_poll → net_rx_action → __do_softirq]
判定：网络包处理消耗 CPU
排查方向：结合 dropwatch 事件确认是否有丢包
```

## 注意事项

1. **按类型解释权重**：CPU 采样 Profile 反映 ON-CPU 时间，不包含 IO/sleep 的阻塞时长；
   IRQ 等计数型 Profile 反映次数分布。不能将所有自动 Profile 都解释成 CPU 时间。
2. **采样精度**：结合采集方式、采样频率及时间窗口判断证据是否充分。只有确认权重为
   采样次数时才能参考样本数量；不能对纳秒、字节等权重套用 `total_samples < 100` 规则。
3. **容器 vs 宿主机**：容器 profile 只包含容器内进程的采样；宿主机 profile 包含所有进程。
4. **多线程**：profile 聚合了所有线程的采样，线程间的锁争抢会体现在 spin_lock 等函数上。
5. **调用栈深度**：call_stack 可能很长，重点关注前 5-10 层即可定位根因。
