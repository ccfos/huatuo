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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/auth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/prometheus"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus"
)

type toolObservationContextKey struct{}

type toolObservation struct {
	errorClass                string
	resultState               string
	profileParsed             int
	profileUnsupported        int
	backendMu                 sync.Mutex
	backendDiagnostics        []backendOperationDiagnostics
	omittedBackendDiagnostics int
}

// Metrics records bounded-label MCP tool activity.
type Metrics struct {
	calls            *prometheus.CounterVec
	duration         *prometheus.HistogramVec
	results          *prometheus.CounterVec
	resultBytes      *prometheus.HistogramVec
	backendAttempts  *prometheus.CounterVec
	backendDuration  *prometheus.HistogramVec
	retries          *prometheus.CounterVec
	rejections       *prometheus.CounterVec
	profileDocuments *prometheus.CounterVec
}

// NewMetrics creates and optionally registers MCP tool metrics.
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricToolCalls, Help: "Total MCP tool calls by outcome and error class.",
		}, []string{constant.LabelTool, constant.LabelOutcome, constant.LabelErrorClass}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricToolDuration, Help: "MCP tool call duration in seconds.",
			Buckets: constant.DurationBuckets(),
		}, []string{constant.LabelTool}),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricToolResults, Help: "Total MCP tool results by quality state.",
		}, []string{constant.LabelTool, constant.LabelState}),
		resultBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricToolResultBytes, Help: "MCP tool result text size in bytes.",
			Buckets: prometheus.ExponentialBuckets(constant.ResultByteBucketStart, constant.ResultByteBucketFactor, constant.ResultByteBucketCount),
		}, []string{constant.LabelTool}),
		backendAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricBackendAttempts, Help: "Total MCP backend operation attempts by outcome.",
		}, []string{constant.LabelBackend, constant.LabelOperation, constant.LabelOutcome}),
		backendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricBackendDuration, Help: "MCP backend operation attempt duration in seconds.",
			Buckets: constant.DurationBuckets(),
		}, []string{constant.LabelBackend, constant.LabelOperation}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricRetries, Help: "Total MCP backend retries.",
		}, []string{constant.LabelBackend, constant.LabelOperation}),
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricRateLimitRejections, Help: "Total MCP rate limit rejections.",
		}, []string{constant.LabelBackend, constant.LabelScope}),
		profileDocuments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: constant.MetricsNamespace, Subsystem: constant.MetricsSubsystem,
			Name: constant.MetricProfileDocuments, Help: "Total MCP profile documents by parse outcome.",
		}, []string{constant.LabelOutcome}),
	}
	if registerer == nil {
		return metrics, nil
	}
	for _, collector := range []prometheus.Collector{
		metrics.calls, metrics.duration, metrics.results, metrics.resultBytes,
		metrics.backendAttempts, metrics.backendDuration, metrics.retries,
		metrics.rejections, metrics.profileDocuments,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

// Wrap records metrics and one bounded semantic completion log for a Tool.
func (m *Metrics) Wrap(name string, handler mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	if m == nil {
		return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			observation := &toolObservation{}
			ctx = context.WithValue(ctx, toolObservationContextKey{}, observation)
			result, err := handler(ctx, request)
			if err != nil {
				observation.errorClass = classifyToolError(err)
				result = mcpgo.NewToolResultError(err.Error())
			}
			result = boundToolResult(result, constant.MaxESResultBytes)
			return boundToolResult(attachBackendDiagnostics(normalizeToolError(name, result, observation), observation), constant.MaxESResultBytes), nil
		}
	}
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		started := time.Now()
		observation := &toolObservation{}
		ctx = context.WithValue(ctx, toolObservationContextKey{}, observation)
		result, err := handler(ctx, request)
		if err != nil {
			observation.errorClass = classifyToolError(err)
			result = mcpgo.NewToolResultError(err.Error())
		}
		bounded := boundToolResult(result, constant.MaxESResultBytes)
		oversized := bounded != result
		result = boundToolResult(attachBackendDiagnostics(normalizeToolError(name, bounded, observation), observation), constant.MaxESResultBytes)
		duration := time.Since(started)
		outcome, errorClass, resultState := classifyToolCall(result, err, observation, oversized)
		m.calls.WithLabelValues(name, outcome, errorClass).Inc()
		m.duration.WithLabelValues(name).Observe(duration.Seconds())
		m.results.WithLabelValues(name, resultState).Inc()
		m.resultBytes.WithLabelValues(name).Observe(float64(toolResultTextBytes(result)))
		if observation.profileParsed > 0 {
			m.profileDocuments.WithLabelValues(constant.ProfileDocumentParsed).Add(float64(observation.profileParsed))
		}
		if observation.profileUnsupported > 0 {
			m.profileDocuments.WithLabelValues(constant.ProfileDocumentUnsupported).Add(float64(observation.profileUnsupported))
		}
		entry := log.WithField("component", constant.LogComponentTool).
			WithField("tool", name).
			WithField("outcome", outcome).
			WithField("error_class", errorClass).
			WithField("duration", duration.Round(time.Millisecond)).
			WithField("arguments", summarizeToolArguments(request.GetArguments())).
			WithField("result", summarizeToolResult(result))
		if principal, ok := auth.PrincipalFromContext(ctx); ok {
			entry = entry.WithField("principal", boundedLogValue(principal.ID, constant.MaxPrincipalLogBytes))
		}
		if err != nil {
			hash := sha256.Sum256([]byte(err.Error()))
			entry.WithField("error_len", len(err.Error())).
				WithField("error_sha256", fmt.Sprintf("%x", hash[:constant.ErrorHashPrefixBytes])).
				Warn("MCP Tool call completed")
		} else if outcome == constant.ToolOutcomeError {
			entry.Warn("MCP Tool call completed")
		} else {
			entry.Info("MCP Tool call completed")
		}
		return result, nil
	}
}

