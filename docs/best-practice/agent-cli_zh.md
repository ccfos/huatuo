# HUATUO Agent CLI 最小接入方案

本文档说明如何使用 `huatuo-insight` 将 HUATUO 的 Prometheus 文本指标转换为紧凑的 JSON 摘要，方便自动化脚本、诊断工具或 AI Agent 进行只读分析。

## 背景

HUATUO 已经提供 `/metrics` 指标出口。`huatuo-insight` 不修改节点状态、不触发内核探针、不写入配置，只读取现有指标并生成结构化摘要，适合作为自动化诊断链路中的最小安全边界。

## 构建

```bash
go build ./cmd/huatuo-insight
```

## 从本地 HUATUO 读取指标

```bash
./huatuo-insight \
  --metrics-url http://127.0.0.1:19704/metrics \
  --prefix huatuo_ \
  --top 20 \
  --pretty
```

输出示例：

```json
{
  "source": "http://127.0.0.1:19704/metrics",
  "generated_at": "2026-07-05T00:00:00Z",
  "total_samples": 120,
  "matched_samples": 80,
  "categories": {
    "cpu": 12,
    "memory": 10,
    "network": 8
  },
  "top": [
    {
      "name": "huatuo_cpu_usage",
      "value": 12.5,
      "category": "cpu"
    }
  ]
}
```

## 从文件读取指标

当生产节点不能直接暴露 HTTP 端口时，可以先保存指标，再离线分析：

```bash
curl -s http://127.0.0.1:19704/metrics > huatuo.metrics
./huatuo-insight --file huatuo.metrics --prefix huatuo_ --pretty
```

默认输出包含分类统计和 Top 样本。如果需要把所有匹配样本也交给下游工具，可以增加 `--include-samples`。

## 面向 Agent 的使用建议

推荐让 Agent 只消费 `huatuo-insight` 输出的 JSON，而不是直接获得节点 Shell 权限。这样可以把能力限制在“读取指标、总结异常、给出排查建议”范围内。

推荐提示词：

```text
你是 Linux 内核可观测性分析助手。请根据以下 HUATUO 指标摘要判断 CPU、内存、网络、I/O、调度或容器维度是否存在异常。只基于输入数据分析，不要假设未提供的信息。
```

## 安全边界

`huatuo-insight` 是只读工具：

- 不需要 root 权限；
- 不启动或停止 HUATUO；
- 不修改 BPF 程序、内核参数或业务进程；
- 不写入存储后端；
- 不直接访问 Kubernetes kubelet、CRI 或宿主机敏感文件。

因此它适合作为 HUATUO 对外暴露给自动化诊断系统的最小接入层。
