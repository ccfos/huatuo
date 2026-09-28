---
title: Challenges of TCP Retransmission and dropwatch Correlation
type: docs
author: HUATUO Team
date: 2026-08-21
weight: 7
---

This document describes the evidence boundaries and the implementation of
local correlation. The two event streams share no common event ID, so
correlation can only make conservative inferences from the namespace, the
4-tuple, TCP sequence/ACK evidence, and monotonic time.

## 1. Result Semantics

| Result | Meaning |
| --- | --- |
| `software` | A host software drop was found that satisfies all strict conditions and has not been consumed yet. |
| `hardware` | A devlink DROP trap record was found that satisfies the same strict conditions. |
| `unknown` | No strict match was found, or the matched record's source enumeration is unknown; `correlation_reason` explicitly distinguishes matched from unmatched terminal outcomes. |

A no-match does not prove that the problem is in the network or in hardware.
The drop may have happened before capture started, in another network
namespace, or the record may have been delivered without the TCP matching
fields.

Shutdown processes the waiting entries with a single time snapshot: expired
entries receive `warmup` or `wait_timeout`, and entries that have not expired
receive `interrupted`. All of these unmatched results report
`drop_location=unknown` plus independent diagnostics.

## 2. Three Operating Scenarios

1. `tcpshark --bpf-path <tcp_retransmit.o>` only captures and directly outputs
   retransmissions.
2. `tcpshark --with-dropwatch --bpf-path-dir <dir>` loads `tcp_retransmit.o`
   and `net_dropwatch.o` inside one process, which owns both perf inputs, the
   timer, the output, and shutdown.
3. The huatuo-bamai standalone dropwatch still runs independently through
   `cmd/dropwatch`, outputs only raw `DropWatchTracing`, and shares no state
   with the embedded source.

All three retransmit hooks use the synthetic L3 filter in both modes.
Correlation mode uses only `EventTracing.TCPRetransmit.Filter`; an empty value
is normalized to `tcp`, and the same expression is passed to both BPF objects.
Expressions that depend on Ethernet addresses and cannot run equivalently on
synthetic L3 input are rejected before startup.
`EventTracing.Dropwatch.Filter` controls only standalone dropwatch.

Both commands use `HardwareAuto` to auto-detect devlink trap support. When the
tracepoint is unavailable, they log a warning and continue software capture;
when it is available, they accept only driver-reported DROP traps.
`--device` / `--device-excluded` share their names with standalone dropwatch
and are mutually exclusive; they filter only the embedded source's software and
hardware records and require `--with-dropwatch`. The retransmit input still
uses the shared L3 filter.

Drop source and reason are resolved uniformly by
`internal/dropwatch.ResolveMetadata`. Software reasons are looked up in a BTF
table loaded once per session; unknown values are decimal numbers, and kernels
without support yield `NOT_SUPPORTED`. Hardware reasons and groups come from
the trap name and the trap group respectively and do not consult the software
reason table. The reader keeps independent source and reason strings and does
not borrow the reusable ABI buffer. After a strict match, the output carries
`drop_source`, `drop_reason`, and `drop_reason_group`; a no-match omits these
fields and outputs the single correlation terminal reason plus independent
diagnostics. `drop_location` echoes `drop_source` on a match and is `unknown`
otherwise; it expresses the correlation classification and differs from the
standalone dropwatch kernel address. An unknown source is never inferred from a
reason or a stack. Strict matching cannot be established when a hardware record
lacks the namespace or TCP matching fields.

## 3. The Two Time Constraints

dropwatch and retransmit use different perf readers, and events can also come
from different CPUs, so the userspace arrival order does not equal the kernel
occurrence order:

```text
CPU 2: drop       monotonic_ns=180, held back in the dropwatch ring
CPU 0: retransmit monotonic_ns=200, reaches userspace first
userspace: retransmit(200) -> drop(180)
```

The implementation uses two windows on different dimensions:

```go
retransmitRetentionDuration = 100 * time.Millisecond
maxDropToRetransmitAge     = time.Second
```

- 100 ms is the userspace out-of-order delivery budget. When a retransmission
  arrives first, it enters the wait queue; if it is still unmatched when the
  wait expires, it is reported as `unknown`. Records whose deadline has already
  passed are finalized before any new event is processed; the timer is only
  responsible for waking the loop and does not define the deadline semantics.
