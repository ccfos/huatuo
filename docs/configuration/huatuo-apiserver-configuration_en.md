---
title: API Server Configuration
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 5
---

### 1. Overview

`huatuo-apiserver.conf` configures the HTTP server, job management, node communication, authentication, and profiling data queries for huatuo-apiserver.

This document quotes the repository configuration file and describes its fields in tables. A default is the value supplied by the program when a field is omitted. Uncommented assignments override defaults. Assignments commented out with `#` are inactive examples and may differ from defaults.

Before startup, replace the `Agent.Auth.BearerToken` placeholder and configure at least one user in `Auth.Users`. Replace example addresses and credentials with values for your deployment.

The configuration file uses strict TOML parsing. Unknown or obsolete fields prevent startup. Restart the service after editing the file.

### 2. Logging

```toml
# Log Configuration
[Log]
    # - Level
    # The log level for huatuo-apiserver: Debug, Info, Warn, Error, Panic.
    # Default: Info
    #
    # Level = "Info"
```

| Field | Description |
| --- | --- |
| `Level` | String. Default: `"Info"`. Accepts `Debug`, `Info`, `Warn`, `Error`, and `Panic`, case-insensitively. The `--log-debug` flag overrides this setting. |

### 3. Resource Limits

The service does not create its own cgroup by default. The following limits apply only when `--enable-cgroup` is passed. Kubernetes and systemd deployments should use their native resource controls.

```toml
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

| Field | Description |
| --- | --- |
| `CPULimitCores` | Integer. Default: `20` CPU cores. Must be greater than `0`. |
| `MemoryLimitMiB` | Integer. Default: `4096` MiB. Must be greater than `0`. |

### 4. HTTP Server

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

| Field | Description |
| --- | --- |
| `ListenAddress` | String. Default: `":12740"`. Uses `host:port` format. An empty host listens on all network interfaces. |
| `RateLimit.RequestsPerSecond` | Integer. Default: `200` requests per second. Must be greater than `0`. Limits the request rate across the entire service process. |
| `RateLimit.Burst` | Integer. Default: `200`. Must be greater than `0`. Token bucket capacity, which limits request bursts. |

HTTP timeouts and request size limits use built-in values and are not configurable.

### 5. Job Management

#### 5.1 Job Storage

```toml
# Job persistence.
[Jobs]
    # - StoreDSN
    # SQLite DSN. Relative paths are resolved from this file's directory.
    # Default: "jobs.db"
    #
    # StoreDSN = "jobs.db"
```

| Field | Description |
| --- | --- |
| `StoreDSN` | String. Default: `"jobs.db"`. Must not be empty. Specifies the SQLite data source for persistent job state. Plain relative paths are resolved against the directory specified by `--config-dir`; `file:` URIs are passed through unchanged. |

Job records must conform to the current storage format and include a valid revision number. Incompatible records cannot be read. Check database compatibility before upgrading.

#### 5.2 Concurrency Quotas

Profiling and Tracing jobs are counted separately and have independent limits. All quotas below are positive integers that limit the number of jobs that have not yet ended.

```toml
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
```

| Field | Description |
| --- | --- |
| `Jobs.Profiling.MaxConcurrentPerHost` | Maximum Profiling jobs per node. Default: `3`. |
| `Jobs.Profiling.MaxConcurrent` | Maximum Profiling jobs across all nodes. Default: `500`. |
| `Jobs.Tracing.MaxConcurrentPerHost` | Maximum Tracing jobs per node. Default: `5`. |
| `Jobs.Tracing.MaxConcurrent` | Maximum Tracing jobs across all nodes. Default: `1000`. |

#### 5.3 Lifecycle

`Jobs.Controller` controls job status polling, timeout handling, and record cleanup in the API server, independently of the node's Operation lifecycle settings. All values below are positive integers.

```toml
    # Apiserver-owned Job supervision policy. These values are independent
    # from the Node operation lifecycle settings.
    [Jobs.Controller]
        # StatusPollIntervalSeconds = 5
        # PendingTimeoutSeconds = 30
        # CompletionGracePeriodSeconds = 60
        # NodeUnavailableGracePeriodSeconds = 30
        # JobRetentionPeriodHours = 720
