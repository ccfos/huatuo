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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/prometheus"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

type prometheusMetricInfo = prombackend.MetricInfo

type regionMetricResult struct {
	Region   string          `json:"region"`
	Response json.RawMessage `json:"response"`
}

type metricDiscoveryStatus struct {
	Region    string   `json:"region"`
	Returned  int      `json:"returned"`
	Truncated bool     `json:"truncated"`
	Warnings  []string `json:"warnings,omitempty"`
}

// PrometheusConfig controls the optional, read-only metrics tools.
type PrometheusConfig struct {
	Address                string
	AuthType               string
	AuthToken              string
	AuthUser               string
	AuthPass               string
	MetricPrefix           string
	MaxSeriesPerQuery      int
	MaxMetricsPerDiscovery int
	ToolTimeout            time.Duration
	Regions                []PrometheusRegionConfig
	Limiter                *utils.RateLimiter
}

// Pointer fields distinguish inheritance from an explicit empty override.
type PrometheusRegionConfig struct {
	NamePattern  string
	Address      *string
	AuthType     *string
	AuthToken    *string
	AuthUser     *string
	AuthPass     *string
	MetricPrefix *string
}

type prometheusTools struct {
	reader        *prombackend.Reader
	metricPrefix  string
	maxQuerySize  int
	maxDiscover   int
	maxRetries    int
	maxDuration   time.Duration
	maxQueryRange time.Duration
	maxRegions    int
	regionWorkers int
	regions       *regionRouter[*prometheusTools]
	metrics       *Metrics
}

type prometheusClientConfig struct {
	address       string
	authType      string
	authToken     string
	authUser      string
	authPass      string
	metricPrefix  string
	timeout       time.Duration
	maxQuerySize  int
	maxDiscover   int
	maxRetries    int
	maxRetriesSet bool
	maxDuration   time.Duration
	maxQueryRange time.Duration
	maxRegions    int
	regionWorkers int
	limiter       *utils.RateLimiter
	metrics       *Metrics
}

