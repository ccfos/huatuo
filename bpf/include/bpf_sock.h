#ifndef __BPF_SOCK_H__
#define __BPF_SOCK_H__

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

static __always_inline u8 skb_sk_state(struct sk_buff *skb)
{
	return BPF_CORE_READ(skb, sk, __sk_common.skc_state);
}

#endif
