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

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
)

const (
	ESAggregationTimeout = 10 * time.Second
	ESAggregationGroups  = "groups"
)

var _ event.Aggregator = (*Reader)(nil)

// AggregateEvents uses one composite page plus lookahead. Unlike distributed
// top-N terms, returned bucket counts do not depend on shard candidate sizes.
func (r *Reader) AggregateEvents(ctx context.Context, query *event.AggregationQuery) (event.AggregationResult, error) {
	if err := validateAggregation(query); err != nil {
		return event.AggregationResult{}, err
	}
	fields, err := r.queryFields(ctx)
	if err != nil {
		return event.AggregationResult{}, err
	}
	if fields.needsSource(query.Filter, false) {
		return event.AggregationResult{}, fmt.Errorf("%w: aggregation requires indexed exact filters and date fields; source scanning is disabled", event.ErrUnsupportedOp)
	}
	sources := make([]any, 0, len(query.GroupBy))
	for _, field := range query.GroupBy {
		variants := fields.exact[field]
		if fields.sourceFields[field] || len(variants) != 1 {
			return event.AggregationResult{}, fmt.Errorf("%w: grouping field %s requires one complete keyword projection across the selected indices; narrow the index selector or align mappings", event.ErrUnsupportedOp, field)
		}
		sources = append(sources, map[string]any{field: map[string]any{"terms": map[string]any{
			"field": variants[0].Name, "missing_bucket": true, "order": "asc",
		}}})
	}
	predicate, err := buildQuery(query.Filter, fields)
	if err != nil {
		return event.AggregationResult{}, err
	}
	clock := map[string]any{"script": map[string]any{"lang": "painless", "source": ESQueryTimeSortScript}}
	if len(fields.times) == 1 {
		clock = map[string]any{"field": fields.times[0]}
	}
	timeout := ESAggregationTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if err := ctx.Err(); err != nil {
		return event.AggregationResult{}, err
	}
	body := map[string]any{
		"size": 0, "track_total_hits": false, "timeout": fmt.Sprintf("%dms", max(1, timeout.Milliseconds())),
		"query": predicate, "aggregations": map[string]any{ESAggregationGroups: map[string]any{
			"composite":    map[string]any{"size": query.Limit + 1, "sources": sources},
			"aggregations": map[string]any{"first_seen": map[string]any{"min": clock}, "last_seen": map[string]any{"max": clock}},
		}},
	}
	var response struct {
		queryStatus
		TerminatedEarly bool                       `json:"terminated_early"`
		Aggregations    map[string]json.RawMessage `json:"aggregations"`
	}
	if err := r.request(ctx, ESSearchOperation, body, &response); err != nil {
		return event.AggregationResult{}, err
	}
	if err := response.check(); err != nil {
		return event.AggregationResult{}, err
	}
	if response.TerminatedEarly {
		return event.AggregationResult{}, &incompleteDataError{summary: "Elasticsearch terminated aggregation early; counts are incomplete"}
	}
	raw := response.Aggregations[ESAggregationGroups]
	if raw == nil {
		raw = response.Aggregations["composite#"+ESAggregationGroups]
	}
	return decodeAggregation(raw, query)
}

func validateAggregation(query *event.AggregationQuery) error {
	if query == nil || query.Filter == nil || query.Filter.Region == "" || query.Filter.StartTime.IsZero() || query.Filter.EndTime.IsZero() {
		return fmt.Errorf("%w: aggregation requires region and bounded time range", event.ErrInvalidQuery)
	}
	if err := validateQuery(query.Filter); err != nil {
		return err
	}
	if query.Filter.EndTime.Sub(query.Filter.StartTime) > event.MaxAggregationTimeRange || query.Filter.Offset != 0 || query.Filter.ProfileOnly || query.Filter.RecordID != "" {
		return fmt.Errorf("%w: aggregation supports at most %s and no offset, profile-only, or record ID", event.ErrInvalidQuery, event.MaxAggregationTimeRange)
	}
	if query.Limit < 1 || query.Limit > event.MaxAggregationBuckets || len(query.GroupBy) < 1 || len(query.GroupBy) > len(event.AggregationFields()) {
		return fmt.Errorf("%w: aggregation requires distinct whitelist fields and 1-%d buckets", event.ErrInvalidQuery, event.MaxAggregationBuckets)
	}
	seen := make(map[string]bool, len(query.GroupBy))
	for _, field := range query.GroupBy {
		if !event.ValidAggregationField(field) || seen[field] {
			return fmt.Errorf("%w: unsupported or duplicate group field %q", event.ErrInvalidQuery, field)
		}
		seen[field] = true
	}
	return nil
}

func decodeAggregation(raw json.RawMessage, query *event.AggregationQuery) (event.AggregationResult, error) {
	var payload struct {
		Buckets *[]struct {
			Key   map[string]*string `json:"key"`
			Count *int64             `json:"doc_count"`
			First struct {
				Value *float64 `json:"value"`
			} `json:"first_seen"`
			Last struct {
				Value *float64 `json:"value"`
			} `json:"last_seen"`
		} `json:"buckets"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Buckets == nil || len(*payload.Buckets) > query.Limit+1 {
		return event.AggregationResult{}, fmt.Errorf("%w: missing, invalid, or oversized composite buckets", event.ErrDecodeFailed)
	}
	result := event.AggregationResult{
		CountExact: true, Truncated: len(*payload.Buckets) > query.Limit,
		Buckets: make([]event.AggregationBucket, 0, min(query.Limit, len(*payload.Buckets))),
	}
	for index := range *payload.Buckets {
		bucket := &(*payload.Buckets)[index]
		if bucket.Count == nil || *bucket.Count <= 0 || len(bucket.Key) != len(query.GroupBy) || bucket.First.Value == nil || bucket.Last.Value == nil {
			return event.AggregationResult{}, fmt.Errorf("%w: bucket requires grouping keys, positive count, and timestamps", event.ErrDecodeFailed)
		}
		for _, field := range query.GroupBy {
			if _, exists := bucket.Key[field]; !exists {
				return event.AggregationResult{}, fmt.Errorf("%w: bucket is missing group field %s", event.ErrDecodeFailed, field)
			}
		}
		first, last := *bucket.First.Value, *bucket.Last.Value
		if first > last || first < -9223372036854775808.0 || last >= 9223372036854775808.0 {
			return event.AggregationResult{}, fmt.Errorf("%w: invalid aggregate timestamps", event.ErrDecodeFailed)
		}
		if len(result.Buckets) < query.Limit {
			result.Buckets = append(result.Buckets, event.AggregationBucket{
				Key: bucket.Key, Count: *bucket.Count,
				FirstSeen: time.UnixMilli(int64(first)).UTC(), LastSeen: time.UnixMilli(int64(last)).UTC(),
			})
		}
	}
	return result, nil
}
