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

package retransmit

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestDropEventFromRecord(t *testing.T) {
	record := newIPv4DropwatchTCPRecord(40)
	record.Meta.KernelObservedNS = 100
	record.Meta.NetNamespaceCookie = 200
	record.Meta.NetNamespaceInum = 300
	record.StackSize = 16
	record.Stack[0], record.Stack[1] = 0x1000, 0x2000

	event, err := dropEventFromRecord(record, nil)
	if err != nil {
		t.Fatalf("dropEventFromRecord() error = %v", err)
	}
	if event.kernelObservedNS != 100 ||
		event.namespace != (namespaceID{cookie: 200, inode: 300}) {
		t.Fatalf("scalar mapping = %+v", event)
	}
	if event.flow != testFlowKey(12345, 80) || event.sequence != 123 ||
		event.endSequence != 123 || event.tcpFlags != packet.TCPFlagACK {
		t.Fatalf("normalized packet = %+v", event)
	}
	if event.stackDepth != 2 || event.stackPCs[0] != 0x1000 ||
		event.stackPCs[1] != 0x2000 {
		t.Fatalf("stack = depth %d pcs %x", event.stackDepth, event.stackPCs[:2])
	}
}

func TestDropEventFromRecordUsesIPLengthForSequenceSpan(t *testing.T) {
	tests := []struct {
		name            string
		record          *abi.DropwatchPacketEvent
		wantEndSequence uint32
		wantIPv6        bool
	}{
		{name: "ipv4", record: newIPv4DropwatchTCPRecord(40), wantEndSequence: 123},
		{name: "ipv6", record: newIPv6DropwatchTCPRecord(20), wantEndSequence: 123, wantIPv6: true},
		{name: "ipv4 payload", record: newIPv4DropwatchTCPRecord(4040), wantEndSequence: 4123},
		{name: "ipv6 payload", record: newIPv6DropwatchTCPRecord(4020), wantEndSequence: 4123, wantIPv6: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.record.Meta.KernelObservedNS = 1
			test.record.Meta.NetNamespaceCookie = 1
			event, err := dropEventFromRecord(test.record, nil)
			if err != nil {
				t.Fatalf("dropEventFromRecord() error = %v", err)
			}
			if event.endSequence != test.wantEndSequence {
				t.Fatalf(
					"end sequence = %d, want %d",
					event.endSequence,
					test.wantEndSequence,
				)
			}
			if event.flow.source.Addr().Is6() != test.wantIPv6 {
				t.Fatalf(
					"source address = %s, want IPv6 %t",
					event.flow.source.Addr(),
					test.wantIPv6,
				)
			}
		})
	}
}

func TestDropEventFromRecordUsesRawFlags(t *testing.T) {
	const flags = packet.TCPFlagSYN | packet.TCPFlagFIN

	record := newIPv4DropwatchTCPRecord(50)
	record.Meta.KernelObservedNS = 1
	record.Meta.NetNamespaceCookie = 1
	record.PktHdr.Raw[33] = flags

	event, err := dropEventFromRecord(record, nil)
	if err != nil {
		t.Fatalf("dropEventFromRecord() error = %v", err)
	}
	if event.endSequence != 135 || event.tcpFlags != flags {
		t.Fatalf(
			"normalized packet = (end %d, flags %#x), want (135, 0x3)",
			event.endSequence,
			event.tcpFlags,
		)
	}
}

func TestDropEventFromRecordParseErrorKeepsScalars(t *testing.T) {
	record := &abi.DropwatchPacketEvent{}
	record.Meta.KernelObservedNS = 100
	record.Meta.NetNamespaceCookie = 200
	record.PktHdr.RawLen = 1

	event, err := dropEventFromRecord(record, nil)
	if err == nil {
		t.Fatal("dropEventFromRecord() error = nil, want parse error")
	}
	if event == nil || event.kernelObservedNS != 100 ||
		event.namespace != (namespaceID{cookie: 200}) {
		t.Fatalf("scalar event = %+v", event)
	}
	if event.flow.source.Addr().IsValid() || event.flow.destination.Addr().IsValid() {
		t.Fatalf("flow = %+v, want invalid zero value", event.flow)
	}
}

func TestDropEventFromRecordRejectsNil(t *testing.T) {
	event, err := dropEventFromRecord(nil, nil)
	if err == nil || event != nil {
		t.Fatalf("dropEventFromRecord(nil, nil) = (%+v, %v), want nil and error", event, err)
	}
}

func TestDropEventFromRecordRejectsZeroKtime(t *testing.T) {
	record := newIPv4DropwatchTCPRecord(40)

	event, err := dropEventFromRecord(record, nil)
	if err == nil || event != nil {
		t.Fatalf("dropEventFromRecord() = (%+v, %v), want nil and error", event, err)
	}
}

