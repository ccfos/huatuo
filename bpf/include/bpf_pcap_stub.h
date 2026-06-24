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
	/* 512 × 8-byte NOP insns; must equal stubReservedInsns in internal/pcapfilter/elfpatch.go */
	asm volatile(".rept 512\n\t"
		     ".byte 0xb7, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00\n\t"
		     ".endr\n\t");
	return data != data_end && _ctx == __ctx && __ctx == ___ctx;
}

static __noinline bool pcap_stub_l2(void *_ctx, void *__ctx, void *___ctx,
				    void *data, void *data_end)
{
	/* 512 × 8-byte NOP insns; must equal stubReservedInsns in internal/pcapfilter/elfpatch.go */
	asm volatile(".rept 512\n\t"
		     ".byte 0xb7, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00\n\t"
		     ".endr\n\t");
	return data != data_end && _ctx == __ctx && __ctx == ___ctx;
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
