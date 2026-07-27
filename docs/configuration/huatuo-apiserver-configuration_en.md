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

```toml
[Log]
    # Level = "Info"

[Runtime]
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
[APIServer]
    # ListenAddress = ":12740"

[APIServer.RateLimit]
    # RequestsPerSecond = 200
    # Burst             = 200
```

`ListenAddress` uses `host:port` form. An empty host listens on all
interfaces. `RateLimit` is a process-wide token bucket; both values must be
positive. HTTP timeouts and request-size limits are fixed service safeguards
and are not user configurable.

### 4. Jobs and Agent Communication

```toml
[Jobs]
    # StoreDSN = "jobs.db"

[Jobs.Profiling]
    # MaxConcurrentPerHost = 3
    # MaxConcurrent        = 500

[Jobs.Tracing]
    # MaxConcurrentPerHost = 5
    # MaxConcurrent        = 1000

[Agent]
    # HTTPPort = 19704
    # RequestTimeoutSeconds = 10
    # StatusPollingIntervalSeconds = 5
    # MaxConsecutiveStatusPollingErrors = 3
```

`StoreDSN` is the SQLite data source for durable job state. Relative paths are
resolved from the configuration directory.

Profiling and tracing use the same quota model but retain independent values.
Their resource cost and expected concurrency differ, so a shared limit would
allow one workload to starve the other.

Agent request retries use internal client defaults. Public configuration only
exposes the Agent port, request timeout, polling interval, and failure
threshold.

During shutdown, the API server leaves active Agent tasks running and logs
their identifiers and target information. A replacement API server recovers
their persisted `pending` or `running` state and resumes monitoring.

### 5. Elasticsearch/OpenSearch

```toml
[Elasticsearch]
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
[[Auth.Users]]
    ID = "administrator"
    BearerToken = "REPLACE_WITH_RANDOM_HEX"
    Admin = true

[[Auth.Users]]
    ID = "huatuo-front"
    BearerToken = "REPLACE_WITH_ANOTHER_RANDOM_HEX"
    Permissions = [
        "GET /v1/traces",
        "GET /v1/traces/**",
        "GET /v1/profiles",
        "GET /v1/profiles/**",
    ]
```

- `ID` is the required stable principal identifier stored with jobs.
- `BearerToken` is a required secret used only to authenticate requests.
- `Admin` grants access to all routes and ignores `Permissions`.
- `Permissions` is required for non-admin users. Entries may be path-only or
  prefixed with an HTTP method. `*` matches one path segment and `**` matches a
  suffix.

IDs and bearer tokens must each be unique. Rotating a bearer token does not
change job ownership because tokens are never used as principal IDs.

`/healthz`, `/readyz`, `/metrics`, and `/version` are public.
`/debug/pprof/**` and `/v1/profiles/flamegraph/**` require an administrator.

### 7. Profiling

```toml
[Profiling]
    # AggregationIntervalSeconds = 10
    # MaxConcurrentProfilerProcesses = 10
    # DashboardBaseURL = "https://grafana.example.com/d"
```

- `AggregationIntervalSeconds` must be greater than zero and less than 1200.
- `MaxConcurrentProfilerProcesses` limits third-party profiler subprocesses.
  Zero disables this process limit; negative values are invalid.
- `DashboardBaseURL` is optional and must use HTTP or HTTPS when configured.
  Completed jobs omit a result URL when it is empty.

The Agent task timeout is derived from the requested profiling duration plus
one aggregation interval. It is not separately configurable.
