#ifndef __BPF_PCAP_STUB_H__
#define __BPF_PCAP_STUB_H__

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

#include "bpf_common.h"
#include "vmlinux_net.h"

/*
 * pcap_stub_l{2,3}: reserved-NOP stub functions patched at load time by
 * internal/pcapfilter with compiled tcpdump filter bytecode. The three
 * ctx parameters reserve R1/R2/R3; the patched bytecode overwrites them.
 * Unpatched body is a pass-through: data != data_end && three-way ctx
 * equality both hold, so the stub always returns true.
 *
 * Each .c file that includes this header gets its own private copies of
 * both symbols (STB_LOCAL via static), patched independently per ELF.
 */
static __noinline bool pcap_stub_l3(void *_ctx, void *__ctx, void *___ctx,
				    void *data, void *data_end)
{
	/*
	 * Bind the five parameters to R1..R5 and route them through the NOP
	 * region as "+r" in/out operands. This creates a real data dependency
	 * that forces clang to (1) emit the comparison below AFTER the region
	 * and (2) read its operands from R1..R5 — exactly the registers the
	 * spliced filter leaves set (R4=verdict, R5=0, R1=R2=R3=0 via the
	 * fall-through epilogue in internal/pcapfilter/bpf_filter.go).
	 *
	 * Without these constraints clang is free to schedule the comparison
	 * before the region: clang-12 does, stashing the result in callee-saved
	 * registers and then letting the region's `r0 = 0` NOPs clobber the
	 * return value, so the filter verdict is silently dropped. Do not
	 * "simplify" the register pinning away.
     *
	 */
	register void *a1 asm("r1") = _ctx;
	register void *a2 asm("r2") = __ctx;
	register void *a3 asm("r3") = ___ctx;
	register void *a4 asm("r4") = data;
	register void *a5 asm("r5") = data_end;
	asm volatile(".rept 512\n\t"
		     ".byte 0xb7, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00\n\t"
		     ".endr\n\t"
		     : "+r"(a1), "+r"(a2), "+r"(a3), "+r"(a4), "+r"(a5)
		     :: "r0");
	return a4 != a5 && a1 == a2 && a2 == a3;
}

static __noinline bool pcap_stub_l2(void *_ctx, void *__ctx, void *___ctx,
				    void *data, void *data_end)
{
	/*
	 * Register-pinned NOP region; see pcap_stub_l3 above for why the
	 * parameters are bound to R1..R5 and fed through the asm as "+r"
	 * operands. Do not "simplify" the register pinning away.
	 *
	 */
	register void *a1 asm("r1") = _ctx;
	register void *a2 asm("r2") = __ctx;
	register void *a3 asm("r3") = ___ctx;
	register void *a4 asm("r4") = data;
	register void *a5 asm("r5") = data_end;
	asm volatile(".rept 512\n\t"
		     ".byte 0xb7, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00\n\t"
		     ".endr\n\t"
		     : "+r"(a1), "+r"(a2), "+r"(a3), "+r"(a4), "+r"(a5)
		     :: "r0");
	return a4 != a5 && a1 == a2 && a2 == a3;
}

/*
 * PCAP_STUB_PASS_SKB(skb): dispatch L2/L3 stub based on skb->mac_len.
 * Returns true if the filter accepts the packet (or no filter is injected),
 * false if the packet should be dropped.
 *
 * Usage:
 *	if (!PCAP_STUB_PASS_SKB(skb))
 *		return 0;
 */
#define PCAP_STUB_PASS_SKB(skb) ({                                          \
	void *__head    = BPF_CORE_READ((skb), head);                       \
	void *__pkt_end = __head + (u64)BPF_CORE_READ((skb), tail);         \
	void *__l3      = skb_network_header(skb);                          \
	void *__l2      = skb_mac_header(skb);                              \
	bool __pass;                                                        \
	if (BPF_CORE_READ((skb), mac_len) == 0)                             \
		__pass = pcap_stub_l3((skb), (skb), (skb), __l3, __pkt_end);\
	else                                                                \
		__pass = pcap_stub_l2((skb), (skb), (skb), __l2, __pkt_end);\
	__pass;                                                             \
})

#endif /* __BPF_PCAP_STUB_H__ */