func newIPv4DropwatchTCPRecord(
	ipTotalLength uint16,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.Meta.DropSource = uint32(abi.DropwatchDropSourceSoftware)
	record.PktHdr.EthProto = 0x0800
	record.PktHdr.RawLen = 40
	record.PktHdr.PacketLenBytes = uint32(ipTotalLength)
	record.PktHdr.Raw[0] = 0x45
	binary.BigEndian.PutUint16(record.PktHdr.Raw[2:], ipTotalLength)
	record.PktHdr.Raw[8] = 64
	record.PktHdr.Raw[9] = 6
	record.PktHdr.Raw[12], record.PktHdr.Raw[15] = 10, 1
	record.PktHdr.Raw[16], record.PktHdr.Raw[19] = 10, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[20:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[22:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[24:], 123)
	record.PktHdr.Raw[32] = 0x50
	record.PktHdr.Raw[33] = packet.TCPFlagACK
	return record
}

func BenchmarkDropEvidenceDecode(b *testing.B) {
	record := newIPv4DropwatchTCPRecord(1500)
	record.Meta.KernelObservedNS = 100
	record.Meta.NetNamespaceCookie = 1
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dropEventFromRecord(record, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func newIPv6DropwatchTCPRecord(
	ipPayloadLength uint16,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.Meta.DropSource = uint32(abi.DropwatchDropSourceSoftware)
	record.PktHdr.EthProto = 0x86dd
	record.PktHdr.RawLen = 60
	record.PktHdr.PacketLenBytes = uint32(ipPayloadLength) + 40
	record.PktHdr.Raw[0] = 0x60
	binary.BigEndian.PutUint16(record.PktHdr.Raw[4:], ipPayloadLength)
	record.PktHdr.Raw[6] = 6
	record.PktHdr.Raw[7] = 64
	record.PktHdr.Raw[8], record.PktHdr.Raw[23] = 0x20, 1
	record.PktHdr.Raw[24], record.PktHdr.Raw[39] = 0x20, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[40:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[42:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[44:], 123)
	record.PktHdr.Raw[52] = 0x50
	record.PktHdr.Raw[53] = packet.TCPFlagACK
	return record
}

func TestTracingPreservesCaptureTimeAndOwnsOutput(t *testing.T) {
	event := testRetransmitEvent(42, "192.0.2.1", "192.0.2.2", 100, 200, 10, 20)
	before := *event
	first, err := event.tracing("tools")
	if err != nil {
		t.Fatal(err)
	}
	first.DropLocation = "software"
	second, err := event.tracing("tools")
	if err != nil {
		t.Fatal(err)
	}
	if *event != before || first == second || second.DropLocation != "" {
		t.Fatal("formatting did not isolate capture data and output")
	}
	if !second.ObservedTimestamp.Equal(event.observedAt) ||
		second.Source != "tools" {
		t.Fatalf("output capture metadata = %+v", second)
	}
}

func TestFormatEventSkbAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		skbAddr uint64
		want    string
		omitted bool
	}{
		{
			name:    "zero pointer",
			omitted: true,
		},
		{
			name:    "kernel pointer",
			skbAddr: 0xffff888012345678,
			want:    "0xffff888012345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
				SKBAddr:   tt.skbAddr,
				EventType: uint8(abi.TCPRetransmitEventSKB),
				Family:    unix.AF_INET,
			}, toolstream.SourceTypeTool)
			if event.SkbAddr != tt.want {
				t.Fatalf("SkbAddr = %q, want %q", event.SkbAddr, tt.want)
			}

			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			fields := map[string]any{}
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			got, present := fields["skb_addr"]
			if tt.omitted && present {
				t.Fatalf("skb_addr = %v, want omitted", got)
			}
			if !tt.omitted && (!present || got != tt.want) {
				t.Fatalf("skb_addr = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEventMemoryCgroupCSSAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    uint64
		want    string
		omitted bool
	}{
		{
			name:    "zero pointer",
			omitted: true,
		},
		{
			name: "kernel pointer",
			addr: 0xffff888012345678,
			want: "0xffff888012345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
				MemcgCSSAddr: tt.addr,
				EventType:    uint8(abi.TCPRetransmitEventSKB),
				Family:       unix.AF_INET,
			}, toolstream.SourceTypeTool)
			if event.MemoryCgroupCSSAddr != tt.want {
				t.Fatalf("MemoryCgroupCSSAddr = %q, want %q", event.MemoryCgroupCSSAddr, tt.want)
			}

			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			fields := map[string]any{}
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			got, present := fields["memory_cgroup_css_addr"]
			if tt.omitted && present {
				t.Fatalf("memory_cgroup_css_addr = %v, want omitted", got)
			}
			if !tt.omitted && (!present || got != tt.want) {
				t.Fatalf("memory_cgroup_css_addr = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEventNetNamespaceIDs(t *testing.T) {
	t.Parallel()

	event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
		NetNamespaceCookie: 0x2000,
		NetNamespaceInum:   4026531992,
		EventType:          uint8(abi.TCPRetransmitEventSKB),
		Family:             unix.AF_INET,
	}, toolstream.SourceTypeTool)
	if event.NetNamespaceCookie != 0x2000 {
		t.Fatalf("NetNamespaceCookie = %d, want %d", event.NetNamespaceCookie, uint64(0x2000))
	}
	if event.NetNamespaceInum != 4026531992 {
		t.Fatalf("NetNamespaceInum = %d, want %d", event.NetNamespaceInum, uint32(4026531992))
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := fields["net_namespace_cookie"]; got != float64(0x2000) {
		t.Fatalf("net_namespace_cookie = %v, want %d", got, uint64(0x2000))
	}
	if got := fields["net_namespace_inum"]; got != float64(4026531992) {
		t.Fatalf("net_namespace_inum = %v, want %d", got, uint32(4026531992))
	}
}

func TestFormatEventSource(t *testing.T) {
	t.Parallel()

	event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
		EventType: uint8(abi.TCPRetransmitEventSKB),
		Family:    unix.AF_INET,
	}, toolstream.SourceTypeTool)
	if event.Source != toolstream.SourceTypeTool {
		t.Fatalf("Source = %q, want %q", event.Source, toolstream.SourceTypeTool)
	}
}

func TestFormatEventTCPFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   *abi.TCPRetransmitEvent
		want string
	}{
		{
			name: "skb flags",
			ev: &abi.TCPRetransmitEvent{
				EventType: uint8(abi.TCPRetransmitEventSKB),
				Family:    unix.AF_INET,
				TCPFlags:  0x18,
			},
			want: "ACK|PSH",
		},
		{
			name: "synack flags derived from event type",
			ev: &abi.TCPRetransmitEvent{
				EventType: uint8(abi.TCPRetransmitEventSynack),
				Family:    unix.AF_INET,
			},
			want: "SYN|ACK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := mustRetransmitEvent(t, tt.ev, toolstream.SourceTypeTool)
			if event.TCPFlags != tt.want {
				t.Fatalf("TCPFlags = %q, want %q", event.TCPFlags, tt.want)
			}

			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			fields := map[string]any{}
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := fields["tcp_flags"]; got != tt.want {
				t.Fatalf("tcp_flags = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEventTLP(t *testing.T) {
	t.Parallel()

	event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
		EventType: uint8(abi.TCPRetransmitEventTlp),
		Family:    unix.AF_INET,
		TCPSeq:    123,
		TCPAck:    100,
	}, toolstream.SourceTypeTool)

	if event.EventType != "tcp_send_loss_probe" {
		t.Errorf("EventType = %q, want tcp_send_loss_probe", event.EventType)
	}
	if event.Phase != "data" {
		t.Errorf("Phase = %q, want data", event.Phase)
	}
	if event.TCPReason != "TLP" {
		t.Errorf("TCPReason = %q, want TLP", event.TCPReason)
	}
	if event.TCPSeq != 123 || event.TCPAckSeq != 100 {
		t.Errorf("sequence fields = (%d, %d), want (123, 100)", event.TCPSeq, event.TCPAckSeq)
	}
}

func TestFormatEventAddresses(t *testing.T) {
	t.Parallel()

	ipv6Saddr := net.ParseIP("2001:db8::1").To16()
	ipv6Daddr := net.ParseIP("2001:db8::2").To16()

	tests := []struct {
		name      string
		ev        *abi.TCPRetransmitEvent
		wantSaddr string
		wantDaddr string
	}{
		{
			name: "ipv4 uses first four bytes",
			ev: &abi.TCPRetransmitEvent{
				EventType: uint8(abi.TCPRetransmitEventSKB),
				Family:    unix.AF_INET,
				Saddr:     [16]byte{127, 0, 0, 1, 0xff},
				Daddr:     [16]byte{10, 0, 0, 1, 0xff},
			},
			wantSaddr: "127.0.0.1",
			wantDaddr: "10.0.0.1",
		},
		{
			name: "ipv6 uses full sixteen bytes",
			ev: &abi.TCPRetransmitEvent{
				EventType: uint8(abi.TCPRetransmitEventSKB),
				Family:    unix.AF_INET6,
			},
			wantSaddr: "2001:db8::1",
			wantDaddr: "2001:db8::2",
		},
	}

	copy(tests[1].ev.Saddr[:], ipv6Saddr)
	copy(tests[1].ev.Daddr[:], ipv6Daddr)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := mustRetransmitEvent(t, tt.ev, toolstream.SourceTypeTool)
			if event.TCPSaddr != tt.wantSaddr {
				t.Fatalf("TCPSaddr = %q, want %q", event.TCPSaddr, tt.wantSaddr)
			}
			if event.TCPDaddr != tt.wantDaddr {
				t.Fatalf("TCPDaddr = %q, want %q", event.TCPDaddr, tt.wantDaddr)
			}
		})
	}
}

