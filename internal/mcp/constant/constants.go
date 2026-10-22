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

// Package constant centralizes MCP protocol values and fixed runtime policies.
package constant

import "time"

// Configured query budgets cannot exceed these limits.
const (
	MaxConcurrent = 128
	MaxRegions    = 5
	MaxDocuments  = 500
	MaxMetrics    = 800
	MaxSeries     = 3000
)

// Shared defaults apply when request or backend configuration omits a value.
const (
	DefaultEventSize         = 200
	DefaultProfileTopN       = 50
	DefaultProfileCallPaths  = 5
	DefaultProfileStackDepth = 12
	DefaultRegionWorkers     = 3
	DefaultTracerCacheTTL    = 5 * time.Minute
	DefaultBackendRetries    = 10
	DefaultRetryDelay        = 100 * time.Millisecond
	DefaultPrometheusStep    = "1m"
	DefaultSearchLimit       = 100
	DefaultPromTimeout       = 30 * time.Second
	DefaultPromDuration      = time.Minute
)

// Allocation limits remain independent of operator-configured query budgets.
const (
	MaxMetricNamesPerLookup = 5
	MaxMetricNameLength     = 256
	MaxTracerCacheEntries   = 1024
	// MaxSourceScanDocuments bounds exact compatibility reads of unindexed fields.
	MaxSourceScanDocuments = 500
)

const (
	// ToolDiscoverMetrics is the fixed MCP name for Prometheus metric discovery.
	ToolDiscoverMetrics = "discover_metrics"
	// ToolQueryMetrics is the fixed MCP name for bounded PromQL range queries.
	ToolQueryMetrics = "query_metrics"
	// ToolDiscoverTracers is the fixed MCP name for region tracer discovery.
	ToolDiscoverTracers = "discover_tracers"
	// ToolQueryEvents is the fixed MCP name for filtered event searches.
	ToolQueryEvents = "query_events"
	// ToolQueryProfiles is the fixed MCP name for automatic profiler analysis.
	ToolQueryProfiles = "query_profiles"
	// ToolDiagnosticGuide is the fixed MCP name of the diagnostic workflow Tool.
	ToolDiagnosticGuide = "diagnostic_guide"
)

const (
	// ArgumentRegion names the exact region used to route backend queries.
	ArgumentRegion = "region"
	// ArgumentHost names host filters and the Prometheus host target.
	ArgumentHost = "host"
	// ArgumentContainerHost names container filters and the Prometheus container target.
	ArgumentContainerHost = "container_host"
	// ArgumentContainerNamespace names the event container-namespace filter.
	ArgumentContainerNamespace = "container_hostnamespace"
	// ArgumentTimeRange names the required duration or RFC3339 query range.
	ArgumentTimeRange   = "time_range"
	ArgumentMetricNames = "metric_names"
	ArgumentRecordID    = "record_id"
	// ArgumentTracerName names event tracer arrays and the profiler tracer.
	ArgumentTracerName = "tracer_name"
	// ArgumentSize names the requested event count.
	ArgumentSize = "size"
	// ArgumentTopN names the requested profiler hotspot count.
	ArgumentTopN = "top_n"
	// ArgumentMaxCallPaths names the requested call paths per hotspot.
	ArgumentMaxCallPaths = "max_call_paths"
	// ArgumentMaxStackDepth names the requested returned call-path depth.
	ArgumentMaxStackDepth = "max_stack_depth"
	// ArgumentAnalysisType selects self_time or total_time profiler ranking.
	ArgumentAnalysisType = "analysis_type"
	// ArgumentQuery names the required PromQL expression; logs store only its hash.
	ArgumentQuery = "query"
	// ArgumentStep names the Prometheus range-query sampling interval.
	ArgumentStep = "step"
	// ArgumentSubsystem selects the subsystem Prompt or subsystem-stage guide.
	ArgumentSubsystem = "subsystem"
	// ArgumentStage selects the guide workflow stage; the default is overview.
	ArgumentStage = "stage"
)

const (
	// MaxRegionNameLength is the fixed byte limit for each exact region name.
	MaxRegionNameLength = 256
)

// MaxESResultBytes is the fixed 4 MiB cap for JSON-encoded MCP Tool results.
const MaxESResultBytes = 4 * 1024 * 1024

