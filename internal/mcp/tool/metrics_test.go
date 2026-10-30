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

package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsWrapBoundsEveryToolResult(t *testing.T) {
	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, metrics := range []*Metrics{nil, metrics} {
		handler := metrics.Wrap("test_tool", func(
			context.Context,
			mcpgo.CallToolRequest,
		) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError(strings.Repeat(`\`, MaxESResultBytes)), nil
		})
		result, err := handler(t.Context(), mcpgo.CallToolRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "result exceeds") {
			t.Fatalf("bounded result = %+v", result)
		}
	}
	if got := counterValue(t, metrics.calls.WithLabelValues("test_tool", ToolOutcomeError, ToolErrorOversized)); got != 1 {
		t.Fatalf("oversized tool calls = %v, want 1", got)
	}
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func TestToolTimeoutBoundsWholeHandler(t *testing.T) {
	handler := withToolTimeout(10*time.Millisecond, func(
		ctx context.Context,
		_ mcpgo.CallToolRequest,
	) (*mcpgo.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	started := time.Now()
	_, err := handler(t.Context(), mcpgo.CallToolRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handler error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("handler elapsed = %v, want near 10ms", elapsed)
	}
}
