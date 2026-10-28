#ifndef __BPF_FUNC_TRACE_H__
#define __BPF_FUNC_TRACE_H__

#include <bpf/bpf_helpers.h>

struct trace_entry_ctx {
	/* Caller-owned identity, such as a thread ID or starting CSS. */
	u64 id;
	u64 start_ns;
	u64 duration_ns;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct trace_entry_ctx);
	__uint(max_entries, 10240);
} func_trace_map SEC(".maps");

static __always_inline void func_trace_begin(u64 key, u64 id)
{
	struct trace_entry_ctx entry = {
		.start_ns = bpf_ktime_get_ns(),
		.id	  = id,
	};

	bpf_map_update_elem(&func_trace_map, &key, &entry, COMPAT_BPF_ANY);
}

static __always_inline struct trace_entry_ctx *
func_trace_end(u64 key)
{
	struct trace_entry_ctx *entry;

	entry = bpf_map_lookup_elem(&func_trace_map, &key);
	if (!entry) {
		return NULL;
	}

	entry->duration_ns = bpf_ktime_get_ns() - entry->start_ns;
	return entry;
}

static __always_inline void func_trace_destroy(u64 key)
{
	bpf_map_delete_elem(&func_trace_map, &key);
}

#endif