const (
	// MaxTracerCatalogSize caps each region's complete tracer catalog.
	MaxTracerCatalogSize = 1000
	// MaxTracerNames caps tracer names accepted by one event query.
	MaxTracerNames = 100
	// MaxTracerNameLength is the fixed byte limit for each tracer name.
	MaxTracerNameLength = 256
	MaxRecordIDLength   = 512
	// MaxTargetValues caps each host, container, or namespace target array.
	MaxTargetValues = 100
	// MaxTargetValueLength is the fixed byte limit for each query target.
	MaxTargetValueLength = 512
)

const (
	// MaxProfileTopN is the fixed cap on returned profiler hotspots.
	MaxProfileTopN = 1000
	// MaxProfileCallPaths is the fixed cap on call paths per hotspot.
	MaxProfileCallPaths = 20
	// MaxProfileStackDepth is the fixed cap on retained call-path frames.
	MaxProfileStackDepth = 32
	// MaxAnalysisFrames caps parsed profiler frames in one Tool call.
	MaxAnalysisFrames = 100000
	// MaxAnalysisFunctions caps distinct frame labels during profile analysis.
	MaxAnalysisFunctions = 10000
	// MaxAnalysisCallPaths caps distinct aggregated paths during profile analysis.
	MaxAnalysisCallPaths = 50000
)

const (
	// MaxPrometheusResponse caps decompressed backend responses and Tool results.
	MaxPrometheusResponse = 4 << 20
	// MaxPromQLLength is the fixed byte limit for one PromQL expression.
	MaxPromQLLength = 4096
	// MaxQueryPoints caps timestamps returned per Prometheus series.
	MaxQueryPoints = 11000
	// MaxSelectorRange caps each PromQL matrix selector independently of query range.
	MaxSelectorRange = time.Hour
)

// LogTag prefixes knowledge logs; the trailing space separates it from messages.
const LogTag = "[mcp-knowledge] "

const (
	EventBackendElasticsearch = "elasticsearch"
	EventBackendOpenSearch    = "opensearch"
	BackendNameOpenSearch     = "OpenSearch"
)

const (
	ServerName            = "huatuo"
	ServerVersion         = "1.0.0"
	Endpoint              = "/v1/mcp"
	MetricsNamespace      = "huatuo_apiserver"
	MetricsSubsystem      = "mcp"
	RequestKindRPC        = "rpc"
	RequestKindStream     = "stream"
	ConcurrencyKindTotal  = "total"
	ConcurrencyKindStream = "stream"
	StreamCapacityDivisor = 4
	ReadOnlyHint          = true
	DestructiveHint       = false
	IdempotentHint        = true
)

const (
	ToolOutcomeSuccess           = "success"
	ToolOutcomePartialSuccess    = "partial_success"
	ToolOutcomeError             = "error"
	ToolErrorNone                = "none"
	ToolErrorInvalidArgument     = "invalid_argument"
	ToolErrorRateLimited         = "rate_limited"
	ToolErrorTimeout             = "timeout"
	ToolErrorCanceled            = "canceled"
	ToolErrorBackend             = "backend"
	ToolErrorOversized           = "oversized"
	ToolErrorUnsupportedPayload  = "unsupported_payload"
	ToolErrorInternal            = "internal"
	ToolResultNonempty           = "nonempty"
	ToolResultEmpty              = "empty"
	ToolResultPartial            = "partial"
	ToolResultError              = "error"
	ToolResultOversized          = "oversized"
	ToolResultUnsupportedPayload = "unsupported_payload"
	BackendES                    = "elasticsearch"
	BackendPrometheus            = "prometheus"
	BackendNameES                = "Elasticsearch"
	BackendNamePrometheus        = "Prometheus"
	BackendOutcomeRetryableError = "retryable_error"
	BackendErrorDeadline         = "deadline"
	BackendErrorRateLimit        = "rate_limit"
	BackendErrorTransport        = "transport"
	BackendErrorIncompleteData   = "incomplete_data"
	BackendErrorDecode           = "decode"
	BackendErrorInvalidQuery     = "invalid_query"
	BackendErrorUnsupported      = "unsupported"
	BackendErrorUnknown          = "unknown"
	RateLimitScopeGlobal         = "global"
	RateLimitScopeUser           = "user"
	RateLimitWindow              = time.Minute
	ProfileDocumentParsed        = "parsed"
	ProfileDocumentUnsupported   = "unsupported"
)