- 1s is the causal candidate age, judged with the BPF `kernel_observed_ns`. A
  candidate must satisfy:

```text
drop.kernel_observed_ns <= retransmit.kernel_observed_ns
retransmit.kernel_observed_ns - drop.kernel_observed_ns <= 1s
```

`observed_timestamp` is userspace wall time, affected by scheduling and system
time adjustments, and does not participate in matching. The fixed 1s is the
currently supported contract and is not a theoretical bound for all Linux RTOs;
results outside the window stay `unknown`.

## 4. Bounded State

Waiting retransmissions and not-yet-matched drops share the in-package generic
store `store[T]`, each holding an independent instance. The container is
responsible only for capacity, TTL, the flow index, and deletion; strict
matching and result finalization belong to the correlator.

```go
type storeEntry[T any] struct {
    value    T
    flow     *flowKey
    deadline time.Time
    sequence uint64
    node     *list.Element
}

type store[T any] struct {
    byDeadline   list.List
    byFlow       map[flowKey][]*storeEntry[T]
    capacity     int
    ttl          time.Duration
    nextSequence uint64
}
```

| Instance | Stored value | Capacity | TTL |
| --- | --- | --- | --- |
| `retransmitStore` | `waitingRetransmit`, inlined in the entry | 1024 | 100ms |
| `dropStore` | `*dropEvent` | 4096 | 1s + 100ms |

`waitingRetransmit` contains only the raw event, the matching fields, and the
cross-namespace candidate flag; it no longer holds a list node, a deadline, or
an independent ID. Drops likewise need no extra cache wrapper type. The list's
`Element.Value` and the flow bucket reference the same `*storeEntry[T]`, and
the entry's `node` points back to its list element. The entry references the
flow from the business data, which is never modified, avoiding a duplicate
address. Deletion unreferences both indexes at once and clears the tail of the
flow slice; an empty bucket is removed after its last entry is deleted.

The flow index is keyed by the canonical order of the two `AddrPort` values, so
both directions share one bucket; the original direction is kept in the
business data for strict matching. The namespace is not part of the index key,
so similar cross-namespace candidates can still set the diagnostic flag. Match
scans and slice deletions are O(k), where k is the number of candidates for
that flow; locating the earliest deadline and unlinking from the list are O(1).

Each instance has a fixed TTL, and the correlation loop passes a monotonically
non-decreasing processing time, so list insertion order is also deadline order.
The list serves expiry cleanup and capacity eviction and exposes no FIFO
consumption interface to business code. When the wait capacity is full, the
record with the earliest deadline is finalized as `queue_full`; when the drop
capacity is full, the candidate with the earliest deadline is evicted directly.
Causal decisions still use `kernel_observed_ns`.

The two readers deliver events through unbuffered channels, and the container
is accessed only by a single correlation loop, with no locks and no separate
cleanup goroutine. One timer always takes the earliest deadline of the two
instances, so cleanup happens on schedule even when only the drop cache holds
data and no retransmission is waiting. On every input and timer wakeup, expired
entries are cleaned first, then matching and capacity checks run;
`now >= deadline` means expired. The 100 ms is the matching deadline; blocking
in synchronous output can still delay actual cleanup and result delivery.

## 5. Strict Matching

A forward segment candidate requires:

- the same network namespace, address family, and direction 4-tuple;
- a drop no later than the retransmission, with a time difference of at most 1s;
- an overlap between the TCP SYN/data/FIN sequence range and the retransmitted
  range;
- RST and unsupported retransmission types produce no forward match.

A reverse ACK candidate uses the reverse 4-tuple and requires the ACK to cover
the retransmitted sequence end. SYN and SYN-ACK use their own stricter ACK/SYN
conditions.

Among multiple drop candidates, the largest `drop.kernel_observed_ns` is
selected first; on a time tie, the larger insertion sequence number wins. When
a drop arrives late, it selects the strictly matched retransmission with the
smallest insertion sequence number while scanning the remaining candidates to
record each retransmission's namespace-match status. After a strict match, the
entry is removed from both the deadline and flow indexes immediately and can be
consumed only once. Forward segment and reverse ACK share the flow index; the
namespace is checked first, then the packet and time conditions.

