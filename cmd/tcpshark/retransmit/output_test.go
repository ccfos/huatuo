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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"

	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

type errWriter struct{ err error }

func (w errWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	return len(p) / 2, nil
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
				ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
				TCPFlags:          "ACK|PSH",
			},
			want: " flags=ACK|PSH ",
		},
		{
			name: "synack flags",
			ev: &types.TCPRetransmitTracing{
				ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
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

func TestTextWriterFormatsCorrelation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	event := &types.TCPRetransmitTracing{
		ObservedTimestamp:       timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
		KernelObservedNS:        8,
		KernelObservedTimestamp: &timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 0, time.UTC)},
		DropLocation:            "unknown",
		CorrelationReasons: []types.CorrelationReason{
			types.CorrelationReasonStartupHistoryIncomplete,
			types.CorrelationReasonPerfEventsLost,
		},
		DropwatchPerfStatus: &types.DropwatchStatus{
			PerfLost:    2,
			LostSamples: 4,
			RateLimited: 3,
		},
	}
	if err := (&textWriter{w: &output}).Write(event); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	for _, want := range []string{
		"kernel_observed_timestamp=2026-07-23T02:14:40.000000000Z",
		"drop_location=unknown",
		"reason=startup_history_incomplete,perf_events_lost",
		"dropwatch_perf_lost=2",
		"dropwatch_lost_samples=4",
		"dropwatch_rate_limited=3",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestTextWriterFormatsMatchedDropStack(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	event := &types.TCPRetransmitTracing{
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
		DropLocation:      "host_software",
		DropStack:         "first\nsecond",
	}
	if err := (&textWriter{w: &output}).Write(event); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	for _, want := range []string{
		"drop_location=host_software",
		"\t#0   first\n",
		"\t#1   second\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output = %q, want %q", output.String(), want)
		}
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
				ObservedTimestamp:       timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 304775546, time.UTC)},
				KernelObservedNS:        123456789,
				KernelObservedTimestamp: &timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 0, time.UTC)},
				TCPReason:               "RTO",
				Source:                  toolstream.SourceTypeTool,
				Comm:                    "worker thread",
				PID:                     1420,
				ContainerID:             "container-1",
				MemoryCgroupCSSAddr:     "0xffff888012345678",
				NetNamespaceCookie:      2,
				NetNamespaceInum:        4026531992,
				TCPState:                "ESTABLISHED",
				TCPSaddr:                "127.0.0.1",
				TCPDaddr:                "127.0.0.1",
				TCPSport:                19996,
				TCPDport:                42128,
				Phase:                   "data",
				EventType:               "tcp_retransmit_skb",
				CaState:                 4,
				IcskRetransmits:         4,
				IcskPending:             1,
				ReordSeen:               2,
				DsackDups:               3,
				TCPSeq:                  3154974646,
				TCPAckSeq:               948393597,
				TCPEndSeq:               3154991030,
				TCPFlags:                "ACK|PSH",
				SkbAddr:                 "0xffff931c14fdf800",
				DropLocation:            "host_software",
			},
			want: "2026-07-23T02:14:40.304775546Z " +
				"[data/RTO] 127.0.0.1:19996 > 127.0.0.1:42128 " +
				"state=ESTABLISHED event_type=tcp_retransmit_skb kernel_observed_timestamp=2026-07-23T02:14:40.000000000Z " +
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
				ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 0, time.UTC)},
				TCPReason:         "RTO",
				TCPState:          "ESTABLISHED",
				TCPSaddr:          "127.0.0.1",
				TCPDaddr:          "127.0.0.1",
				TCPSport:          19996,
				TCPDport:          42128,
				Phase:             "data",
				EventType:         "tcp_retransmit_skb",
			},
			want: "2026-07-23T02:14:40.000000000Z " +
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

	err := w.Write(&types.TCPRetransmitTracing{ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}})
	if !errors.Is(err, boom) {
		t.Fatalf("Write() error = %v, want %v", err, boom)
	}
}

func TestJSONWriterPropagatesIOError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	w := &jsonWriter{w: errWriter{err: boom}}

	err := w.Write(&types.TCPRetransmitTracing{ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}})
	if !errors.Is(err, boom) {
		t.Fatalf("Write() error = %v, want %v", err, boom)
	}
}

func TestWritersDetectShortWrites(t *testing.T) {
	t.Parallel()

	event := &types.TCPRetransmitTracing{ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}}
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
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)},
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
	if !got.ObservedTimestamp.Equal(event.ObservedTimestamp.Time) || got.TCPReason != event.TCPReason {
		t.Fatalf("decoded event = %+v, want timestamp %q and reason %q", got, event.ObservedTimestamp.FormatUTC(), event.TCPReason)
	}
}

func TestNewWriterUsesInjectedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		outputFormat string
		wantType     string
	}{
		{name: "text", outputFormat: OutputText, wantType: "text"},
		{name: "json", outputFormat: OutputJSON, wantType: "json"},
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
			options: writerOptions{outputFormat: OutputText},
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
		ObservedTimestamp:       timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 304775546, time.UTC)},
		KernelObservedTimestamp: &timeutil.Timestamp{Time: time.Date(2026, 7, 23, 2, 14, 40, 304000000, time.UTC)},
		TCPReason:               "RTO",
		Source:                  toolstream.SourceTypeTool,
		Comm:                    "worker",
		PID:                     1420,
		ContainerID:             "container-1",
		MemoryCgroupCSSAddr:     "0xffff888012345678",
		NetNamespaceCookie:      2,
		NetNamespaceInum:        4026531992,
		TCPState:                "ESTABLISHED",
		TCPSaddr:                "127.0.0.1",
		TCPDaddr:                "127.0.0.1",
		TCPSport:                19996,
		TCPDport:                42128,
		Phase:                   "data",
		EventType:               "tcp_retransmit_skb",
		CaState:                 4,
		IcskRetransmits:         4,
		IcskPending:             1,
		ReordSeen:               2,
		DsackDups:               3,
		TCPSeq:                  3154974646,
		TCPAckSeq:               948393597,
		TCPEndSeq:               3154991030,
		TCPFlags:                "ACK|PSH",
		SkbAddr:                 "0xffff931c14fdf800",
		DropLocation:            "host_software",
	}
}
