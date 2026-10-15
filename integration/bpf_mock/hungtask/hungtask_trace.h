/* SPDX-License-Identifier: GPL-2.0 */
/* Copyright 2026 The HuaTuo Authors. */

#undef TRACE_SYSTEM
#define TRACE_SYSTEM sched

#if !defined(HUATUO_HUNGTASK_TRACE_H) || defined(TRACE_HEADER_MULTI_READ)
#define HUATUO_HUNGTASK_TRACE_H

#include <linux/sched.h>
#include <linux/tracepoint.h>

#include "hungtask_event.h"

#endif

#undef TRACE_INCLUDE_PATH
#define TRACE_INCLUDE_PATH .
#undef TRACE_INCLUDE_FILE
#define TRACE_INCLUDE_FILE hungtask_trace
#include <trace/define_trace.h>