func registerPrometheusTools(server *mcpserver.MCPServer, cfg *PrometheusConfig, limits QueryLimits, metrics *Metrics) (cleanup func(context.Context) error, returnedErr error) {
	if cfg.MaxSeriesPerQuery > constant.MaxSeries || cfg.MaxMetricsPerDiscovery > constant.MaxMetrics {
		return nil, errors.New("Prometheus series or discovery limit exceeds supported maximum")
	}
	if strings.TrimSpace(cfg.Address) == "" {
		return nil, nil
	}
	var owned []*prombackend.Reader
	closeReaders := func(ctx context.Context) error {
		var errs []error
		for _, reader := range owned {
			errs = append(errs, reader.Close(ctx))
		}
		return errors.Join(errs...)
	}
	defer func() {
		if returnedErr != nil {
			_ = closeReaders(context.Background())
		}
	}()
	base := prometheusClientConfig{
		address: cfg.Address, authType: cfg.AuthType, authToken: cfg.AuthToken, authUser: cfg.AuthUser, authPass: cfg.AuthPass,
		metricPrefix: cfg.MetricPrefix, timeout: cfg.ToolTimeout,
		maxQuerySize: cfg.MaxSeriesPerQuery, maxDiscover: cfg.MaxMetricsPerDiscovery,
		maxDuration:   cfg.ToolTimeout,
		maxQueryRange: limits.MaxQueryTimeRange, maxRegions: limits.MaxRegionsPerQuery,
		limiter: cfg.Limiter, metrics: metrics,
	}
	r, err := newPrometheusClientWithConfig(&base)
	if err != nil {
		return nil, err
	}
	owned = append(owned, r.reader)
	regions := make([]regionValue[*prometheusTools], 0, len(cfg.Regions))
	for _, region := range cfg.Regions {
		regionConfig := base
		regionConfig.address = optionalString(region.Address, base.address)
		regionConfig.authType = optionalString(region.AuthType, base.authType)
		regionConfig.authToken = optionalString(region.AuthToken, base.authToken)
		regionConfig.authUser = optionalString(region.AuthUser, base.authUser)
		regionConfig.authPass = optionalString(region.AuthPass, base.authPass)
		if region.AuthType != nil {
			switch strings.ToLower(strings.TrimSpace(*region.AuthType)) {
			case "", constant.AuthTypeNone:
				regionConfig.authToken, regionConfig.authUser, regionConfig.authPass = "", "", ""
			case constant.AuthTypeBearer:
				regionConfig.authUser, regionConfig.authPass = "", ""
			case constant.AuthTypeBasic:
				regionConfig.authToken = ""
			}
		}
		regionConfig.metricPrefix = optionalString(region.MetricPrefix, base.metricPrefix)
		client, err := newPrometheusClientWithConfig(&regionConfig)
		if err != nil {
			return nil, fmt.Errorf("configure Prometheus region %q: %w", region.NamePattern, err)
		}
		owned = append(owned, client.reader)
		regions = append(regions, regionValue[*prometheusTools]{name: region.NamePattern, value: client})
	}
	r.regions, err = newRegionRouter(r, regions)
	if err != nil {
		return nil, fmt.Errorf("configure Prometheus regions: %w", err)
	}
	annotation := mcpgo.ToolAnnotation{ReadOnlyHint: boolPointer(constant.ReadOnlyHint), DestructiveHint: boolPointer(constant.DestructiveHint), IdempotentHint: boolPointer(constant.IdempotentHint)}
	server.AddTool(mcpgo.NewTool(
		constant.ToolDiscoverMetrics,
		mcpgo.WithDescription("List candidate metric names by prefix; optionally inspect up to five exact metric names for regional availability, label keys and backend metadata."+constant.ToolResultUsage),
		mcpgo.WithArray(constant.ArgumentRegion, mcpgo.Required(), mcpgo.MinItems(1), mcpgo.MaxItems(r.maxRegions), mcpgo.WithStringItems(mcpgo.MaxLength(constant.MaxRegionNameLength))),
		mcpgo.WithString(constant.ArgumentTimeRange, mcpgo.Required(), mcpgo.Description("Required duration or RFC3339 range for metric and label discovery.")),
		mcpgo.WithArray(constant.ArgumentMetricNames, mcpgo.MinItems(1), mcpgo.MaxItems(constant.MaxMetricNamesPerLookup), mcpgo.WithStringItems(mcpgo.MaxLength(constant.MaxMetricNameLength)), mcpgo.Description("Optional exact metric names to inspect; omitted means fast candidate-name catalog.")),
		mcpgo.WithToolAnnotation(annotation),
	), metrics.Wrap(constant.ToolDiscoverMetrics, withToolTimeout(cfg.ToolTimeout, r.discover)))
	server.AddTool(mcpgo.NewTool(
		constant.ToolQueryMetrics,
		mcpgo.WithDescription("Execute a bounded range PromQL query for one or more exact regions."+constant.ToolResultUsage),
		mcpgo.WithString(constant.ArgumentQuery, mcpgo.Required(), mcpgo.MaxLength(constant.MaxPromQLLength)),
		mcpgo.WithArray(constant.ArgumentRegion, mcpgo.Required(), mcpgo.MinItems(1), mcpgo.MaxItems(r.maxRegions), mcpgo.WithStringItems(mcpgo.MaxLength(constant.MaxRegionNameLength))),
		mcpgo.WithString(constant.ArgumentHost, mcpgo.MaxLength(constant.MaxTargetValueLength)),
		mcpgo.WithString(constant.ArgumentContainerHost, mcpgo.MaxLength(constant.MaxTargetValueLength)),
		mcpgo.WithString(constant.ArgumentTimeRange, mcpgo.Required(), mcpgo.Description("Required duration or RFC3339 range.")),
		mcpgo.WithString(constant.ArgumentStep, mcpgo.Description(fmt.Sprintf("Range step; minimum 15s, default %s.", constant.DefaultPrometheusStep))),
		mcpgo.WithToolAnnotation(annotation),
	), metrics.Wrap(constant.ToolQueryMetrics, withToolTimeout(cfg.ToolTimeout, r.query)))
	return closeReaders, nil
}

