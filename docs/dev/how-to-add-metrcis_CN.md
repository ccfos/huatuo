简体中文 | [English](./how-to-add-metrcis.md)

### 概述

Metrics 类型用于采集系统性能等指标数据，可输出为 Prometheus 格式，作为服务端对外提供数据，通过接口 `/metrics` (`curl localhost:<port>/metrics`) 获取。

- **类型**：指标数据采集
- **功能**：采集各子系统的性能指标数据
- **特点**：
  - metrics 主要用于采集系统的性能指标，如 CPU 使用率、内存使用率、网络等，适合用于监控系统的性能指标，支持实时分析和长期趋势观察。
  - 指标数据可以来自常规 procfs/sysfs 采集，也可以从 tracing (autotracing, event) 类型生成指标数据
  - Prometheus 格式输出，便于无缝集成到 Prometheus 观测体系
 
- **已集成**：
    - cpu (sys, usr, util, load, nr_running...)
    - memory（vmstat, memory_stat, directreclaim, asyncreclaim...）
    - IO (d2c, q2c, freeze, flush...)
    - 网络（arp, socket mem, qdisc, netstat, netdev, socketstat...）

### 如何添加统计指标

只需实现 `Collector` 接口并完成注册，即可将指标添加到系统中。

```go
type Collector interface {
    // Get new metrics and expose them via prometheus registry.
    Update() ([]*Data, error)
}
```

#### 1. 创建结构体
在 `core/metrics` 目录下创建实现 `Collector` 接口的结构体：

```go
type exampleMetric struct{
}
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

在项目 `core/metrics` 目录下已集成了多种实际场景的 `Metrics` 示例，以及框架提供的丰富底层接口，包括 bpf prog, map 数据交互、容器信息等，更多详情可参考对应代码实现。
