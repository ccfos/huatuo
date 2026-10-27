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
	"sort"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

const (
	DefaultEventSize      = 200
	DefaultAggregateSize  = 500
	DefaultRegionWorkers  = 3
	DefaultTracerCacheTTL = 5 * time.Minute
	DefaultBackendRetries = 10
	MaxTracerCacheEntries = 1024
	// ToolDiscoverEventTypes exposes event types identified by their tracer names.
	ToolDiscoverEventTypes = "discover_event_types"
	// ToolQueryEvents is the fixed MCP name for filtered event searches.
	ToolQueryEvents = "query_events"
	// ToolQueryEventProfiles analyzes profiles captured with diagnostic events.
	ToolQueryEventProfiles = "query_event_profiles"
	// ArgumentHost names host filters and the Prometheus host target.
	ArgumentHost = "host"
	// ArgumentContainerHost names container filters and the Prometheus container target.
	ArgumentContainerHost = "container_host"
	// ArgumentContainerNamespace names the event container-namespace filter.
	ArgumentContainerNamespace = "container_hostnamespace"
	// ArgumentTimeRange names the required duration or RFC3339 query range.
	ArgumentTimeRange = "time_range"
	// ArgumentTracerName names event tracer arrays and the profiler tracer.
	ArgumentTracerName = "tracer_name"
	// ArgumentScope distinguishes target evidence from regional statistics.
	ArgumentScope = "scope"
	// ArgumentGroupBy names the ordered event aggregation dimensions.
	ArgumentGroupBy = "group_by"
	// ArgumentSize names the total event or aggregation bucket budget.
	ArgumentSize = "size"
	// ArgumentTopN names the requested profiler hotspot count.
	ArgumentTopN = "top_n"
	// ArgumentMaxCallPaths names the requested call paths per hotspot.
	ArgumentMaxCallPaths = "max_call_paths"
	// ArgumentMaxStackDepth names the requested returned call-path depth.
	ArgumentMaxStackDepth = "max_stack_depth"
	// ArgumentAnalysisType selects self_time or total_time profiler ranking.
	ArgumentAnalysisType     = "analysis_type"
	ReadOnlyHint             = true
	DestructiveHint          = false
	IdempotentHint           = true
	ToolErrorTimeout         = "timeout"
	ToolErrorBackend         = "backend"
	ToolResultEmpty          = "empty"
	ToolResultPartial        = "partial"
	ToolResultUsage          = " Check MCP isError and JSON status before interpreting data. For invalid_argument, correct the parameter using message and inputSchema. For status=error, inspect errors (backend, operation, region, error_class, error, retryable, suggestion); a failed query is not an empty result or proof of a backend outage. Do not repeat unchanged requests when retryable=false. For status=partial, use only returned data and report errors/warnings as coverage gaps. backend_diagnostics contains prior attempt errors; recovered=true means the operation eventually succeeded, not missing data. Count fallback warnings mean total count is unknown, not that returned events are invalid."
	EventSearchOperation     = "search_events"
	ProfileAnalysisTotalTime = "total_time"
)

// EventRegion maps a region name or regular expression to a tracing event store.
type EventRegion struct {
	NamePattern string
	Store       tracingstore.Reader
}

// EventConfig controls event queries.
type EventConfig struct {
	MaxDocumentsPerQuery int
	ToolTimeout          time.Duration
}

type tracerCacheEntry struct {
	timeRange string
	tracers   []string
	profiler  map[string]bool
	fetched   time.Time
}

type tracerCatalogItem struct {
	Region      string `json:"region"`
	TracerName  string `json:"tracer_name"`
	HasProfiler bool   `json:"has_profiler"`
}

type regionFailure struct {
	Region      string          `json:"region"`
	Error       string          `json:"error"`
	Backend     string          `json:"backend"`
	Operation   string          `json:"operation"`
	ErrorClass  string          `json:"error_class"`
	Retryable   bool            `json:"retryable"`
	Suggestion  string          `json:"suggestion"`
	Tracers     []string        `json:"tracers,omitempty"`
	Causes      []regionFailure `json:"causes,omitempty"`
	metricClass string
}

func (f regionFailure) String() string { return fmt.Sprintf("{%s %s}", f.Region, f.Error) }

