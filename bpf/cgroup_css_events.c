#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_cgroup_css.h"

char __license[] SEC("license") = "GPL";

SEC("raw_tracepoint/cgroup_mkdir")
int bpf_cgroup_mkdir_prog(struct bpf_raw_tracepoint_args *ctx)
{
	return submit_cgroup_css_event(ctx, (void *)ctx->args[0],
				       CGROUP_CSS_OPERATION_UPDATE);
}

SEC("raw_tracepoint/cgroup_rmdir")
int bpf_cgroup_rmdir_prog(struct bpf_raw_tracepoint_args *ctx)
{
	return submit_cgroup_css_event(ctx, (void *)ctx->args[0],
				       CGROUP_CSS_OPERATION_REMOVE);
}

/* CPU-only v2 cgroups may have no memory.current notification file. */
SEC("kprobe/cpu_weight_nice_read_s64")
int bpf_cgroup_cpu_weight_nice_prog(struct pt_regs *ctx)
{
	struct cgroup_subsys_state *css = (void *)PT_REGS_PARM1(ctx);

	return submit_cgroup_css_event(ctx, BPF_CORE_READ(css, cgroup),
				       CGROUP_CSS_OPERATION_UPDATE);
}
