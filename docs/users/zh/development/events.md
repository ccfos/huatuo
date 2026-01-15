---
title: 自定义事件
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

只需实现 `ITracingEvent` 接口并完成注册即可。

```go
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

#### 创建
```go
type exampleTracing struct{}
```

#### 注册
```go
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleTracing{},
        Internal:    10, // 再次开启 tracing 的间隔时间，单位秒
        Flag:        tracing.FlagTracing, // 标记为 tracing 类型；tracing.FlagMetric（可选）
    }, nil
}
```

#### 实现 `Start`
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // do something
    ...

    // 存储数据到 ES 和 本地
    storage.Save("example", ccontainerID, time.Now(), tracerData)
}
```

此外，可同时实现接口 Collector 并以 Prometheus 格式输出 （可选）

```go
func (c *exampleTracing) Update() ([]*metric.Data, error) {
    // from tracerData to prometheus.Metric 
    ...

    return data, nil
}
```
