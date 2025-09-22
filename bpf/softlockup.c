#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "linux_kernel.h"

char __license[] SEC("license") = "Dual MIT/GPL";

BPF_RATELIMIT_IN_MAP(rate, 1, COMPAT_CPU_NUM * 10000, 0);

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} softlockup_perf_events SEC(".maps");

struct softlockup_info {
	u32 cpu;
	u32 pid;
	char comm[COMPAT_TASK_COMM_LEN];
};

SEC("kprobe/add_taint")
int kprobe_softlockup(struct pt_regs *ctx)
{
	// check the add_taint flag
	if (PT_REGS_PARM1(ctx) != TAINT_SOFTLOCKUP)
		return 0;

	if (bpf_ratelimited_in_map(ctx, rate))
		return 0;

	struct softlockup_info info = {
		.cpu = bpf_get_smp_processor_id(),
		.pid = bpf_get_current_pid_tgid() >> 32,
	};

	struct task_struct *task = (struct task_struct *)bpf_get_current_task();
	BPF_CORE_READ_STR_INTO(&info.comm, task, comm);
	bpf_perf_event_output(ctx, &softlockup_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}
