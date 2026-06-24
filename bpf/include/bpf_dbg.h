#ifndef __BPF_DBG_H__
#define __BPF_DBG_H__

#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

#define BPF_DBG_MSG_LEN 64

struct bpf_dbg_event {
	u64 timestamp;
	u32 file_id;
	u32 line;
	u64 args[4];
	char msg[BPF_DBG_MSG_LEN];
};

#define BPF_DBG_MAP(prog_name)                                          \
	struct {                                                          \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);              \
		__uint(key_size, sizeof(int));                            \
		__uint(value_size, sizeof(u32));                          \
	} dbg_##prog_name##_events SEC(".maps")

#ifndef BPF_DBG_FILE_ID
#define BPF_DBG_FILE_ID 0
#endif

volatile const u32 bpf_dbg_enabled = 0;

#define bpf_dbg(ctx, map_name, msg, a1, a2, a3)                               \
	do {                                                                    \
		if (bpf_dbg_enabled) {                                          \
			struct bpf_dbg_event __event = {                        \
				.timestamp = bpf_ktime_get_ns(),               \
				.file_id   = BPF_DBG_FILE_ID,                  \
				.line      = __LINE__,                        \
				.args      = {a1, a2, a3, 0},                 \
			};                                                      \
			bpf_probe_read_str(__event.msg,                        \
						   sizeof(__event.msg), msg);          \
			bpf_perf_event_output(ctx, &dbg_##map_name##_events,  \
					      COMPAT_BPF_F_CURRENT_CPU,          \
					      &__event, sizeof(__event));         \
		}                                                               \
	} while (0)

#define bpf_dbg_msg(ctx, map_name, msg)  bpf_dbg(ctx, map_name, msg, 0, 0, 0)

#endif /* __BPF_DBG_H__ */
