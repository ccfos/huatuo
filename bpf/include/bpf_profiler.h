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
	u64 pid_tgid;  // Full pid_tgid from bpf_get_current_pid_tgid(): tgid (process) in upper 32 bits, pid (thread) in lower 32 bits
	char comm[COMPAT_TASK_COMM_LEN];
	int kernstack;
	int userstack;
	s64 value;  // CPU: always 1 (sample count), Memory: page/byte delta
};

#endif /* __BPF_PROFILER_H__ */
