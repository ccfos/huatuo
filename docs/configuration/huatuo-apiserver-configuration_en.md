---
title: huatuo-apiserver Configuration
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 5
---

### 1. Overview

`huatuo-apiserver` uses a strictly decoded TOML configuration file. Unknown
or obsolete options prevent startup. Commented options use the built-in
defaults shown below.

### 2. Logging and Runtime Limits

huatuo-apiserver does not create its own cgroup by default. The `[Runtime]` section
applies only when `--enable-cgroup` is explicitly passed; Kubernetes and systemd
deployments should use their native resource controls.

```toml
# Log Configuration
[Log]
    # - Level
    # The log level for huatuo-apiserver: Debug, Info, Warn, Error, Panic.
    # Default: Info
    #
    # Level = "Info"

# Runtime limits for the huatuo-apiserver process.
[Runtime]
    # - CPULimitCores
    # CPU limit in cores.
    # Default: 20
    #
    # - MemoryLimitMiB
    # Memory limit in MiB.
    # Default: 4096
    #
    # CPULimitCores = 20
    # MemoryLimitMiB = 4096
```

- `Log.Level` supports `Debug`, `Info`, `Warn`, `Error`, and `Panic`.
- `CPULimitCores` limits the API server process in CPU cores.
- `MemoryLimitMiB` limits the API server process in MiB.

All resource limits must be greater than zero. `--log-debug` overrides
`Log.Level`.

### 3. HTTP Server

```toml
# HTTP server configuration.
[APIServer]
    # - ListenAddress
    # Listen address in "host:port" form.
    # Default: ":12740"
    #
    # ListenAddress = ":12740"

    # Request rate limiting.
    [APIServer.RateLimit]
        # - RequestsPerSecond
        # Maximum process-wide request rate per second.
        # Default: 200
        #
        # - Burst
        # Maximum process-wide request burst.
        # Default: 200
        #
        # RequestsPerSecond = 200
        # Burst = 200
```

`ListenAddress` uses `host:port` form. An empty host listens on all
interfaces. `RateLimit` is a process-wide token bucket; both values must be
positive. HTTP timeouts and request-size limits are fixed service safeguards
and are not user configurable.

### 4. Jobs and Agent Communication

```toml
# Job persistence.
[Jobs]
    # - StoreDSN
    # SQLite DSN. Relative paths are resolved from this file's directory.
    # Default: "jobs.db"
    #
    # StoreDSN = "jobs.db"

    # Job records use an internal revision for concurrent updates. Only records
    # written by this server version are supported; stop the old service and
    # remove or replace an incompatible database before starting this version.

    # Profiling and tracing retain independent quotas because their resource
    # costs and expected concurrency differ.
    [Jobs.Profiling]
        # - MaxConcurrentPerHost
        # Maximum concurrent profiling jobs on one host.
        # Default: 3
        #
        # - MaxConcurrent
        # Maximum concurrent profiling jobs across all hosts.
        # Default: 500
        #
        # MaxConcurrentPerHost = 3
        # MaxConcurrent = 500

    [Jobs.Tracing]
        # - MaxConcurrentPerHost
        # Maximum concurrent tracing jobs on one host.
        # Default: 5
        #
        # - MaxConcurrent
        # Maximum concurrent tracing jobs across all hosts.
        # Default: 1000
        #
        # MaxConcurrentPerHost = 5
        # MaxConcurrent = 1000

    # Apiserver-owned Job lifecycle policy.
    [Jobs.Controller]
        # StatusPollIntervalSeconds = 5
        # PendingTimeoutSeconds = 30
        # CompletionGracePeriodSeconds = 60
        # NodeUnavailableGracePeriodSeconds = 30
        # JobRetentionPeriodHours = 720

# huatuo-bamai Agent HTTP client configuration.
[Agent]
    # - HTTPPort
    # Agent HTTP server port.
    # Default: 19704
    #
    # HTTPPort = 19704

    [Agent.Auth]
        # Must match HTTPServer.Auth.BearerToken on every Node.
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
```

`StoreDSN` is the SQLite data source for durable job state. Relative paths are
resolved from the configuration directory.

Profiling and tracing use the same quota model but retain independent values.
Their resource cost and expected concurrency differ, so a shared limit would
allow one workload to starve the other.

Agent request transport safeguards use internal client defaults. Job polling,
deadlines, unavailable grace, and retention are owned independently by
`Jobs.Controller`; no runtime negotiation with the Node is performed.

During shutdown, the API server leaves active Node operations running. A
replacement API server recovers active Jobs from durable state and resumes
monitoring without replaying Start.

### 5. Elasticsearch/OpenSearch

