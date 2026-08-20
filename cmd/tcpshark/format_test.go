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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/types"
)

type errWriter struct{ err error }

func (w errWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	return len(p) / 2, nil
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

			event := formatEvent(&abi.TCPRetransmitEvent{
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

			event := formatEvent(&abi.TCPRetransmitEvent{
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

	event := formatEvent(&abi.TCPRetransmitEvent{
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

	event := formatEvent(&abi.TCPRetransmitEvent{
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

			event := formatEvent(tt.ev, toolstream.SourceTypeTool)
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

	event := formatEvent(&abi.TCPRetransmitEvent{
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

			event := formatEvent(tt.ev, toolstream.SourceTypeTool)
			if event.TCPSaddr != tt.wantSaddr {
				t.Fatalf("TCPSaddr = %q, want %q", event.TCPSaddr, tt.wantSaddr)
			}
			if event.TCPDaddr != tt.wantDaddr {
				t.Fatalf("TCPDaddr = %q, want %q", event.TCPDaddr, tt.wantDaddr)
			}
		})
	}
}

func TestTextWriterFormatsTCPFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   *types.TCPRetransmitTracing
		want string
	}{
		{
			name: "skb flags",
			ev: &types.TCPRetransmitTracing{
				ObservedTimestamp: "now",
				TCPFlags:          "ACK|PSH",
			},
			want: " flags=ACK|PSH ",
		},
		{
			name: "synack flags",
			ev: &types.TCPRetransmitTracing{
				ObservedTimestamp: "now",
				EventType:         "tcp_retransmit_synack",
				TCPFlags:          "SYN|ACK",
			},
			want: " flags=SYN|ACK ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			w := &textWriter{w: &buf}

			if err := w.Write(tt.ev); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := buf.String(); !strings.Contains(got, tt.want) {
				t.Fatalf("output = %q, want rendered TCP flags %q", got, tt.want)
			}
		})
	}
}

func TestTextWriterFormatsAllEventFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   *types.TCPRetransmitTracing
		want string
	}{
		{
			name: "full event",
			ev: &types.TCPRetransmitTracing{
				ObservedTimestamp:   "2026-07-23T02:14:40.304775546Z",
				TCPReason:           "RTO",
				Source:              toolstream.SourceTypeTool,
				Comm:                "worker thread",
				PID:                 1420,
				ContainerID:         "container-1",
				MemoryCgroupCSSAddr: "0xffff888012345678",
				NetNamespaceCookie:  2,
				NetNamespaceInum:    4026531992,
				TCPState:            "ESTABLISHED",
				TCPSaddr:            "127.0.0.1",
				TCPDaddr:            "127.0.0.1",
				TCPSport:            19996,
				TCPDport:            42128,
				Phase:               "data",
				EventType:           "tcp_retransmit_skb",
				CaState:             4,
				IcskRetransmits:     4,
				IcskPending:         1,
				ReordSeen:           2,
				DsackDups:           3,
				TCPSeq:              3154974646,
				TCPAckSeq:           948393597,
				TCPEndSeq:           3154991030,
				TCPFlags:            "ACK|PSH",
				SkbAddr:             "0xffff931c14fdf800",
				DropLocation:        "host_software",
			},
			want: "2026-07-23T02:14:40.304775546Z " +
				"[data/RTO] 127.0.0.1:19996 > 127.0.0.1:42128 " +
				"state=ESTABLISHED event_type=tcp_retransmit_skb " +
				"skb=0xffff931c14fdf800 seq=3154974646 end=3154991030 " +
				"ack=948393597 flags=ACK|PSH pid=1420 comm=worker thread " +
				"ca=4 retrans=4 icsk_pending=1 reord_seen=2 dsack_dups=3 " +
				"container_id=container-1 memory_cgroup_css_addr=0xffff888012345678 net_namespace_cookie=2 " +
				"net_namespace_inum=4026531992 drop_location=host_software " +
				"source=tools\n",
		},
		{
			name: "omitempty fields",
			ev: &types.TCPRetransmitTracing{
				ObservedTimestamp: "2026-07-23T02:14:40Z",
				TCPReason:         "RTO",
				TCPState:          "ESTABLISHED",
				TCPSaddr:          "127.0.0.1",
				TCPDaddr:          "127.0.0.1",
				TCPSport:          19996,
				TCPDport:          42128,
				Phase:             "data",
				EventType:         "tcp_retransmit_skb",
			},
			want: "2026-07-23T02:14:40Z " +
				"[data/RTO] 127.0.0.1:19996 > 127.0.0.1:42128 " +
				"state=ESTABLISHED event_type=tcp_retransmit_skb " +
				"seq=0 ack=0 pid=0 comm= ca=0 retrans=0 icsk_pending=0\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := (&textWriter{w: &output}).Write(tt.ev); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := output.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextWriterPropagatesIOError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	w := &textWriter{w: errWriter{err: boom}}

	err := w.Write(&types.TCPRetransmitTracing{ObservedTimestamp: "now"})
	if !errors.Is(err, boom) {
		t.Fatalf("Write() error = %v, want %v", err, boom)
	}
}

func TestJSONWriterPropagatesIOError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	w := &jsonWriter{w: errWriter{err: boom}}

	err := w.Write(&types.TCPRetransmitTracing{ObservedTimestamp: "now"})
	if !errors.Is(err, boom) {
		t.Fatalf("Write() error = %v, want %v", err, boom)
	}
}

func TestWritersDetectShortWrites(t *testing.T) {
	t.Parallel()

	event := &types.TCPRetransmitTracing{ObservedTimestamp: "now"}
	writers := []writer{
		&textWriter{w: shortWriter{}},
		&jsonWriter{w: shortWriter{}},
	}
	for _, output := range writers {
		if err := output.Write(event); !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("Write() error = %v, want %v", err, io.ErrShortWrite)
		}
	}
}

func TestJSONWriterWritesNDJSON(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	w := &jsonWriter{w: &output}
	event := &types.TCPRetransmitTracing{
		ObservedTimestamp: "2026-08-05T00:00:00Z",
		TCPReason:         "RTO",
	}
	if err := w.Write(event); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	encoded := output.String()
	if !strings.HasSuffix(encoded, "\n") || strings.Count(encoded, "\n") != 1 {
		t.Fatalf("output = %q, want one newline-terminated JSON object", encoded)
	}
	var got types.TCPRetransmitTracing
	if err := json.Unmarshal([]byte(strings.TrimSuffix(encoded, "\n")), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.ObservedTimestamp != event.ObservedTimestamp || got.TCPReason != event.TCPReason {
		t.Fatalf("decoded event = %+v, want timestamp %q and reason %q", got, event.ObservedTimestamp, event.TCPReason)
	}
}

func TestNewWriterUsesInjectedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		outputFormat string
		wantType     string
	}{
		{name: "text", outputFormat: outputText, wantType: "text"},
		{name: "json", outputFormat: outputJSON, wantType: "json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			got, cleanup, err := newWriter(&output, &writerOptions{outputFormat: tt.outputFormat})
			if err != nil {
				t.Fatalf("newWriter() error = %v", err)
			}
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup() error = %v", err)
			}

			switch tt.wantType {
			case "text":
				if _, ok := got.(*textWriter); !ok {
					t.Fatalf("newWriter() type = %T, want *textWriter", got)
				}
			case "json":
				if _, ok := got.(*jsonWriter); !ok {
					t.Fatalf("newWriter() type = %T, want *jsonWriter", got)
				}
			}
		})
	}
}

func TestNewWriterRejectsInvalidOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  io.Writer
		options writerOptions
		wantErr string
	}{
		{
			name:    "nil output",
			options: writerOptions{outputFormat: outputText},
			wantErr: "output is nil",
		},
		{
			name:    "unsupported format",
			output:  io.Discard,
			options: writerOptions{outputFormat: "yaml"},
			wantErr: `unsupported output "yaml"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := newWriter(tt.output, &tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("newWriter() error = %v, want containing %q", err, tt.wantErr)
			}
		})
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

			event := formatEvent(&tt.event, toolstream.SourceTypeTool)
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

func BenchmarkTextWriter(b *testing.B) {
	event := benchmarkEvent()
	w := &textWriter{w: io.Discard}

	b.ReportAllocs()
	for b.Loop() {
		if err := w.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatEvent(b *testing.B) {
	event := abi.TCPRetransmitEvent{
		EventType: uint8(abi.TCPRetransmitEventSKB),
		State:     unix.BPF_TCP_ESTABLISHED,
		TCPFlags:  tcpFlagACK,
		CaState:   uint8(abi.TCPRetransmitCaRecovery),
		Family:    unix.AF_INET,
	}

	b.ReportAllocs()
	var formatted *types.TCPRetransmitTracing
	for b.Loop() {
		formatted = formatEvent(&event, toolstream.SourceTypeTool)
	}
	_ = formatted
}

func BenchmarkJSONWriter(b *testing.B) {
	event := benchmarkEvent()
	w := &jsonWriter{w: io.Discard}

	b.ReportAllocs()
	for b.Loop() {
		if err := w.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkEvent() *types.TCPRetransmitTracing {
	return &types.TCPRetransmitTracing{
		ObservedTimestamp:   "2026-07-23T02:14:40.304775546Z",
		TCPReason:           "RTO",
		Source:              toolstream.SourceTypeTool,
		Comm:                "worker",
		PID:                 1420,
		ContainerID:         "container-1",
		MemoryCgroupCSSAddr: "0xffff888012345678",
		NetNamespaceCookie:  2,
		NetNamespaceInum:    4026531992,
		TCPState:            "ESTABLISHED",
		TCPSaddr:            "127.0.0.1",
		TCPDaddr:            "127.0.0.1",
		TCPSport:            19996,
		TCPDport:            42128,
		Phase:               "data",
		EventType:           "tcp_retransmit_skb",
		CaState:             4,
		IcskRetransmits:     4,
		IcskPending:         1,
		ReordSeen:           2,
		DsackDups:           3,
		TCPSeq:              3154974646,
		TCPAckSeq:           948393597,
		TCPEndSeq:           3154991030,
		TCPFlags:            "ACK|PSH",
		SkbAddr:             "0xffff931c14fdf800",
		DropLocation:        "host_software",
	}
}
