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
	"fmt"
	"time"

	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

const (
	EventCountOperation  = "count_events"
	TracerCountOperation = "count_tracers"
	// EventAggregateOperation identifies the backend operation, not a Tool name.
	EventAggregateOperation = "aggregate_events"
)

type httpStatusError interface {
	HTTPStatusCode() int
}

func retryEventReader[T any](
	ctx context.Context,
	backend string,
	operation string,
	parameters string,
	policy retryPolicy,
	metrics *Metrics,
	resultSize func(T) int,
	call func(context.Context) (T, error),
) (T, error) {
	return retryBackend(
		ctx, backend, operation, parameters, policy, metrics, "result_count", resultSize,
		func(ctx context.Context) (T, bool, error) {
			result, err := call(ctx)
			return result, retryableEventReaderError(err), err
		},
	)
}

func retryableEventReaderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, utils.ErrRateLimitExceeded) || errors.Is(err, tracingstore.ErrInvalidQuery) ||
		errors.Is(err, tracingstore.ErrUnsupportedOp) || errors.Is(err, tracingstore.ErrDecodeFailed) {
		return false
	}
	var classified tracingstore.RetryableError
	return errors.As(err, &classified) && classified.Retryable()
}

type retryingEventStore struct {
	tracingstore.Reader
	policy  retryPolicy
	metrics *Metrics
}

type retryingCountingEventStore struct {
	*retryingEventStore
	counter tracingstore.TracerCounter
}

func withEventRetry(store tracingstore.Reader, policy retryPolicy, metrics *Metrics) tracingstore.Reader {
	if store == nil {
		return nil
	}
	retrying := &retryingEventStore{Reader: store, policy: policy, metrics: metrics}
	if counter, ok := store.(tracingstore.TracerCounter); ok {
		return &retryingCountingEventStore{retryingEventStore: retrying, counter: counter}
	}
	return retrying
}

func (s *retryingEventStore) SearchEvents(
	ctx context.Context,
	filter *tracingstore.Query,
) (tracingstore.SearchResult, error) {
	return retryEventReader(
		ctx, s.BackendType(), EventSearchOperation, eventFilterSummary(filter), s.policy, s.metrics,
		func(result tracingstore.SearchResult) int { return len(result.Documents) },
		func(ctx context.Context) (tracingstore.SearchResult, error) {
			return s.Reader.SearchEvents(ctx, filter)
		},
	)
}

func (s *retryingEventStore) Count(ctx context.Context, filter *tracingstore.Query) (int64, error) {
	return retryEventReader(
		ctx, s.BackendType(), EventCountOperation, eventFilterSummary(filter), s.policy, s.metrics,
		func(count int64) int { return int(count) },
		func(ctx context.Context) (int64, error) { return s.Reader.Count(ctx, filter) },
	)
}

func (s *retryingEventStore) AggregateEvents(ctx context.Context, query *tracingstore.AggregationQuery) (tracingstore.AggregationResult, error) {
	if query == nil {
		return tracingstore.AggregationResult{}, tracingstore.ErrInvalidQuery
	}
	aggregator, ok := s.Reader.(tracingstore.Aggregator)
	if !ok {
		return tracingstore.AggregationResult{}, tracingstore.ErrUnsupportedOp
	}
	return retryEventReader(ctx, s.BackendType(), EventAggregateOperation, eventFilterSummary(query.Filter), s.policy, s.metrics,
		func(result tracingstore.AggregationResult) int { return len(result.Buckets) },
		func(ctx context.Context) (tracingstore.AggregationResult, error) {
			return aggregator.AggregateEvents(ctx, query)
		},
	)
}

func (s *retryingEventStore) TracerNames(
	ctx context.Context,
	query *tracingstore.Query,
) ([]string, error) {
	return retryEventReader(
		ctx, s.BackendType(), ToolDiscoverEventTypes, eventFilterSummary(query), s.policy, s.metrics,
		func(values []string) int { return len(values) },
		func(ctx context.Context) ([]string, error) {
			return s.Reader.TracerNames(ctx, query)
		},
	)
}

func (s *retryingCountingEventStore) TracerCounts(
	ctx context.Context,
	filter *tracingstore.Query,
) ([]tracingstore.TracerCount, error) {
	return retryEventReader(
		ctx, s.BackendType(), TracerCountOperation, eventFilterSummary(filter), s.policy, s.metrics,
		func(values []tracingstore.TracerCount) int { return len(values) },
		func(ctx context.Context) ([]tracingstore.TracerCount, error) {
			return s.counter.TracerCounts(ctx, filter)
		},
	)
}

func eventFilterSummary(filter *tracingstore.Query) string {
	if filter == nil {
		return "filter=nil"
	}
	return fmt.Sprintf(
		"region=%s hosts=%d containers=%d namespaces=%d tracers=%d start=%s end=%s limit=%d offset=%d profile_only=%t",
		boundedLogValue(filter.Region, 64),
		len(filter.Hostnames),
		len(filter.ContainerHostnames),
		len(filter.ContainerHostNamespaces),
		len(filter.TracerNames),
		filter.StartTime.UTC().Format(time.RFC3339),
		filter.EndTime.UTC().Format(time.RFC3339),
		filter.Limit,
		filter.Offset,
		filter.ProfileOnly,
	)
}