```toml
# Optional Elasticsearch/OpenSearch backend for querying profiling data.
[Elasticsearch]
    # Address, Username, and Password must be configured together to enable
    # this backend.
    #
    # - Address
    # Elasticsearch or OpenSearch HTTP address.
    #
    # - Username
    # Elasticsearch or OpenSearch username.
    #
    # - Password
    # Elasticsearch or OpenSearch password.
    #
    # - Index
    # Index containing huatuo-bamai profiling data.
    # Default: "huatuo_bamai"
    #
    # Address = "https://elasticsearch.example.com:9200"
    # Username = "huatuo-apiserver"
    # Password = "REPLACE_WITH_STRONG_PASSWORD"
    # Index = "huatuo_bamai"
```

Storage is optional. `Address`, `Username`, and `Password` must either all be
empty or all be configured. `Index` defaults to `huatuo_bamai` and must match
the collector storage index. When disabled, raw-profile and flame graph query
routes are not registered.

### 6. Authentication and Authorization

```toml
# Authentication configuration.
[Auth]
    # - ID
    # Stable principal identifier stored with jobs.
    #
    # - BearerToken
    # Secret used only to authenticate requests. IDs and tokens must be unique.
    #
    # - Admin
    # Whether the principal has unrestricted API access.
    #
    # - Permissions
    # API method and path patterns granted to a restricted principal.
    #
    # Administrator example:
    # [[Auth.Users]]
    #     ID = "administrator"
    #     BearerToken = "REPLACE_WITH_RANDOM_HEX"
    #     Admin = true
    #
    # Restricted example:
    # [[Auth.Users]]
    #     ID = "huatuo-front"
    #     BearerToken = "REPLACE_WITH_ANOTHER_RANDOM_HEX"
    #     Permissions = [
    #         "GET /v1/tracing",
    #         "GET /v1/tracing/**",
    #         "GET /v1/profiling",
    #         "GET /v1/profiling/**",
    #     ]
```

- `ID` is the required stable principal identifier stored with jobs.
- `BearerToken` is a required secret used only to authenticate requests.
- `Admin` grants access to all routes and ignores `Permissions`.
- `Permissions` is required for non-admin users. Entries may be path-only or
  prefixed with an HTTP method. `*` matches one path segment and `**` matches a
  suffix.

IDs and bearer tokens must each be unique. Rotating a bearer token does not
change job ownership because tokens are never used as principal IDs.

`/readyz`, `/metrics`, and `/version` are public.
`/debug/pprof/**` and `/v1/profiling/flamegraph/**` require an administrator.

### 7. Profiling

```toml
# Profiling result-link configuration.
[Profiling]
    # - DashboardBaseURL
    # Optional dashboard base URL. Result URLs are omitted when empty.
    # Default: empty
    #
    # DashboardBaseURL = "https://grafana.example.com/d"
```

- `DashboardBaseURL` is optional and must use HTTP or HTTPS when configured.
  Result-capable jobs omit a dashboard URL when it is empty.

Profiler execution and aggregation settings are Node-local configuration.

### 8. Model Context Protocol

MCP currently provides read-only access for any agent that supports MCP to
existing anomaly context, profile evidence, and time-series metrics. Replace the
example endpoints, index, and authentication with deployment settings.
Unconfigured backends expose no data tools; `diagnostic_guide` is always
available.

```toml
[MCP]
    Enabled = false
    KnowledgeOverlayDir = ""

[MCP.Events]
    Type = "elasticsearch" # or "opensearch"

[MCP.Events.ESCompatible]
    Address = "http://127.0.0.1:9200"
    Index = "huatuo_events"
    AuthType = "none"

[MCP.Prometheus]
    Address = "http://127.0.0.1:9090"
    AuthType = "none"
    MetricPrefix = "huatuo_"
```

#### Access and backends

- **Endpoint:** `/v1/mcp` uses stateless Streamable HTTP on the API server's port
  with existing bearer authentication. It is disabled by default; enable it with
  `Enabled=true`, `--enable-mcp`, or `ENABLE_MCP=true`. Non-admin users need
  `POST /v1/mcp`, `GET /v1/mcp`, and `DELETE /v1/mcp` in their `Permissions`.
- **Events:** `escompatible` reads existing Elasticsearch/OpenSearch data using
  the address and index in `[MCP.Events.ESCompatible]`, independently of the global
  `[Elasticsearch]` section. `Type` defaults to `elasticsearch`; use `opensearch`
  for OpenSearch without changing the connection section name.
- **Backend authentication:** ES-compatible uses `AuthType="basic"` with
  `Username`/`Password`; `AuthType="none"` clears credentials. Prometheus supports
  `none`/empty for anonymous access, `bearer` with `AuthToken`, or `basic` with
  `AuthUser`/`AuthPass`. Backend credentials are not returned to MCP clients.
