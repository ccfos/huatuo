#ifndef __BPF_TRACEPOINT_H__
#define __BPF_TRACEPOINT_H__

/*
 * Linux 7.0 renamed the tcp_retransmit_skb BTF context type, so CO-RE
 * cannot resolve the old name. Use a local layout to avoid that relocation.
 */
struct trace_event_raw_tcp_event_sk_skb_compat {
	struct trace_entry ent;
	const void *skbaddr;
	const void *skaddr;
};

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