func (r *prometheusTools) discover(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	regions, err := requiredRegions(request, r.maxRegions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	requestedRange, err := request.RequireString(constant.ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError("time_range is required"), nil
	}
	start, end, err := utils.ParseTimeRange(requestedRange, r.maxQueryRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid time_range: %v", err)), nil
	}
	metricNames, err := boundedStringArray(request.GetArguments(), constant.ArgumentMetricNames, false, constant.MaxMetricNamesPerLookup, constant.MaxMetricNameLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if _, present := request.GetArguments()[constant.ArgumentMetricNames]; present && len(metricNames) == 0 {
		return mcpgo.NewToolResultError("metric_names must not be empty"), nil
	}
	if len(metricNames)*len(regions) > r.maxDiscover {
		return mcpgo.NewToolResultError("metric_names across regions exceed the configured discovery limit"), nil
	}
	quotas, err := distributeLimit(r.maxDiscover, regions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (prombackend.DiscoveryResult, error) {
		if quotas[region] == 0 {
			return prombackend.DiscoveryResult{}, nil
		}
		return r.regions.forRegion(region).discoverRegionMetrics(ctx, region, quotas[region], metricNames, start, end)
	})
	metrics := make([]prometheusMetricInfo, 0, r.maxDiscover)
	failures := make([]regionFailure, 0)
	statuses := make([]metricDiscoveryStatus, 0, len(outcomes))
	truncated, partial := false, false
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(constant.BackendPrometheus, constant.ToolDiscoverMetrics, outcome.Region, outcome.Err))
		}
		metrics = append(metrics, outcome.Value.Metrics...)
		truncated = truncated || outcome.Value.Truncated
		partial = partial || outcome.Value.Truncated || len(outcome.Value.Warnings) > 0
		if outcome.Value.Metrics != nil {
			statuses = append(statuses, metricDiscoveryStatus{
				Region: outcome.Region, Returned: len(outcome.Value.Metrics),
				Truncated: outcome.Value.Truncated, Warnings: outcome.Value.Warnings,
			})
		}
	}
	if len(metrics) == 0 && len(failures) == len(regions) {
		return backendFailuresResult(ctx, "discover metrics failed", failures), nil
	}
	observeCollectionResult(ctx, len(metrics), failures)
	if partial {
		observeToolResult(ctx, constant.ToolResultPartial)
	}
	return boundedIndentedJSONResult(map[string]any{"status": collectionStatus(partial, failures), "metrics": metrics, "errors": failures, "truncated": truncated, "query_status": statuses}, constant.MaxPrometheusResponse)
}

func (r *prometheusTools) discoverRegionMetrics(ctx context.Context, region string, maximum int, metricNames []string, start, end time.Time) (prombackend.DiscoveryResult, error) {
	return prombackend.Discover(ctx, r.do, &prombackend.DiscoveryOptions{
		Region: region, MetricPrefix: r.metricPrefix, Maximum: maximum, MetricNames: metricNames,
		StartTime: start, EndTime: end,
	})
}

func (r *prometheusTools) query(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	query, err := request.RequireString(constant.ArgumentQuery)
	if err != nil || strings.TrimSpace(query) == "" {
		return mcpgo.NewToolResultError("query is required"), nil
	}
	regions, err := requiredRegions(request, r.maxRegions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	targetLabel, targetValue, err := metricTarget(request)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	requestedRange, err := request.RequireString(constant.ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError("time_range is required"), nil
	}
	start, end, err := utils.ParseTimeRange(requestedRange, r.maxQueryRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid time_range: %v", err)), nil
	}
	step, err := time.ParseDuration(request.GetString(constant.ArgumentStep, constant.DefaultPrometheusStep))
	if err != nil || step < constant.MinPrometheusStep {
		return mcpgo.NewToolResultError("step must be at least 15s"), nil
	}
	if end.Sub(start)/step+1 > constant.MaxQueryPoints {
		return mcpgo.NewToolResultError("time_range and step exceed 11000 points"), nil
	}
	quotas, err := distributeLimit(r.maxQuerySize, regions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (prombackend.QueryResult, error) {
		client := r.regions.forRegion(region)
		scopedQuery, err := scopePromQL(query, region, targetLabel, targetValue, client.metricPrefix)
		if err != nil {
			return prombackend.QueryResult{}, fmt.Errorf("%w: %w", prombackend.ErrInvalidRequest, err)
		}
		values := url.Values{constant.ArgumentQuery: {scopedQuery}, constant.QueryStartParameter: {start.Format(time.RFC3339Nano)}, constant.QueryEndParameter: {end.Format(time.RFC3339Nano)}, constant.ArgumentStep: {step.String()}}
		data, err := client.post(ctx, constant.PrometheusRangeQueryPath, values)
		if err != nil {
			return prombackend.QueryResult{}, err
		}
		return prombackend.BoundResponse(data, quotas[region])
	})
	results := make([]regionMetricResult, 0, len(regions))
	failures := make([]regionFailure, 0)
	resultItems, partial := 0, false
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(constant.BackendPrometheus, constant.ToolQueryMetrics, outcome.Region, outcome.Err))
			continue
		}
		results = append(results, regionMetricResult{Region: outcome.Region, Response: outcome.Value.Response})
		resultItems += outcome.Value.Count
		partial = partial || outcome.Value.Truncated || len(outcome.Value.Warnings) > 0
	}
	if len(results) == 0 {
		return backendFailuresResult(ctx, "query metrics failed", failures), nil
	}
	observeCollectionResult(ctx, resultItems, failures)
	if partial {
		observeToolResult(ctx, constant.ToolResultPartial)
	}
	return boundedIndentedJSONResult(map[string]any{"status": collectionStatus(partial, failures), "results": results, "errors": failures}, constant.MaxPrometheusResponse)
}

