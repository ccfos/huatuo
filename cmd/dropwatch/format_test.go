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
	"errors"
	"io"
	"net"
	"testing"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

// errWriter always fails Write with the configured error. Used to verify that
// the dropwatch writers propagate IO errors instead of swallowing them.
type errWriter struct{ err error }

func (w errWriter) Write(_ []byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestTextWriterFormatsAllEventFields(t *testing.T) {
	var output bytes.Buffer
	w := &textWriter{w: &output}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: "2026-08-04T01:02:03.456789Z",
		DropSource:        dropSourceSoftware,
		DropReason:        "SKB_DROP_REASON_TCP_CSUM",
		DropLocation:      "0xffffffff81000000",
		Source:            "tools",
		Comm:              "worker thread",
		PID:               1420,
		NetdevName:        "eth0",
		PacketSkbAddr:     "0xffff888012345678",
		PacketLenBytes:    1500,
		Layers: &packet.Packet{
			Label: "IPv4/TCP",
			IPv4: &packet.IPv4{
				Saddr: net.IPv4(10, 0, 0, 1),
				Daddr: net.IPv4(10, 0, 0, 2),
			},
			TCP: &packet.TCP{
				Sport:   12345,
				Dport:   443,
				Seq:     123,
				AckSeq:  456,
				Flags:   "ACK|PSH",
				Window:  4096,
				SkState: "ESTABLISHED",
			},
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := "2026-08-04T01:02:03.456789Z " +
		"IPv4/TCP 10.0.0.1:12345 > 10.0.0.2:443 [ACK|PSH] seq=123 ack=456 win=4096 sk=ESTABLISHED " +
		"reason=SKB_DROP_REASON_TCP_CSUM drop_source=software drop_location=0xffffffff81000000 " +
		"len=1500 dev=eth0 pid=1420[worker thread] " +
		"addr=0xffff888012345678 source=tools\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestFormatHardwareEvent(t *testing.T) {
	ev := abi.DropwatchPacketEvent{}
	ev.Meta.DropSource = uint32(abi.DropwatchDropSourceHardware)
	ev.Meta.SKBAddr = 0xffff888012345678
	copy(ev.Meta.TrapName[:], "ingress_vlan_filter")
	copy(ev.Meta.TrapGroupName[:], "l2_drops")

	got := formatEvent(&ev, nil, "tools")
	if got.DropSource != dropSourceHardware {
		t.Errorf("DropSource = %q, want %q", got.DropSource, dropSourceHardware)
	}
	if got.DropReason != "ingress_vlan_filter" {
		t.Errorf("DropReason = %q, want ingress_vlan_filter", got.DropReason)
	}
	if got.DropReasonGroup != "l2_drops" {
		t.Errorf("DropReasonGroup = %q, want l2_drops", got.DropReasonGroup)
	}
	if got.PacketSkbAddr != "0xffff888012345678" {
		t.Errorf("PacketSkbAddr = %q, want 0xffff888012345678", got.PacketSkbAddr)
	}
	if got.DropLocation != "" {
		t.Errorf("DropLocation = %q, want empty", got.DropLocation)
	}
}

func TestTextWriterCombinesHardwareReasonGroup(t *testing.T) {
	var output bytes.Buffer
	w := &textWriter{w: &output}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: "now",
		DropSource:        dropSourceHardware,
		DropReason:        "ingress_vlan_filter",
		DropReasonGroup:   "l2_drops",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("reason=l2_drops/ingress_vlan_filter")) {
		t.Fatalf("output = %q, want combined hardware reason", output.String())
	}
}

func TestTextWriterPropagatesIOError(t *testing.T) {
	boom := errors.New("boom")
	w := &textWriter{w: errWriter{err: boom}}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: "now",
		NetdevName:        "eth0",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want %v", err, boom)
	}
}

func TestJSONWriterPropagatesIOError(t *testing.T) {
	boom := errors.New("boom")
	w := &jsonWriter{w: errWriter{err: boom}}

	err := w.Write(&types.DropWatchTracing{ObservedTimestamp: "now"})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want %v", err, boom)
	}
}

func TestWritersRejectShortWrites(t *testing.T) {
	t.Parallel()

	event := &types.DropWatchTracing{ObservedTimestamp: "now"}
	if err := (&textWriter{w: shortWriter{}}).Write(event); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("text writer error = %v, want %v", err, io.ErrShortWrite)
	}
	if err := (&jsonWriter{w: shortWriter{}}).Write(event); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("JSON writer error = %v, want %v", err, io.ErrShortWrite)
	}
}

func BenchmarkTextWriter(b *testing.B) {
	event := &types.DropWatchTracing{
		ObservedTimestamp: "2026-08-04T01:02:03.456789Z",
		DropSource:        dropSourceHardware,
		DropReason:        "ingress_vlan_filter",
		DropReasonGroup:   "l2_drops",
		PacketLenBytes:    1500,
		NetdevName:        "eth0",
	}
	w := &textWriter{w: io.Discard}

	b.ReportAllocs()
	for b.Loop() {
		if err := w.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}
