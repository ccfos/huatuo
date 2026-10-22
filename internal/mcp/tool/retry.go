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
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/prometheus"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

type retryBackoff func(retry int, initialDelay time.Duration) time.Duration

type backendDiagnosticError interface {
	DiagnosticSummary() string
}

type retryPolicy struct {
	Timeout      time.Duration
	MaxRetries   int
	InitialDelay time.Duration
	Backoff      retryBackoff
}

func newRetryPolicy(timeout time.Duration, maxRetries int) retryPolicy {
	return retryPolicy{
		Timeout:      timeout,
		MaxRetries:   maxRetries,
		InitialDelay: constant.DefaultRetryDelay,
		Backoff:      fullJitterRetryBackoff(rand.Int64N),
	}
}

func fullJitterRetryBackoff(randomInt64N func(int64) int64) retryBackoff {
	return func(retry int, initialDelay time.Duration) time.Duration {
		maximum := exponentialRetryDelay(retry, initialDelay)
		if maximum <= 0 {
			return 0
		}
		return time.Duration(randomInt64N(int64(maximum) + 1))
	}
}

func exponentialRetryDelay(retry int, initialDelay time.Duration) time.Duration {
	if initialDelay <= 0 {
		return 0
	}
	shift := min(max(retry, 1)-1, constant.MaxRetryBackoffShift)
	if initialDelay > constant.MaxRetryDelay>>shift {
		return constant.MaxRetryDelay
	}
	return initialDelay << shift
}

func retryBackend[T any](
	ctx context.Context,
	backend string,
	operation string,
	parameters string,
	policy retryPolicy,
	metrics *Metrics,
	resultField string,
	resultSize func(T) int,
	call func(context.Context) (result T, retryable bool, err error),
) (T, error) {
	var zero T
	var lastErr error
	diagnostic := backendOperationDiagnostics{Backend: backend, Operation: operation}
	diagnostic.Region, _ = ctx.Value(backendRegionContextKey{}).(string)
	defer func() { observeBackendDiagnostics(ctx, &diagnostic) }()
	if policy.MaxRetries < 0 {
		return zero, errors.New("maximum retries must not be negative")
	}
	if policy.InitialDelay < 0 {
		return zero, errors.New("initial retry delay must not be negative")
	}
	if policy.Backoff == nil {
		return zero, errors.New("retry backoff is required")
	}
	if policy.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}

	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return zero, errors.Join(lastErr, err)
			}
			return zero, err
		}
		if attempt > 0 {
			metrics.Retry(backend, operation)
		}
		started := time.Now()
		log.WithField("component", constant.LogComponentBackend).
			WithField("backend", backend).
			WithField("operation", operation).
			WithField("attempt", attempt+1).
			WithField("parameters", parameters).
			Info("MCP backend query started")

		result, retryable, err := call(ctx)
		duration := time.Since(started)
		metrics.BackendAttempt(backend, operation, backendMetricOutcome(err, retryable), duration)
		lastErr = err
		entry := log.WithField("component", constant.LogComponentBackend).
			WithField("backend", backend).
			WithField("operation", operation).
			WithField("attempt", attempt+1).
			WithField("duration", duration.Round(time.Millisecond))
		if err == nil {
			diagnostic.Recovered = true
			entry.WithField(resultField, resultSize(result)).Info("MCP backend query completed")
			return result, nil
		}
		diagnostic.record(attempt+1, retryable, err)
		var diagnostic backendDiagnosticError
		if errors.As(err, &diagnostic) {
			entry = entry.WithField("error_detail", boundedLogValue(diagnostic.DiagnosticSummary(), constant.MaxBackendErrorBodyBytes))
		}
		entry.WithField("error_class", backendErrorClass(err)).
			WithField("retryable", retryable).
			Warn("MCP backend query completed")
		if attempt == policy.MaxRetries || !retryable {
			if attempt > 0 {
				return zero, &backendRetryError{
					backend: backendDisplayName(backend), retries: attempt, err: err,
				}
			}
			return zero, err
		}

		wait := policy.Backoff(attempt+1, policy.InitialDelay)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return zero, errors.New("backend retry loop exhausted")
}

func backendMetricOutcome(err error, retryable bool) string {
	var statusErr httpStatusError
	switch {
	case err == nil:
		return constant.ToolOutcomeSuccess
	case errors.Is(err, context.DeadlineExceeded):
		return constant.ToolErrorTimeout
	case errors.Is(err, context.Canceled):
		return constant.ToolErrorCanceled
	case errors.Is(err, utils.ErrRateLimitExceeded):
		return constant.ToolErrorRateLimited
	case errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusTooManyRequests:
		return constant.ToolErrorRateLimited
	case retryable:
		return constant.BackendOutcomeRetryableError
	default:
		return constant.ToolOutcomeError
	}
}

func backendDisplayName(backend string) string {
	switch backend {
	case constant.BackendES:
		return constant.BackendNameES
	case constant.EventBackendOpenSearch:
		return constant.BackendNameOpenSearch
	case constant.BackendPrometheus:
		return constant.BackendNamePrometheus
	default:
		return backend
	}
}

func backendErrorClass(err error) string {
	var statusErr httpStatusError
	var networkErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return constant.ToolErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return constant.BackendErrorDeadline
	case errors.Is(err, utils.ErrRateLimitExceeded):
		return constant.BackendErrorRateLimit
	case errors.As(err, &statusErr):
		return fmt.Sprintf("http_%d", statusErr.HTTPStatusCode())
	case errors.Is(err, event.ErrIncompleteData):
		return constant.BackendErrorIncompleteData
	case errors.Is(err, event.ErrDecodeFailed), errors.Is(err, prombackend.ErrDecodeFailed), errors.Is(err, prombackend.ErrResponseTooLarge):
		return constant.BackendErrorDecode
	case errors.Is(err, event.ErrInvalidQuery), errors.Is(err, prombackend.ErrInvalidRequest):
		return constant.BackendErrorInvalidQuery
	case errors.Is(err, event.ErrUnsupportedOp):
		return constant.BackendErrorUnsupported
	case errors.As(err, &networkErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, prombackend.ErrTransport):
		return constant.BackendErrorTransport
	default:
		return constant.BackendErrorUnknown
	}
}
