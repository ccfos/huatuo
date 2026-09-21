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

package dropwatch

import (
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

func TestResolveMetadata(t *testing.T) {
	names := ReasonNames{5: "SKB_DROP_REASON_TCP_CSUM"}
	tests := []struct {
		name   string
		source abi.DropwatchDropSource
		reason uint32
		trap   string
		group  string
		want   Metadata
	}{
		{
			name: "software", source: abi.DropwatchDropSourceSoftware, reason: 5,
			want: Metadata{Source: SourceSoftware, Reason: "SKB_DROP_REASON_TCP_CSUM"},
		},
		{
			name: "numeric fallback", source: abi.DropwatchDropSourceSoftware, reason: 999,
			want: Metadata{Source: SourceSoftware, Reason: "999"},
		},
		{
			name: "unsupported kernel", source: abi.DropwatchDropSourceSoftware, reason: ^uint32(0),
			want: Metadata{Source: SourceSoftware, Reason: "NOT_SUPPORTED"},
		},
		{
			name: "hardware ignores software reason", source: abi.DropwatchDropSourceHardware, reason: 5,
			trap: "ingress_vlan_filter", group: "l2_drops",
			want: Metadata{Source: SourceHardware, Reason: "ingress_vlan_filter", ReasonGroup: "l2_drops"},
		},
		{
			name: "hardware missing trap", source: abi.DropwatchDropSourceHardware, reason: 5,
			want: Metadata{Source: SourceHardware},
		},
		{
			name: "software ignores trap", source: abi.DropwatchDropSourceSoftware, reason: 5,
			trap: "ingress_vlan_filter", group: "l2_drops",
			want: Metadata{Source: SourceSoftware, Reason: "SKB_DROP_REASON_TCP_CSUM"},
		},
		{
			name: "unknown source does not infer hardware", source: abi.DropwatchDropSourceUnknown, reason: 5,
			trap: "ingress_vlan_filter", group: "l2_drops",
			want: Metadata{Source: SourceUnknown, Reason: "SKB_DROP_REASON_TCP_CSUM"},
		},
		{
			name: "future source", source: abi.DropwatchDropSource(99), reason: 999,
			want: Metadata{Source: SourceUnknown, Reason: "999"},
		},
		{
			name: "terminated trap", source: abi.DropwatchDropSourceHardware,
			trap: "trap\x00ignored", group: "group\x00ignored",
			want: Metadata{Source: SourceHardware, Reason: "trap", ReasonGroup: "group"},
		},
		{
			name: "full trap name", source: abi.DropwatchDropSourceHardware,
			trap: strings.Repeat("x", len(abi.DropwatchPacketMeta{}.TrapName)),
			want: Metadata{Source: SourceHardware, Reason: strings.Repeat("x", len(abi.DropwatchPacketMeta{}.TrapName))},
		},
	}
	for i := range tests {
		test := &tests[i]
		t.Run(test.name, func(t *testing.T) {
			record := abi.DropwatchPacketMeta{DropSource: uint32(test.source), DropReason: test.reason}
			copy(record.TrapName[:], test.trap)
			copy(record.TrapGroupName[:], test.group)
			got := ResolveMetadata(&record, names)
			if got != test.want {
				t.Fatalf("ResolveMetadata() = %+v, want %+v", got, test.want)
			}
			clear(record.TrapName[:])
			clear(record.TrapGroupName[:])
			if got != test.want {
				t.Fatalf("record reuse changed metadata: %+v", got)
			}
		})
	}
}

func BenchmarkResolveMetadata(b *testing.B) {
	for _, source := range []abi.DropwatchDropSource{abi.DropwatchDropSourceSoftware, abi.DropwatchDropSourceHardware} {
		record := abi.DropwatchPacketMeta{DropSource: uint32(source), DropReason: 5}
		copy(record.TrapName[:], "ingress_vlan_filter")
		copy(record.TrapGroupName[:], "l2_drops")
		names := ReasonNames{5: "SKB_DROP_REASON_TCP_CSUM"}
		b.Run(ResolveMetadata(&record, names).Source, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = ResolveMetadata(&record, names)
			}
		})
	}
}
