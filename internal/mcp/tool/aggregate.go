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
	"math"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

const AggregationOrderKeyAscendingPerRegion = "key_asc_per_region"

type regionalAggregationBucket struct {
	Region string `json:"region"`
	event.AggregationBucket
}

type eventAggregationResponse struct {
	Status      string                      `json:"status"`
	Scope       string                      `json:"scope"`
	Purpose     string                      `json:"purpose"`
	UsageHint   string                      `json:"usage_hint"`
	GroupBy     []string                    `json:"group_by"`
	Order       string                      `json:"order"`
	Buckets     []regionalAggregationBucket `json:"buckets"`
	StartTime   time.Time                   `json:"start_time"`
	EndTime     time.Time                   `json:"end_time"`
	Errors      []regionFailure             `json:"errors"`
	Truncated   bool                        `json:"truncated"`
	QueryStatus []event.QueryStatus         `json:"query_status"`
}

func (r *eventTools) aggregateEvents(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	regions, err := requiredRegions(request, 1)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	groups, err := requiredStringArray(request.GetArguments(), ArgumentGroupBy)
	if err != nil || len(groups) == 0 || len(groups) > len(event.AggregationFields()) {
		return mcpgo.NewToolResultError(fmt.Sprintf("%s requires distinct whitelist fields", ArgumentGroupBy)), nil
	}
	seen := make(map[string]bool, len(groups))
	for _, field := range groups {
		if !event.ValidAggregationField(field) || seen[field] {
			return mcpgo.NewToolResultError(fmt.Sprintf("%s supports distinct %s fields", ArgumentGroupBy, strings.Join(event.AggregationFields(), ", "))), nil
		}
		seen[field] = true
	}
	requestedRange, err := request.RequireString(ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError(ArgumentTimeRange + " is required"), nil
	}
	maxRange := event.MaxAggregationTimeRange
	if r.maxQueryTimeRange > 0 {
		maxRange = min(maxRange, r.maxQueryTimeRange)
	}
	start, end, err := utils.ParseTimeRange(requestedRange, maxRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid %s: %v", ArgumentTimeRange, err)), nil
	}
	filter := &event.Query{StartTime: start, EndTime: end}
	for _, target := range []struct {
		name            string
		values          *[]string
		maximum, length int
	}{
		{ArgumentHost, &filter.Hostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{ArgumentContainerHost, &filter.ContainerHostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{ArgumentContainerNamespace, &filter.ContainerHostNamespaces, event.MaxTargetValues, event.MaxTargetValueLength},
		{ArgumentTracerName, &filter.TracerNames, event.MaxTracerNames, event.MaxTracerNameLength},
	} {
		*target.values, err = boundedStringArray(request.GetArguments(), target.name, false, target.maximum, target.length)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
	}
	maximum := event.MaxAggregationBuckets
	size := DefaultAggregateSize
	if value, exists := request.GetArguments()[ArgumentSize]; exists {
		var number float64
		switch value := value.(type) {
		case int:
			number = float64(value)
		case float64:
			number = value
		default:
			return mcpgo.NewToolResultError(ArgumentSize + " must be an integer"), nil
		}
		if number < 1 || number > float64(maximum) || math.IsNaN(number) || math.Trunc(number) != number {
			return mcpgo.NewToolResultError(fmt.Sprintf("%s must be an integer between 1 and %d", ArgumentSize, maximum)), nil
		}
		size = int(number)
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (event.AggregationResult, error) {
		reader, ok := r.stores.forRegion(region).(event.Aggregator)
		if !ok {
			return event.AggregationResult{}, event.ErrUnsupportedOp
		}
		regional := *filter
		regional.Region = region
		return reader.AggregateEvents(ctx, &event.AggregationQuery{Filter: &regional, GroupBy: groups, Limit: size})
	})
	buckets := make([]regionalAggregationBucket, 0, size)
	statuses := make([]event.QueryStatus, 0, len(regions))
	failures := make([]regionFailure, 0)
	successful, truncated := 0, false
	partial := false
	for index := range outcomes {
		outcome := &outcomes[index]
		result := outcome.Value
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(r.stores.forRegion(outcome.Region).BackendType(), ToolQueryEvents, outcome.Region, outcome.Err))
			statuses = append(statuses, event.QueryStatus{Region: outcome.Region})
			continue
		}
		successful++
		if len(result.Buckets) > size {
			result.Buckets = result.Buckets[:size]
			result.Truncated = true
		}
		for index := range result.Buckets {
			buckets = append(buckets, regionalAggregationBucket{Region: outcome.Region, AggregationBucket: result.Buckets[index]})
		}
		truncated = truncated || result.Truncated
		partial = partial || result.Truncated || !result.CountExact || len(result.Warnings) > 0
		statuses = append(statuses, event.QueryStatus{
			Region: outcome.Region, Returned: len(result.Buckets),
			Truncated: result.Truncated, CountExact: result.CountExact, Warnings: result.Warnings,
		})
	}
	if successful == 0 && len(failures) > 0 {
		return backendFailuresResult(ctx, "event aggregation failed", failures), nil
	}
	observeCollectionResult(ctx, len(buckets), failures)
	if partial {
		observeToolResult(ctx, ToolResultPartial)
	}
	return boundedIndentedJSONResult(&eventAggregationResponse{
		Purpose: ResultPurposeStatistics, UsageHint: StatisticsUsageHint,
		Scope:  QueryScopeRegion,
		Status: collectionStatus(partial, failures), GroupBy: groups, Order: AggregationOrderKeyAscendingPerRegion, Buckets: buckets,
		StartTime: start, EndTime: end, Errors: failures, Truncated: truncated, QueryStatus: statuses,
	}, MaxESResultBytes)
}
