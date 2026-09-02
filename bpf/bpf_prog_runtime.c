#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "abi/bpf_prog_runtime_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, u32);
	__type(value, struct bpf_prog_runtime_value);
	__uint(max_entries, 1);
} profile_stats SEC(".maps");

SEC("fentry/XXX")
int BPF_PROG(profile_fentry)
{
	u32 key = 0;
	struct bpf_prog_runtime_value *value;

	value = bpf_map_lookup_elem(&profile_stats, &key);
	if (value)
		value->start_time_ns = bpf_ktime_get_ns();

	return 0;
}

SEC("fexit/XXX")
int BPF_PROG(profile_fexit)
{
	u64 end_time_ns = bpf_ktime_get_ns();
	u32 key = 0;
	struct bpf_prog_runtime_value *value;

	value = bpf_map_lookup_elem(&profile_stats, &key);
	if (!value || !value->start_time_ns ||
	    end_time_ns < value->start_time_ns)
		return 0;

	value->run_time_ns += end_time_ns - value->start_time_ns;
	value->run_count++;
	value->start_time_ns = 0;

	return 0;
}
