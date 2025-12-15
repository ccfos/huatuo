简体中文 | [English](./how-to-add-autotracing.md)

### 概述
- **类型**：异常事件驱动（tracing/autotracing）
- **功能**：自动跟踪系统异常状态，并在异常发生时再触发抓取现场上下文信息
- **特点**：
    - 当系统出现异常时，`autotracing` 会自动触发，捕获相关的上下文信息
    - 事件数据会实时存储在本地并存储到远端ES，同时你也可以生成Prometheus 统计指标进行观测。
    - 适用于获取现场时**性能开销较大的场景**，例如检测到指标上升到一定阈值、上升速度过快再触发抓取
- **已集成**：cpu 异常使用跟踪（cpu idle）、D状态跟踪（dload）、容器内外部争抢（waitrate）、内存突发分配（memburst）、磁盘异常跟踪（iotracer）

### 如何添加 Autotracing ？
`AutoTracing` 只需实现 `ITracingEvent` 接口并完成注册，即可将事件添加到系统中。
>`AutoTracing` 与 `Event` 类型在框架实现上没有任何区别，只是针对不同的场景进行了实际应用的区分。

```go
// ITracingEvent represents a autotracing or event
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

#### 1. 创建结构体
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
        Flag:        tracing.FlagTracing, // 标记为 tracing 类型； | tracing.FlagMetric（可选）
    }, nil
}
```

#### 3. 实现接口 ITracingEvent
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // detect your care about 
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

在项目 `core/autotracing` 目录下已集成了多种实际场景的 `autotracing` 示例，以及框架提供的丰富底层接口，包括 bpf prog，map 数据交互、容器信息等，更多详情可参考对应代码实现。