const (
	ToolResultUsage = " Check MCP isError and JSON status before interpreting data. For invalid_argument, correct the parameter using message and inputSchema. For status=error, inspect errors (backend, operation, region, error_class, error, retryable, suggestion); a failed query is not an empty result or proof of a backend outage. Do not repeat unchanged requests when retryable=false. For status=partial, use only returned data and report errors/warnings as coverage gaps. backend_diagnostics contains prior attempt errors; recovered=true means the operation eventually succeeded, not missing data. Count fallback warnings mean total count is unknown, not that returned events are invalid."

	MaxESResponseBytes              = 8 << 20
	MaxBackendErrorBodyBytes        = 4096
	MaxBackendDiagnosticOperations  = 32
	MaxBackendDiagnosticAttempts    = 16
	MaxShardFailureSummaries        = 3
	MaxShardReasonDepth             = 3
	MaxBackendErrorReasonBytes      = 512
	MaxBackendErrorTypeBytes        = 128
	MaxShardIdentifierBytes         = 128
	MaxRetryBackoffShift            = 20
	MaxRetryDelay                   = time.Duration(1<<63 - 2)
	ESDialTimeout                   = 10 * time.Second
	HTTPKeepAlive                   = 30 * time.Second
	ESResponseHeaderTimeout         = 10 * time.Second
	TLSHandshakeTimeout             = 10 * time.Second
	HTTPExpectContinueTimeout       = time.Second
	HTTPMaxIdleConnections          = 100
	ESIdleConnectionTimeout         = 50 * time.Second
	PrometheusDialTimeout           = 30 * time.Second
	PrometheusIdleConnectionTimeout = 90 * time.Second
	MCPWriteTimeoutGrace            = 5 * time.Second
	MinPrometheusStep               = 15 * time.Second
	TimeRangeNow                    = "now"
	AuthTypeNone                    = "none"
	AuthTypeBearer                  = "bearer"
	AuthTypeBasic                   = "basic"
	MIMEJSON                        = "application/json"
	MIMEForm                        = "application/x-www-form-urlencoded"
	MIMEMarkdown                    = "text/markdown"
	MIMEYAML                        = "text/yaml"
	HeaderAccept                    = "Accept"
	HeaderContentType               = "Content-Type"
	HeaderContentEncoding           = "Content-Encoding"
	HeaderAuthorization             = "Authorization"
	HeaderElasticProduct            = "X-Elastic-Product"
	BearerPrefix                    = "Bearer "
	EncodingGzip                    = "gzip"
)

const (
	PrometheusQueryPath       = "/api/v1/query"
	PrometheusRangeQueryPath  = "/api/v1/query_range"
	PrometheusMetricNamesPath = "/api/v1/label/__name__/values"
	PrometheusSeriesPath      = "/api/v1/series"
	PrometheusLabelsPath      = "/api/v1/labels"
	PrometheusMetadataPath    = "/api/v1/metadata"
	MetricAvailabilityUnknown = "unknown"
	MetricAvailabilityPresent = "present"
	MetricAvailabilityAbsent  = "absent"
	MetricMetadataUnavailable = "unavailable"
	MetricMetadataAmbiguous   = "ambiguous_or_incomplete"
	MetricMetadataAvailable   = "available"
	PrometheusMetricNameLabel = "__name__"
	PrometheusMatchParameter  = "match[]"
	QueryStartParameter       = "start"
	QueryEndParameter         = "end"
	ResultTypeVector          = "vector"
	ResultTypeMatrix          = "matrix"
	APIStatusSuccess          = "success"
	ESSearchOperation         = "search"
	ESCountOperation          = "count"
	ESFieldCapsOperation      = "field_caps"
	ESFieldCapsPathSuffix     = "/_field_caps"
	ESUploadedTimeField       = "uploaded_time"
	ESTimeField               = "time"
	ESQueryTimeFormat         = "strict_date_optional_time_nanos"
	ESTracerSourceScript      = "def s = params['_source']; if (s == null || s.isEmpty()) { throw new IllegalArgumentException('MCP tracer aggregation requires readable _source'); } return s[params['field']];"
	ESFieldCapsCacheTTL       = 5 * time.Minute
	ESQueryTimeSortScript     = "def v; if (doc.containsKey('uploaded_timestamp') && doc['uploaded_timestamp'].size() != 0) { v = doc['uploaded_timestamp'].value; } else if (doc.containsKey('uploaded_time') && doc['uploaded_time'].size() != 0) { v = doc['uploaded_time'].value; } else if (doc.containsKey('time') && doc['time'].size() != 0) { v = doc['time'].value; } else { return 0; } return v instanceof ZonedDateTime ? v.toInstant().toEpochMilli() : v.getMillis();"
	ESSearchPathSuffix        = "/_search"
	ESCountPathSuffix         = "/_count"
	EventSearchOperation      = "search_events"
	EventCountOperation       = "count_events"
	TracerCountOperation      = "count_tracers"
	ProfileFramesField        = "tracer_data.flamedata.label"
	ProfileSamplesField       = "tracer_data.flamedata.profile.sample.value"
	ProfileLocationsField     = "tracer_data.flamedata.profile.location.line.function_id"
	ProfileFunctionsField     = "tracer_data.flamedata.profile.function.name"
	ProfileSampleTypesField   = "tracer_data.flamedata.profile.sample_type.type"
	ProfileRootLabel          = "[root]"
	MaxProfileSourceDepth     = 1024
	ProfileAnalysisSelfTime   = "self_time"
	ProfileAnalysisTotalTime  = "total_time"
	ProfilePayloadFormat      = "frame_data"
)