When a drop with the same namespace appears on the same flow, `netNamespace`
is set to true; the flag is retained even when time or sequence fails the
strict conditions, and later non-matching candidates do not clear it. A strict
match always yields true. With no candidates, only other namespaces, or an
incomparable namespace, the value is false, meaning no match was observed; it
does not establish that the namespaces differ. The output preserves this
status through `NetNamespace`; the JSON and text field is
`matched_net_namespace`, omitted when false.

## 6. Mutually Exclusive Correlation Outcomes

`types.CorrelationReason` is a string type; each correlation outputs exactly
one `correlation_reason`:

| Go constant | JSON value | Meaning |
|---------|---------|------|
| `CorrelationMatched` | `matched` | Strict match succeeded, including drops whose source is unknown. |
| `CorrelationUnsupported` | `unsupported` | The current rules cannot handle this retransmission; it does not enter the wait queue. |
| `CorrelationWarmup` | `warmup` | The wait expired without a match, and the retransmission predates source readiness. |
| `CorrelationWaitTimeout` | `wait_timeout` | The wait expired without a match, and the retransmission occurred at or after source readiness. |
| `CorrelationQueueFull` | `queue_full` | The retransmission was evicted before expiry because the wait queue was full. |
| `CorrelationInterrupted` | `interrupted` | The correlation loop exited, and the unexpired wait was interrupted early. |

Each terminal branch assigns `correlationResult.reason` directly. The output
side uses this enum to classify results and validates that `matched` must carry
a drop while the other five outcomes must not; empty values, unknown values, or
contradictory combinations return an error. Correlation-disabled output omits
the field; once correlation is enabled, every finalized event contains one
valid terminal outcome. The scalar field replaces the previous reason array;
tools and receivers migrate in lockstep.

On wait expiry, the terminal outcome is chosen by the retransmission's kernel
time: earlier than source readiness yields `warmup`, at or after readiness
yields `wait_timeout`, including waits under 1s after readiness. The 1s
drop-to-retransmit limit applies only to candidate matching and plays no part
in the warmup decision. Userspace processing delay does not change the
classification. This decision runs only in the expiry branch; strict matches,
unsupported events, queue evictions, and early interruptions keep their own
reasons. Warmup neither blocks matching nor ends the wait early.

`matched_net_namespace` remains an independent diagnostic flag: a drop with the
same namespace was observed on the same flow, independent of the packet and
time conditions; it cannot substitute for `correlation_reason=matched`.

The flag can coexist with rate limiting and loss counters without changing the
single terminal outcome. Drops that fail normalization and drop-cache capacity
evictions do not directly end a retransmission wait and do not produce a
retransmission-level reason.

## 7. Perf Status

The status consists of three independent channels; each reports on its own and
none is summed with another:

- `perf_lost`: the cumulative per-CPU `bpf_perf_out_dropwatch` map (the common
  perf output accounting facility `bpf_perf_output.h`); BPF increments it when
  `bpf_perf_event_output` returns a negative value (for example, when the
  current CPU has no attached reader).
- `lost_samples`: the userspace cumulative count of ring buffer overflows
  reported by `PERF_RECORD_LOST`, visible only while the reader runs, and
  queryable through ReadStatus once the lost records are processed.
- `rate_limited`: the `total_missed` value of the rate-limit status map
  `bpf_rlimit_dropwatch`.

On success, `ReadStatus` sets `HasMapCounters=true`, serialized as
`map_counters_available=true`. On error the flag is false and `PerfLost` and
`RateLimited` are zero, meaning unavailable rather than an absence of loss or
rate limiting; `LostSamples` remains valid and the output keeps that snapshot.
These values are per-instance cumulative counters, sampled separately by the
map and the reader, and cannot explain why any individual retransmission failed
to match.

Userspace sums perf_lost across all CPUs and returns the current snapshot each
time; it does not check for counter regressions or uint64 addition overflow.
This status describes evidence integrity only and never promotes a no-match
into a deterministic network classification. The old active-epoch, dual-slot,
inflight, frontier, and drain watermark mechanisms have been removed.

The correlation side computes the TCP sequence span from the IPv4 total length
or the IPv6 payload length. Under GSO/offload the IP header length may not
cover the whole skb; matching is not widened on that basis today.

