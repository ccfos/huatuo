# Profiling data analysis methodology

## Overview
HuaTuo flamegraph/profile data may be frame arrays or pprof objects containing
`profile_type` and `profile`. The event adapter normalizes both to frame data.
`has_profiler` indicates payload markers, not guaranteed parseability. `query_event_profiles`
returns structured hotspots, self/inclusive percentages, and call paths.
It analyzes stored profiling payloads and does not trigger profiling collection.

`self_time` and `total_time` are historical API names: not all profile weights
represent CPU time. Inspect `summary.sample_type` and `sample_unit` first.
`cpu/nanoseconds` represents CPU time; count-based profiles, such as IRQ profiles,
represent event counts. For frame arrays without type/unit metadata, confirm the
meaning from the source rather than inferring it from function or analysis names.

## Analysis modes

### self_time — exclusive weight
- Weight attributed to the function itself, excluding callees.
- Use it to locate functions with the greatest exclusive weight.
- High `self_percent` means a high share of total weight in that function.
  Discuss CPU consumption only for confirmed CPU profiles; an IRQ-count percentage
  is not a CPU-time percentage.

### total_time — inclusive weight
- Weight attributed to the function and its callees.
- Use it to understand the weight distribution across call chains.
- High `total_percent` means a high inclusive share. The sample type/unit determines
  whether this represents CPU time, event counts, or another quantity.

### Choosing a mode
| Goal | Mode | Reason |
|------|------|--------|
| Find hotspot functions | self_time | Identify the greatest exclusive sample weights |
| Understand call chains | total_time | Identify expensive paths overall |
| Distinguish userspace from kernel work | self_time | Locate the responsible functions |
| Assess subsystem overhead | total_time | Aggregate by call stack |

## Reading results

### Step 1: Validate the data
Inspect `summary`:
- `total_samples`: total analyzed weight. For pprof, interpret it with `sample_type`
  and `sample_unit`. `cpu/nanoseconds` is cumulative CPU time, not a sample count.
  Sample-count sufficiency thresholds apply only to count-based weights.
- Aggregated hotspots from nearby events are not single-event hotspots. To compare
  events, call `query_event_profiles(record_id=..., region=[...])` separately using each
  event's `source.record_id` from `query_events`, retaining the same targets,
  tracer, and time range.
- `unique_functions`: distinct function count.
- `documents`: successfully parsed profiling documents.
- `unsupported_documents`: documents not parseable as valid frame arrays or pprof samples.
- `payload_format`: currently `frame_data`.
- `coverage`: sum of the returned top_n functions' self weights as a percentage of
  total weight. It is neither host CPU utilization nor proof of complete event/process coverage.

Preserve native `source` entries in `sources`. They identify successfully parsed
documents included in aggregation. The caller's Skill may use them to locate
original profiles; MCP does not generate viewing links.

### Step 2: Locate self_time hotspots
Inspect the top five `top_functions` by descending `self_percent`:
- One function above 50%: concentrated exclusive weight. Interpret this as a
  computation hotspot only for CPU profiles; otherwise use the actual sample units.
- Several similar percentages: distributed overhead; inspect stacks.
- Determine whether functions are kernel or userspace functions to choose the next steps.

### Step 3: Analyze call paths
For each hotspot, inspect `call_paths[].path`:
- Paths run from the hotspot toward callers and the root; index 0 is the hotspot.
- `total_time` returns caller context and inclusive `total_samples` even when the
  hotspot has no self samples. `self_time` returns only paths with self samples.
- Interpret key kernel functions using `huatuo://profiling/kernel-signatures`.
- Shared call-path segments across hotspots may suggest a common cause.

### Step 4: Cross-check
- Profiler hotspots + ES event `key_fields`: corroborate findings.
- Profiler hotspots + Prometheus trends: distinguish sustained from transient behavior.
- Profiler hotspots + subsystem patterns: identify candidate root causes.

## Common hotspot patterns
The CPU-overhead interpretations below apply only to confirmed CPU profiles.
For IRQ-count and other profiles, interpret the same functions according to
their actual sample types and units.

### Compute-intensive
```text
Signature: one userspace function has self_percent > 80%
call_stack: [app_func → libc → ...]
Assessment: application code consumes CPU
```

### Kernel overhead
```text
Signature: several kernel functions have distributed but collectively high self_percent
call_stack: [kernel_func → syscall → user_func]
Assessment: application system calls trigger kernel work
Next: identify the subsystem (memory / I/O / networking / locking)
```

### Lock contention
```text
Signature: high native_queued_spin_lock_slowpath or rwsem_slowpath weight
call_stack: [spin_lock_slowpath → caller → ...]
Assessment: CPUs contend for the same lock
Next: identify the caller and the resource protected by the lock
```

### I/O-related waits
```text
Signature: high io_schedule / blkdev_issue_flush / wait_on_page_bit weight
call_stack: [io_schedule → submit_bio → ...]
Lead: samples involve I/O wait paths; CPU profiles do not measure blocked duration
Next: corroborate disk latency with iotracing events and wait metrics
```

### Memory reclaim
```text
Signature: high shrink_node / shrink_slab / try_to_free_pages weight
call_stack: [shrink_node → try_to_free_pages → alloc_pages → page_fault]
Assessment: memory pressure triggers reclaim
Next: corroborate memory shortage with memburst events
```

### Softirq processing
```text
Signature: high net_rx_action / napi_poll / net_tx_action weight
call_stack: [napi_poll → net_rx_action → __do_softirq]
Assessment: packet processing consumes CPU
Next: check dropwatch events for packet drops
```

## Caveats
1. **Interpret weights by type**: CPU profiles measure on-CPU time, excluding
   blocked I/O/sleep duration. IRQ/count profiles describe event distributions.
   Do not interpret all profiling data as CPU time.
2. **Sampling precision**: consider collection method, sampling frequency, and
   time window. Use sample-count thresholds only for actual counts; never apply
   a `total_samples < 100` rule to nanoseconds, bytes, or other non-count weights.
3. **Container versus host**: container profiles cover container tasks; host
   profiles cover processes across the host.
4. **Threads**: profiles aggregate thread samples. Contention may appear in
   functions such as spin_lock.
5. **Stack depth**: inspect the first 5–10 relevant frames to focus the investigation,
   while preserving complete original stacks when reporting stack-bearing events.
