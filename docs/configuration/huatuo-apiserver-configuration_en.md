---
title: huatuo-apiserver Configuration
type: docs
description:
author: HUATUO Team
date: 2026-07-20
weight: 5
---

### 1. Overview

`huatuo-apiserver` is the HUATUO API service. It provides task management,
tracing, and profiling APIs. Its configuration file defines the log level,
process resource limits, service listen address, task concurrency limits,
Elasticsearch/OpenSearch backend, user authentication and authorization, and
profiling parameters.

This document is based on `huatuo-apiserver.conf` in the repository root and
describes the purpose, default value, and considerations for each option. The
configuration file uses TOML. Options beginning with `#` are commented out,
so the application uses their built-in defaults. Remove `#` and set an
appropriate value to override a default. Restart `huatuo-apiserver` after
changing the configuration.

**Note**: The Elasticsearch/OpenSearch password and user IDs in the examples
are placeholders. Replace them in production and never commit real
credentials to version control.

### 2. Logging

```toml
# LogLevel = "Info"
```

- **LogLevel**: Log level.

  Supported values are `Debug`, `Info`, `Warn`, `Error`, and `Panic`. The
  default is `Info`.

  **Note**: Use `Info` or `Warn` in most production environments. `Debug`
  provides more diagnostic details and is useful for temporary
  troubleshooting, but it may substantially increase log volume.

### 3. Runtime Resource Limits

```toml
[RuntimeCgroup]
    # LimitCPU = 20
    # LimitMem = 4096
```

- **LimitCPU**: CPU resource limit.

  The default is `20`, measured in CPU cores.

  **Note**: This option limits CPU usage by the `huatuo-apiserver` process.
  Adjust it according to node capacity and API request load.

- **LimitMem**: Memory resource limit.

  The default is `4096`, measured in MB.

  **Note**: The application converts this value to bytes after loading the
  configuration and applies it as a runtime resource limit. A value that is
  too low may cause insufficient memory during highly concurrent queries or
  task scheduling.

### 4. API Server

```toml
[APIServer]
    # TCPAddr = ":12740"
    # ReadHeaderTimeoutSeconds = 10
    # ReadTimeoutSeconds       = 30
    # WriteTimeoutSeconds      = 60
    # IdleTimeoutSeconds       = 120
    # ShutdownTimeoutSeconds   = 60
    # MaxHeaderBytes           = 1048576
    # MaxBodyBytes             = 4194304
    # RateLimit                = 200
    # RateBurst                = 200
```

- **TCPAddr**: API server listen address.

  The default is `:12740`, in `host:port` format. An empty host means that the
  server listens on all network interfaces.

  **Note**: To allow local access only, use `127.0.0.1:12740`. When exposing
  the service externally, configure a firewall, reverse proxy, and access
  controls as appropriate.

- **ReadHeaderTimeoutSeconds**, **ReadTimeoutSeconds**,
  **WriteTimeoutSeconds**, and **IdleTimeoutSeconds** bound slow or stalled
  connections. Defaults are `10`, `30`, `60`, and `120` seconds.

- **ShutdownTimeoutSeconds** sets the total graceful-shutdown deadline shared
  by all components. The default is `60` seconds. Each component receives a
  portion of the remaining deadline.

- **MaxHeaderBytes** and **MaxBodyBytes** cap request headers and bodies.
  Defaults are 1 MiB and 4 MiB.

- **RateLimit** and **RateBurst** configure the process-wide HTTP token bucket.
  Both default to `200`.

### 5. Task Scheduling

```toml
[TaskConfig]
    # MaxProfilingTasksPerHost = 3
    # MaxTracingTasksPerHost   = 5
    # MaxTotalProfilingTasks   = 500
    # MaxTotalTracingTasks     = 1000
    # JobStoreDSN              = "jobs.db"
    # ShutdownConcurrency      = 16
```

- **MaxProfilingTasksPerHost**: Maximum number of concurrent profiling tasks
  on one host.

  The default is `3`.

  **Note**: This option limits per-host profiling concurrency so that multiple
  simultaneous CPU or memory profiling tasks do not disrupt application
  processes.

