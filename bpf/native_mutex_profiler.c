#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_lock_profiler.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/* Flags exported by lock:contention_begin since Linux 5.19. */
#define LCB_F_MUTEX (1U << 5)

SEC("kprobe/huatuo_mutex_lock_slowpath")
int trace_mutex_lock_slowpath(struct pt_regs *ctx)
{
	return lock_wait_begin_kprobe(bpf_get_current_pid_tgid(),
				      PT_REGS_PARM1(ctx),
				      PROFILER_LOCK_ACCESS_UNKNOWN);
}

SEC("kretprobe/huatuo_mutex_lock_slowpath")
int trace_mutex_lock_slowpath_return(struct pt_regs *ctx)
{
	return lock_wait_end(ctx, bpf_get_current_pid_tgid(), 0, true);
}

SEC("tracepoint/lock/contention_begin")
int trace_mutex_contention_begin(struct lock_contention_begin_ctx *ctx)
{
	if (!(ctx->flags & LCB_F_MUTEX))
		return 0;
	return lock_wait_begin(bpf_get_current_pid_tgid(), ctx->lock_addr,
			       PROFILER_LOCK_ACCESS_UNKNOWN);
}

SEC("tracepoint/lock/contention_end")
int trace_mutex_contention_end(struct lock_contention_end_ctx *ctx)
{
	return lock_wait_end(ctx, bpf_get_current_pid_tgid(), ctx->lock_addr,
			     ctx->ret == 0);
}
