#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_lock_profiler.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/* Flags exported by lock:contention_begin since Linux 5.19. */
#define LCB_F_SPIN (1U << 0)
#define LCB_F_READ (1U << 1)
#define LCB_F_WRITE (1U << 2)

SEC("kprobe/huatuo_rwlock_read_slowpath")
int trace_rwlock_read_slowpath(struct pt_regs *ctx)
{
	return lock_wait_begin(bpf_get_current_pid_tgid(), PT_REGS_PARM1(ctx),
			       PROFILER_LOCK_ACCESS_READ);
}

SEC("kretprobe/huatuo_rwlock_read_slowpath")
int trace_rwlock_read_slowpath_return(struct pt_regs *ctx)
{
	return lock_wait_end(ctx, bpf_get_current_pid_tgid(), 0, true);
}

SEC("kprobe/huatuo_rwlock_write_slowpath")
int trace_rwlock_write_slowpath(struct pt_regs *ctx)
{
	return lock_wait_begin(bpf_get_current_pid_tgid(), PT_REGS_PARM1(ctx),
			       PROFILER_LOCK_ACCESS_WRITE);
}

SEC("kretprobe/huatuo_rwlock_write_slowpath")
int trace_rwlock_write_slowpath_return(struct pt_regs *ctx)
{
	return lock_wait_end(ctx, bpf_get_current_pid_tgid(), 0, true);
}

SEC("tracepoint/lock/contention_begin")
int trace_rwlock_contention_begin(struct lock_contention_begin_ctx *ctx)
{
	u32 access = ctx->flags & (LCB_F_READ | LCB_F_WRITE);
	if (!(ctx->flags & LCB_F_SPIN))
		return 0;
	if (access == LCB_F_READ)
		return lock_wait_begin(bpf_get_current_pid_tgid(), ctx->lock_addr,
				       PROFILER_LOCK_ACCESS_READ);
	if (access == LCB_F_WRITE)
		return lock_wait_begin(bpf_get_current_pid_tgid(), ctx->lock_addr,
				       PROFILER_LOCK_ACCESS_WRITE);
	return 0;
}

SEC("tracepoint/lock/contention_end")
int trace_rwlock_contention_end(struct lock_contention_end_ctx *ctx)
{
	return lock_wait_end(ctx, bpf_get_current_pid_tgid(), ctx->lock_addr,
			     ctx->ret == 0);
}