func TestFormatEventKernelObservation(t *testing.T) {
	t.Parallel()

	event := mustRetransmitEvent(t, &abi.TCPRetransmitEvent{
		KernelObservedNS: 42,
		EventType:        uint8(abi.TCPRetransmitEventSKB),
		Family:           unix.AF_INET,
		TCPFlags:         0x18,
	}, toolstream.SourceTypeTool)
	if event.KernelObservedNS != 42 {
		t.Fatalf("KernelObservedNS = %d, want 42", event.KernelObservedNS)
	}
	if event.TCPFlagsRaw != 0x18 {
		t.Fatalf("TCPFlagsRaw = 0x%02x, want 0x18", event.TCPFlagsRaw)
	}
}

func TestFormatEventHandlesUnknownABIValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		event         abi.TCPRetransmitEvent
		wantEventType string
		wantSaddr     string
		wantDaddr     string
	}{
		{
			name: "unknown event type",
			event: abi.TCPRetransmitEvent{
				EventType: 99,
				Family:    unix.AF_INET,
			},
			wantEventType: "unknown",
			wantSaddr:     "0.0.0.0",
			wantDaddr:     "0.0.0.0",
		},
		{
			name: "unknown address family",
			event: abi.TCPRetransmitEvent{
				EventType: uint8(abi.TCPRetransmitEventSKB),
				Family:    unix.AF_UNSPEC,
			},
			wantEventType: "tcp_retransmit_skb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := mustRetransmitEvent(t, &tt.event, toolstream.SourceTypeTool)
			if event.EventType != tt.wantEventType {
				t.Fatalf("EventType = %q, want %q", event.EventType, tt.wantEventType)
			}
			if event.TCPSaddr != tt.wantSaddr {
				t.Fatalf("TCPSaddr = %q, want %q", event.TCPSaddr, tt.wantSaddr)
			}
			if event.TCPDaddr != tt.wantDaddr {
				t.Fatalf("TCPDaddr = %q, want %q", event.TCPDaddr, tt.wantDaddr)
			}
		})
	}
}

