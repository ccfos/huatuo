---
title: 内核事件订阅
type: docs
description: ""
author: HUATUO Team
date: 2026-07-27
weight: 3
---

{{% alert color="info" title="🎯 关于 HUATUO（华佗）" %}}
<div style="text-align: center;">
HUATUO（华佗）是由滴滴开源并依托 CCF（中国计算机学会）孵化的操作系统深度观测项目，专注为云原生通用计算、AI 计算、云服务、基础服务等提供操作系统内核级深度观测能力。
</div>
{{% /alert %}}

## 📖 概述

`/v1/events/watch` 是华佗（HUATUO）提供的实时内核事件订阅接口。客户端通过一次 HTTP POST 长连接即可持续接收节点上发生的内核异常事件。事件以 [CloudEvents 1.0](https://cloudevents.io/) 规范封装，通过 [Server-Sent Events（SSE）](https://html.spec.whatwg.org/multipage/server-sent-events.html) 协议推送。

---

## 🎯 应用场景

内核事件订阅将操作系统层的异常信号直接暴露给上层系统，消除了传统轮询带来的延迟与开销。以下是典型的集成场景。

### 故障自愈系统

内核事件是自愈决策的第一手信号源。订阅 `events/watch` 后，自愈控制器可在事件发生的瞬间触发处置动作，而不必等待监控系统的告警流转：

- **OOM 自愈**：收到 `oom` 事件后，立即对触发容器执行扩容、重启或流量摘除，将服务中断时间从分钟级压缩到秒级。
- **Hung Task 自愈**：收到 `hungtask` 事件后，自动隔离节点并驱逐 Pod，防止级联阻塞蔓延至整个集群。
- **网络故障自愈**：收到 `netdev_txqueue_timeout` 或 `netdev_bonding_lacp` 事件后，触发网卡重置或流量切换，实现分钟级网络链路自愈。
- **I/O 风暴自愈**：收到 `iotracing` 事件后，结合 cgroup blkio 限速策略动态降低问题容器的磁盘 I/O 配额，保护同节点其他服务。

### 可观测性平台

将华佗内核事件接入可观测性平台，补齐应用指标和日志之外的内核视角：

- **事件时间线关联**：将 `softlockup`、`oom` 等内核事件叠加到 Grafana 时间线上，与应用错误率、延迟曲线精确对齐，快速定位根因。
- **异常驱动告警**：以内核事件替代固定阈值告警，降低误报率。例如收到 `ras` 硬件错误事件时直接触发高优告警，而不依赖 CPU 错误率超阈值。
- **容量与稳定性分析**：长期订阅 `memburst`、`dload` 等 AutoTracing 事件，建立节点稳定性基线，为容量规划提供内核级依据。
- **多维下钻**：事件中携带容器 ID、命名空间、地域等上下文，告警链接可直接下钻到对应的 Pod、Node、Region 视图。

### 安全审计与合规

- **异常行为检测**：`oom`、`hungtask`、`softlockup` 等事件若在非业务高峰期集中出现，可能指示资源滥用或恶意负载，触发安全审查流程。
- **事件留存与追溯**：将 CloudEvents 事件流写入消息队列（Kafka、Pulsar）或对象存储，满足等保合规对系统异常事件留存的要求。

### 混沌工程与压测验证

- **故障注入验证**：混沌工程平台注入网络延迟、内存压力等故障后，实时订阅 `net_rx_latency`、`memburst` 事件验证故障是否生效，取代人工观察。
- **压测基线建立**：压测期间持续订阅全量事件，记录首个内核异常事件的出现时机，精确标定系统承压极限。

### AIOps 智能运维

- **事件驱动根因分析**：将内核事件作为特征输入 AI/ML 模型，结合应用指标进行多维根因推断，减少人工排查时间。
- **预测性维护**：对 `ras` 硬件错误、`netdev_bonding_lacp` 等硬件层事件建模，在设备彻底失效前提前预警并触发迁移。
- **智能抑制与聚合**：对同一时间窗口内同类事件自动聚合，避免告警风暴，向 On-call 工程师呈现精简的根因摘要。

---

## 💎 价值

| 维度         | 传统方案                              | 接入华佗 events/watch                              |
|------------|-------------------------------------|---------------------------------------------------|
| 时效性      | 告警触发延迟 1–5 分钟                  | 内核事件实时推送，延迟 < 1 秒                        |
| 信号准确性  | 基于指标阈值，误报率高                 | 事件源自内核判定，误报率接近零                       |
| 上下文丰富度 | 指标维度有限                          | 携带容器、节点、地域等完整上下文                     |
| 集成成本    | 需自建 eBPF 采集或依赖第三方 Agent     | 一次 HTTP POST 即可订阅，标准 CloudEvents 格式       |
| 协议兼容性  | 各厂商私有格式                         | 遵循 CloudEvents 1.0 标准，可接入任意兼容平台        |

---

## 🚀 使用

### 1. CloudEvents 规范说明

#### 1.1 CloudEvents 1.0 信封字段

每条推送事件均为一个符合 CloudEvents 1.0 规范的 JSON 对象：

| 字段                | 类型   | 说明                                                    |
|-------------------|------|---------------------------------------------------------|
| `specversion`     | string | 固定值 `"1.0"`                                          |
| `id`              | string | 事件唯一标识符（UUID v4），每条事件独立生成                |
| `source`          | string | 事件来源路径，格式 `/huatuo/{hostname}/{tracer_name}`    |
| `type`            | string | 固定值 `"tech.huatuo.kernel.event"`                     |
| `datacontenttype` | string | 固定值 `"application/json"`                             |
| `time`            | string | 事件采集时间（RFC 3339 纳秒精度，UTC）                   |
| `data`            | object | 事件数据体，即 `WatchEventData` 结构体                   |

#### 1.2 华佗事件数据结构（WatchEventData）

`data` 字段包含华佗的标准事件记录：

```json
{
  "specversion": "1.0",
  "id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "source": "/huatuo/node-1/oom",
  "type": "tech.huatuo.kernel.event",
  "datacontenttype": "application/json",
  "time": "2026-05-18T10:23:45.123456789Z",
  "data": {
    "hostname": "node-1",
    "region": "cn-beijing",
    "observed_timestamp": "2026-05-18T10:23:45Z",
    "tracer_name": "oom",
    "tracer_id": "abc123",
    "tracer_run_type": "auto",
    "container_id": "d3f1a2b4c5e6",
    "container_hostname": "app-pod",
    "container_host_namespace": "prod",
    "container_type": "docker",
    "container_qos": "Guaranteed"
  }
}
```

**WatchEventData 字段说明：**

| 字段                         | 类型   | 说明                                        |
|----------------------------|------|---------------------------------------------|
| `hostname`                 | string | 节点主机名                                  |
| `region`                   | string | 节点所在地域                                |
| `observed_timestamp`       | string | 内核事件发生时间（Tracer 采集时间）          |
| `tracer_name`              | string | 触发事件的采集器名称（见下文内核事件列表）   |
| `tracer_id`                | string | 事件实例唯一 ID                             |
| `tracer_run_type`          | string | 采集模式，`auto`（自动触发）或 `manual`     |
| `container_id`             | string | 容器 ID（容器级事件时存在）                 |
| `container_hostname`       | string | 容器主机名                                  |
| `container_host_namespace` | string | 容器所在命名空间                            |
| `container_type`           | string | 容器运行时类型（docker / containerd 等）    |
| `container_qos`            | string | 容器 QoS 等级                              |

---

### 2. 支持的内核事件列表

| `tracer_name`              | 说明                                           |
|--------------------------|-----------------------------------------------|
| `oom`                    | 内存不足（OOM Killer）触发事件                  |
| `hungtask`               | 内核任务长时间 D 状态（Hung Task）检测          |
| `softlockup`             | CPU 软锁死（Soft Lockup）检测                  |
| `ras`                    | 硬件可靠性（RAS）错误，如 ECC 内存错误         |
| `dropwatch`              | 内核网络数据包丢弃（Drop Watch）事件            |
| `netdev_events`          | 网络设备状态变更事件（Link Up/Down 等）        |
| `netdev_txqueue_timeout` | 网络设备发送队列超时事件                        |
| `netdev_bonding_lacp`    | Bond 设备 LACP 协议异常事件                    |
| `net_rx_latency`         | 网络接收延迟异常事件                            |
| `softirq_tracing`        | 软中断耗时异常追踪事件                          |
| `memory_reclaim_events`  | 内存回收异常事件                               |
| `cpuidle`                | CPU 空闲率异常（AutoTracing 自动触发）         |
| `cpusys`                 | CPU 系统态占用率异常（AutoTracing 自动触发）   |
| `dload`                  | 系统负载异常（AutoTracing 自动触发）           |
| `iotracing`              | I/O 延迟异常（AutoTracing 自动触发）           |
| `memburst`               | 内存突增异常（AutoTracing 自动触发）           |

---

### 3. POST 请求说明

#### 3.1 接口地址

```http
POST /v1/events/watch
```

#### 3.2 请求头

```http
Content-Type: application/json
```

#### 3.3 请求体结构

```json
{
  "filters": {
    "tracer_name": "<regex>",
    "hostname": "<regex>",
    "container_hostname": "<regex>",
    "container_host_namespace": "<regex>",
    "region": "<regex>"
  }
}
```

**filters 字段说明：**

| 字段                         | 类型   | 是否必填 | 说明                                         |
|----------------------------|------|--------|----------------------------------------------|
| `tracer_name`              | string | 否     | 按采集器名称过滤，支持正则表达式               |
| `hostname`                 | string | 否     | 按节点主机名过滤，支持正则表达式               |
| `container_hostname`       | string | 否     | 按容器主机名过滤，支持正则表达式               |
| `container_host_namespace` | string | 否     | 按容器命名空间过滤，支持正则表达式             |
| `region`                   | string | 否     | 按地域过滤，支持正则表达式                     |

- 所有过滤字段均为可选；省略或留空表示匹配所有值。
- 多个字段同时指定时，所有条件须**同时满足**（AND 语义）。
- 过滤器在服务端生效，仅匹配的事件才会推送到客户端。

#### 3.4 响应格式（SSE 流）

连接建立后，服务端以 SSE 格式持续推送事件：

```text
data: {"specversion":"1.0","id":"...","source":"/huatuo/node-1/oom",...}\n\n
```

服务端还会定期发送心跳注释行以保持连接：

```text
: ping\n
```

---

### 4. HTTP 服务事件流配置

在华佗配置文件的 `[HTTPServer]` 段配置事件流参数：

```toml
[HTTPServer]
    # 最大并发客户端连接数，超出后新连接返回 HTTP 429
    # Default: 100
    MaxEventStreamClients = 100

    # SSE 心跳间隔（秒），防止代理/负载均衡因空闲而断开连接
    # 连续 3 次心跳写入失败则主动关闭该客户端连接
    # Default: 30
    EventStreamKeepAliveIntervalSeconds = 30
```

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `MaxEventStreamClients` | 100 | `/v1/events/watch` 长连接上限，超出返回 HTTP 429 |
| `EventStreamKeepAliveIntervalSeconds` | 30 | 心跳间隔，应小于上游代理的 idle timeout |

---

### 5. Curl 调用示例

#### 5.1 订阅所有内核事件

```bash
curl -s -N -X POST http://<node-ip>:19704/v1/events/watch \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "Cache-Control: no-cache" \
  -H "Connection: keep-alive" \
  -d '{}'
```

#### 5.2 只订阅 OOM 事件

```bash
curl -s -N -X POST http://<node-ip>:19704/v1/events/watch \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "Cache-Control: no-cache" \
  -H "Connection: keep-alive" \
  -d '{"filters": {"tracer_name": "^oom$"}}'
```

#### 5.3 订阅指定节点的网络类事件

```bash
curl -s -N -X POST http://<node-ip>:19704/v1/events/watch \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "Cache-Control: no-cache" \
  -H "Connection: keep-alive" \
  -d '{
    "filters": {
      "hostname": "^node-1$",
      "tracer_name": "netdev|dropwatch|net_rx_latency"
    }
  }'
```

#### 5.4 订阅 prod 命名空间的容器事件

```bash
curl -s -N -X POST http://<node-ip>:19704/v1/events/watch \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "Cache-Control: no-cache" \
  -H "Connection: keep-alive" \
  -d '{
    "filters": {
      "container_host_namespace": "^prod$"
    }
  }'
```

> **说明：** `-N` 参数禁用 curl 缓冲，使 SSE 事件即时输出到终端。

---

### 6. Go 编程调用示例

以下示例展示如何在 Go 程序中订阅 `events/watch` 接口，实时消费 CloudEvents 事件。

```go
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// WatchRequest 是发送给 /v1/events/watch 的请求体。
type WatchRequest struct {
	Filters WatchFilters `json:"filters"`
}

type WatchFilters struct {
	TracerName             string `json:"tracer_name,omitempty"`
	Hostname               string `json:"hostname,omitempty"`
	ContainerHostname      string `json:"container_hostname,omitempty"`
	ContainerHostNamespace string `json:"container_host_namespace,omitempty"`
	Region                 string `json:"region,omitempty"`
}

// WatchEvent 是华佗推送的 CloudEvents 1.0 信封。
type WatchEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	DataContentType string          `json:"datacontenttype"`
	Time            string          `json:"time"`
	Data            json.RawMessage `json:"data"`
}

func watchEvents(ctx context.Context, endpoint string, filters WatchFilters) error {
	reqBody, err := json.Marshal(WatchRequest{Filters: filters})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0} // SSE 长连接，不设超时
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// 跳过心跳注释行和空行
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// SSE data 行格式：`data: <json>`
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var event WatchEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			slog.Warn("parse event", "err", err)
			continue
		}

		fmt.Printf("[%s] source=%s id=%s\n", event.Time, event.Source, event.ID)
		fmt.Printf("  data: %s\n", event.Data)
	}

	return scanner.Err()
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	err := watchEvents(ctx, "http://192.168.1.10:19704/v1/events/watch", WatchFilters{
		TracerName: "oom|hungtask|softlockup",
	})
	if err != nil {
		slog.Error("watch events", "err", err)
		os.Exit(1)
	}
}
```

#### 6.1 使用 pkg/types 官方包（推荐）

如果你的项目与华佗在同一 Go module，可直接引用官方类型：

```go
import pkgtypes "huatuo-bamai/pkg/types"

var event pkgtypes.WatchEvent
if err := json.Unmarshal([]byte(data), &event); err != nil { ... }

// WatchEvent.Data 是 json.RawMessage（延迟解析），需二次反序列化才能访问具体字段
dataBytes, err := json.Marshal(event.Data)
if err != nil {
    slog.Warn("marshal event data", "err", err)
    return
}
var payload pkgtypes.WatchEventData
if err := json.Unmarshal(dataBytes, &payload); err != nil {
    slog.Warn("unmarshal event data", "err", err)
    return
}
fmt.Println("tracer:", payload.TracerName)
fmt.Println("observed_timestamp:", payload.ObservedTimestamp)
```

#### 6.2 重连机制建议

生产环境中，网络抖动或服务重启会导致连接断开，建议加入指数退避重连逻辑：

```go
func watchWithRetry(ctx context.Context, endpoint string, filters WatchFilters) {
	backoff := time.Second
	for {
		if err := watchEvents(ctx, endpoint, filters); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("disconnected, retrying", "err", err, "backoff", backoff)
			// time.NewTimer + Stop 确保 context 取消时计时器资源立即释放
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}
```

---

## ⚙️ 原理

### 系统架构

HUATUO Agent 部署在每个节点上，通过 eBPF、Kprobe、Tracepoint 等机制挂钩内核关键路径，将内核异常事件采集后经过滤、封装，以 SSE 长连接推送给多个并发订阅客户端。

```mermaid
graph TB
    subgraph kernel["Linux 内核"]
        K1[OOM Killer]
        K2[Hung Task 检测]
        K3[Soft Lockup 检测]
        K4[RAS 硬件错误]
        K5[网络子系统]
        K6[AutoTracing]
    end

    subgraph huatuo["HUATUO Agent（节点级）"]
        T["Tracer 采集层\neBPF / Kprobe / Tracepoint"]
        F["过滤器\nhostname / tracer / namespace / region"]
        CE["CloudEvents 1.0 封装\nid / source / time / data"]
        EW["EventsWatch 分发层\nSSE 长连接管理"]
    end

    subgraph clients["订阅客户端"]
        C1[故障自愈系统]
        C2[可观测性平台]
        C3[AIOps 系统]
        C4[安全审计系统]
    end

    kernel --> T
    T --> F
    F --> CE
    CE --> EW
    EW -->|SSE 推送| C1
    EW -->|SSE 推送| C2
    EW -->|SSE 推送| C3
    EW -->|SSE 推送| C4
```

### 事件采集与推送原理

客户端发起 POST 请求后，连接保持打开状态。内核每次触发异常事件，HUATUO Agent 完成过滤和封装后立即将事件写入所有匹配的 SSE 流，无需客户端轮询。

```mermaid
sequenceDiagram
    participant C as 客户端
    participant EW as EventsWatch
    participant T as Tracer 采集层
    participant K as Linux 内核

    C->>EW: POST /v1/events/watch {"filters": {...}}
    EW-->>C: 200 OK (Content-Type: text/event-stream)

    loop SSE 长连接持续推送
        K->>T: 内核事件触发（oom / hungtask / softlockup ...）
        T->>EW: 上报原始事件
        EW->>EW: 过滤器匹配
        alt 匹配成功
            EW-->>C: data: {CloudEvents JSON}\n\n
        else 不匹配
            note over EW: 丢弃，不推送
        end
        EW-->>C: : ping（按配置间隔发送心跳）
    end
```

### 事件处理流程

从内核事件产生到推送至客户端，经过采集、过滤、封装三个阶段，整体链路延迟小于 1 秒。

```mermaid
flowchart LR
    A([内核异常触发]) --> B["Tracer 采集\neBPF / Kprobe"]
    B --> C{过滤器匹配?}
    C -- 否 --> D([丢弃])
    C -- 是 --> E["封装 CloudEvents 1.0\nid / source / time / data"]
    E --> F[写入 SSE 流]
    F --> G([推送至订阅客户端])
```

---

## 🌟 结尾

{{% alert color="info" %}}
<div style="text-align: center;">
🌟 欢迎 Star: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
<br><br>
👀 欢迎订阅官方微信公众号<br>
<img src="/img/contact-weixin.png" alt="微信公众号二维码" style="max-width: 200px; margin-top: 10px;">
</div>
{{% /alert %}}