- **Startup:** configuration is decoded strictly and invalid settings prevent
  startup. Tools are registered from valid configuration without connectivity
  probes; runtime failures are reported explicitly, and backend recovery does not
  require a service restart.

#### Query budgets

The table lists defaults; budgets with an upper limit can only be lowered.
`ToolTimeout` and `MaxQueryTimeRange` use seconds. Rate limits count backend
requests per minute.

| Setting | Default | Constraint and purpose |
|---------|--------:|------------------------|
| `MCP.MaxConcurrentRequests` | 128 | In-process concurrent requests; maximum 128 |
| `MCP.MaxQueryTimeRange` | 2592000 | 30 days; `0` disables the span cap |
| `MCP.MaxRegionsPerQuery` | 5 | Regions per call; maximum 5 |
| `MCP.UserRateLimit` | 120 | Per-principal budget across backends; `0` disables |
| `MCP.Events.BackendRateLimit` | 300 | Shared event-backend budget; `0` disables |
| `MCP.Events.MaxDocumentsPerQuery` | 500 | Total event/profile source documents; maximum 500 |
| `MCP.Events.ToolTimeout` | 60 | Total event/profile Tool timeout; must be positive |
| `MCP.Prometheus.BackendRateLimit` | 300 | Shared metrics-backend budget; `0` disables |
| `MCP.Prometheus.MaxSeriesPerQuery` | 3000 | Total returned query series; maximum 3000 |
| `MCP.Prometheus.MaxMetricsPerDiscovery` | 800 | Total discovered metrics; maximum 800 |
| `MCP.Prometheus.ToolTimeout` | 60 | Total metrics Tool timeout; must be positive |

Every backend attempt, including retries, consumes budget. Default and regional
connections share their backend budget. Transient failures allow up to 10 retries
within the total Tool timeout; permanent errors such as invalid arguments,
authentication failures, and date parsing errors are not retried unchanged.
MCP sets finite HTTP response write deadlines to the larger Tool timeout plus
5 seconds, or 65 seconds by default. GET notification streams clear the write
deadline; shutdown cancels active requests and releases connections.

#### Multiple regions

Configure the default backend before adding overrides. Routing uses exact names,
then regular expressions in declaration order, then the default backend. Omitted
fields inherit only from the compatible MCP default connection and policy.

```toml
[[MCP.Events.Regions]]
    NamePattern = "^cn-"
    Type = "opensearch"
[MCP.Events.Regions.ESCompatible]
    Address = "http://cn-events:9200"

[[MCP.Prometheus.Regions]]
    NamePattern = "^cn-"
    Address = "http://cn-prometheus:9090"
    MetricPrefix = "huatuo_cn_"
```

ES-compatible regions use `AuthType="none"` to clear inherited credentials.
Prometheus regions can explicitly clear fields such as `AuthToken` and
`MetricPrefix` with empty values; `AuthType="none"` also selects anonymous access.

#### Query contract and results

- All five data tools require exact `region` arrays and `time_range`, accepting Go
  durations, an RFC3339 start time, or `start/end`. Event/profile queries also
  require targets. `query_metrics` requires one `host` or `container_host`; PromQL
  must include the same target matcher and satisfy `MetricPrefix`. The server
  injects region matchers.
- PromQL uses a minimum 15-second step, at most 11000 timestamps per series, and
  matrix ranges of at most one hour; subqueries, offset, and `@` are unsupported.
  Profiles return at most 1000 hotspots, 20 paths per hotspot, and 32 frames per
  path. Decompressed ES responses are capped at 8 MiB and Tool results at 4 MiB.
- `discover_metrics` returns candidate names with `availability=unknown` by
  default. Pass up to 5 exact `metric_names` to verify `present`/`absent`/`unknown`
  and retrieve available label keys and HELP/TYPE/UNIT metadata.
- Total result budgets are divided across regions; a budget smaller than the
  region count is rejected. Caching, count-aware batching, and bounded fan-out
  control query cost without Scroll cursors, bulk writers, or new collection.
- Failed queries return `isError=true` and specific causes. Partial results use
  `status=partial`, retaining `errors`, `warnings`, `query_status`, and truncation
  markers. Count fallback reports an unknown total; `backend_diagnostics` retains
  attempt history. Failed or unknown results must not be interpreted as no data.

#### Text knowledge

An empty `KnowledgeOverlayDir` uses embedded knowledge. Set an external directory
to update Markdown/YAML files under the matching `prompts/` and `resources/`
hierarchy. Existing file edits take effect on the next read without rebuilding.
Missing files fall back to embedded copies; restart after adding Resources to
refresh their registration catalog.

See [MCP diagnostics](/docs/key-feature/mcp_en.md) for the Tool catalog, timestamp
compatibility, and knowledge usage.
