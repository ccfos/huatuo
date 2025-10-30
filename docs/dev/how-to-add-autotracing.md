English | [简体中文](./how-to-add-autotracing_CN.md)

### Overview
- **Type**：Exception event-driven（tracing/autotracing）
- **Function**：Automatically tracks system abnormal states and triggers context information capture when exceptions occur
- **Characteristics**：
    - When system abnormalities occur, `autotracing` automatically triggers and captures relevant context information
    - Event data is stored locally in real-time and also sent to remote ES, while you can also generate Prometheus metrics for observation
    - Suitable for **significant performance overhead**， such as triggering capture when detecting metrics rising above certain thresholds or rising too rapidly
- **Already Integrated**：abnormal usage tracking (cpu idle), D-state tracking (dload), container internal/external contention (waitrate), sudden memory allocation (memburst), disk abnormal tracking (iotracer)

### How to Add Autotracing
`AutoTracing` only requires implementing the `ITracingEvent` interface and completing registration to add events to the system.
>There is no implementation difference between `AutoTracing` and `Event` in the framework; they are only differentiated based on practical application scenarios.

```go
// ITracingEvent represents a autotracing or event
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

#### 1. Create Structure
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

#### 3. Implement ITracingEvent
```go
func (t *exampleTracing) Start(ctx context.Context) error {
    // detect your care about 
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

The `core/autotracing` directory in the project has integrated various practical `autotracing` 示examples, along with rich underlying interfaces provided by the framework, including BPF program and map data interaction, container information, etc. For more details, refer to the corresponding code implementations.
