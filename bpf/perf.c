#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"

char __license[] SEC("license") = "Dual MIT/GPL";

volatile const u64 css = 0;
volatile const u64 pid = 0;

#define PERF_STACK_DEPTH 20

struct key_t {
	u64 ustack[PERF_STACK_DEPTH];
	u64 kstack[PERF_STACK_DEPTH];
	s64 ustack_size;
	s64 kstack_size;
	u32 pid;
	char name[COMPAT_TASK_COMM_LEN];
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(struct key_t));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 1024);
} counts SEC(".maps");

SEC("perf_event/software/cpu_clock")
int perf_event_sw_cpu_clock(struct pt_regs *ctx)
{
	struct task_struct *curr = (struct task_struct *)bpf_get_current_task();
	u64 cpu_css = (u64)BPF_CORE_READ(curr, cgroups, subsys[cpu_cgrp_id]);
	if (css != 0 && css != cpu_css)
		return 0;

	u64 tgid = bpf_get_current_pid_tgid() >> 32;
	if (pid != 0 && pid != tgid)
		return 0;

	struct key_t key = {.pid = bpf_get_current_pid_tgid() >> 32};
	bpf_get_current_comm(&key.name, sizeof(key.name));

	key.ustack_size = bpf_get_stack(ctx, key.ustack, sizeof(key.ustack),
					COMPAT_BPF_F_USER_STACK);
	key.kstack_size = bpf_get_stack(ctx, key.kstack, sizeof(key.kstack), 0);

	u64 *valp = bpf_map_lookup_elem(&counts, &key);
	if (!valp) {
		u64 cnt = 1;
		bpf_map_update_elem(&counts, &key, &cnt, COMPAT_BPF_ANY);
		return 0;
	}

	__sync_fetch_and_add(valp, 1);
	return 0;
}
