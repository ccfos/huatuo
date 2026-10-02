#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_cgroup_css.h"

char __license[] SEC("license") = "GPL";

SEC("kprobe/cgroup_clone_children_read_or_memory_current_read")
int bpf_cgroup_subsys_state_prog(struct pt_regs *ctx)
{
	struct cgroup_subsys_state *css = (void *)PT_REGS_PARM1(ctx);
	struct cgroup *cgrp		= BPF_CORE_READ(css, cgroup);

	return submit_cgroup_css_event(ctx, cgrp, CGROUP_CSS_OPERATION_UPDATE);
}
