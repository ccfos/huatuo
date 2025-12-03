#include "vmlinux.h"

#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} ad_event_map SEC(".maps");

SEC("kprobe/ad_disable_collecting_distributing")
int ad_disable(struct pt_regs *ctx)
{
	// nothing to do here, only notify user space, because this is a
	// ko module and CO-RE relocation is not supported directly at old
	// kernel
	u64 nothing = 0;
	bpf_perf_event_output(ctx, &ad_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      &nothing, sizeof(nothing));
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";
