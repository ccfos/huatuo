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
	"fmt"
	"net/url"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"

	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
)

const (
	MaxRegionMetricSeries    = 1000
	MaxRegionMetricSelectors = 4
	MaxRegionMetricNodes     = 100
	RegionMetricTimeout      = 10 * time.Second
	MetricTimeoutParameter   = "timeout"
)

type metricStatisticsResponse struct {
	Status    string               `json:"status"`
	Scope     string               `json:"scope"`
	Purpose   string               `json:"purpose"`
	UsageHint string               `json:"usage_hint"`
	Time      time.Time            `json:"time"`
	Results   []regionMetricResult `json:"results"`
	Errors    []regionFailure      `json:"errors"`
}

func (r *prometheusTools) queryRegionStatistics(ctx context.Context, request mcpgo.CallToolRequest, query string) (*mcpgo.CallToolResult, error) {
	regions, err := requiredRegions(request, 1)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	for _, key := range []string{ArgumentHost, ArgumentContainerHost, ArgumentTimeRange, ArgumentStep} {
		if _, exists := request.GetArguments()[key]; exists {
			return mcpgo.NewToolResultError(fmt.Sprintf("%s is unsupported for scope=region; use time and instant aggregation, or scope=target for target evidence", key)), nil
		}
	}
	instant, err := request.RequireString(ArgumentTime)
	if err != nil {
		return mcpgo.NewToolResultError(ArgumentTime + " is required for scope=region"), nil
	}
	at, err := time.Parse(time.RFC3339Nano, instant)
	if err != nil {
		return mcpgo.NewToolResultError(ArgumentTime + " must be an RFC3339 timestamp"), nil
	}
	client := r.regions.forRegion(regions[0])
	scoped, err := scopeRegionStatistics(query, regions[0], client.metricPrefix, r.maxQueryRange)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	ctx, cancel := context.WithTimeout(ctx, RegionMetricTimeout)
	defer cancel()
	maximum := min(client.maxQuerySize, MaxRegionMetricSeries)
	outcomes := fanOutRegions(ctx, regions, 1, func(ctx context.Context, _ string) (prombackend.QueryResult, error) {
		timeout := RegionMetricTimeout
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline))
		}
		values := url.Values{
			ArgumentQuery: {scoped}, ArgumentTime: {at.UTC().Format(time.RFC3339Nano)},
			MetricTimeoutParameter: {fmt.Sprintf("%dms", max(1, timeout.Milliseconds()))},
		}
		data, err := client.post(ctx, prombackend.PrometheusQueryPath, values)
		if err != nil {
			return prombackend.QueryResult{}, err
		}
		return prombackend.StatisticsResponse(data, maximum)
	})
	outcome := &outcomes[0]
	if outcome.Err != nil {
		return backendFailuresResult(ctx, "regional metric statistics failed", []regionFailure{
			newRegionFailure(BackendPrometheus, ToolQueryMetrics, regions[0], outcome.Err),
		}), nil
	}
	partial := len(outcome.Value.Warnings) > 0
	observeCollectionResult(ctx, outcome.Value.Count, nil)
	if partial {
		observeToolResult(ctx, ToolResultPartial)
	}
	return boundedIndentedJSONResult(&metricStatisticsResponse{
		Status: collectionStatus(partial, nil), Scope: QueryScopeRegion, Purpose: ResultPurposeStatistics,
		UsageHint: StatisticsUsageHint, Time: at.UTC(),
		Results: []regionMetricResult{{Region: regions[0], Response: outcome.Value.Response}}, Errors: []regionFailure{},
	}, prombackend.MaxPrometheusResponse)
}

func scopeRegionStatistics(query, region, prefix string, maximumRange time.Duration) (string, error) {
	if err := validatePromQLSafety(query, prefix); err != nil {
		return "", err
	}
	expression, err := parser.ParseExpr(query)
	if err != nil {
		return "", err
	}
	nodes, selectors := 0, 0
	var invalid error
	parser.Inspect(expression, func(node parser.Node, parents []parser.Node) error {
		if invalid != nil || node == nil {
			return invalid
		}
		nodes++
		if nodes > MaxRegionMetricNodes {
			invalid = fmt.Errorf("regional statistics expression exceeds %d nodes", MaxRegionMetricNodes)
			return invalid
		}
		switch node := node.(type) {
		case *parser.MatrixSelector:
			if maximumRange > 0 && node.Range > maximumRange {
				invalid = fmt.Errorf("regional selector range exceeds configured query span %s", maximumRange)
			}
		case *parser.BinaryExpr:
			if node.LHS.Type() == parser.ValueTypeVector && node.RHS.Type() == parser.ValueTypeVector {
				invalid = fmt.Errorf("regional statistics do not support vector-to-vector binary operations; query aggregates separately")
			}
		case *parser.VectorSelector:
			selectors++
			if selectors > MaxRegionMetricSelectors {
				invalid = fmt.Errorf("regional statistics require at most %d vector selectors", MaxRegionMetricSelectors)
				return invalid
			}
			named, aggregated := false, false
			for _, matcher := range node.LabelMatchers {
				named = named || matcher.Name == labels.MetricName && matcher.Type == labels.MatchEqual && matcher.Value != ""
			}
			for _, parent := range parents {
				if _, ok := parent.(*parser.AggregateExpr); ok {
					aggregated = true
				}
			}
			if !named || !aggregated {
				invalid = fmt.Errorf("scope=region is for statistics: every vector selector requires an explicit metric name and aggregation; use scope=target for raw target evidence")
			}
		}
		return invalid
	})
	if invalid != nil {
		return "", invalid
	}
	return scopePromQL(query, region, "", "", prefix)
}
