// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

#define CGROUP_SUBSYS_NAME_LEN 16

struct cgroup_subsys_id_event {
	u64 cgroup;
	s32 subsystem_id;
	u32 subsystem_count;
	u8 subsystem_name[CGROUP_SUBSYS_NAME_LEN];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} cgroup_subsys_id_events SEC(".maps");

char __license[] SEC("license") = "GPL";

/*
 * Report the identity stored in each live CSS independently of the BTF enum
 * parser under test. Userspace compares these records with the BTF-derived map
 * to detect an incorrect subsystem ID or name mapping.
 */
SEC("kprobe/cgroup_subsys_id")
int cgroup_subsys_id_prog(struct pt_regs *ctx)
{
	struct cgroup_subsys_state *subsystems[CGROUP_SUBSYS_COUNT] = {};
	struct cgroup_subsys_state *trigger_css = (void *)PT_REGS_PARM1(ctx);
	struct cgroup *cgrp = BPF_CORE_READ(trigger_css, cgroup);
	u32 subsystem_size = bpf_core_field_size(cgrp->subsys);
	u32 copy_size = subsystem_size;
	u32 subsystem_count = 0;

	if (!cgrp)
		return 0;

	if (copy_size > sizeof(subsystems))
		copy_size = sizeof(subsystems);
	if (bpf_probe_read_kernel(&subsystems, copy_size,
				  BPF_CORE_READ(cgrp, subsys)) < 0)
		return 0;

#pragma unroll
	for (int i = 0; i < CGROUP_SUBSYS_COUNT; i++) {
		struct cgroup_subsys_state *css = subsystems[i];

		if (css && BPF_CORE_READ(css, ss))
			subsystem_count++;
	}

#pragma unroll
	for (int i = 0; i < CGROUP_SUBSYS_COUNT; i++) {
		struct cgroup_subsys_state *css = subsystems[i];
		struct cgroup_subsys_id_event event = {};
		struct cgroup_subsys *ss;
		const char *name;

		if (!css)
			continue;

		ss = BPF_CORE_READ(css, ss);
		if (!ss)
			continue;

		event.cgroup = (u64)cgrp;
		event.subsystem_id = BPF_CORE_READ(ss, id);
		event.subsystem_count = subsystem_count;

		name = BPF_CORE_READ(ss, name);
		if (bpf_probe_read_kernel_str(&event.subsystem_name,
					      sizeof(event.subsystem_name),
					      name) <= 0)
			continue;

		bpf_perf_event_output(ctx, &cgroup_subsys_id_events,
				      COMPAT_BPF_F_CURRENT_CPU, &event,
				      sizeof(event));
	}

	return 0;
}
