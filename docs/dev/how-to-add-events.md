English | [简体中文](./how-to-add-events_CN.md)

### Overview

- **Type**: Exception event-driven（tracing/event）
- **Function**：Continuously runs in the system and captures context information when preset thresholds are reached
- **Characteristics**: 
    - Unlike `autotracing`, `event` runs continuously rather than being triggered only when exceptions occur.
    - Event data is stored locally in real-time and also sent to remote ES. You can also generate Prometheus metrics for observation.
    - Suitable for **continuous monitoring** and **real-time analysis**, enabling timely detection of abnormal behaviors in the system. The performance impact of `event` type collection is negligible.
- **Already Integrated**: Soft interrupt abnormalities（softirq）、abnormal memory allocation（oom）、soft lockups（softlockup）、D-state processes（hungtask）、memory reclaim（memreclaim）、abnormal packet loss（dropwatch）、network inbound latency (netrecvlat), etc.

### How to Add Event Metrics
Simply implement the `ITracingEvent` interface and complete registration to add events to the system.
>There is no implementation difference between `AutoTracing` and `Event` in the framework; they are only differentiated based on practical application scenarios.

```go
// ITracingEvent represents a tracing/event
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

#### 1. Create Event Structure
```go
type exampleTracing struct{}
```

#### 2. Register Callback Function
```go
func init() {
    tracing.RegisterEventTracing("example", newExample)
}

func newExample() (*tracing.EventTracingAttr, error) {
    return &tracing.EventTracingAttr{
        TracingData: &exampleTracing{},
        Internal:    10, // Interval in seconds before re-enabling tracing
        Flag:        tracing.FlagTracing, // Mark as tracing type; | tracing.FlagMetric (optional)
    }, nil
}
```

#### 3. Implement the ITracingEvent Interface
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // do something
    ...

    // Store data to ES and locally
    storage.Save("example", ccontainerID, time.Now(), tracerData)
}
```

Additionally, you can optionally implement the Collector interface to output in Prometheus format:

```go
func (c *exampleTracing) Update() ([]*metric.Data, error) {
    // from tracerData to prometheus.Metric 
    ...

    return data, nil
}
```

The `core/events` directory in the project has integrated various practical `events` examples, along with rich underlying interfaces provided by the framework, including BPF program and map data interaction, container information, etc. For more details, refer to the corresponding code implementations.
