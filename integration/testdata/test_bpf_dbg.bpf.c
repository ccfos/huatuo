// Fixture for integration/test_bpf_debug_macro.sh. Compiled twice via
// build/clang.sh — once with -DDEBUG_BPF, once without — to verify that
// bpf/include/bpf_dbg.h gates code emission as documented. Not referenced
// by any //go:generate, so the main build never sees it.
//
// Uses the project's vmlinux.h (not <linux/bpf.h>) because bpf_dbg.h
// pulls in bpf_common.h which uses kernel types like u32 that live in
// vmlinux.h. This also keeps the fixture aligned with how production
// BPF objects are built, so it exercises the real include path.

#include "vmlinux.h"

#include <bpf/bpf_helpers.h>

#include "bpf_dbg.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// BASE marker: plain .rodata literal, present in BOTH builds. Sanity
// check — if missing, the test harness itself is broken and the DEBUG
// marker assertions would be meaningless.
const volatile char huatuo_bpf_dbg_base_marker[]
	SEC(".rodata") = "HUATUO_BPF_DBG_BASE_MARKER_V1";

BPF_DBG_MAP(probe);

// DEBUG marker: literal argument to bpf_dbg_msg(). With -DDEBUG_BPF it
// is emitted as a .rodata string the macro memcpy's into the event;
// without it the macro collapses to (void)(msg_) and the literal is
// dropped entirely. Marker is long and unique so `strings | grep -F`
// cannot collide with libbpf or path-name substrings.
SEC("raw_tracepoint/sys_enter")
int huatuo_bpf_dbg_probe(void *ctx)
{
	bpf_dbg_msg(ctx, probe, "HUATUO_BPF_DBG_DEBUG_MARKER_V1");
	return 0;
}
