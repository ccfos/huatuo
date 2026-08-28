# HUATUO Agent CLI Integration

This document describes `huatuo-insight`, a small read-only CLI that converts HUATUO Prometheus text metrics into a compact JSON summary for automation scripts, diagnostic tools, and AI agents.

## Background

HUATUO already exposes runtime data through `/metrics`. `huatuo-insight` does not mutate node state, start tracing jobs, load BPF programs, or write configuration. It only reads existing metrics and emits a structured summary, making it a minimal and safe integration point for automated diagnostics.

## Build

```bash
go build ./cmd/huatuo-insight
```

## Read from a local HUATUO instance

```bash
./huatuo-insight \
  --metrics-url http://127.0.0.1:19704/metrics \
  --prefix huatuo_ \
  --top 20 \
  --pretty
```

Example output:

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

## Read from a metrics file

For environments where the production node should not expose the metrics endpoint directly, metrics can be captured first and analyzed offline:

```bash
curl -s http://127.0.0.1:19704/metrics > huatuo.metrics
./huatuo-insight --file huatuo.metrics --prefix huatuo_ --pretty
```

By default, the output contains category counts and top samples. Add `--include-samples` when downstream tools need every matched sample.

## Agent usage

Agents should consume only the JSON output from `huatuo-insight` rather than receiving direct shell access to the node. This keeps the integration focused on reading metrics, summarizing anomalies, and suggesting troubleshooting steps.

Suggested prompt:

```text
You are a Linux kernel observability assistant. Based on the following HUATUO metrics summary, identify whether CPU, memory, network, I/O, scheduling, or container signals look abnormal. Only reason from the provided data.
```

## Safety boundary

`huatuo-insight` is read-only:

- it does not require root privileges;
- it does not start or stop HUATUO;
- it does not modify BPF programs, kernel parameters, or business processes;
- it does not write to storage backends;
- it does not directly access kubelet, CRI, or sensitive host files.

This makes it a minimal integration layer for exposing HUATUO signals to automated diagnostic systems.
