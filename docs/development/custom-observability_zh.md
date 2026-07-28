---
title: 扩展观测能力
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 1
---

HUATUO 支持 Metrics、Event 和 AutoTracing 三种观测扩展。三者共享注册机制，
但触发方式、运行开销和数据输出不同。

| 类型 | 触发方式 | 数据输出 | 适用场景 |
| --- | --- | --- | --- |
| Metrics | 周期采集 | Prometheus | 持续观测系统性能和长期趋势 |
| Event | 内核事件或阈值触发 | ES、本地文件，可选 Prometheus | 常态运行并捕获异常现场 |
| AutoTracing | 系统异常触发 | ES、本地文件，可选 Prometheus | 按需执行开销较高的上下文采集 |

## 模式说明

### Metrics

Metrics 通过 procfs、sysfs 或 eBPF 周期采集系统状态，以 Prometheus 格式输出，
适合实时监控和长期趋势分析。内置采集能力包括：

- CPU：sys、usr、util、load、nr_running 等。
- 内存：vmstat、memory_stat、directreclaim、asyncreclaim 等。
- IO：d2c、q2c、freeze、flush 等。
- 网络：ARP、socket memory、qdisc、netstat、netdev、sockstat 等。

### Event

Event 常态监听内核事件或阈值条件，在异常发生时保存内核运行上下文。该模式
面向需要持续开启的低开销观测，数据写入 Elasticsearch 和本地文件，也可以
同步生成 Prometheus 指标。内置事件包括：

- 软中断异常 `softirq_tracing`。
- 内存异常分配 `oom`。
- 软锁定 `softlockup`。
- D 状态进程 `hungtask`。
- 内存回收 `memory_reclaim_events`。
- 异常丢包 `dropwatch`。
- 网络接收延迟 `net_rx_latency`。

### AutoTracing

AutoTracing 在检测到系统异常后自动调用诊断工具，采集火焰图或上下文快照等
现场信息。它适合采集成本较高、无法持续运行的诊断数据。结果写入
Elasticsearch 和本地文件，也可以转换为 Prometheus 指标。内置能力包括：

- CPU 空闲和系统态异常追踪 `cpuidle`、`cpusys`。
- D 状态负载追踪 `dload`。
- 内存突发分配追踪 `memburst`。
- 磁盘 IO 异常追踪 `iotracing`。

Event 和 AutoTracing 都属于 Tracing 模式，共享 `ITracingEvent` 接口。它们既能
保存异常上下文用于根因分析，也能通过实现 `Collector` 将统计结果暴露给
Prometheus。

## 添加 Metrics

自定义 Metrics 通过 `/metrics` 接口输出 Prometheus 指标。

### 实现 Collector

在 `core/metrics` 下创建实现 `Collector` 接口的类型：

```go
type Collector interface {
    Update() ([]*Data, error)
}

type exampleMetric struct{}

func (c *exampleMetric) Update() ([]*metric.Data, error) {
    return []*metric.Data{
        metric.NewGaugeData("example", value, "example value", nil),
    }, nil
}
```

### 注册采集器

通过 `FlagMetric` 将实现注册为指标采集器：

```go
func init() {
    tracing.RegisterEventTracing("example", newExampleMetric)
}

func newExampleMetric() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleMetric{},
        Flag:        tracing.FlagMetric,
    }, nil
}
```

### 管理 BPF 对象

同一个实现同时提供 `Start` 和 `Update` 时，两个方法可能并发执行。不要直接在
collector 字段中读写 `bpf.BPF` 接口；使用
[`internal/bpf/bpf_ref.go`](../../internal/bpf/bpf_ref.go) 提供的 `Reference`
和 `Lease` 管理对象生命周期：

```go
type example struct {
    object bpf.Reference
}

func (c *example) Start(ctx context.Context) (retErr error) {
    object, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
    if err != nil {
        return err
    }

    if err := object.Attach(); err != nil {
        return errors.Join(err, object.Close())
    }
    if err := c.object.Publish(object); err != nil {
        return errors.Join(err, object.Close())
    }
    defer func() {
        retErr = errors.Join(retErr, c.object.UnPublish())
    }()

    <-ctx.Done()
    return nil
}

func (c *example) Update() ([]*metric.Data, error) {
    lease, ok := c.object.Acquire()
    if !ok {
        return nil, nil
    }
    defer lease.Release()

    items, err := lease.DumpMapByName("example_map")
    if err != nil {
        return nil, fmt.Errorf("dump example_map: %w", err)
    }

    return buildMetrics(items), nil
}
```

接口约束：

- `Publish` 将对象所有权转移给 `Reference`。成功后不要直接调用`object.Close()`。
- `Acquire` 返回的 `Lease` 将 BPF 对象固定到本次 `Update` 结束；必须配对调用`Release`，且不要复制 `Lease`。
- `UnPublish` 先阻止新的 `Acquire`，再等待所有 Lease 释放，最后关闭 BPF 对象并返回关闭错误。
- `Publish` 和 `UnPublish` 必须串行调用。当前框架保证同一实例的 `Start` 不会并发执行。

因此，已经开始的 `Update` 可以完整使用原 BPF 对象；`Start` 退出或重启时，
对象只会在这些 `Update` 完成后关闭。

## 添加 Event

Event 需要实现 `ITracingEvent`：

```go
type ITracingEvent interface {
    Start(ctx context.Context) error
}

type exampleEvent struct{}

func (e *exampleEvent) Start(ctx context.Context) error {
    // 检测事件并采集上下文。
    // storage.Save 将数据写入 ES 和本地存储。
    storage.Save("example", containerID, time.Now(), eventData)
    return nil
}
```

注册时使用 `FlagTracing`：

```go
func init() {
    tracing.RegisterEventTracing("example", newExampleEvent)
}

func newExampleEvent() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleEvent{},
        Interval:    10,
        Flag:        tracing.FlagTracing,
    }, nil
}
```

如果事件还需要输出 Prometheus 指标，可以同时实现 `Collector`，并将
`tracing.FlagMetric` 合并到 `Flag`。

## 添加 AutoTracing

AutoTracing 与 Event 使用相同的 `ITracingEvent` 接口和注册框架：

```go
type exampleAutoTracing struct{}

func (t *exampleAutoTracing) Start(ctx context.Context) error {
    // 异常触发后采集上下文。
    storage.Save("example", containerID, time.Now(), tracingData)
    return nil
}

func init() {
    tracing.RegisterEventTracing("example", newExampleAutoTracing)
}

func newExampleAutoTracing() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleAutoTracing{},
        Interval:    10,
        Flag:        tracing.FlagTracing,
    }, nil
}
```

项目中的 `core/metrics`、`core/events` 和 `core/autotracing` 提供了完整实现示例，
包括 BPF map 交互、容器信息获取、数据存储和 Prometheus 输出。
