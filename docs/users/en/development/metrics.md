---
title: Add Metrics
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

### Overview

The Metrics type is used to collect system performance and other indicator data. It can output in Prometheus format, serving as a data provider through the `/metrics` (`curl localhost:<port>/metrics`) .

- **Type**：Metrics collection
- **Function**：Collects performance metrics  from various subsystems
- **Characteristics**：
  - Metrics are primarily used to collect system performance metrics such as CPU usage, memory usage, network statistics, etc. They are suitable for monitoring system performance and support real-time analysis and long-term trend observation.
  - Metrics can come from regular procfs/sysfs collection or be generated from tracing types (autotracing, event).
  - Outputs in Prometheus format for seamless integration into the Prometheus observability ecosystem.
 
- **Already Integrated**：
    - cpu (sys, usr, util, load, nr_running...)
    - memory（vmstat, memory_stat, directreclaim, asyncreclaim...）
    - IO (d2c, q2c, freeze, flush...)
    - Network（arp, socket mem, qdisc, netstat, netdev, socketstat...）

### How to Add Statistical Metrics

Simply implement the `Collector` interface and complete registration to add metrics to the system.

```go
type Collector interface {
    // Get new metrics and expose them via prometheus registry.
    Update() ([]*Data, error)
}
```

#### 1. Create a Structure
Create a structure that implements the `Collector` interface in the `core/metrics` directory:

```go
type exampleMetric struct{
}
```

#### 2. Register Callback Function
```go
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleMetric{},
        Flag: tracing.FlagMetric, // Mark as Metric type
    }, nil
}

```

#### 3. Implement the `Update` Method

```go
func (c *exampleMetric) Update() ([]*metric.Data, error) {
    // do something
    ...
	return []*metric.Data{
		metric.NewGaugeData("example", value, "description of example", nil),
	}, nil

}
```

The `core/metrics` directory in the project has integrated various practical  `Metrics` examples, along with rich underlying interfaces provided by the framework, including BPF program and map data interaction, container information, etc. For more details, refer to the corresponding code implementations.