- **MaxTracingTasksPerHost**: Maximum number of concurrent tracing tasks on
  one host.

  The default is `5`.

  **Note**: Set this value according to host performance and the overhead of
  the enabled tracers.

- **MaxTotalProfilingTasks**: Maximum number of profiling tasks across the
  cluster.

  The default is `500`.

  **Note**: This option controls cluster-wide profiling concurrency and
  prevents bursts of tasks from overloading the API service or storage
  backend.

- **MaxTotalTracingTasks**: Maximum number of tracing tasks across the
  cluster.

  The default is `1000`.

  **Note**: This limit applies together with the per-host limit. Adjust it in
  production according to cluster size and backend capacity.

- **JobStoreDSN**: SQLite data source for durable job state.

  The default is `jobs.db`. Relative paths are resolved from the directory
  containing the configuration file. Use a persistent writable path in
  production.

- **ShutdownConcurrency**: Maximum concurrent Agent stop requests during
  graceful shutdown.

  The default is `16`.

#### Agent Communication and Status Polling

```toml
[Agent]
    # Port                      = 19704
    # RequestTimeoutSeconds     = 10
    # StatusRetryAttempts       = 3
    # StatusRetryBackoffMillis  = 100
    # StatusPollIntervalSeconds = 5
    # MaxConsecutivePollErrors  = 3
```

- **Port** and **RequestTimeoutSeconds** set the Agent HTTP port and the
  timeout for one request.
- **StatusRetryAttempts** and **StatusRetryBackoffMillis** configure status
  query retries.
- **StatusPollIntervalSeconds** configures the task status polling interval.
- **MaxConsecutivePollErrors** sets how many consecutive polling errors are
  allowed before a job is marked failed.

### 6. Storage

```toml
[ElasticSearch]
    # Address = "http://127.0.0.1:9200"
    Username = "elastic"
    Password = "huatuo-bamai"
    # Index = "huatuo_bamai"
```

`huatuo-apiserver` uses Elasticsearch/OpenSearch to query tracing and event
data produced by `huatuo-bamai`. `Username` and `Password` are optional, but
they must be configured together.

- **Address**: Elasticsearch/OpenSearch service address.

  The default is `http://127.0.0.1:9200`. To override it, specify a complete
  URL including the scheme and port, such as `https://127.0.0.1:9200`.

  **Note**: Use HTTPS in production and ensure that the API service can reach
  this address.

- **Username**: Authentication username.

  There is no default.

  **Note**: Use a least-privilege service account with access only to the
  required index.

- **Password**: Authentication password.

  There is no default.

  **Note**: Do not use the example password. Inject the configuration securely
  through the deployment system and restrict read access to the file.

- **Index**: Data index name.

  The default is `huatuo_bamai`.

  **Note**: This value must match the storage index in `huatuo-bamai.conf`.
  Otherwise, the API service cannot query data written by the collector.

### 7. Authentication and Authorization

`/healthz`, `/readyz`, `/metrics`, and `/version` are public. When pprof is enabled,
`/debug/pprof/**` shares the API listener and is restricted to administrators.
All other routes require `Authorization: Bearer <Auth.users.ID>`. Every
`/v1/profiles/flamegraph/**` query route is restricted to administrators.

Declare each user in a separate `[[Auth.users]]` array table. Multiple users
can be configured, for example:

```toml
# Administrator: has access to all APIs; Permissions is ignored.
[[Auth.users]]
    ID      = "REPLACE_WITH_RANDOM_HEX"
    Name    = "Administrator"
    IsAdmin = true

# Regular user: read-only access to tracing and profiling APIs.
[[Auth.users]]
    ID          = "REPLACE_WITH_RANDOM_HEX"
    Name        = "huatuo-front"
    IsAdmin     = false
    Permissions = [
        "GET /v1/traces",
        "GET /v1/traces/**",
        "GET /v1/profiles",
        "GET /v1/profiles/**",
    ]
```

- **ID**: Unique user identifier and bearer credential.

  There is no default.

  **Note**: The service uses the ID supplied by a client request to look up the
  user. Treat this value as a sensitive credential and use a sufficiently
  random string, for example one generated with `openssl rand -hex 16`. Do not
  share an ID between users.

