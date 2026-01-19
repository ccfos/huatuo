---
title: 自定义指标
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

只需实现 `Collector` 接口并完成注册即可。

```go
type Collector interface {
    Update() ([]*Data, error)
}
```

#### 创建

在 `core/metrics/your-new-metric` 目录创建 `Collector` 接口的结构体：

```go
type exampleMetric struct{}
```

#### 注册
```go
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleMetric{},
        Flag: tracing.FlagMetric, // 标记为 Metric 类型
    }, nil
}

```

#### 实现 `Update`

```go
func (c *exampleMetric) Update() ([]*metric.Data, error) {
    // do something
    return []*metric.Data{
        metric.NewGaugeData("example", value, "description of example", nil),
    }, nil
}
```

框架提供的丰富底层接口，包括 eBPF, Procfs, Cgroups, Storage, Utils, Pods 等。
