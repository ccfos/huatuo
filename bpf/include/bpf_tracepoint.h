#ifndef __BPF_TRACEPOINT_H__
#define __BPF_TRACEPOINT_H__

/* Vendor kernels can append fields to trace_entry, shifting tracepoint args. */
static __always_inline const void *tracepoint_arg(const void *ctx, u32 index)
{
	u32 offset = bpf_core_type_size(struct trace_entry);
	const void *arg = NULL;

	offset = (offset + sizeof(void *) - 1) & ~(sizeof(void *) - 1);
	bpf_probe_read_kernel(&arg, sizeof(arg),
			      (const char *)ctx + offset + index * sizeof(void *));
	return arg;
}

/*
 * hungtask: trace_event_raw_sched_process_hang::comm changed from a
 * fixed-size __array to a __data_loc string on 7.0+.
 */
struct trace_event_raw_sched_process_hang___7_0_compat {
	struct trace_entry ent;
	u32 __data_loc_comm;
	pid_t pid;
} __attribute__((preserve_access_index));

static __always_inline char *__data_loc_address(char *ctx, u32 __data_loc)
{
	return ((char *)ctx + (__data_loc & 0xffff));
}

#endif /* __BPF_TRACEPOINT_H__ */
