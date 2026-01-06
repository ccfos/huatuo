[English](./CUSTOM.md) | 简体中文

本框架提供三种数据采集模式：`autotracing`、`event` 和 `metrics`，分别针对不同的监控场景和需求，帮助用户全面掌握系统的运行状态。

## 采集模式对比
| 模式             | 类型           | 触发条件       | 数据输出          | 适用场景         |
|-------------    |----------------|--------------|------------------|-----------------|
| **Autotracing** | 异常事件驱动     | 系统异常时触发  | ES + 本地存储，Prometheus（可选）| 不能常态运行，异常时触发运行 |
| **Event**       | 异常事件驱动     | 常态运行       | ES + 本地存储，Prometheus（可选）| 常态运行，直接抓取上下文信息 |
| **Metrics**     | 指标数据采集     | 被动采集       | Prometheus 格式  | 监控系统性能指标   |

-  **Autotracing**
   - **类型**：异常事件驱动（tracing）。
   - **功能**：自动跟踪系统异常状态，并在异常发生时再触发抓取现场上下文信息。
   - **特点**：
     - 当系统出现异常时，`autotracing` 会自动触发，捕获相关的上下文信息。
     - 数据会实时上报到 ES 并存储在本地，便于后续分析和排查问题，也可通过 Prometheus 格式进行监控，便于统计和告警。
     - 适用于获取现场时性能开销较大的场景，例如检测到指标上升到一定阈值、上升速度过快再触发抓取。
   - **已集成**：cpu 异常使用跟踪（cpu idle）、D状态跟踪（dload）、容器内外部争抢（waitrate）、内存突发分配（memburst）、磁盘异常跟踪（iotracer）。

- **Event**
   - **类型**：异常事件驱动（tracing）。
   - **功能**：常态运行在系统上下文中，达到预设阈值直接抓取上下文信息。
   - **特点**：
     - 与 `autotracing` 不同，`event` 是常态运行，而不是在异常时再触发。
     - 数据同样会实时上报到 ES 并存储在本地，也可通过 Prometheus 格式进行监控。
     - 适合用于常态监控和实时分析，能够及时发现系统中的异常行为， `event` 类型的采集对系统性能影响可忽略。
   - **已集成**：软中断异常（softirq）、内存异常分配（oom）、软锁定（softlockup）、D 状态进程（hungtask）、内存回收（memreclaim）、异常丢包（dropwatch）、网络入向延迟（net_rx_latency）。

- **Metrics**
   - **类型**：指标数据采集。
   - **功能**：采集各子系统的性能指标数据。
   - **特点**：
     - 指标数据可以来自常规 procfs 采集，也可以从 `tracing` (autotracing,event) 类型获取数据。
     - 以 Prometheus 格式输出，便于集成到 Prometheus 监控系统中。
     - 与 `tracing` 类数据不同，`metrics` 主要用于采集系统的性能指标，如 CPU 使用率、内存使用率、网络等。
     - 适合用于监控系统的性能指标，支持实时分析和长期趋势观察。
   - **已集成**：cpu (sys, usr, util, load, nr_running...), memory（vmstat, memory_stat, directreclaim, asyncreclaim...）, IO(d2c, q2c, freeze, flush...), 网络（arp, socket mem, qdisc, netstat, netdev, socketstat...）

## Tracing 模式的多重用途
`autotracing` 和 `event` 都属于 **tracing** 类数据采集模式，它们具备以下双重用途：
1. **实时保存到 ES 和 本地存储**：用于异常事件的追踪和分析，帮助用户快速根因定位。
2. **以 Prometheus 格式输出**：作为指标数据集成到 Prometheus 监控系统中，提供更全面的系统监控能力。

通过这三种模式的灵活组合，用户可以全面监控系统的运行状态，既能捕获异常事件的上下文信息，也能持续采集性能指标数据，满足不同场景下的监控需求。

# 如何添加自定义采集
框架提供了非常便捷的 API，包括模块启动、数据存储、容器信息、bpf 相关 （load, attach, read, detach, unload）等，用户可通过自定义的采集逻辑，灵活选择合适的采集模式和数据存储的方式。

## tracing 类型
根据实际场景，你可以在 `core/autotracing` 或 `core/events` 目录下实现接口 `ITracingEvent` 即可完成 tracing 类型的采集。
```go
// ITracingEvent represents a tracing/event
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

步骤如下：
```go
type exampleTracing struct{}

// 注册回调
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

// 创建 tracing
func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleTracing{},
        Interval:    10, // 再次开启 tracing 的间隔时间 seconds
        Flag:        tracing.FlagTracing, // 标记为 tracing 类型
    }, nil
}

// 实现接口 ITracingEvent
func (t *exampleTracing) Start(ctx context.Context) error {
    // do something
    ...

    // 存储数据到 ES 和 本地
    storage.Save("example", ccontainerID, time.Now(), tracerData)
}

// 也可同时实现接口 Collector 以 Prometheus 格式输出 （可选）
func (c *exampleTracing) Update() ([]*metric.Data, error) {
    // from tracerData to prometheus.Metric 
    ...

    return data, nil
}
```

## Metric 类型
在 `core/metrics` 目录下添加接口 `Collector` 的实现即可完成 Metric 类型的采集。

```go
type Collector interface {
    // Get new metrics and expose them via prometheus registry.
    Update() ([]*Data, error)
}
```

步骤如下：
```go
type exampleMetric struct{}

// 注册回调
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

// 创建 Metric
func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &filenrCollector{
            metric: []*metric.Data{
                metric.NewGaugeData("name1", 0, "description of example_name1", nil),
                metric.NewGaugeData("name2", 0, "description of example_name2", nil),                
            },
        },
        Flag: tracing.FlagMetric, // 标记为 Metric 类型
    }, nil
}

// 实现接口 Collector 以 Prometheus 格式输出
func (c *exampleMetric) Update() ([]*metric.Data, error) {
    // do something
    ...

    return data, nil
}
```

在项目 core 目录下已集成了 3 个采集模块的多种实际场景的示例，包括 bpf 代码、map 数据交互、容器信息等，更多详情可参考对应代码实现。
