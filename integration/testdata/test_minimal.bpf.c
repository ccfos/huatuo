#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

/* rewrite constant */
const volatile __u32 target_key = 0;

/* map for map operation test */
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} counter_map SEC(".maps");

/* perf event pipe map */
struct {
    __uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
} events SEC(".maps");

/* Kprobe / Kretprobe / Tracepoint */ 

// kprobe
SEC("kprobe/sys_openat")
int test_kprobe(struct pt_regs *ctx)
{
    return 0;
}

// kretprobe
SEC("kretprobe/sys_openat")
int test_kretprobe(struct pt_regs *ctx)
{
    return 0;
}

// tracepoint
SEC("tracepoint/syscalls/sys_enter_openat")
int test_tracepoint(void *ctx)
{
    return 0;
}

// raw tracepoint
SEC("raw_tracepoint/sys_enter")
int test_raw_tracepoint(void *ctx)
{
    return 0;
}


/* Perf Event / EventPipe */

// event pipe trigger
SEC("tracepoint/syscalls/sys_enter_nanosleep")
int eventpipe_prog(void *ctx)
{
    return 0;
}

SEC("tracepoint/syscalls/sys_enter_getpid")
int perf_event_prog(void *ctx) 
{ 
	return 0; 
}