The dropwatch hot path is:

```text
software: hardware marker lookup/delete -> device/pcap filter
          -> status map lookup -> bpf_ktime_get_ns() -> rate limit -> perf output
hardware: device/pcap filter -> status map lookup -> bpf_ktime_get_ns()
          -> rate limit -> perf output
```

Consequently, events rejected by the filter pay no status lookup, event-time
helper, or counter update cost; a software drop first consumes any existing
hardware dedup marker so rejected kfree paths leave no marker behind.

## 8. Shutdown

`tcpshark` uses one `errgroup.WithContext` to manage the retransmit rate-limit
reader, the retransmit reader, the embedded dropwatch reader, and the
correlation loop. All command workers share the same `groupCtx`; when any
worker returns an error the rest are canceled, and `Wait` returns the first
worker error. Resource close errors are merged by each owner with
`errors.Join`; shutdown no longer maintains a private error-aggregating group.

`internal/dropwatch.Tracer` owns the dropwatch object, the readers, and the
rate-limit alert worker. The instance context derives from `groupCtx`; a
rate-limit alert failure cancels instance reads, and the concrete error is
returned by `Close`. `ReadInto` checks close and cancellation causes before
reading; once the underlying read is entered, the read result is returned
as-is and is no longer overwritten by a subsequent close or cancellation.
`ReadStatus` can still read the final counters after the context is canceled.
`Close` first cancels the instance, detaches, and closes the event and alert
readers, then waits for the alert worker to exit, and finally releases the
object; closing the alert reader wakes an idle read, so no polling deadline
needs to elapse. The status type is `types.DropwatchStatus`, the output field
is `drop_perf_status`, and it includes the map-counter availability flag.

On normal completion or worker failure:

1. Cancel the shared `groupCtx`;
2. the two `ReadInto` readers and the correlation loop exit;
3. records still in the dropwatch perf ring that were never handed to the
   correlator are no longer read;
4. `drainRetransmits(now)` is called to finalize expired entries with the same
   time snapshot, then clear the unexpired ones, writing output in deadline
   order;
5. after all workers exit, the dropwatch and retransmit Tracers are closed,
   collecting internal alert-worker and resource-release errors;
6. finally the socket output is finished; the caller-provided io.Writer is not
   closed by the session.

At shutdown, expired entries report `warmup` or `wait_timeout` and unexpired
entries report `interrupted`, all retaining `drop_location=unknown`, the
diagnostic flags, and the perf status. Already matched or already evicted
events are never finalized twice. Tail drops can still be lost, so a
retransmission that could have matched may also be ended early.

## 9. File Responsibilities

```text
cmd/tcpshark/
├── main.go                         # signals, version, CLI entry point
├── cli.go                          # argument validation, path and filter normalization
├── run.go                          # global BPF lifecycle, timeout, feature dispatch
└── retransmit/                     # retransmit feature package
    ├── config.go                   # Config, RunConfig
    ├── run.go                      # Run: resource assembly, execution, and cleanup
    ├── tracer.go                   # Open, ReadInto, Close, and the rate-limit worker
    ├── bpf_load.go                 # loadBPF, attachBPF, and probe selection
    ├── event_reader.go             # dual-stream reading, conversion, and channel delivery
    ├── correlation_loop.go         # select, timer, and exit finalization
    ├── correlation.go              # correlation state machine, expiry finalization
    ├── correlation_match.go        # candidate selection, consumption, and namespace-match status
    ├── match.go                    # matching types: namespace/flow/sequence/ktime
    ├── store.go                    # entry storage, flow index, capacity and expiry management
    ├── event.go                    # conversion of the two ABI record types
    ├── classify.go                 # retransmission classification
    ├── correlation_output.go       # status completion, symbolization, correlation output fields
    └── output.go                   # text, JSON, and socket output
```

The top level runs the retransmit feature through
`retransmit.Run(ctx, *RunConfig)` and does not touch ABI records, channels, the
correlation timer, or status counters. The CLI validates scalars and argument
combinations first, then compile-checks the filter. When a non-empty
`--output-storage` is set, the `--output` value is not validated; if the latter
is given explicitly, a note on stderr says it is ignored. Without storage, only
json or text is accepted. `RunConfig.Tracing` is the capture configuration;
when Dropwatch is nil, it reads and outputs directly; when non-nil, it uses two
read workers and a single correlation loop. The correlation capability is a
composition of the retransmit feature, not a general capture framework.