func scopePromQL(query, region, targetLabel, targetValue, prefix string) (string, error) {
	if err := validatePromQLSafety(query, prefix); err != nil {
		return "", err
	}
	expression, err := parser.ParseExpr(query)
	if err != nil {
		return "", fmt.Errorf("invalid PromQL: %w", err)
	}
	regionMatcher, err := labels.NewMatcher(labels.MatchEqual, constant.ArgumentRegion, region)
	if err != nil {
		return "", err
	}
	var scopeErr error
	parser.Inspect(expression, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		targetMatches := false
		for _, matcher := range selector.LabelMatchers {
			if matcher.Name == constant.ArgumentRegion {
				scopeErr = errors.New("PromQL must omit the region matcher; region is injected by the server")
				return scopeErr
			}
			if matcher.Name == targetLabel && matcher.Type == labels.MatchEqual && matcher.Value == targetValue {
				targetMatches = true
			}
		}
		if !targetMatches {
			scopeErr = fmt.Errorf("every vector selector must contain %s=%q", targetLabel, targetValue)
			return scopeErr
		}
		selector.LabelMatchers = append(selector.LabelMatchers, regionMatcher)
		return nil
	})
	if scopeErr != nil {
		return "", scopeErr
	}
	return expression.String(), nil
}

func validatePromQLSafety(query, prefix string) error {
	if len(query) > constant.MaxPromQLLength {
		return errors.New("PromQL query exceeds 4096 bytes")
	}
	lower := strings.ToLower(query)
	for _, forbidden := range []string{"delete(", "drop(", "replace(", "reset(", "clear("} {
		if containsPrometheusFunction(lower, forbidden) {
			return fmt.Errorf("promql contains forbidden operation: %s", forbidden)
		}
	}
	expression, err := parser.ParseExpr(query)
	if err != nil {
		return fmt.Errorf("invalid PromQL: %w", err)
	}
	var costError error
	parser.Inspect(expression, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.MatrixSelector:
			if typed.Range > constant.MaxSelectorRange {
				costError = errors.New("PromQL selector range must not exceed 1h")
			}
		case *parser.VectorSelector:
			if typed.OriginalOffset != 0 || typed.Timestamp != nil || typed.StartOrEnd != 0 {
				costError = errors.New("PromQL offset and @ modifiers are not supported")
			}
		case *parser.SubqueryExpr:
			costError = errors.New("PromQL subqueries are not supported")
		}
		return costError
	})
	if costError != nil {
		return costError
	}
	selectors := parser.ExtractSelectors(expression)
	if len(selectors) == 0 {
		return errors.New("PromQL must contain at least one vector selector")
	}
	for _, matchers := range selectors {
		metricName := ""
		for _, matcher := range matchers {
			if matcher.Name == labels.MetricName && matcher.Type == labels.MatchEqual {
				metricName = matcher.Value
			}
		}
		if prefix != "" && !strings.HasPrefix(metricName, prefix) {
			return fmt.Errorf("metric %q is outside configured prefix %q", metricName, prefix)
		}
	}
	return nil
}

func containsPrometheusFunction(query, function string) bool {
	for {
		index := strings.Index(query, function)
		if index < 0 {
			return false
		}
		if index == 0 || !isPrometheusIdentifier(query[index-1]) {
			return true
		}
		query = query[index+len(function):]
	}
}

func isPrometheusIdentifier(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_'
}

func (r *prometheusTools) get(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return r.do(ctx, http.MethodGet, path, values)
}

func (r *prometheusTools) post(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return r.do(ctx, http.MethodPost, path, values)
}