- **Name**: User display name.

  There is no default. This value identifies the user but is not used for
  authorization decisions.

- **IsAdmin**: Administrator flag.

  The default is `false`. When set to `true`, the user can access every API and
  `Permissions` is ignored.

  **Note**: Keep the number of administrator accounts to a minimum. Define
  explicit permission lists for regular clients.

- **Permissions**: List of allowed URL patterns, optionally prefixed by an
  HTTP method.

  There is no default. This option applies only when `IsAdmin = false`. It
  supports exact paths and wildcard paths. `**` matches any content after its
  position, as in `/v1/traces/**`. Use `GET /v1/traces/**` for read-only
  access. A path-only pattern permits every method for compatibility.

  **Note**: A collection path and its subpaths usually require separate
  entries, such as both `/v1/traces` and `/v1/traces/**`. Follow the principle
  of least privilege and expose only the APIs required by the client.

### 8. Profiling

```toml
[Profiling]
    # AggregationInterval  = 10
    # ExecutionTimeout     = 20
    # MaxProfilerProcs     = 10
    # FlameGraphBaseURL     = "http://localhost:8006/d"
```

- **AggregationInterval**: Interval for aggregating and reporting profiling
  data.

  The default is `10`, measured in seconds. Valid values range from `1` to
  `1199`.

  **Note**: This value maps to the profiler's `--aggr-interval` option and also
  schedules continuous profiling work. A shorter interval updates results more
  frequently but increases aggregation, reporting, and storage overhead.

- **ExecutionTimeout**: Execution timeout for one profiler subprocess.

  The default is `20`, measured in seconds, and must be at least twice
  `AggregationInterval`.

  **Note**: This value limits the profiler subprocess, not the entire profiling
  job. The API server refuses to start when this constraint is not satisfied.

- **MaxProfilerProcs**: Maximum number of concurrent third-party profiler
  processes.

  The default is `10`.

  **Note**: This option limits external profiler concurrency to avoid
  exhausting CPU, memory, or process resources. It cannot be negative; set it
  to `0` to disable the limit.

- **FlameGraphBaseURL**: Flame graph dashboard base URL.

  The default is `http://localhost:8006/d`.

  **Note**: The API service appends a dashboard identifier to this address to
  create the flame graph URL for each trace. When using a separate
  visualization service, set this to an address accessible to clients. The URL
  must use HTTP or HTTPS and include a host.

### 9. Configuration Example

The following example enables backend storage and defines an administrator
account and a regular read-only account:

```toml
LogLevel = "Info"

[RuntimeCgroup]
    LimitCPU = 20
    LimitMem = 4096

[APIServer]
    TCPAddr = ":12740"

[TaskConfig]
    MaxProfilingTasksPerHost = 3
    MaxTracingTasksPerHost = 5
    MaxTotalProfilingTasks = 500
    MaxTotalTracingTasks = 1000

[Agent]
    Port = 19704
    RequestTimeoutSeconds = 10

[ElasticSearch]
    Address = "https://elasticsearch.example.com:9200"
    Username = "huatuo-apiserver"
    Password = "REPLACE_WITH_STRONG_PASSWORD"
    Index = "huatuo_bamai"

[[Auth.users]]
    ID = "REPLACE_WITH_RANDOM_HEX"
    Name = "Administrator"
    IsAdmin = true

[[Auth.users]]
    ID = "REPLACE_WITH_ANOTHER_RANDOM_HEX"
    Name = "huatuo-front"
    IsAdmin = false
    Permissions = [
        "/v1/traces",
        "/v1/traces/**",
        "/v1/profiles",
        "/v1/profiles/**",
    ]

[Profiling]
    AggregationInterval = 10
    ExecutionTimeout = 20
    MaxProfilerProcs = 10
    FlameGraphBaseURL = "https://grafana.example.com/d"
```

Before deployment, replace the example password, user IDs, backend address,
and flame graph address. Adjust the resource and task concurrency limits for
the capacity of the hosts and cluster.
