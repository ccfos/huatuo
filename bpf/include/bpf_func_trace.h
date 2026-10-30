#ifndef __BPF_FUNC_TRACE_H__
#define __BPF_FUNC_TRACE_H__

#include <bpf/bpf_helpers.h>

struct trace_entry_ctx {
	/* Caller-owned identity, such as a thread ID or starting CSS. */
	u64 id;
	u64 start_ns;
	u64 duration_ns;
};

static __always_inline void func_trace_begin(void *map, const void *key, u64 id)
{
	struct trace_entry_ctx entry = {
		.start_ns = bpf_ktime_get_ns(),
		.id	  = id,
	};

	bpf_map_update_elem(map, key, &entry, COMPAT_BPF_ANY);
}

static __always_inline struct trace_entry_ctx *
func_trace_end(void *map, const void *key)
{
	struct trace_entry_ctx *entry;

	entry = bpf_map_lookup_elem(map, key);
	if (!entry) {
		return NULL;
	}

	entry->duration_ns = bpf_ktime_get_ns() - entry->start_ns;
	return entry;
}

static __always_inline void func_trace_destroy(void *map, const void *key)
{
	bpf_map_delete_elem(map, key);
}

#endif
