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
