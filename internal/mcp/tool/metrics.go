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

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ccfos/huatuo/internal/httpserver/auth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

const (
	// ArgumentSubsystem selects the subsystem Prompt or subsystem-stage guide.
	ArgumentSubsystem = "subsystem"
	// ArgumentStage selects the guide workflow stage; the default is overview.
	ArgumentStage = "stage"
	// MaxESResultBytes is the fixed 4 MiB cap for JSON-encoded MCP Tool results.
	MaxESResultBytes             = 4 * 1024 * 1024
	MetricsNamespace             = "huatuo_apiserver"
	MetricsSubsystem             = "mcp"
	ToolOutcomePartialSuccess    = "partial_success"
	ToolOutcomeError             = "error"
	ToolErrorNone                = "none"
	ToolErrorOversized           = "oversized"
	ToolErrorInternal            = "internal"
	ToolResultNonempty           = "nonempty"
	ToolResultError              = "error"
	ToolResultOversized          = "oversized"
	ToolResultUnsupportedPayload = "unsupported_payload"
	ProfileDocumentParsed        = "parsed"
	ProfileDocumentUnsupported   = "unsupported"
	LogComponentTool             = "mcp-tool"
	MaxPrincipalLogBytes         = 64
	MaxArgumentLogBytes          = 128
	MaxShortLogBytes             = 64
	MaxLogSliceItems             = 8
	ErrorHashPrefixBytes         = 6
	ResultByteBucketStart        = 256
	ResultByteBucketFactor       = 4
	ResultByteBucketCount        = 8
	MetricToolCalls              = "tool_calls_total"
	MetricToolDuration           = "tool_duration_seconds"
	MetricToolResults            = "tool_results_total"
	MetricToolResultBytes        = "tool_result_bytes"
	MetricBackendAttempts        = "backend_attempts_total"
	MetricBackendDuration        = "backend_attempt_duration_seconds"
	MetricRetries                = "retries_total"
	MetricRateLimitRejections    = "rate_limit_rejections_total"
	MetricProfileDocuments       = "profile_documents_total"
	LabelOutcome                 = "outcome"
	LabelTool                    = "tool"
	LabelErrorClass              = "error_class"
	LabelState                   = "state"
	LabelBackend                 = "backend"
	LabelOperation               = "operation"
	LabelScope                   = "scope"
)

// DurationBuckets returns separate slices so collectors cannot mutate a shared
// policy table. Go does not permit slices to be declared as const.
func DurationBuckets() []float64 {
	return []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
}

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
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricToolCalls, Help: "Total MCP tool calls by outcome and error class.",
		}, []string{LabelTool, LabelOutcome, LabelErrorClass}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricToolDuration, Help: "MCP tool call duration in seconds.",
			Buckets: DurationBuckets(),
		}, []string{LabelTool}),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricToolResults, Help: "Total MCP tool results by quality state.",
		}, []string{LabelTool, LabelState}),
		resultBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricToolResultBytes, Help: "MCP tool result text size in bytes.",
			Buckets: prometheus.ExponentialBuckets(ResultByteBucketStart, ResultByteBucketFactor, ResultByteBucketCount),
		}, []string{LabelTool}),
		backendAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricBackendAttempts, Help: "Total MCP backend operation attempts by outcome.",
		}, []string{LabelBackend, LabelOperation, LabelOutcome}),
		backendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricBackendDuration, Help: "MCP backend operation attempt duration in seconds.",
			Buckets: DurationBuckets(),
		}, []string{LabelBackend, LabelOperation}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricRetries, Help: "Total MCP backend retries.",
		}, []string{LabelBackend, LabelOperation}),
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricRateLimitRejections, Help: "Total MCP rate limit rejections.",
		}, []string{LabelBackend, LabelScope}),
		profileDocuments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: MetricsNamespace, Subsystem: MetricsSubsystem,
			Name: MetricProfileDocuments, Help: "Total MCP profile documents by parse outcome.",
		}, []string{LabelOutcome}),
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
			result = boundToolResult(result, MaxESResultBytes)
			return boundToolResult(attachBackendDiagnostics(normalizeToolError(name, result, observation), observation), MaxESResultBytes), nil
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
		bounded := boundToolResult(result, MaxESResultBytes)
		oversized := bounded != result
		result = boundToolResult(attachBackendDiagnostics(normalizeToolError(name, bounded, observation), observation), MaxESResultBytes)
		duration := time.Since(started)
		outcome, errorClass, resultState := classifyToolCall(result, err, observation, oversized)
		m.calls.WithLabelValues(name, outcome, errorClass).Inc()
		m.duration.WithLabelValues(name).Observe(duration.Seconds())
		m.results.WithLabelValues(name, resultState).Inc()
		m.resultBytes.WithLabelValues(name).Observe(float64(toolResultTextBytes(result)))
		if observation.profileParsed > 0 {
			m.profileDocuments.WithLabelValues(ProfileDocumentParsed).Add(float64(observation.profileParsed))
		}
		if observation.profileUnsupported > 0 {
			m.profileDocuments.WithLabelValues(ProfileDocumentUnsupported).Add(float64(observation.profileUnsupported))
		}
		entry := log.WithField("component", LogComponentTool).
			WithField("tool", name).
			WithField("outcome", outcome).
			WithField("error_class", errorClass).
			WithField("duration", duration.Round(time.Millisecond)).
			WithField("arguments", summarizeToolArguments(request.GetArguments())).
			WithField("result", summarizeToolResult(result))
		if principal, ok := auth.PrincipalFromContext(ctx); ok {
			entry = entry.WithField("principal", boundedLogValue(principal.ID, MaxPrincipalLogBytes))
		}
		if err != nil {
			hash := sha256.Sum256([]byte(err.Error()))
			entry.WithField("error_len", len(err.Error())).
				WithField("error_sha256", fmt.Sprintf("%x", hash[:ErrorHashPrefixBytes])).
				Warn("MCP Tool call completed")
		} else if outcome == ToolOutcomeError {
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
		return ToolOutcomeError, ToolErrorOversized, ToolResultOversized
	}
	if err != nil {
		return ToolOutcomeError, classifyToolError(err), ToolResultError
	}
	if result != nil && result.IsError {
		errorClass := observation.errorClass
		if strings.HasPrefix(toolResultErrorText(result), "result exceeds ") {
			errorClass = ToolErrorOversized
		}
		if errorClass == "" {
			errorClass = ToolErrorInvalidArgument
		}
		state := ToolResultError
		switch errorClass {
		case ToolErrorOversized:
			state = ToolResultOversized
		case ToolErrorUnsupportedPayload:
			state = ToolResultUnsupportedPayload
		}
		return ToolOutcomeError, errorClass, state
	}
	if observation.resultState == ToolResultPartial {
		errorClass := observation.errorClass
		if errorClass == "" {
			errorClass = ToolErrorNone
		}
		return ToolOutcomePartialSuccess, errorClass, ToolResultPartial
	}
	state := observation.resultState
	if state == "" {
		state = ToolResultNonempty
	}
	return ToolOutcomeSuccess, ToolErrorNone, state
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
		return ToolErrorTimeout
	case errors.Is(err, context.Canceled):
		return ToolErrorCanceled
	case errors.Is(err, utils.ErrRateLimitExceeded):
		return ToolErrorRateLimited
	case errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusTooManyRequests:
		return ToolErrorRateLimited
	default:
		return ToolErrorInternal
	}
}