func (r *prometheusTools) do(ctx context.Context, method, path string, values url.Values) ([]byte, error) {
	return retryBackend(ctx, constant.BackendPrometheus, path, prometheusRequestSummary(method, path, values), newRetryPolicy(r.maxDuration, r.maxRetries), r.metrics, "result_bytes", func(data []byte) int { return len(data) },
		func(ctx context.Context) ([]byte, bool, error) { return r.requestOnce(ctx, method, path, values) },
	)
}

func (r *prometheusTools) requestOnce(ctx context.Context, method, path string, values url.Values) ([]byte, bool, error) {
	data, err := r.reader.Request(ctx, method, path, values)
	return data, retryablePrometheusError(err), err
}

func retryablePrometheusError(err error) bool {
	if err == nil || errors.Is(err, utils.ErrRateLimitExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var status httpStatusError
	if errors.As(err, &status) {
		switch status.HTTPStatusCode() {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	return errors.Is(err, prombackend.ErrTransport)
}

func prometheusRequestSummary(method, path string, values url.Values) string {
	parts := []string{"method=" + boundedLogValue(method, 16), "path=" + boundedLogValue(path, 128)}
	if query := values.Get(constant.ArgumentQuery); query != "" {
		parts = append(parts, summarizeToolArguments(map[string]any{constant.ArgumentQuery: query}))
	}
	for _, key := range []string{constant.QueryStartParameter, constant.QueryEndParameter, constant.ArgumentStep} {
		if value := values.Get(key); value != "" {
			parts = append(parts, key+"="+boundedLogValue(value, 64))
		}
	}
	if matches := values[constant.PrometheusMatchParameter]; len(matches) > 0 {
		parts = append(parts, fmt.Sprintf("match_count=%d", len(matches)))
	}
	return strings.Join(parts, " ")
}

func newPrometheusClientWithConfig(input *prometheusClientConfig) (*prometheusTools, error) {
	cfg := *input
	if cfg.maxQuerySize <= 0 {
		cfg.maxQuerySize = constant.MaxSeries
	}
	if cfg.maxDiscover <= 0 {
		cfg.maxDiscover = constant.MaxMetrics
	}
	if cfg.maxRetries < 0 {
		return nil, errors.New("Prometheus max retries must not be negative")
	}
	if !cfg.maxRetriesSet && cfg.maxRetries == 0 {
		cfg.maxRetries = constant.DefaultBackendRetries
	}
	if cfg.maxDuration <= 0 {
		cfg.maxDuration = constant.DefaultPromDuration
	}
	if cfg.maxRegions <= 0 {
		cfg.maxRegions = constant.MaxRegions
	}
	if cfg.regionWorkers <= 0 {
		cfg.regionWorkers = constant.DefaultRegionWorkers
	}
	reader, err := prombackend.New(&prombackend.Config{
		Address: cfg.address, AuthType: cfg.authType, AuthToken: cfg.authToken, AuthUser: cfg.authUser, AuthPass: cfg.authPass,
		Timeout: cfg.timeout, BeforeRequest: cfg.limiter.AllowBackendRequest,
	})
	if err != nil {
		return nil, err
	}
	return &prometheusTools{
		reader: reader, metricPrefix: cfg.metricPrefix,
		maxQuerySize: cfg.maxQuerySize, maxDiscover: cfg.maxDiscover,
		maxRetries: cfg.maxRetries, maxDuration: cfg.maxDuration,
		maxQueryRange: cfg.maxQueryRange, maxRegions: cfg.maxRegions, regionWorkers: cfg.regionWorkers,
		metrics: cfg.metrics,
	}, nil
}

func optionalString(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}

func metricTarget(request mcpgo.CallToolRequest) (string, string, error) {
	host := strings.TrimSpace(request.GetString(constant.ArgumentHost, ""))
	container := strings.TrimSpace(request.GetString(constant.ArgumentContainerHost, ""))
	if (host == "") == (container == "") {
		return "", "", errors.New("exactly one of host or container_host is required")
	}
	if len(host) > constant.MaxTargetValueLength || len(container) > constant.MaxTargetValueLength {
		return "", "", fmt.Errorf("host and container_host must not exceed %d bytes", constant.MaxTargetValueLength)
	}
	if host != "" {
		return constant.ArgumentHost, host, nil
	}
	return constant.ArgumentContainerHost, container, nil
}