func BenchmarkFormatEvent(b *testing.B) {
	monotonicNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		b.Fatal(err)
	}
	event := retransmitEvent{record: abi.TCPRetransmitEvent{
		KernelObservedNS: monotonicNS,
		EventType:        uint8(abi.TCPRetransmitEventSKB),
		State:            unix.BPF_TCP_ESTABLISHED,
		TCPFlags:         packet.TCPFlagACK,
		CaState:          uint8(abi.TCPRetransmitCaRecovery),
		Family:           unix.AF_INET,
	}}

	b.ReportAllocs()
	var formatted *types.TCPRetransmitTracing
	for b.Loop() {
		event.observedAt = time.Now()
		formatted, err = event.tracing(toolstream.SourceTypeTool)
		if err != nil {
			b.Fatal(err)
		}
	}
	_ = formatted
}

func mustRetransmitEvent(t *testing.T, record *abi.TCPRetransmitEvent, sourceType string) *types.TCPRetransmitTracing {
	t.Helper()
	event := retransmitEvent{record: *record, observedAt: time.Now()}
	tracing, err := event.tracing(sourceType)
	if err != nil {
		t.Fatal(err)
	}
	return tracing
}

func TestKernelObservationUsesEventTime(t *testing.T) {
	monotonicNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatal(err)
	}
	if monotonicNS < uint64(time.Second) {
		t.Skip("host has been up for less than one second")
	}
	record := abi.TCPRetransmitEvent{KernelObservedNS: monotonicNS - uint64(time.Second)}
	capture := retransmitEvent{record: record, observedAt: time.Now()}
	event, err := capture.tracing("tools")
	if err != nil {
		t.Fatal(err)
	}
	if event.KernelObservedTimestamp == nil {
		t.Fatal("kernel observation timestamp is missing")
	}
	kernel := event.KernelObservedTimestamp.Time
	observed := event.ObservedTimestamp
	age := observed.Sub(kernel)
	if age < 900*time.Millisecond || age > 2*time.Second {
		t.Fatalf("kernel-to-userspace delay = %v, expected about one second", age)
	}
}
