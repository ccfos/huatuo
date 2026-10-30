// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus"

	mcptool "github.com/ccfos/huatuo/internal/mcp/tool"
)

const (
	ConcurrencyKindTotal        = "total"
	ConcurrencyKindStream       = "stream"
	MetricOperations            = "operations_total"
	MetricActiveRequests        = "active_requests"
	MetricConcurrencyRejections = "concurrency_rejections_total"
	MetricConcurrencyLimit      = "concurrency_limit"
	LabelMethod                 = "method"
	LabelKind                   = "kind"
)

type metrics struct {
	operations *prometheus.CounterVec
	active     *prometheus.GaugeVec
	rejected   *prometheus.CounterVec
	limits     *prometheus.GaugeVec
}

func newMetrics(registerer prometheus.Registerer, totalLimit, streamLimit int) (*metrics, error) {
	metrics := &metrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: mcptool.MetricsNamespace,
			Subsystem: mcptool.MetricsSubsystem,
			Name:      MetricOperations,
			Help:      "Total handled MCP protocol operations by method and outcome.",
		}, []string{LabelMethod, mcptool.LabelOutcome}),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: mcptool.MetricsNamespace,
			Subsystem: mcptool.MetricsSubsystem,
			Name:      MetricActiveRequests,
			Help:      "MCP requests currently being handled by kind.",
		}, []string{LabelKind}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: mcptool.MetricsNamespace,
			Subsystem: mcptool.MetricsSubsystem,
			Name:      MetricConcurrencyRejections,
			Help:      "Total MCP requests rejected by concurrency limit kind.",
		}, []string{LabelKind}),
		limits: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: mcptool.MetricsNamespace,
			Subsystem: mcptool.MetricsSubsystem,
			Name:      MetricConcurrencyLimit,
			Help:      "Configured MCP concurrency limits by kind.",
		}, []string{LabelKind}),
	}
	metrics.active.WithLabelValues(RequestKindRPC).Set(0)
	metrics.active.WithLabelValues(RequestKindStream).Set(0)
	metrics.rejected.WithLabelValues(ConcurrencyKindTotal).Add(0)
	metrics.rejected.WithLabelValues(ConcurrencyKindStream).Add(0)
	metrics.limits.WithLabelValues(ConcurrencyKindTotal).Set(float64(totalLimit))
	metrics.limits.WithLabelValues(ConcurrencyKindStream).Set(float64(streamLimit))
	if registerer == nil {
		return metrics, nil
	}
	collectors := []prometheus.Collector{
		metrics.operations,
		metrics.active,
		metrics.rejected,
		metrics.limits,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *metrics) hooks() *mcpserver.Hooks {
	hooks := &mcpserver.Hooks{}
	hooks.AddOnSuccess(func(_ context.Context, _ any, method mcpgo.MCPMethod, _, result any) {
		outcome := mcptool.ToolOutcomeSuccess
		if toolResult, ok := result.(*mcpgo.CallToolResult); ok && toolResult.IsError {
			outcome = mcptool.ToolOutcomeError
		}
		m.operations.WithLabelValues(string(method), outcome).Inc()
	})
	hooks.AddOnError(func(_ context.Context, _ any, method mcpgo.MCPMethod, _ any, _ error) {
		m.operations.WithLabelValues(string(method), mcptool.ToolOutcomeError).Inc()
	})
	return hooks
}