const (
	GuideStageOverview           = "overview"
	GuideStageSubsystem          = "subsystem"
	GuideStageCorrelation        = "correlation"
	GuideStageProfiler           = "profiler"
	SubsystemCPU                 = "cpu"
	SubsystemMemory              = "memory"
	SubsystemIO                  = "io"
	SubsystemNetwork             = "network"
	SubsystemHardware            = "hardware"
	ResourceURIPrefix            = "huatuo://"
	ManifestURI                  = "huatuo://data-manifest"
	ResourceDirectory            = "resources"
	ResourcePathPrefix           = "resources/"
	PromptDirectory              = "prompts"
	AutoDiagnosisPrompt          = "auto-diagnosis"
	SubsystemDiagnosisPrompt     = "subsystem-diagnosis"
	AutoDiagnosisPromptPath      = "prompts/auto-diagnosis.md"
	SubsystemDiagnosisPromptPath = "prompts/subsystem-diagnosis.md"
	StorageModelPath             = "resources/storage_model.md"
	PlaybookPath                 = "resources/playbook/guide.md"
	CrossDomainPath              = "resources/cross_domain.md"
	ProfilingMethodologyPath     = "resources/profiling/methodology.md"
	ProfilingSignaturesPath      = "resources/profiling/kernel_signatures.md"
	SubsystemResourcesPrefix     = "resources/subsystems/"
	TracerResourceSuffix         = "/tracers.yaml"
	PatternResourceSuffix        = "/patterns.md"
)

const (
	LogComponentTool       = "mcp-tool"
	LogComponentBackend    = "mcp-backend"
	MaxPrincipalLogBytes   = 64
	MaxArgumentLogBytes    = 128
	MaxShortLogBytes       = 64
	MaxLogSliceItems       = 8
	ErrorHashPrefixBytes   = 6
	ResultByteBucketStart  = 256
	ResultByteBucketFactor = 4
	ResultByteBucketCount  = 8
)

const (
	MetricOperations            = "operations_total"
	MetricActiveRequests        = "active_requests"
	MetricConcurrencyRejections = "concurrency_rejections_total"
	MetricConcurrencyLimit      = "concurrency_limit"
	MetricToolCalls             = "tool_calls_total"
	MetricToolDuration          = "tool_duration_seconds"
	MetricToolResults           = "tool_results_total"
	MetricToolResultBytes       = "tool_result_bytes"
	MetricBackendAttempts       = "backend_attempts_total"
	MetricBackendDuration       = "backend_attempt_duration_seconds"
	MetricRetries               = "retries_total"
	MetricRateLimitRejections   = "rate_limit_rejections_total"
	MetricProfileDocuments      = "profile_documents_total"
	LabelMethod                 = "method"
	LabelOutcome                = "outcome"
	LabelKind                   = "kind"
	LabelTool                   = "tool"
	LabelErrorClass             = "error_class"
	LabelState                  = "state"
	LabelBackend                = "backend"
	LabelOperation              = "operation"
	LabelScope                  = "scope"
	EnableEnvironmentVariable   = "ENABLE_MCP"
	MarkdownExtension           = ".md"
	YAMLExtension               = ".yaml"
	ShortYAMLExtension          = ".yml"
)

// DurationBuckets returns separate slices so collectors cannot mutate a shared
// policy table. Go does not permit slices to be declared as const.
func DurationBuckets() []float64 {
	return []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
}
