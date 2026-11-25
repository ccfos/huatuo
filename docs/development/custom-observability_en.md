---
title: Extending Features
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 1
---

HUATUO supports three observability extensions: Metrics, Event, and AutoTracing. They share a registration mechanism but differ in triggers, runtime cost, and output.

| Type | Trigger | Output | Use Case |
| --- | --- | --- | --- |
| Metrics | Periodic collection | Prometheus | Continuous system monitoring and long-term trends |
| Event | Kernel events or thresholds | ES, local files, optional Prometheus | Continuous observation that captures abnormal conditions |
| AutoTracing | Detected system anomalies | ES, local files, optional Prometheus | More expensive context collection on demand |

## Modes

### Metrics

Metrics periodically collects system state through procfs, sysfs, or eBPF and exports it in Prometheus format for real-time monitoring and trend analysis. Built-in collectors cover:

- CPU: sys, usr, util, load, nr_running, and more.
- Memory: vmstat, memory_stat, directreclaim, asyncreclaim, and more.
- I/O: d2c, q2c, freeze, flush, and more.
- Network: ARP, socket memory, qdisc, netstat, netdev, sockstat, conntrack entry counts and limits, and more.

### Event

Event continuously watches kernel events or thresholds and saves kernel context when an anomaly occurs. It targets low-overhead observation that can remain enabled. Data is written to Elasticsearch and local files and can also produce Prometheus metrics. Built-in events include:

- Abnormal scheduling tick intervals: `sched_tick`.
- OOM kills: `memory_oom_kill`.
- Soft lockups: `softlockup`.
- Tasks in D state: `hungtask`.
- Memory reclaim: `memory_reclaim_events`.
- Packet drops: `dropwatch`.
- Network receive latency: `net_rx_latency`.

### AutoTracing

AutoTracing invokes diagnostic tools after detecting a system anomaly, collecting flame graphs, context snapshots, and other evidence. It suits diagnostic data that is too costly to collect continuously. Results are written to Elasticsearch and local files and can also be converted into Prometheus metrics. Built-in capabilities include:

- Container CPU and host system-mode CPU tracing: `cpuidle`, `cpusys`.
- D-state load tracing: `dload`.
- Memory allocation burst tracing: `memburst`.
- Disk I/O anomaly tracing: `iotracing`.

Event and AutoTracing both use Tracing mode and share the `ITracingEvent` interface. They save abnormal context for root-cause analysis and can expose statistics to Prometheus by also implementing `Collector`.

## Add Metrics

Custom Metrics exports Prometheus metrics through `/metrics`.

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

### Register the Collector

Register the implementation as a metric collector with `FlagMetric`:

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

### Manage BPF Objects

If an implementation provides both `Start` and `Update`, these methods may run concurrently. Do not read or write a `bpf.BPF` interface directly in collector fields. Use `Reference` and `Lease` from [`internal/bpf/bpf_ref.go`](../../internal/bpf/bpf_ref.go) to manage the object lifecycle:

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

Interface contracts:

- `Publish` transfers object ownership to `Reference`. After success, do not call `object.Close()` directly.
- The `Lease` returned by `Acquire` keeps the BPF object alive until the current `Update` completes. Pair it with `Release` and do not copy the `Lease`.
- `UnPublish` blocks new `Acquire` calls, waits for all leases to be released, then closes the BPF object and returns any close error.
- Serialize `Publish` and `UnPublish`. The current framework ensures that `Start` does not run concurrently for the same instance.

An `Update` that has already started can therefore finish using the original BPF object. When `Start` exits or restarts, the object is closed only after those `Update` calls complete.

## Add an Event

An Event must implement `ITracingEvent`:

```go
type ITracingEvent interface {
    Start(ctx context.Context) error
}

type exampleEvent struct{}

func (e *exampleEvent) Start(ctx context.Context) error {
    // Detect the event and collect context.
    // storage.Save writes data to ES and local storage.
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

To also export Prometheus metrics, implement `Collector` and add `tracing.FlagMetric` to `Flag`.

## Add AutoTracing

AutoTracing uses the same `ITracingEvent` interface and registration framework as Event:

```go
type exampleAutoTracing struct{}

func (t *exampleAutoTracing) Start(ctx context.Context) error {
    // Collect context after an anomaly triggers tracing.
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

See `core/metrics`, `core/events`, and `core/autotracing` for complete implementations covering BPF map access, container information, data storage, and Prometheus output.
