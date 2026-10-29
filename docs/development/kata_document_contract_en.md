# Kata document contract

Host and Guest writers use the same persisted document types. The producer
origin is `source_type`: `host` or `kata_guest`. The common document builder
sets it from the daemon mode. Empty/missing origin remains valid for historical
documents and is not retroactively assigned to the Host. Other values are
rejected by document validation.

| Result | Document and storage | Public output |
| --- | --- | --- |
| Kernel/event observation | `pkg/tracing/store.Document`, `tracing_documents` | Event subscribers and CloudEvents watch |
| Tracing JSON, including cpuidle perf flame data and memory snapshot event data | Same tracing document/store | Existing tracing storage subscribers; CloudEvents only emits `tracer_type=event` |
| Typed profiling aggregation window | `pkg/profiling/store.Document`, `profiling_metadata` | Existing profiling result queries and raw profile responses |

The payload type determines the storage destination. An automatic trigger alone
does not select a store. In particular, `internal/tracing.Save` persists cpuidle
JSON, while `internal/profiling.DocumentWriter` persists typed profiling windows.
Do not convert a profiling window into heterogeneous `tracer_data` for transport.

Both stores index `source_type`. CloudEvents data and raw profile API results
expose it as an optional field, preserving absence in historical records. The
OpenAPI schemas and generated code carry the same field. It is descriptive
metadata, never proof of a connection's identity.

## Decode, validate and persist

Each document package exposes `DecodeDocument` for a single JSON object. Null,
malformed input and trailing JSON values are rejected; whitespace is allowed.
Tracing decoding uses `json.Number` so counters and addresses above 2^53 retain
their digits even inside nested `tracer_data`. Profiling fields retain their
existing typed integers. Callers handling decoded heterogeneous data must not
assume all numbers are float64.

Decoding parses data; it does not authenticate a sender or replace store
validation. A receiving bridge must bind trusted connection identity, sanitize
ownership and document IDs, and only then pass the prebuilt document to the
appropriate store's existing Save/SaveSync entry point. This stage does not
register a Guest listener or enable an unauthenticated receiving path.

The document itself is the JSON payload, without another metadata envelope.
The generic transport can route tracing and profiling documents separately by
handshake tool name; the integration test uses `tracing-document` and
`profiling-document` on a loopback TCP listener. Product VSOCK routing and
connection authentication belong to the bridge implementation.

## Time ownership

Store persistence assigns `uploaded_timestamp`, including when a prebuilt
Guest document is persisted again by the receiving Host. Producer-supplied
`started_timestamp`, userspace `observed_timestamp` and optional
`kernel_observed_timestamp` retain their existing roles. Receiving must not
synthesize one observation clock from another. Missing kernel time stays
missing. Timestamp requirements still follow `tracer_type`.

## Local files

Local storage accepts a single local filename derived from `tracer_name`.
Empty names, dot entries, parent traversal, absolute paths, either path
separator and NUL are rejected before creating a directory or accessing the
writer cache. Valid names retain their existing output format and rotation.
Cache reads and writer insertion share a lock for concurrent tracer names.
The configured output directory remains owned by the deployment.

## Verification

The document integration test exercises loopback TCP, bounded frames, JSON
numbers, both storage mappers, tracing local files and tracing subscriptions.
Profiling persistence uses a recording backend; it does not certify a live
Elasticsearch deployment or VSOCK/Kata transport:

```sh
go test -tags integration ./pkg/profiling/store \
  -run TestDocumentTransportIntegration -count=1 -v
```