Run builds the output before attaching probes and, before returning, completes
waiting for all session workers, final finalization, Tracer closing, and output
completion. Configuration failures, cancellation, and run failures all release
the resources already acquired. Draining kernel buffers at close is not
guaranteed. Plain mode creates no correlation channels or timers.

`retransmit.Tracer` still owns only the retransmit BPF object, the readers, and
the rate-limit alert worker; event conversion, classification, correlation, and
output belong to the feature session in the same package and stay out of the
Tracer. An `Open` failure rolls back every acquired resource; `Close` is still
required after the session context is canceled. A rate-limit worker failure
cancels the capture, and the original error is also preserved by `Close`. Once
reading has started, `ReadInto` keeps the underlying result as-is and no longer
checks close or cancellation state. `Close` may interrupt a read, but callers
must serialize `Close` calls. Process-level `bpf.Init/Shutdown` and the run
timeout are managed centrally at the command layer.

The correlator uniformly cleans up expired candidates while handling each input
and timer wakeup; the caches do not repeat expiry checks. The correlator
returns match evidence, a single terminal outcome, and diagnostic flags, and
does not modify output fields; correlation_output.go builds the final output in
one place. Each non-empty output batch reads the dropwatch status once at its
entry, and empty batches read nothing; symbols are resolved only for matched
drops. Status is read even when the whole batch is matches; on a read failure
the batch is still written out and the status error is returned afterwards; if
the write fails, batch output stops and the write error is merged with the
status error. Only no-match events carry the status snapshot and diagnostic
flags; a failed read still keeps valid reader counters, and matched events do
not carry these fields. Internal retransmission events keep only the ABI record
and the observedAt from a successful read; the wait queue does not store full
presentation objects. Matching constructs netip addresses directly from ABI
addresses and uses the ABI event enums, with no string conversions.
IPv4-mapped IPv6 is still normalized to IPv4. Single-event reads handle
lost-record retries and read times in one place; plain mode reuses the event
and outputs synchronously, while correlation mode reads into a dedicated event
before sending it, avoiding overwriting data that is still waiting. run.go
creates the channels and starts the read workers in one place, and the sender
closes the channels; read functions start no goroutines and use neither
consumer callbacks nor borrowed-event contracts. TCPRetransmitTracing is
constructed only at final output; ObservedTimestamp uses the original read
time, not the correlation end time. The ABI, SYNACK flag completion, and
matching rules stay unchanged.

`cmd/tcpshark/retransmit` does not enforce a Go `internal` import restriction;
the convention is that only tcpshark uses it, and it does not depend back on
the command layer. The capture implementation shared by the two commands lives
in:

```text
internal/dropwatch/
├── config.go    # Config, HardwareMode, and configuration validation
├── tracer.go    # Tracer API, newTracer resource takeover, rollback, and worker lifecycle
├── load.go      # loadBPF, attachBPF: loading, map/reader preparation, and probe attachment
├── netdev.go    # device filtering and map configuration
├── status.go    # ReadStatus: output failures, ring loss, and rate-limit counters
├── reason.go    # LoadReasonNames: kernel BTF reason table and numeric fallback
├── metadata.go  # ResolveMetadata: source, software reason, hardware trap/group
└── decode.go    # DecodePacket: ABI header adaptation and packet.Parse
```

`Open(ctx, *Config)` returns a concrete `*Tracer`. Each instance allows only
one `ReadInto` caller; status reads and closing may run concurrently, but
`Close` on the same instance must be serialized by the resource owner. The
first close returns cleanup errors; later calls return nil, never retry
cleanup, and never return the first error again. The caller owns global BPF
initialization and shutdown, and must close the Tracer after the final
finalization. `DecodePacket` results do not borrow the original record memory.
Symbolization and the `DropWatchTracing` presentation conversion stay in the
standalone command; TCP correlation keeps KernelObservedNS, the namespace,
stack PCs, and the resolved drop source and reason directly from the ABI. No
general BPF consumption-loop interface is introduced.