```

| Field | Description |
| --- | --- |
| `StatusPollIntervalSeconds` | Status polling interval. Default: `5` seconds. |
| `PendingTimeoutSeconds` | Startup timeout, measured from just before the node Operation is initiated. Default: `30` seconds. A job still in Pending when the timeout expires enters the stop process. |
| `CompletionGracePeriodSeconds` | Completion grace period. Default: `60` seconds. Once the job is confirmed running, its execution deadline is the current time plus the job duration and this grace period. The same period is used to wait for the node Operation to end after the stop process begins. |
| `NodeUnavailableGracePeriodSeconds` | Recovery grace period for an unavailable node. Default: `30` seconds. Starts when the node is first found unavailable. The job fails if the node remains unavailable beyond this period. |
| `JobRetentionPeriodHours` | Retention period for ended job records. Default: `720` hours (30 days). Measured from the time the job ends; expired records are removed by periodic cleanup. |

Service shutdown does not stop active Operations on nodes. After restarting, the service recovers unfinished jobs from the database and resumes managing them. Operations already initiated are not started again.

### 6. Node Communication

These settings control API server access to huatuo-bamai nodes. They are separate from `Auth.Users`, which controls client access to the API server.

```toml
# huatuo-bamai Agent HTTP client configuration.
[Agent]
    # - HTTPPort
    # Agent HTTP server port.
    # Default: 19704
    #
    # HTTPPort = 19704

    [Agent.Auth]
        # Required service credential. It must match HTTPServer.Auth on every
        # huatuo-bamai Node.
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
```

| Field | Description |
| --- | --- |
| `HTTPPort` | Integer. Default: `19704`. Range: `1`–`65535`. Must match the node's HTTP server port. |
| `Auth.BearerToken` | String. Required, with no built-in credential. Must not contain whitespace. Must match `HTTPServer.Auth.BearerToken` on every node. Replace the placeholder in the configuration file. |

Transport timeouts and other safeguards for node requests use built-in client values.

### 7. Authentication and Authorization

Configure at least one `[[Auth.Users]]` entry. The user configurations below are commented examples. Uncomment them and replace the credentials before use. Each user must have a distinct ID and BearerToken.

```toml
# Authentication configuration.
[Auth]
    # /readyz, /metrics, and /version are public. Flame graph query
    # endpoints and /debug/pprof/** are restricted to administrators.
    #
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
    #     BearerToken = "REPLACE_WITH_RANDOM_HEX"
    #     Permissions = [
    #         "GET /v1/tracing",
    #         "GET /v1/tracing/**",
    #         "GET /v1/profiling",
    #         "GET /v1/profiling/**",
    #     ]
```

| Field | Description |
| --- | --- |
| `ID` | String. Required and unique. Stable user identifier persisted with jobs. |
| `BearerToken` | String. Required and unique. Used only for request authentication. Rotating the token does not change job ownership. |
| `Admin` | Boolean. Default: `false`. When `true`, grants access to all APIs without restrictions from `Permissions`. |
| `Permissions` | Array of strings. Empty by default; required for non-admin users. Each entry is a path or `HTTP method path`. A path without a method allows any method. |

In paths, `*` matches one segment and `**` matches one or more subsequent segments. For example, `GET /v1/profiling/**` does not cover the collection path `GET /v1/profiling`; grant each permission separately.

`/readyz`, `/metrics`, `/version`, and `/openapi.json` require no authentication. `/debug/pprof/**` and `/v1/profiling/flamegraph/**` require administrator access. The pprof endpoints must also be enabled with `--enable-pprof`.

### 8. Profiling

#### 8.1 Data Queries

Elasticsearch/OpenSearch provides queries for profiling data written by nodes. This data is stored separately from job records in `Jobs.StoreDSN`. `Address`, `Username`, and `Password` must either all be empty or all be configured.

```toml
# Optional Elasticsearch/OpenSearch backend for querying profiling data.
[Elasticsearch]
    # Write-only options; bulk is opt-in.
    # DisableBulk = true
    # BulkNumWorkers = 4
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
    # Address = "http://127.0.0.1:9200"
    # Username = "elastic"
    # Password = "REPLACE_WITH_PASSWORD"
    # Index = "huatuo_bamai"
```

| Field | Description |
| --- | --- |
| `Address` | String. Empty by default. HTTP/HTTPS address; separate multiple addresses with commas. |
| `Username` | String. Empty by default. Required when the query backend is enabled. |
| `Password` | String. Empty by default. Required when the query backend is enabled. Replace the example placeholder. |
| `Index` | String. Default: `"huatuo_bamai"`. Must not be empty when the query backend is enabled. Must match the index to which nodes write profiling data. |

When the backend is disabled, the endpoints remain registered, but raw Profile and flame graph data cannot be queried.

`DisableBulk=true` defaults to synchronous single-document writes: Save waits
for the server response and returns errors. `false` enables asynchronous `_bulk`;
Save only confirms enqueueing and later failures are logged. Neither mode forces
refresh; successful writes may not yet be searchable. Bulk flushes at 5 MiB per
worker, every second, or on graceful Close. Crashes can lose queued data; Close
does not report per-item failures. `BulkNumWorkers` defaults to 4 per backend
instance (0 also selects 4), is ignored when bulk is disabled, and raises
concurrent server load when increased. Direct writes increase request volume
and caller latency. These options do not change query behavior or SDK retries.

#### 8.2 Result Links

```toml
# Profiling result-link configuration.
[Profiling]
    # - DashboardBaseURL
    # Optional dashboard base URL. Result URLs are omitted when empty.
    # Default: empty
    #
    # DashboardBaseURL = "http://localhost:8006/d"
```

| Field | Description |
| --- | --- |
| `DashboardBaseURL` | String. Empty by default, which omits Dashboard URLs. When set, must be an HTTP/HTTPS address with a host. The address in the configuration file is only an example. |

Result links are generated only when a job has ended with a completed or unknown outcome and the query backend confirms that its data has been published. Profiler execution and aggregation settings are maintained in the node configuration.
