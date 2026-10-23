---
title: Retransmission and Drop Correlation Design
type: docs
author: HUATUO Team
date: 2026-08-21
weight: 8
---

This guide explains the design, evidence limits, state management, and resource lifecycle of local TCP retransmission/drop correlation for developers and maintainers.

The two streams have no shared event ID, so correlation makes conservative inferences from namespaces, four-tuples, TCP sequence/ACK evidence, and monotonic time.

## 1. Result Semantics

| Result | Meaning |
| --- | --- |
| `software` | An unconsumed host software drop satisfies every strict condition. |
| `hardware` | A devlink DROP trap record satisfies the same strict conditions. |
| `unknown` | No strict match, or the matched record has an unknown source enum; `correlation_reason` distinguishes matched and unmatched terminal states. |

No match does not prove a network or hardware problem. The drop may predate collection, belong to another network namespace, or arrive without the TCP fields required for matching.

Shutdown finalizes waits according to whether they have expired; see [Shutdown](#8-shutdown).

## 2. Operating Modes

See [Retransmission Tracing](/docs/best-practice/tcpshark_en.md) for setup, full parameters, and command examples. The modes below define responsibilities and state ownership.

1. Without correlation, tcpshark loads `tcp_retransmit.o` and collects and directly outputs retransmissions only.
2. With correlation, tcpshark loads `tcp_retransmit.o` and `net_dropwatch.o` in one process, which owns both perf inputs, the timer, output, and shutdown.
3. huatuo-bamai's standalone dropwatch runs independently through `cmd/dropwatch`, emits only raw `DropWatchTracing`, and shares no state with the embedded source.

All three retransmission hooks use synthetic L3 filters in both modes. Correlation uses only `EventTracing.TCPRetransmit.Filter`: an empty value becomes `tcp`, and the same expression goes to both BPF objects. Expressions depending on Ethernet addresses that cannot be evaluated equivalently on synthetic L3 input are rejected before startup. `EventTracing.Dropwatch.Filter` controls only standalone dropwatch.

Both commands use `HardwareAuto` to detect devlink trap support. If the tracepoint is unavailable, they warn and continue software collection. If available, they accept only driver-reported DROP traps. `--device` and `--device-excluded` have the same names as in standalone dropwatch, are mutually exclusive, and filter only the embedded source's software and hardware records. They require `--with-dropwatch`. Retransmissions use the shared L3 filter.

`internal/dropwatch.ResolveMetadata` resolves drop sources and reasons. Software reasons come from a BTF table loaded once per session; unknown values are decimal numbers, and unsupported older kernels use `NOT_SUPPORTED`. Hardware reasons and groups come from trap names and groups, not the software table. Readers own source and reason strings independently of reusable ABI buffers.

Matches output `drop_source`, `drop_reason`, and `drop_reason_group`. Unmatched records omit them and carry one correlation outcome plus separate diagnostics. On a match, `drop_location` uses `drop_source`; otherwise it is `unknown`. This is a correlation classification, unlike standalone dropwatch's kernel address. Unknown sources are not inferred from reasons or stacks. Hardware records missing namespace or TCP match fields cannot match strictly.

## 3. Time Windows

dropwatch and retransmission use separate perf readers, and events may come from different CPUs. User-space arrival order therefore differs from kernel occurrence order:

```text
CPU 2: drop       monotonic_ns=180, still in the dropwatch ring
CPU 0: retransmit monotonic_ns=200, reaches user space first
User space: retransmit(200) -> drop(180)
```

The implementation uses two windows for different purposes:

```go
retransmitRetentionDuration = 100 * time.Millisecond
maxDropToRetransmitAge     = time.Second
```

- 100ms is the user-space delivery reordering budget. A retransmission arriving first enters a waiting queue and outputs `unknown` if still unmatched at expiry. Expired records are finalized before each new event is processed; the timer only wakes the loop and does not define deadline semantics.
- 1s is the maximum causal candidate age, checked using BPF `kernel_observed_ns`. Candidates must satisfy:

```text
drop.kernel_observed_ns <= retransmit.kernel_observed_ns
retransmit.kernel_observed_ns - drop.kernel_observed_ns <= 1s
```

`observed_timestamp` is user-space wall time, affected by scheduling and clock adjustments, and is not used for matching. The fixed 1s window is the supported scope, not a theoretical upper bound for every Linux RTO. Results outside it remain `unknown`.

## 4. Bounded State

Waiting retransmissions and unmatched drops use separate instances of the package-local generic `store[T]`. The container handles capacity, TTL, flow indexing, and deletion; the correlator handles strict matching and finalization.

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

| Instance | Stored Value | Capacity | TTL |
| --- | --- | --- | --- |
| `retransmitStore` | `waitingRetransmit`, inline in the entry | 1024 | 100ms |
| `dropStore` | `*dropEvent` | 4096 | 1s + 100ms |

`waitingRetransmit` contains only the raw event, matching fields, and cross-namespace candidate flags. `storeEntry` manages list nodes, deadlines, and insertion sequence; drops use `*dropEvent` directly. The list's `Element.Value` and flow bucket reference the same `*storeEntry[T]`, whose `node` points back to the list element. Entries reference read-only flow data to avoid copying addresses. Deletion removes both index references and clears the flow slice tail; removing the final entry deletes the empty bucket.

Flow keys canonically order two `AddrPort` values so both directions share one bucket. Original direction remains in event data for strict matching. Namespace is excluded from the key so similar candidates across namespaces can set diagnostic flags. Matching scans and slice deletion are O(k), where k is the flow's candidate count. Finding the earliest deadline and removing a list node are O(1).

Each instance has a fixed TTL, and the loop supplies nondecreasing processing time, so insertion order also orders deadlines. The list supports expiry and capacity eviction without exposing a FIFO consumer interface. When the waiting queue is full, the earliest-deadline record is finalized as `queue_full`. A full drop cache simply evicts its earliest-deadline candidate. Causal checks use `kernel_observed_ns`.

Two readers deliver events through unbuffered channels. A single correlation loop accesses the stores, with no locks or separate cleanup goroutine. One timer always targets the earliest deadline across both stores, ensuring cleanup even when only cached drops exist. Each input or timer wakeup expires entries before matching and capacity checks; `now >= deadline` means expired. 100ms is the matching deadline, but blocking synchronous output can delay actual cleanup and delivery.

## 5. Strict Matching

Forward segment candidates require:

- The same network namespace, address family, and directional four-tuple.
- A drop no later than the retransmission, with a difference of at most 1s.
- Overlapping TCP SYN/data/FIN and retransmission sequence ranges.
- No positive match for RST or unsupported retransmission types.

Reverse ACK candidates use the reverse four-tuple and require an ACK covering the retransmission sequence end. SYN and SYN-ACK apply their own stricter ACK/SYN conditions.

Among drop candidates, choose the greatest `drop.kernel_observed_ns`, breaking ties by larger insertion sequence. When a drop arrives later, choose the strict-matching retransmission with the smallest insertion sequence while scanning remaining candidates to record namespace-match state. A strict match is immediately removed from both deadline and flow indexes and consumed once. Forward segments and reverse ACKs share the flow index; namespace checks precede packet and time checks.

A same-namespace drop in the same flow sets `netNamespace` to true even if time or sequence checks fail. Later nonmatching candidates do not clear it. Strict matches always have true. No candidates, only other namespaces, or incomparable namespaces leave false: no match was observed, rather than different namespaces being proven.

`NetNamespace` preserves this state in output. JSON and text use `matched_net_namespace`, omitted when false.

## 6. Terminal States

`types.CorrelationReason` is a string type. Each correlation outputs one `correlation_reason`:

| Go Constant | JSON Value | Meaning |
|---------|---------|------|
| `CorrelationMatched` | `matched` | Strict match, including a drop with an unknown source. |
| `CorrelationUnsupported` | `unsupported` | Current rules cannot handle this retransmission; it never enters the waiting queue. |
| `CorrelationWarmup` | `warmup` | Wait expired without a match; retransmission predates source readiness. |
| `CorrelationWaitTimeout` | `wait_timeout` | Wait expired without a match; retransmission is at or after source readiness. |
| `CorrelationQueueFull` | `queue_full` | The full waiting queue evicted this retransmission before expiry. |
| `CorrelationInterrupted` | `interrupted` | The loop exited and interrupted an unexpired wait. |

Each terminal branch assigns `correlationResult.reason` directly. Output uses the enum and validates that `matched` has a drop and the other five states do not. Empty, unknown, or contradictory values return errors. Fields are omitted when correlation is disabled; all finalized events have a valid state when enabled. Upgrade tools and receivers together when migrating from the reason array to the single field.

At expiry, kernel retransmission time determines the state: `warmup` before source readiness, otherwise `wait_timeout`, including within 1s after readiness. The 1s drop-to-retransmission bound applies only to matching candidates, not warmup. User-space delays do not change classification. Only the expiry branch applies this rule; strict matches, unsupported events, eviction, and interruption retain their own reasons. Warmup neither prevents matches nor ends waits early.

`matched_net_namespace` independently indicates a same-namespace drop in the same flow, regardless of packet and time conditions. It cannot replace `correlation_reason=matched`.

The flag can coexist with loss and rate-limit counters without changing the single terminal state. Unnormalizable drops and drop-cache eviction neither end a retransmission wait directly nor produce retransmission-level reasons.

## 7. Sample Loss and Rate Limiting

Three independent channels report status separately; do not add them together:

- `perf_lost`: sum of per-CPU `bpf_perf_out_dropwatch` map counters, using the shared perf-output counter facility `bpf_perf_output.h`. BPF increments by 1 when `bpf_perf_event_output` returns a negative value, for example when no reader is attached on the current CPU.
- `lost_samples`: ring-buffer overflow counts accumulated from user-space `PERF_RECORD_LOST`, visible only while the reader runs and queryable through ReadStatus after processing the loss record.
- `rate_limited`: `total_missed` in the `bpf_rlimit_dropwatch` rate-limit status map.

Successful `ReadStatus` sets `HasMapCounters=true`, serialized as `map_counters_available=true`. On error, the flag is false and `PerfLost` and `RateLimited` are zero, meaning unavailable rather than no loss or throttling. `LostSamples` remains valid and the snapshot is retained. These are instance-wide cumulative counters; maps and readers are sampled separately and cannot establish why an individual retransmission was unmatched.

User space sums `perf_lost` across CPUs and returns the current snapshot without checking counter rollback or uint64 addition overflow. This describes evidence completeness and cannot classify the network fault behind an unmatched event.

The correlator derives TCP sequence spans from IPv4 total length or IPv6 payload length. With GSO/offload, IP header lengths may not cover the full skb; matching scope is not expanded on that basis.

The dropwatch hot path is:

```text
software: hardware marker lookup/delete -> device/pcap filter
          -> status map lookup -> bpf_ktime_get_ns() -> rate limit -> perf output
hardware: device/pcap filter -> status map lookup -> bpf_ktime_get_ns()
          -> rate limit -> perf output
```

Filter-rejected events therefore perform no status lookup, event-time read, or counter update. Software drops first consume any hardware dedup marker so a rejected kfree path does not leave it behind.

## 8. Shutdown

### Session and Resource Ownership

The command layer manages process-wide `bpf.Init/Shutdown` and runtime timeouts. `retransmit.Run` creates output before attaching probes and finishes finalization, worker waits, Tracer closure, and output completion before returning. Configuration failure, cancellation, and runtime failure release acquired resources. Global BPF resources close after session cleanup.

`tcpshark` uses one `errgroup.WithContext` for the retransmission rate-limit reader, retransmission reader, embedded dropwatch reader, and correlation loop. All command workers share `groupCtx`. A worker error cancels the others, and `Wait` returns the first worker error. Resource owners combine close errors with `errors.Join`.

`retransmit.Tracer` and `internal/dropwatch.Tracer` each own their BPF objects, readers, and rate-limit alert workers. Failed `Open` rolls back acquired resources. Callers must still call `Close` after session context cancellation. An alert-worker failure cancels its collector, and `Close` returns the original error.

### Read and Close Contracts

`ReadInto` checks closure or cancellation before reading. Once a low-level read starts, its result is preserved rather than replaced by later closure or cancellation. `Close` can interrupt reads; callers must serialize `Close` calls on an instance.

`internal/dropwatch.Tracer` also has these constraints:

- Its context derives from `groupCtx`. Each instance allows one `ReadInto` caller; status reads and closure can run concurrently.
- `ReadStatus` can read final counters after context cancellation. The status type is `types.DropwatchStatus`; output uses `drop_perf_status`, including map-counter availability.
- `Close` cancels the instance, detaches and closes event and alert readers, waits for the alert worker, then releases the BPF object. Closing the alert reader wakes idle reads without waiting for the polling deadline.
- The first `Close` returns cleanup errors. Later calls return nil without retrying cleanup or replaying the first error.

### Finalization on Exit

On normal termination or worker failure:

1. Cancel the shared `groupCtx`.
2. Both `ReadInto` readers and the correlation loop exit. Stop reading dropwatch perf-ring records not yet delivered to the correlator.
3. Call `drainRetransmits(now)` with one timestamp: finalize expired entries, then clear unexpired entries, writing output in deadline order.
4. After all workers exit, close both Tracers and collect internal alert-worker and resource-release errors.
5. Finish socket output last. The session does not close the caller's `io.Writer`.

Expired items output `warmup` or `wait_timeout`; unexpired items output `interrupted`. All retain `drop_location=unknown`, diagnostic flags, and perf status. Matched or evicted events are not finalized again.

Shutdown does not guarantee draining kernel buffers. Trailing drops may be lost, ending otherwise matchable retransmissions early.

After cancellation, Unix socket output shares one five-second finalization budget across the active send, pending results, and End; each event does not restart it. A healthy receiver gets the trailing results followed by one End. When the budget expires, the connection closes and pending user-space results and End may be lost. Exhausting the cancellation budget is a normal stop; independent encoding, transport, and status-read errors remain reported.

## 9. File Responsibilities

```text
cmd/tcpshark/
├── main.go                         # Signals, version, CLI entry
├── cli.go                          # Argument validation, path and filter normalization
├── run.go                          # Global BPF lifecycle, timeout, feature dispatch
└── retransmit/                     # Retransmission feature package
    ├── config.go                   # Config、RunConfig
    ├── run.go                      # Run: resource composition, execution, cleanup
    ├── tracer.go                   # Open, ReadInto, Close, rate-limit worker
    ├── bpf_load.go                 # loadBPF, attachBPF, probe selection
    ├── event_reader.go             # Dual-stream reads, conversion, channel delivery
    ├── correlation_loop.go         # select, timer, exit finalization
    ├── correlation.go              # Correlation state machine, expiry finalization
    ├── correlation_match.go        # Candidate selection, consumption, namespace match state
    ├── match.go                    # Match types, namespace/flow/sequence/ktime
    ├── store.go                    # Entries, flow index, capacity and expiry management
    ├── event.go                    # Conversion of both ABI record types
    ├── classify.go                 # Retransmission classification
    ├── correlation_output.go       # Status completion, symbol resolution, correlation fields
    └── output.go                   # Text, JSON, socket output
```

The top level invokes `retransmit.Run(ctx, *RunConfig)` without handling ABI records, channels, correlation timers, or status counters.

The CLI validates scalar values and argument combinations before compiling the filter. A nonempty `--output-storage` skips validation of `--output` and warns on stderr if the latter was explicitly set. Without storage, only json and text are accepted. RunConfig.Tracing defines collection settings. A nil Dropwatch reads and outputs directly; otherwise two read workers and one correlation loop run. Correlation composes the retransmission feature rather than forming a general collection framework.

Ordinary mode creates no correlation channels or timers. The feature session handles conversion, classification, correlation, and output; Tracer owns low-level collection resources. See [Shutdown](#8-shutdown) for lifecycle details.

The correlator expires candidates before processing each input or timer; stores do not repeat expiry checks. It returns matching evidence, one terminal state, and diagnostic flags without modifying output fields. `correlation_output.go` constructs final output.

Each nonempty output batch reads dropwatch status once at entry; empty batches do not. Only matched drops undergo symbol resolution. Status is read even for an all-matched batch. On read failure, events are still written before returning the status error. Write failure stops the batch and combines both errors. Only unmatched events carry status snapshots and diagnostic flags; valid reader counters survive status-read failure. Matched events do not receive these fields.

Internal retransmission events retain only the ABI record and successful-read observedAt. Waiting queues do not store full display objects. Matching constructs netip addresses directly from ABI data and uses ABI enums without string conversion. IPv4-mapped IPv6 normalizes to IPv4. Single-event reads handle loss retries and read time consistently. Ordinary mode reuses an event with synchronous output; correlation mode reads into independent events before delivery to avoid overwriting pending data.

`run.go` creates channels and starts read workers; senders close channels. Read functions run synchronously and deliver correlation events through channels. Receivers own events independently of the sender's reusable buffer. TCPRetransmitTracing is created only for final output; ObservedTimestamp uses the original read time, not correlation completion time. SYNACK conversion fills TCP flags with SYN and ACK.

`cmd/tcpshark/retransmit` has no Go `internal` import restriction but is reserved by convention for tcpshark and must not depend back on the command layer. The two commands share collection code in:

```text
internal/dropwatch/
├── config.go    # Config, HardwareMode, configuration validation
├── tracer.go    # Tracer API, newTracer ownership, rollback, worker lifecycle
├── load.go      # loadBPF, attachBPF: loading, map/reader setup, probe attachment
├── netdev.go    # Device filtering and map configuration
├── status.go    # ReadStatus: output failures, ring loss, rate-limit counters
├── reason.go    # LoadReasonNames: kernel BTF reason table and numeric fallback
├── metadata.go  # ResolveMetadata: source, software reason, hardware trap/group
└── decode.go    # DecodePacket: ABI header adaptation and packet.Parse
```

`Open(ctx, *Config)` returns a concrete `*Tracer`; see [Shutdown](#8-shutdown) for read and close contracts. `DecodePacket` results do not borrow raw record memory.

Symbol resolution and `DropWatchTracing` display conversion belong to standalone dropwatch. TCP correlation directly retains ABI KernelObservedNS, namespace, stack PCs, and resolved drop sources and reasons.
