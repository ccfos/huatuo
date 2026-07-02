---
title: 自定义追踪
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 4
---

### 概述
- **类型**：异常事件驱动（tracing/autotracing）
- **功能**：自动追踪系统异常状态，在异常发生时触发上下文信息捕获
- **特点**：
    - 当系统出现异常时，`autotracing` 自动触发并捕获相关上下文信息
    - 事件数据实时存储到本地，同时发送到远程 ES，还可以生成 Prometheus 指标进行观测
    - 适用于**性能开销较大**的场景，例如在检测到指标超过阈值或上升过快时触发捕获
- **已集成**：CPU 空闲异常追踪（cpu idle）、D 状态追踪（dload）、容器内外部竞争（waitrate）、内存突发分配（memburst）、磁盘异常追踪（iotracer）

### 如何添加 Autotracing
`AutoTracing` 只需实现 `ITracingEvent` 接口并完成注册即可将事件添加到系统。
> `AutoTracing` 与 `Event` 在框架实现上没有区别，只是根据实际应用场景进行区分。

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
        Internal:    10, // 重新触发追踪的间隔（秒）
        Flag:        tracing.FlagTracing, // 标记为 tracing 类型；| tracing.FlagMetric（可选）
    }, nil
}
```

#### 3. 实现 ITracingEvent
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // 检测你关注的内容
    ...

    // 将数据存储到 ES 和本地
    storage.Save("example", ccontainerID, time.Now(), tracerData)
}
```

此外，可以选择实现 Collector 接口以 Prometheus 格式输出：

```go
func (c *exampleTracing) Update() ([]*metric.Data, error) {
    // 将 tracerData 转换为 prometheus.Metric
    ...

    return data, nil
}
```

项目 `core/autotracing` 目录中已集成多种实用的 `autotracing` 示例，框架还提供了丰富的底层接口，包括 BPF 程序和 map 数据交互、容器信息等。更多详情请参考对应的代码实现。
