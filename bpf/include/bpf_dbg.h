#ifndef __BPF_DBG_H__
#define __BPF_DBG_H__

#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

#define BPF_DBG_MSG_LEN	 64
#define BPF_DBG_FILE_LEN 64

struct bpf_dbg_event {
	char file_name[BPF_DBG_FILE_LEN];
	u32 file_line;
	u32 pad0;
	char msg[BPF_DBG_MSG_LEN];
	u64 args[4];
	u64 timestamp;
};

/*
 * bpf_dbg_enabled is intentionally defined OUTSIDE the DEBUG_BPF guard so the
 * symbol is *always* present in .rodata, regardless of build mode. The Go
 * loader unconditionally rewrites it via RewriteConstants (see WithBpfDbg);
 * if it were compiled out, RewriteConstants would fail with a missing-constant
 * error and abort program loading. Keeping it public makes the load path
 * identical in both modes and removes the need for fragile error-handling
 * fallbacks. In non-debug builds nothing references it, so the verifier simply
 * ignores it and it adds no runtime cost.
 */
volatile const u32 bpf_dbg_enabled = 0;

/*
 * Two-stage gating to minimize BPF runtime impact:
 *
 *   1. Compile-time (DEBUG_BPF macro, this header):
 *      When DEBUG_BPF is NOT defined (default), bpf_dbg/bpf_dbg_msg expand
 *      to no-ops and BPF_DBG_MAP becomes empty. The debug perf event array,
 *      the per-call event struct on the BPF stack, the two __builtin_memcpy
 *      calls, bpf_ktime_get_ns, and bpf_perf_event_output are *not emitted
 *      at all*. Verifier never sees them, .o size shrinks, no fd is consumed
 *      at load. (bpf_dbg_enabled itself stays defined; see note above.)
 *      Enable with: BPF_DEBUG=1 make bpf-build  (passes -DDEBUG_BPF)
 *
 *   2. Run-time (bpf_dbg_enabled volatile const):
 *      Even when compiled in, output is suppressed until the Go side calls
 *      bpf.EnableBpfDbg() before LoadBpf, which rewrites the constant to 1
 *      via WithBpfDbg(). Without that rewrite the verifier folds the `if`
 *      away as dead code.
 */
#ifdef DEBUG_BPF

#define BPF_DBG_MAP(prog_map_name)                                             \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);                   \
		__uint(key_size, sizeof(int));                                 \
		__uint(value_size, sizeof(u32));                               \
	} dbg_##prog_map_name##_events SEC(".maps")

/* __builtin_memcpy is required here because msg_ and __FILE_NAME__ are
 * compile-time string literals residing in the BPF .rodata section, which
 * are not valid kernel or user memory addresses. bpf_probe_read_str expects
 * a runtime memory pointer and silently returns an empty buffer when given
 * a .rodata address. */
#define bpf_dbg(ctx, map_name, msg_, a1, a2, a3)                               \
	do {                                                                   \
		if (bpf_dbg_enabled) {                                         \
			struct bpf_dbg_event __event = {                       \
				.timestamp = bpf_ktime_get_ns(),               \
				.file_line = __LINE__,                         \
				.args = {a1, a2, a3, 0},                       \
			};                                                     \
			__builtin_memcpy(__event.file_name, __FILE_NAME__,     \
					 sizeof(__FILE_NAME__) <               \
							 BPF_DBG_FILE_LEN      \
						 ? sizeof(__FILE_NAME__)       \
						 : BPF_DBG_FILE_LEN);          \
			__builtin_memcpy(__event.msg, msg_,                    \
					 sizeof(msg_) < BPF_DBG_MSG_LEN        \
						 ? sizeof(msg_)                \
						 : BPF_DBG_MSG_LEN);           \
			bpf_perf_event_output(ctx, &dbg_##map_name##_events,   \
					      COMPAT_BPF_F_CURRENT_CPU,        \
					      &__event, sizeof(__event));      \
		}                                                              \
	} while (0)

#define bpf_dbg_msg(ctx, map_name, msg_) bpf_dbg(ctx, map_name, msg_, 0, 0, 0)

#else /* !DEBUG_BPF */

/* Compile-time no-ops. The (void) casts silence -Wunused-value while still
 * type-checking ctx/msg_/args, so a DEBUG_BPF rebuild won't surface fresh
 * compile errors. BPF_DBG_MAP expands to nothing so no perf event array
 * map is emitted. */
#define BPF_DBG_MAP(prog_map_name)

#define bpf_dbg(ctx, map_name, msg_, a1, a2, a3)                               \
	do {                                                                   \
		(void)(ctx);                                                   \
		(void)(msg_);                                                  \
		(void)(a1);                                                    \
		(void)(a2);                                                    \
		(void)(a3);                                                    \
	} while (0)

#define bpf_dbg_msg(ctx, map_name, msg_)                                       \
	do {                                                                   \
		(void)(ctx);                                                   \
		(void)(msg_);                                                  \
	} while (0)

#endif /* DEBUG_BPF */

#endif /* __BPF_DBG_H__ */