func classifyToolCall(
	result *mcpgo.CallToolResult,
	err error,
	observation *toolObservation,
	oversized bool,
) (string, string, string) {
	if oversized {
		return constant.ToolOutcomeError, constant.ToolErrorOversized, constant.ToolResultOversized
	}
	if err != nil {
		return constant.ToolOutcomeError, classifyToolError(err), constant.ToolResultError
	}
	if result != nil && result.IsError {
		errorClass := observation.errorClass
		if strings.HasPrefix(toolResultErrorText(result), "result exceeds ") {
			errorClass = constant.ToolErrorOversized
		}
		if errorClass == "" {
			errorClass = constant.ToolErrorInvalidArgument
		}
		state := constant.ToolResultError
		switch errorClass {
		case constant.ToolErrorOversized:
			state = constant.ToolResultOversized
		case constant.ToolErrorUnsupportedPayload:
			state = constant.ToolResultUnsupportedPayload
		}
		return constant.ToolOutcomeError, errorClass, state
	}
	if observation.resultState == constant.ToolResultPartial {
		errorClass := observation.errorClass
		if errorClass == "" {
			errorClass = constant.ToolErrorNone
		}
		return constant.ToolOutcomePartialSuccess, errorClass, constant.ToolResultPartial
	}
	state := observation.resultState
	if state == "" {
		state = constant.ToolResultNonempty
	}
	return constant.ToolOutcomeSuccess, constant.ToolErrorNone, state
}

func toolResultErrorText(result *mcpgo.CallToolResult) string {
	if result == nil || !result.IsError {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(mcpgo.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func classifyToolError(err error) string {
	var statusErr httpStatusError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return constant.ToolErrorTimeout
	case errors.Is(err, context.Canceled):
		return constant.ToolErrorCanceled
	case errors.Is(err, utils.ErrRateLimitExceeded):
		return constant.ToolErrorRateLimited
	case errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusTooManyRequests:
		return constant.ToolErrorRateLimited
	default:
		return constant.ToolErrorInternal
	}
}

func classifyBackendToolError(err error) string {
	if errors.Is(err, event.ErrInvalidQuery) || errors.Is(err, prombackend.ErrInvalidRequest) {
		return constant.ToolErrorInvalidArgument
	}
	class := classifyToolError(err)
	if class == constant.ToolErrorInternal {
		return constant.ToolErrorBackend
	}
	return class
}

func observeToolErrorClass(ctx context.Context, class string) {
	if observation, ok := ctx.Value(toolObservationContextKey{}).(*toolObservation); ok {
		observation.errorClass = class
	}
}

func observeToolResult(ctx context.Context, state string) {
	if observation, ok := ctx.Value(toolObservationContextKey{}).(*toolObservation); ok {
		observation.resultState = state
	}
}

func observeProfileDocuments(ctx context.Context, parsed, unsupported int) {
	if observation, ok := ctx.Value(toolObservationContextKey{}).(*toolObservation); ok {
		observation.profileParsed = parsed
		observation.profileUnsupported = unsupported
	}
}

func withToolTimeout(timeout time.Duration, handler mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	if timeout <= 0 {
		return handler
	}
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handler(ctx, request)
	}
}

func summarizeToolArguments(arguments map[string]any) string {
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if !isSafeToolLogArgument(key) {
			continue
		}
		value := arguments[key]
		if key == constant.ArgumentQuery {
			encoded, _ := json.Marshal(value)
			hash := sha256.Sum256(encoded)
			parts = append(parts, fmt.Sprintf("%s_len=%d %s_sha256=%x", key, len(encoded), key, hash[:constant.ErrorHashPrefixBytes]))
			continue
		}
		parts = append(parts, key+"="+summarizeArgumentValue(value))
	}
	return strings.Join(parts, " ")
}

