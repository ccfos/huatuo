---
title: Flame Graph Formats
type: docs
description: ""
author: HUATUO Team
date: 2026-06-18
weight: 4
---

This guide explains collapsed stacks and flame graphs and how to interpret them. See the [Profiling Guide](/docs/best-practice/continuous-profiling_en.md#results) for collection, storage, and task completion.

HUATUO's `profiler` and `irqtracing` use the same window formatter. Sample units depend on the collection mode: nanoseconds for CPU time, bytes for memory, and counts for IRQ events. Confirm the mode and unit before interpreting values.

## Choose a Format

| Goal | Format | Notes |
| --- | --- | --- |
| Aggregate, filter, or render again with scripts | `collapsed` | Each line contains a stack and its sample value |
| Inspect call paths in a browser | `flamegraph`, `svg` | HUATUO produces SVG with interactive scripts |
| Preserve windows, labels, and full symbols | `json` or pprof-compatible data | Avoids name normalization losses in collapsed stacks |

## Collapsed Stacks

### Line Format

Collapsed stacks, also called folded stacks, separate frames with semicolons and separate the stack from its final numeric value with a space:

```text
frame1;frame2;frame3;...;frameN COUNT
```

| Part | Meaning |
| --- | --- |
| `frame1` | Root frame, such as `main` or `start_thread` |
| `;` | Frame separator |
| `frameN` | Leaf frame |
| `COUNT` | Accumulated sample value for this stack; its unit depends on the collection mode |

Frames run from root to leaf, left to right. Values for identical call paths can be aggregated. Before comparing results, confirm that the collection mode, unit, target, and time range match.

### Frame Names

Collapsed stacks have no universal escaping convention. HUATUO replaces `;` in frame names with `:` and carriage returns and newlines with spaces so each stack occupies one line. Internal symbols and structured formats preserve the original names.

For example, `generic::<[u8; 7]>` becomes `generic::<[u8: 7]>`. Symbols differing only in `;` and `:` merge. Use a structured format when full symbol fidelity is required.

### Numeric Example

This illustrative CPU-time output uses nanoseconds for the final values, not sample counts:

```text
main;handle;read 2000000
main;handle;parse 1000000
```

The two call paths accumulate 2 ms and 1 ms. In this dataset, `handle` totals 3 ms and the `read` path accounts for two-thirds. Interpret memory and IRQ modes in bytes and counts, respectively.

## Flame Graphs

| Dimension | Meaning |
| --- | --- |
| Horizontal position | Arrangement of call paths, not chronological order |
| Vertical position | Call depth, usually with roots at the bottom and leaves at the top |
| Frame width | Accumulated value and proportion of the selected measurement |
| Frame color | Defined by the renderer; not directly comparable across tools |

A wider frame has a larger share of the selected measurement. It does not by itself prove a bottleneck or fault cause. Keep workloads and collection conditions comparable when assessing results before and after optimization.

HUATUO's `flamegraph` and `svg` outputs are equivalent: both are SVG with interactive scripts. Use zoom, search, and hover details to locate call paths and inspect values.

First confirm the collection mode and unit, then inspect call paths with large shares and use other observations to assess the cause. Retain collapsed stacks for rendering and aggregation; retain structured results when full symbols, windows, or labels are needed.
