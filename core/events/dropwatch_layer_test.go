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

package events

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"huatuo-bamai/pkg/types"
)

func TestClassifyDropLayer(t *testing.T) {
	cases := []struct {
		name  string
		stack string
		want  string
	}{
		// protocol stack drops — resolved kallsyms frame "fn+offset/size"
		{"tcp v4 receive", "kfree_skb_reason+0x40/0x60\ntcp_v4_do_rcv+0x1a2/0x3b0\ntcp_v4_rcv+0x99/0xc0", types.DropLayerProtocol},
		{"tcp established", "__kfree_skb+0x10/0x20\ntcp_rcv_established+0x55/0x110", types.DropLayerProtocol},
		{"udp queue", "kfree_skb_reason+0x40/0x60\nudp_queue_rcv_one_skb+0x77/0x200", types.DropLayerProtocol},
		{"ip receive", "kfree_skb+0x10/0x20\nip_rcv+0x33/0x90", types.DropLayerProtocol},
		{"ipv6 receive", "kfree_skb_reason+0x40/0x60\nipv6_rcv+0x21/0x80", types.DropLayerProtocol},
		{"icmpv6", "__kfree_skb+0x10/0x20\nicmpv6_rcv+0x12/0x60", types.DropLayerProtocol},
		{"arp", "kfree_skb_reason+0x40/0x60\narp_rcv+0x9/0x40", types.DropLayerProtocol},
		{"socket filter", "kfree_skb+0x10/0x20\nsk_filter_trim_cap+0x44/0x120", types.DropLayerProtocol},
		{"sock queue rcv", "kfree_skb_reason+0x40/0x60\nsock_queue_rcv_skb+0x5/0x50", types.DropLayerProtocol},
		{"netfilter hook", "kfree_skb_reason+0x40/0x60\nnf_hook_slow+0x80/0xf0", types.DropLayerProtocol},

		// driver / link-layer ingress
		{"netif receive core", "kfree_skb_reason+0x40/0x60\n__netif_receive_skb_core+0x200/0x500", types.DropLayerDriver},
		{"netif receive skb", "kfree_skb+0x10/0x20\nnetif_receive_skb+0x10/0x40", types.DropLayerDriver},
		{"napi gro receive", "kfree_skb_reason+0x40/0x60\nnapi_gro_receive+0x77/0x1b0", types.DropLayerDriver},
		{"qdisc direct xmit", "kfree_skb_reason+0x40/0x60\nsch_direct_xmit+0x120/0x200", types.DropLayerDriver},

		// unknown — unclassifiable drop location
		{"driver private func", "kfree_skb_reason+0x40/0x60\nmlx5e_rx_reporter+0x5/0x30", types.DropLayerUnknown},
		{"arbitrary func", "kfree_skb+0x10/0x20\nsome_other_kernel_func+0x1/0x2", types.DropLayerUnknown},

		// module-annotated frames
		{"tcp with module", "kfree_skb_reason+0x40/0x60\ntcp_v4_rcv+0x99/0xc0 [kernel]", types.DropLayerProtocol},

		// raw-address (unresolved) frame form "fn/hexaddr", as produced when
		// kallsyms cannot resolve the symbol.
		{"raw addr protocol", "kfree_skb/ffffffff963047b0\ntcp_v4_rcv/ffffffff96310000", types.DropLayerProtocol},
		{"raw addr wrappers only", "kfree_skb/ffffffff963047b0\n__kfree_skb/ffffffff96304800", types.DropLayerUnknown},

		// degenerate inputs
		{"empty stack", "", types.DropLayerUnknown},
		{"blank lines", "\n  \n\t", types.DropLayerUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDropLayer(tc.stack)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("classifyDropLayer(%q) (-want +got):\n%s", tc.stack, diff)
			}
		})
	}
}

func TestFirstStackFunc(t *testing.T) {
	cases := []struct {
		stack string
		want  string
	}{
		{"kfree_skb_reason+0x40/0x60\ntcp_v4_do_rcv+0x1a2/0x3b0", "tcp_v4_do_rcv"},
		{"tcp_v4_rcv+0x99/0xc0 [kernel]", "tcp_v4_rcv"},
		{"kfree_skb/ffffffff963047b0", ""}, // wrapper only, nothing after
		{"", ""},
		{"  \n  \n", ""},
	}
	for _, tc := range cases {
		got := firstStackFunc(tc.stack)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("firstStackFunc(%q) (-want +got):\n%s", tc.stack, diff)
		}
	}
}
