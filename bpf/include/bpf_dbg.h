#ifndef __BPF_DBG_H__
#define __BPF_DBG_H__

#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

#define BPF_DBG_MSG_LEN 64
#define BPF_DBG_FILE_LEN 64

struct bpf_dbg_event {
	char file_name[BPF_DBG_FILE_LEN];
	u32 file_line;
	u32 pad0;
	char msg[BPF_DBG_MSG_LEN];
	u64 args[4];
	u64 timestamp;
};

#define BPF_DBG_MAP(prog_map_name)                                          \
	struct {                                                          \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);              \
		__uint(key_size, sizeof(int));                            \
		__uint(value_size, sizeof(u32));                          \
	} dbg_##prog_map_name##_events SEC(".maps")

volatile const u32 bpf_dbg_enabled = 0;

/* __builtin_memcpy is required here because msg_ and __FILE_NAME__ are
 * compile-time string literals residing in the BPF .rodata section, which
 * are not valid kernel or user memory addresses. bpf_probe_read_str expects
 * a runtime memory pointer and silently returns an empty buffer when given
 * a .rodata address. */
#define bpf_dbg(ctx, map_name, msg_, a1, a2, a3)                                    \
	do {                                                                          \
		if (bpf_dbg_enabled) {                                                \
			struct bpf_dbg_event __event = {                              \
				.timestamp = bpf_ktime_get_ns(),                      \
				.file_line = __LINE__,                                \
				.args      = {a1, a2, a3, 0},                         \
			};                                                            \
			__builtin_memcpy(__event.file_name,                           \
					 __FILE_NAME__,                               \
					 sizeof(__FILE_NAME__) < BPF_DBG_FILE_LEN ?   \
					 sizeof(__FILE_NAME__) : BPF_DBG_FILE_LEN);   \
			__builtin_memcpy(__event.msg, msg_,                           \
					 sizeof(msg_) < BPF_DBG_MSG_LEN ?             \
					 sizeof(msg_) : BPF_DBG_MSG_LEN);             \
			bpf_perf_event_output(ctx, &dbg_##map_name##_events,          \
					      COMPAT_BPF_F_CURRENT_CPU,               \
					      &__event, sizeof(__event));             \
		}                                                                     \
	} while (0)

#define bpf_dbg_msg(ctx, map_name, msg_)  bpf_dbg(ctx, map_name, msg_, 0, 0, 0)

#endif /* __BPF_DBG_H__ */
