#ifndef __BPF_PROFILER_H__
#define __BPF_PROFILER_H__

#include "bpf_common.h"

/*
 * profiler_event_base_t is the common base for all profiler events.
 * Both CPU and Memory profilers share these fields to enable unified
 * event processing in userspace.
 *
 * This structure must be kept in sync with Go's ProfilerEventBase in
 * cmd/profiler/provider/native_bpf_context.go for binary compatibility.
 */
struct profiler_event_base_t {
	u32 pid;
	char comm[COMPAT_TASK_COMM_LEN];
	int kernstack;
	int userstack;
};

#endif /* __BPF_PROFILER_H__ */