func classifyBackendToolError(err error) string {
	if errors.Is(err, event.ErrInvalidQuery) || errors.Is(err, prombackend.ErrInvalidRequest) {
		return ToolErrorInvalidArgument
	}
	class := classifyToolError(err)
	if class == ToolErrorInternal {
		return ToolErrorBackend
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
		if key == ArgumentQuery {
			encoded, _ := json.Marshal(value)
			hash := sha256.Sum256(encoded)
			parts = append(parts, fmt.Sprintf("%s_len=%d %s_sha256=%x", key, len(encoded), key, hash[:ErrorHashPrefixBytes]))
			continue
		}
		parts = append(parts, key+"="+summarizeArgumentValue(value))
	}
	return strings.Join(parts, " ")
}

func isSafeToolLogArgument(key string) bool {
	switch key {
	case ArgumentQuery,
		utils.ArgumentRegion,
		ArgumentHost,
		ArgumentContainerHost,
		ArgumentContainerNamespace,
		ArgumentTracerName,
		ArgumentTimeRange,
		ArgumentSize,
		ArgumentMaxCallPaths,
		ArgumentMaxStackDepth,
		ArgumentAnalysisType,
		ArgumentTopN,
		ArgumentStep,
		ArgumentStage,
		ArgumentSubsystem:
		return true
	default:
		return false
	}
}

func summarizeArgumentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return boundedLogValue(typed, MaxArgumentLogBytes)
	case []string:
		return summarizeStringSlice(typed)
	case []any:
		values := make([]string, 0, min(len(typed), MaxLogSliceItems))
		for _, item := range typed[:min(len(typed), MaxLogSliceItems)] {
			values = append(values, boundedLogValue(fmt.Sprint(item), MaxShortLogBytes))
		}
		return fmt.Sprintf("count=%d values=%v", len(typed), values)
	case nil:
		return "null"
	default:
		return boundedLogValue(fmt.Sprint(value), MaxShortLogBytes)
	}
}

func summarizeStringSlice(values []string) string {
	bounded := make([]string, 0, min(len(values), MaxLogSliceItems))
	for _, value := range values[:min(len(values), MaxLogSliceItems)] {
		bounded = append(bounded, boundedLogValue(value, MaxShortLogBytes))
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
				errorHash = fmt.Sprintf("%x", hash[:ErrorHashPrefixBytes])
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
