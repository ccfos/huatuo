---
title: 自定义指标
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

### 概述

Metrics 类型用于采集系统性能等指标数据，可以 Prometheus 格式输出，作为 `/metrics`（`curl localhost:<port>/metrics`）的数据提供方。

- **类型**：指标采集
- **功能**：采集各子系统的性能指标
- **特点**：
  - 指标主要用于采集 CPU 使用率、内存使用量、网络统计等系统性能数据，适用于监控系统性能，支持实时分析和长期趋势观察。
  - 指标来源可以是常规 procfs/sysfs 采集，也可以由 tracing 类型（autotracing、event）生成。
  - 以 Prometheus 格式输出，无缝集成 Prometheus 可观测性生态。

- **已集成**：
    - CPU（sys、usr、util、load、nr_running...）
    - 内存（vmstat、memory_stat、directreclaim、asyncreclaim...）
    - IO（d2c、q2c、freeze、flush...）
    - 网络（arp、socket mem、qdisc、netstat、netdev、socketstat...）

### 如何添加统计指标

只需实现 `Collector` 接口并完成注册即可将指标添加到系统。

```go
type Collector interface {
    // Get new metrics and expose them via prometheus registry.
    Update() ([]*Data, error)
}
```

#### 1. 创建结构体

在 `core/metrics` 目录下创建实现 `Collector` 接口的结构体：

```go
type exampleMetric struct{}
```

#### 2. 注册回调函数
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

#### 3. 实现 `Update` 方法

```go
func (c *exampleMetric) Update() ([]*metric.Data, error) {
    // do something
    ...
    return []*metric.Data{
        metric.NewGaugeData("example", value, "description of example", nil),
    }, nil
}
```

项目 `core/metrics` 目录中已集成多种实用的 `Metrics` 示例，框架还提供了丰富的底层接口，包括 BPF 程序和 map 数据交互、容器信息等。更多详情请参考对应的代码实现。
