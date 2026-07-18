---
title: Extending Observability
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 1
---

HUATUO supports three extension types: Metrics, Event, and AutoTracing. They use
the same registration framework but differ in activation, runtime cost, and
data output.

| Type | Activation | Data output | Use case |
| --- | --- | --- | --- |
| Metrics | Periodic collection | Prometheus | Continuous performance monitoring and long-term trends |
| Event | Kernel event or threshold | ES, local files, optional Prometheus | Continuous operation with anomaly context capture |
| AutoTracing | System anomaly | ES, local files, optional Prometheus | On-demand, higher-cost context capture |

## Collection Modes

### Metrics

Metrics periodically collect system state through procfs, sysfs, or eBPF and
expose it in Prometheus format. This mode supports real-time monitoring and
long-term trend analysis. Built-in collectors cover:

- CPU: sys, usr, util, load, nr_running, and related metrics.
- Memory: vmstat, memory_stat, directreclaim, and asyncreclaim.
- I/O: d2c, q2c, freeze, and flush.
- Networking: ARP, socket memory, qdisc, netstat, netdev, and sockstat.

### Event

Events continuously observe kernel events or threshold conditions and preserve
kernel context when an anomaly occurs. This mode is intended for low-overhead,
always-on observation. Data is written to Elasticsearch and local files and
can also produce Prometheus metrics. Built-in events include:

- Soft interrupt anomalies (`softirq_tracing`).
- Abnormal memory allocation (`oom`).
- Soft lockups (`softlockup`).
- D-state processes (`hungtask`).
- Memory reclaim (`memory_reclaim_events`).
- Packet drops (`dropwatch`).
- Network receive latency (`net_rx_latency`).

### AutoTracing

AutoTracing invokes diagnostic tools after detecting a system anomaly. It is
intended for flame graphs, context snapshots, and other diagnostic operations
that are too expensive to run continuously. Results are written to
Elasticsearch and local files and can also be converted into Prometheus
metrics. Built-in capabilities include:

- CPU idle and system-time anomaly tracing (`cpuidle`, `cpusys`).
- D-state load tracing (`dload`).
- Burst memory allocation tracing (`memburst`).
- Disk I/O anomaly tracing (`iotracing`).

Event and AutoTracing are both Tracing modes and share the `ITracingEvent`
interface. They can preserve anomaly context for root-cause analysis and expose
statistics to Prometheus by also implementing `Collector`.

## Adding Metrics

Custom Metrics collectors expose Prometheus metrics through `/metrics`.

### Implement Collector

Create a type under `core/metrics` that implements `Collector`:

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

### Register the collector

Use `FlagMetric` when registering the implementation:

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

## Adding an Event

An Event implements `ITracingEvent`:

```go
type ITracingEvent interface {
    Start(ctx context.Context) error
}

type exampleEvent struct{}

func (e *exampleEvent) Start(ctx context.Context) error {
    // Detect the event and capture its context.
    // storage.Save writes the data to ES and local storage.
    storage.Save("example", containerID, time.Now(), eventData)
    return nil
}
```

Register it with `FlagTracing`:

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

To expose Prometheus metrics for the event, also implement `Collector` and add
`tracing.FlagMetric` to `Flag`.

## Adding AutoTracing

AutoTracing and Event use the same `ITracingEvent` interface and registration
framework:

```go
type exampleAutoTracing struct{}

func (t *exampleAutoTracing) Start(ctx context.Context) error {
    // Capture context after the anomaly trigger fires.
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

See `core/metrics`, `core/events`, and `core/autotracing` for complete examples
covering BPF map interaction, container metadata, storage, and Prometheus
output.
