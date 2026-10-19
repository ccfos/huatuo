/* SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0) */
/* Copyright 2026 The HuaTuo Authors. */
#undef TRACE_SYSTEM
#define TRACE_SYSTEM vmscan
#if !defined(MEMORY_FREE_COMPACT_TRACE_H) || defined(TRACE_HEADER_MULTI_READ)
#define MEMORY_FREE_COMPACT_TRACE_H
#include <linux/memcontrol.h>
#include <linux/tracepoint.h>
#include <trace/events/mmflags.h>
#include "memory_free_compact_event.h"
#endif
#undef TRACE_INCLUDE_PATH
#define TRACE_INCLUDE_PATH .
#undef TRACE_INCLUDE_FILE
#define TRACE_INCLUDE_FILE memory_free_compact_trace
#include <trace/define_trace.h>
