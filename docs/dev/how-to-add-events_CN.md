简体中文 | [English](./how-to-add-events.md)

### 概述

- **类型**：异常事件驱动（tracing/event）
- **功能**：常态运行在系统达到预设阈值后抓取上下文信息
- **特点**：
    - 与 `autotracing` 不同，`event` 是常态运行，而不是在异常时再触发。
    - 事件数据会实时存储在本地并存储到远端ES，同时你也可以生成Prometheus 统计指标进行观测。
    - 适合用于**常态监控**和**实时分析**，能够及时发现系统中的异常行为， `event` 类型的采集对系统性能影响可忽略。
- **已集成**：软中断异常（softirq）、内存异常分配（oom）、软锁定（softlockup）、D 状态进程（hungtask）、内存回收（memreclaim）、异常丢包（dropwatch）、网络入向延迟（net_rx_latency） 等

### 如何添加事件指标
只需实现 `ITracingEvent` 接口并完成注册，即可将事件添加到系统。
>`AutoTracing` 与 `Event` 类型在框架实现上没有任何区别，只是针对不同的场景进行了实际应用的区分。

```go
// ITracingEvent represents a tracing/event
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

#### 1. 创建 Event 结构体
```go
type exampleTracing struct{}
```

#### 2. 注册回调函数
```go
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleTracing{},
        Interval:    10, // 再次开启 tracing 的间隔时间 seconds
        Flag:        tracing.FlagTracing, // 标记为 tracing 类型；| tracing.FlagMetric（可选）
    }, nil
}
```

#### 3. 实现接口 ITracingEvent
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // do something
    ...

    // 存储数据到 ES 和 本地
    storage.Save("example", ccontainerID, time.Now(), tracerData)
}
```

另外也可同时实现接口 Collector 以 Prometheus 格式输出 （可选）

```go
func (c *exampleTracing) Update() ([]*metric.Data, error) {
    // from tracerData to prometheus.Metric 
    ...

    return data, nil
}
```

在项目 `core/events` 目录下已集成了多种实际场景的 `events` 示例，以及框架提供的丰富底层接口，包括 bpf prog, map 数据交互、容器信息等，更多详情可参考对应代码实现。
