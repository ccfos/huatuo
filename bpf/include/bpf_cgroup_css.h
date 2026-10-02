#ifndef __BPF_CGROUP_CSS_H__
#define __BPF_CGROUP_CSS_H__

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "abi/cgroup_types.h"

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} cgroup_perf_events SEC(".maps");

static __noinline int
submit_cgroup_css_event(void *ctx, struct cgroup *cgrp,
			 enum cgroup_css_operation operation)
{
	struct cgroup_css_event data = {};
	int knode_len;
	u32 css_size;

	knode_len =
		bpf_probe_read_kernel_str(&data.knode_name, sizeof(data.knode_name),
					  BPF_CORE_READ(cgrp, kn, name));
	if (knode_len < CGROUP_KNODE_NAME_MINLEN + 1)
		return 0;

	data.cgroup	  = (u64)cgrp;
	data.operation   = operation;
	data.cgroup_root  = BPF_CORE_READ(cgrp, root, hierarchy_id);
	data.cgroup_level = BPF_CORE_READ(cgrp, level);

	css_size = bpf_core_field_size(cgrp->subsys);
	if (css_size > sizeof(data.css))
		css_size = sizeof(data.css);
	bpf_probe_read(&data.css, css_size, BPF_CORE_READ(cgrp, subsys));

	bpf_perf_event_output(ctx, &cgroup_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &data, sizeof(data));
	return 0;
}

#endif /* __BPF_CGROUP_CSS_H__ */
