#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#define HUATUO_LOCK_PROFILER_SPIN
#include "bpf_lock_profiler.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/* Flags exported by lock:contention_begin since Linux 5.19. */
#define LCB_F_SPIN (1U << 0)
#define LCB_F_READ (1U << 1)
#define LCB_F_WRITE (1U << 2)

SEC("tracepoint/lock/contention_begin")
int trace_spin_contention_begin(struct lock_contention_begin_ctx *ctx)
{
	if (!(ctx->flags & LCB_F_SPIN) ||
	    (ctx->flags & (LCB_F_READ | LCB_F_WRITE)))
		return 0;
	return spin_wait_begin(ctx, ctx->lock_addr);
}

SEC("tracepoint/lock/contention_end")
int trace_spin_contention_end(struct lock_contention_end_ctx *ctx)
{
	return spin_wait_end(ctx, ctx->lock_addr, ctx->ret == 0);
}