type eventQueryResult struct {
	*tracingstore.Document
}

type eventTools struct {
	stores               *regionRouter[tracingstore.Reader]
	tracerCache          map[string]tracerCacheEntry
	tracerCacheMu        sync.RWMutex
	tracerCacheTTL       time.Duration
	maxDocumentsPerQuery int
	maxQueryTimeRange    time.Duration
	maxRegions           int
	regionWorkers        int
}

func registerEventTools(
	server *mcpserver.MCPServer,
	events tracingstore.Reader,
	regions []EventRegion,
	config EventConfig,
	limits QueryLimits,
	metrics *Metrics,
) error {
	if config.MaxDocumentsPerQuery > tracingstore.MaxDocuments {
		return fmt.Errorf("document limit must not exceed %d", tracingstore.MaxDocuments)
	}
	retryPolicy := newRetryPolicy(config.ToolTimeout, DefaultBackendRetries)
	events = withEventRetry(events, retryPolicy, metrics)
	values := make([]regionValue[tracingstore.Reader], 0, len(regions))
	for _, region := range regions {
		store := withEventRetry(region.Store, retryPolicy, metrics)
		values = append(values, regionValue[tracingstore.Reader]{
			name:  region.NamePattern,
			value: store,
		})
	}
	stores, err := newRegionRouter(events, values)
	if err != nil {
		return fmt.Errorf("configure event regions: %w", err)
	}
	r := &eventTools{
		stores:               stores,
		tracerCache:          make(map[string]tracerCacheEntry),
		tracerCacheTTL:       DefaultTracerCacheTTL,
		maxDocumentsPerQuery: config.MaxDocumentsPerQuery,
		maxQueryTimeRange:    limits.MaxQueryTimeRange,
		maxRegions:           limits.MaxRegionsPerQuery,
		regionWorkers:        DefaultRegionWorkers,
	}
	annotation := mcpgo.ToolAnnotation{
		ReadOnlyHint:    boolPointer(ReadOnlyHint),
		DestructiveHint: boolPointer(DestructiveHint),
		IdempotentHint:  boolPointer(IdempotentHint),
	}
	server.AddTool(mcpgo.NewTool(
		ToolDiscoverEventTypes,
		mcpgo.WithDescription("Discover available event types identified by tracer names and whether their stored events have profiling payload markers (has_profiler); this returns a type catalog, not event records or a guarantee that every profile is parseable."+ToolResultUsage),
		mcpgo.WithArray(utils.ArgumentRegion, mcpgo.Required(), mcpgo.MinItems(1), mcpgo.MaxItems(limits.MaxRegionsPerQuery), mcpgo.WithStringItems(mcpgo.MaxLength(utils.MaxRegionNameLength)), mcpgo.Description("Exact regions to discover.")),
		mcpgo.WithString(ArgumentTimeRange, mcpgo.Required(), mcpgo.Description("Required duration or RFC3339 range for tracer discovery.")),
		mcpgo.WithToolAnnotation(annotation),
	), metrics.Wrap(ToolDiscoverEventTypes, withToolTimeout(config.ToolTimeout, r.discoverEventTypes)))
	server.AddTool(mcpgo.NewTool(
		ToolQueryEvents,
		mcpgo.WithDescription(fmt.Sprintf("Query events with explicit scope. scope=target (default) requires tracers and host/container targets, returning bounded events with source references for evidence. scope=region requires exactly one region, time_range (at most %.0f days), and distinct whitelist group_by fields; targets and tracers are optional. It returns bucket counts and first/last timestamps in key order, not raw events or top-count rankings. Null keys retain missing values; multivalued fields can place an event in several buckets. Unsupported mappings fail rather than scan source. For scope=region: ", tracingstore.MaxAggregationTimeRange.Hours()/24)+StatisticsUsageHint+ToolResultUsage),
		mcpgo.WithString(ArgumentScope, mcpgo.Enum(QueryScopeTarget, QueryScopeRegion), mcpgo.DefaultString(QueryScopeTarget)),
		mcpgo.WithArray(ArgumentGroupBy, mcpgo.MinItems(1), mcpgo.MaxItems(len(tracingstore.AggregationFields())), mcpgo.WithStringItems(mcpgo.Enum(tracingstore.AggregationFields()...)), mcpgo.Description("Required only for scope=region; unsupported for scope=target.")),
		mcpgo.WithArray(ArgumentTracerName, mcpgo.Description("Required for scope=target; optional for scope=region."), mcpgo.MinItems(1), mcpgo.MaxItems(tracingstore.MaxTracerNames), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTracerNameLength))),
		mcpgo.WithArray(utils.ArgumentRegion, mcpgo.Required(), mcpgo.MinItems(1), mcpgo.MaxItems(limits.MaxRegionsPerQuery), mcpgo.WithStringItems(mcpgo.MaxLength(utils.MaxRegionNameLength)), mcpgo.Description("Exact regions to query.")),
		mcpgo.WithArray(ArgumentHost, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Host hostnames.")),
		mcpgo.WithArray(ArgumentContainerHost, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Container hostnames.")),
		mcpgo.WithArray(ArgumentContainerNamespace, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Container host namespaces.")),
		mcpgo.WithString(ArgumentTimeRange, mcpgo.Required(), mcpgo.Description("Required duration or RFC3339 range.")),
		mcpgo.WithNumber(ArgumentSize, mcpgo.Max(float64(tracingstore.MaxAggregationBuckets)), mcpgo.Description(fmt.Sprintf("scope=target: event document budget (default %d, capped by configured document limit). scope=region: aggregation buckets (default %d, maximum %d, independent of document limits).", DefaultEventSize, DefaultAggregateSize, tracingstore.MaxAggregationBuckets))),
		mcpgo.WithToolAnnotation(annotation),
	), metrics.Wrap(ToolQueryEvents, withToolTimeout(config.ToolTimeout, r.queryEvents)))
	server.AddTool(mcpgo.NewTool(
		ToolQueryEventProfiles,
		mcpgo.WithDescription("Analyze profiling data stored in diagnostic events for one event type (tracer), returning hotspots and native source locators. This does not start or trigger profiling collection."+ToolResultUsage),
		mcpgo.WithString(ArgumentTracerName, mcpgo.Required(), mcpgo.MaxLength(tracingstore.MaxTracerNameLength), mcpgo.Description("One tracer marked has_profiler by discover_event_types.")),
		mcpgo.WithString(ArgumentRecordID, mcpgo.MaxLength(tracingstore.MaxRecordIDLength), mcpgo.Description("Optional source.record_id from query_events to analyze a single profile document.")),
		mcpgo.WithArray(utils.ArgumentRegion, mcpgo.Required(), mcpgo.MinItems(1), mcpgo.MaxItems(limits.MaxRegionsPerQuery), mcpgo.WithStringItems(mcpgo.MaxLength(utils.MaxRegionNameLength)), mcpgo.Description("Exact regions to query.")),
		mcpgo.WithArray(ArgumentHost, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Host hostnames.")),
		mcpgo.WithArray(ArgumentContainerHost, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Container hostnames.")),
		mcpgo.WithArray(ArgumentContainerNamespace, mcpgo.MaxItems(tracingstore.MaxTargetValues), mcpgo.WithStringItems(mcpgo.MaxLength(tracingstore.MaxTargetValueLength)), mcpgo.Description("Container host namespaces.")),
		mcpgo.WithString(ArgumentTimeRange, mcpgo.Required(), mcpgo.Description("Required duration or RFC3339 range.")),
		mcpgo.WithNumber(ArgumentTopN, mcpgo.Description(fmt.Sprintf("Top hotspot functions. Default: %d.", DefaultProfileTopN))),
		mcpgo.WithNumber(ArgumentMaxCallPaths, mcpgo.Description(fmt.Sprintf("Max call paths per function. Default: %d.", DefaultProfileCallPaths))),
		mcpgo.WithNumber(ArgumentMaxStackDepth, mcpgo.Description(fmt.Sprintf("Max stack depth. Default: %d.", DefaultProfileStackDepth))),
		mcpgo.WithString(ArgumentAnalysisType, mcpgo.Description("self_time or total_time"), mcpgo.Enum(ProfileAnalysisSelfTime, ProfileAnalysisTotalTime)),
		mcpgo.WithToolAnnotation(annotation),
	), metrics.Wrap(ToolQueryEventProfiles, withToolTimeout(config.ToolTimeout, r.queryEventProfiles)))
	return nil
}

func (r *eventTools) discoverEventTypes(
	ctx context.Context,
	request mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	regions, err := requiredRegions(request, r.maxRegions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	requestedRange, err := request.RequireString(ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError("time_range is required"), nil
	}
	start, end, err := utils.ParseTimeRange(requestedRange, r.maxQueryTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid time_range: %v", err)), nil
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (tracerCacheEntry, error) {
		return r.discoverRegionTracers(ctx, region, strings.TrimSpace(requestedRange), start, end)
	})
	items := make([]tracerCatalogItem, 0)
	failures := make([]regionFailure, 0)
	successfulRegions := 0
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(r.stores.forRegion(outcome.Region).BackendType(), ToolDiscoverEventTypes, outcome.Region, outcome.Err))
			continue
		}
		successfulRegions++
		for _, tracer := range outcome.Value.tracers {
			items = append(items, tracerCatalogItem{
				Region:      outcome.Region,
				TracerName:  tracer,
				HasProfiler: outcome.Value.profiler[tracer],
			})
		}
	}
	if len(items) == 0 && successfulRegions == 0 && len(failures) > 0 {
		return backendFailuresResult(ctx, "discover ES tracers failed", failures), nil
	}
	observeCollectionResult(ctx, len(items), failures)
	return indentedJSONResult(map[string]any{"status": collectionStatus(false, failures), "tracers": items, "errors": failures})
}

func (r *eventTools) discoverRegionTracers(ctx context.Context, region, timeRange string, start, end time.Time) (tracerCacheEntry, error) {
	r.tracerCacheMu.RLock()
	entry, cached := r.tracerCache[region]
	r.tracerCacheMu.RUnlock()
	if cached && entry.timeRange == timeRange && time.Since(entry.fetched) < r.tracerCacheTTL {
		return copyTracerCacheEntry(entry), nil
	}
	store := r.stores.forRegion(region)
	query := &tracingstore.Query{Region: region, StartTime: start, EndTime: end, Limit: tracingstore.MaxTracerCatalogSize + 1}
	tracers, err := store.TracerNames(ctx, query)
	if err != nil {
		return tracerCacheEntry{}, err
	}
	if len(tracers) > tracingstore.MaxTracerCatalogSize {
		return tracerCacheEntry{}, fmt.Errorf("tracer catalog exceeds %d names", tracingstore.MaxTracerCatalogSize)
	}
	profileQuery := *query
	profileQuery.ProfileOnly = true
	profileTracers, err := store.TracerNames(ctx, &profileQuery)
	if err != nil {
		return tracerCacheEntry{}, err
	}
	if len(profileTracers) > tracingstore.MaxTracerCatalogSize {
		return tracerCacheEntry{}, fmt.Errorf("profiler tracer catalog exceeds %d names", tracingstore.MaxTracerCatalogSize)
	}
	profiler := make(map[string]bool, len(profileTracers))
	for _, tracer := range profileTracers {
		profiler[tracer] = true
	}
	sort.Strings(tracers)
	entry = tracerCacheEntry{timeRange: timeRange, tracers: append([]string(nil), tracers...), profiler: profiler, fetched: time.Now()}
	r.tracerCacheMu.Lock()
	for name, cachedEntry := range r.tracerCache {
		if time.Since(cachedEntry.fetched) >= r.tracerCacheTTL {
			delete(r.tracerCache, name)
		}
	}
	if _, exists := r.tracerCache[region]; !exists && len(r.tracerCache) >= MaxTracerCacheEntries {
		var oldest string
		var fetched time.Time
		for name, cachedEntry := range r.tracerCache {
			if fetched.IsZero() || cachedEntry.fetched.Before(fetched) {
				oldest, fetched = name, cachedEntry.fetched
			}
		}
		delete(r.tracerCache, oldest)
	}
	r.tracerCache[region] = entry
	r.tracerCacheMu.Unlock()
	return copyTracerCacheEntry(entry), nil
}

func (r *eventTools) queryEvents(
	ctx context.Context,
	request mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	scope, err := queryScope(request)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if scope == QueryScopeRegion {
		return r.aggregateEvents(ctx, request)
	}
	if _, exists := request.GetArguments()[ArgumentGroupBy]; exists {
		return mcpgo.NewToolResultError(ArgumentGroupBy + " is only supported for scope=region"), nil
	}
	tracers, err := boundedStringArray(
		request.GetArguments(), ArgumentTracerName, true, tracingstore.MaxTracerNames, tracingstore.MaxTracerNameLength,
	)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	regions, err := requiredRegions(request, r.maxRegions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	hostnames, err := boundedStringArray(request.GetArguments(), ArgumentHost, false, tracingstore.MaxTargetValues, tracingstore.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	containerHostnames, err := boundedStringArray(request.GetArguments(), ArgumentContainerHost, false, tracingstore.MaxTargetValues, tracingstore.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	namespaces, err := boundedStringArray(request.GetArguments(), ArgumentContainerNamespace, false, tracingstore.MaxTargetValues, tracingstore.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if len(hostnames) == 0 && len(containerHostnames) == 0 && len(namespaces) == 0 {
		return mcpgo.NewToolResultError("at least one of host, container_host or container_hostnamespace must be specified"), nil
	}
	requestedRange, err := request.RequireString(ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError("time_range is required"), nil
	}
	start, end, err := utils.ParseTimeRange(requestedRange, r.maxQueryTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid time_range: %v", err)), nil
	}
	size := clampEventInt(request.GetInt(ArgumentSize, DefaultEventSize), r.queryLimit(), DefaultEventSize)
	quotas, err := distributeLimit(size, regions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (tracingstore.SearchResult, error) {
		if quotas[region] == 0 {
			return tracingstore.SearchResult{}, nil
		}
		filter := &tracingstore.Query{
			Region:                  region,
			Hostnames:               hostnames,
			ContainerHostnames:      containerHostnames,
			ContainerHostNamespaces: namespaces,
			TracerNames:             append([]string(nil), tracers...),
			StartTime:               start,
			EndTime:                 end,
		}
		return r.searchEvents(ctx, r.stores.forRegion(region), filter, tracers, quotas[region])
	})
	documents := make([]eventQueryResult, 0, size)
	failures := make([]regionFailure, 0)
	statuses := make([]tracingstore.QueryStatus, 0, len(outcomes))
	truncated := false
	partial := false
	successfulRegions := 0
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(r.stores.forRegion(outcome.Region).BackendType(), ToolQueryEvents, outcome.Region, outcome.Err))
		} else {
			successfulRegions++
		}
		statuses = append(statuses, outcome.Value.Status(outcome.Region))
		truncated = truncated || outcome.Value.Truncated
		partial = partial || outcome.Value.Truncated || len(outcome.Value.Warnings) > 0
		for _, document := range outcome.Value.Documents {
			if document != nil {
				documentCopy := tracingstore.RoutedDocument(document, r.stores.forRegion(outcome.Region).BackendType(), outcome.Region)
				documents = append(documents, eventQueryResult{
					Document: documentCopy,
				})
			}
		}
	}
	sort.SliceStable(documents, func(i, j int) bool {
		return documents[i].QueryTimestamp().After(documents[j].QueryTimestamp())
	})
	if len(documents) > size {
		documents = documents[:size]
	}
	if len(documents) == 0 && successfulRegions == 0 && len(failures) > 0 {
		return backendFailuresResult(ctx, "ES event query failed", failures), nil
	}
	observeCollectionResult(ctx, len(documents), failures)
	if partial {
		observeToolResult(ctx, ToolResultPartial)
	}
	return boundedIndentedJSONResult(map[string]any{"status": collectionStatus(partial, failures), "events": documents, "errors": failures, "truncated": truncated, "query_status": statuses}, MaxESResultBytes)
}

func observeCollectionResult(ctx context.Context, items int, failures []regionFailure) {
	switch {
	case len(failures) > 0:
		observeToolResult(ctx, ToolResultPartial)
		observeToolErrorClass(ctx, regionFailureErrorClass(failures))
	case items == 0:
		observeToolResult(ctx, ToolResultEmpty)
	}
}

func regionFailureErrorClass(failures []regionFailure) string {
	for _, class := range []string{
		ToolErrorBackend, ToolErrorTimeout, ToolErrorRateLimited,
		ToolErrorCanceled, ToolErrorInternal,
	} {
		for index := range failures {
			if failures[index].metricClass == class {
				return class
			}
		}
	}
	return ToolErrorInvalidArgument
}

func requiredStringArray(arguments map[string]any, name string) ([]string, error) {
	value, exists := arguments[name]
	if !exists {
		return nil, fmt.Errorf("%s is required", name)
	}
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		result := make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s must contain only strings", name)
			}
			result[index] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
}

func boundedStringArray(
	arguments map[string]any,
	name string,
	required bool,
	maximumItems int,
	maximumLength int,
) ([]string, error) {
	if _, exists := arguments[name]; !exists {
		if required {
			return nil, fmt.Errorf("%s is required", name)
		}
		return nil, nil
	}
	values, err := requiredStringArray(arguments, name)
	if err != nil {
		return nil, err
	}
	if required && len(values) == 0 {
		return nil, fmt.Errorf("%s is required", name)
	}
	if maximumItems > 0 && len(values) > maximumItems {
		return nil, fmt.Errorf("%s must contain at most %d values", name, maximumItems)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s values must not be blank", name)
		}
		if maximumLength > 0 && len(value) > maximumLength {
			return nil, fmt.Errorf("%s values must not exceed %d bytes", name, maximumLength)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (r *eventTools) queryLimit() int {
	if r.maxDocumentsPerQuery > 0 {
		return r.maxDocumentsPerQuery
	}
	return tracingstore.MaxDocuments
}

func (r *eventTools) searchEvents(
	ctx context.Context,
	store tracingstore.Reader,
	base *tracingstore.Query,
	tracers []string,
	size int,
) (tracingstore.SearchResult, error) {
	if len(tracers) <= 1 {
		filter := copyEventFilter(base)
		filter.TracerNames = append([]string(nil), tracers...)
		filter.Limit = size
		return boundedEventSearch(ctx, store, filter, size)
	}
	totalFilter := copyEventFilter(base)
	totalFilter.TracerNames = append([]string(nil), tracers...)
	total, err := store.Count(ctx, totalFilter)
	if err != nil {
		totalFilter.Limit = size
		result, searchErr := boundedEventSearch(ctx, store, totalFilter, size)
		result.TotalCount, result.CountExact = nil, false
		result.Warnings = append(result.Warnings, "Count failed; fell back to bounded SearchEvents; total count is unknown: "+boundedLogValue(err.Error(), tracingstore.MaxBackendErrorBodyBytes))
		if searchErr != nil {
			return result, errors.Join(fmt.Errorf("event Count failed: %w", err), searchErr)
		}
		return result, nil
	}
	if total <= int64(size) {
		totalFilter.Limit = size
		return boundedEventSearch(ctx, store, totalFilter, size)
	}

	counts, err := r.eventTracerCounts(ctx, store, base, tracers)
	if err != nil {
		return tracingstore.SearchResult{}, err
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count == counts[j].Count {
			return counts[i].Name < counts[j].Name
		}
		return counts[i].Count < counts[j].Count
	})

	var documents []*tracingstore.Document
	result := tracingstore.SearchResult{Truncated: true, TotalCount: &total, CountExact: true}
	var failed []string
	var failures []regionFailure
	var causes []error
	for index := 0; index < len(counts); {
		remaining := size - len(documents)
		if remaining <= 0 {
			break
		}
		batch := []string{counts[index].Name}
		batchTotal := counts[index].Count
		index++
		for index < len(counts) && batchTotal+counts[index].Count <= int64(size) {
			batch = append(batch, counts[index].Name)
			batchTotal += counts[index].Count
			index++
		}
		filter := copyEventFilter(base)
		filter.TracerNames = batch
		filter.Limit = remaining
		batchResult, searchErr := boundedEventSearch(ctx, store, filter, remaining)
		documents = append(documents, batchResult.Documents...)
		result.Warnings = append(result.Warnings, batchResult.Warnings...)
		if searchErr != nil {
			failed = append(failed, batch...)
			failure := newRegionFailure(store.BackendType(), EventSearchOperation, base.Region, searchErr)
			failure.Tracers = append([]string(nil), batch...)
			failures = append(failures, failure)
			causes = append(causes, searchErr)
			continue
		}
	}
	result.Documents = documents
	if len(failed) > 0 {
		return result, &partialQueryError{FailedTracers: failed, ReturnedRecords: len(documents), Failures: failures, causes: causes}
	}
	return result, nil
}

func boundedEventSearch(
	ctx context.Context,
	store tracingstore.Reader,
	filter *tracingstore.Query,
	limit int,
) (tracingstore.SearchResult, error) {
	result, err := store.SearchEvents(ctx, filter)
	if len(result.Documents) > limit {
		result.Documents = result.Documents[:limit]
		result.Truncated = true
	}
	return result, err
}

func (r *eventTools) eventTracerCounts(
	ctx context.Context,
	store tracingstore.Reader,
	base *tracingstore.Query,
	tracers []string,
) ([]tracingstore.TracerCount, error) {
	if counter, ok := store.(tracingstore.TracerCounter); ok {
		filter := copyEventFilter(base)
		filter.TracerNames = append([]string(nil), tracers...)
		filter.Limit = len(tracers)
		values, err := counter.TracerCounts(ctx, filter)
		if err != nil && !errors.Is(err, tracingstore.ErrUnsupportedOp) {
			return nil, err
		}
		if err == nil {
			byName := make(map[string]int64, len(values))
			for _, value := range values {
				byName[value.Name] = value.Count
			}
			counts := make([]tracingstore.TracerCount, 0, len(tracers))
			for _, tracer := range tracers {
				counts = append(counts, tracingstore.TracerCount{Name: tracer, Count: byName[tracer]})
			}
			return counts, nil
		}
	}

	counts := make([]tracingstore.TracerCount, 0, len(tracers))
	for _, tracer := range tracers {
		filter := copyEventFilter(base)
		filter.TracerNames = []string{tracer}
		count, err := store.Count(ctx, filter)
		if err != nil {
			return nil, err
		}
		counts = append(counts, tracingstore.TracerCount{Name: tracer, Count: count})
	}
	return counts, nil
}

type partialQueryError struct {
	FailedTracers   []string
	ReturnedRecords int
	Failures        []regionFailure
	causes          []error
}

func (e *partialQueryError) Error() string {
	return fmt.Sprintf("partial es query: %d tracers failed (%v), %d returned: %v", len(e.FailedTracers), e.FailedTracers, e.ReturnedRecords, errors.Join(e.causes...))
}

func (e *partialQueryError) Unwrap() []error { return e.causes }

func copyTracerCacheEntry(entry tracerCacheEntry) tracerCacheEntry {
	copyEntry := entry
	copyEntry.tracers = append([]string(nil), entry.tracers...)
	copyEntry.profiler = make(map[string]bool, len(entry.profiler))
	for tracer, available := range entry.profiler {
		copyEntry.profiler[tracer] = available
	}
	return copyEntry
}

func distributeLimit(total int, regions []string) (map[string]int, error) {
	limits := make(map[string]int, len(regions))
	if len(regions) == 0 {
		return limits, nil
	}
	if total < len(regions) {
		return nil, fmt.Errorf("result limit %d must be at least the number of queryable regions (%d)", total, len(regions))
	}
	base, remainder := total/len(regions), total%len(regions)
	for index, region := range regions {
		limit := base
		if index < remainder {
			limit++
		}
		limits[region] = limit
	}
	return limits, nil
}

func copyEventFilter(filter *tracingstore.Query) *tracingstore.Query {
	copyFilter := *filter
	copyFilter.Hostnames = append([]string(nil), filter.Hostnames...)
	copyFilter.ContainerHostnames = append([]string(nil), filter.ContainerHostnames...)
	copyFilter.ContainerHostNamespaces = append([]string(nil), filter.ContainerHostNamespaces...)
	copyFilter.TracerNames = append([]string(nil), filter.TracerNames...)
	return &copyFilter
}

func clampEventInt(value, maximum, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	return min(value, maximum)
}

func indentedJSONResult(value any) (*mcpgo.CallToolResult, error) {
	return boundedIndentedJSONResult(value, MaxESResultBytes)
}