func isSafeToolLogArgument(key string) bool {
	switch key {
	case constant.ArgumentQuery,
		constant.ArgumentRegion,
		constant.ArgumentHost,
		constant.ArgumentContainerHost,
		constant.ArgumentContainerNamespace,
		constant.ArgumentTracerName,
		constant.ArgumentTimeRange,
		constant.ArgumentSize,
		constant.ArgumentMaxCallPaths,
		constant.ArgumentMaxStackDepth,
		constant.ArgumentAnalysisType,
		constant.ArgumentTopN,
		constant.ArgumentStep,
		constant.ArgumentStage,
		constant.ArgumentSubsystem:
		return true
	default:
		return false
	}
}

func summarizeArgumentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return boundedLogValue(typed, constant.MaxArgumentLogBytes)
	case []string:
		return summarizeStringSlice(typed)
	case []any:
		values := make([]string, 0, min(len(typed), constant.MaxLogSliceItems))
		for _, item := range typed[:min(len(typed), constant.MaxLogSliceItems)] {
			values = append(values, boundedLogValue(fmt.Sprint(item), constant.MaxShortLogBytes))
		}
		return fmt.Sprintf("count=%d values=%v", len(typed), values)
	case nil:
		return "null"
	default:
		return boundedLogValue(fmt.Sprint(value), constant.MaxShortLogBytes)
	}
}

func summarizeStringSlice(values []string) string {
	bounded := make([]string, 0, min(len(values), constant.MaxLogSliceItems))
	for _, value := range values[:min(len(values), constant.MaxLogSliceItems)] {
		bounded = append(bounded, boundedLogValue(value, constant.MaxShortLogBytes))
	}
	return fmt.Sprintf("count=%d values=%v", len(values), bounded)
}

func summarizeToolResult(result *mcpgo.CallToolResult) string {
	if result == nil {
		return "content=0 bytes=0"
	}
	bytes := toolResultTextBytes(result)
	var errorHash string
	for _, content := range result.Content {
		if text, ok := content.(mcpgo.TextContent); ok {
			if result.IsError && errorHash == "" {
				hash := sha256.Sum256([]byte(text.Text))
				errorHash = fmt.Sprintf("%x", hash[:constant.ErrorHashPrefixBytes])
			}
		}
	}
	summary := fmt.Sprintf("content=%d bytes=%d", len(result.Content), bytes)
	if errorHash != "" {
		summary += " error_sha256=" + errorHash
	}
	return summary
}

func toolResultTextBytes(result *mcpgo.CallToolResult) int {
	if result == nil {
		return 0
	}
	bytes := 0
	for _, content := range result.Content {
		if text, ok := content.(mcpgo.TextContent); ok {
			bytes += len(text.Text)
		}
	}
	return bytes
}

func boundedLogValue(value string, maximum int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "..."
}

// Retry records one backend retry.
func (m *Metrics) Retry(backend, operation string) {
	if m != nil {
		m.retries.WithLabelValues(backend, operation).Inc()
	}
}

// BackendAttempt records one backend operation attempt, including local rejection.
func (m *Metrics) BackendAttempt(backend, operation, outcome string, duration time.Duration) {
	if m != nil {
		m.backendAttempts.WithLabelValues(backend, operation, outcome).Inc()
		m.backendDuration.WithLabelValues(backend, operation).Observe(duration.Seconds())
	}
}

// RateLimitRejected records one rejected backend attempt.
func (m *Metrics) RateLimitRejected(backend, scope string) {
	if m != nil {
		m.rejections.WithLabelValues(backend, scope).Inc()
	}
}
