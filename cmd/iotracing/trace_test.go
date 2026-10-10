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

// Tests preserve useful observations when a capture stops before its deadline.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/pkg/types"
)

var _ bpf.PerfEventReader = (*readerErrorAfterEvent)(nil)

type readerErrorAfterEvent struct {
	bpf.PerfEventReader
	readErr error
	read    bool
}

func (r *readerErrorAfterEvent) ReadInto(data any) error {
	if r.read {
		return r.readErr
	}

	r.read = true
	event := data.(*bpfScheduleDelay)
	event.TGID = 1
	event.TID = 2
	event.CPU = 3
	event.DurationNS = 2500
	copy(event.Comm[:], "init")
	return nil
}

func TestCollectStallsReturnsEventsBeforeReaderError(t *testing.T) {
	readerErr := errors.New("reader failed")
	stalls, err := collectStalls(
		&readerErrorAfterEvent{readErr: readerErr},
		1,
	)

	if !errors.Is(err, readerErr) {
		t.Fatalf("collectStalls() error = %v, want %v", err, readerErr)
	}
	if len(stalls) != 1 {
		t.Fatalf("collectStalls() returned %d stalls, want 1", len(stalls))
	}
	if stalls[0].PID != 1 || stalls[0].Comm != "init" ||
		stalls[0].ScheduleLatencyUS != 2 || stalls[0].TID != 2 || stalls[0].CPU != 3 {
		t.Fatalf("collectStalls() stall = %+v", stalls[0])
	}
}

type readerWithSampleLoss struct {
	bpf.PerfEventReader
	reads int
}

func (r *readerWithSampleLoss) ReadInto(data any) error {
	r.reads++
	switch r.reads {
	case 1:
		return bpf.ErrPerfEventSamplesLost
	case 2:
		event := data.(*bpfScheduleDelay)
		event.TGID = 7
		event.DurationNS = 5000
		return nil
	default:
		return types.ErrExitByCancelCtx
	}
}

func TestCollectStallsContinuesAfterSampleLoss(t *testing.T) {
	reader := &readerWithSampleLoss{}
	stalls, err := collectStalls(reader, 1)
	if err != nil || len(stalls) != 1 || stalls[0].PID != 7 ||
		stalls[0].ScheduleLatencyUS != 5 || reader.reads != 3 {
		t.Fatalf("sample loss ended capture: stalls=%+v, reads=%d, error=%v", stalls, reader.reads, err)
	}
}

type reportTestBPF struct {
	bpf.BPF
	t        *testing.T
	rows     []bpf.MapItem
	detached bool
}

func (b *reportTestBPF) Detach() error {
	b.detached = true
	return nil
}

func (b *reportTestBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	b.t.Helper()
	if !b.detached || name != bpfSourceMapName {
		b.t.Fatalf("map dump before detach or unexpected map: %s", name)
	}
	return b.rows, nil
}

type failingReportOutput struct{ err error }

func (w failingReportOutput) Write([]byte) (int, error) { return 0, w.err }

func TestCollectReportWritesPartialOutput(t *testing.T) {
	readerErr := errors.New("reader failed")
	outputErr := errors.New("output failed")
	for _, test := range []struct {
		name       string
		beforeRead bool
		readErr    error
		wantStalls int
		wantReason string
	}{
		{"error before first event", true, readerErr, 0, types.IOTracingFailureReader},
		{"error after event", false, readerErr, 1, types.IOTracingFailureReader},
		{"normal cancellation", false, types.ErrExitByCancelCtx, 1, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var row bytes.Buffer
			record := bpfFilesystemIO{TGID: 1, BlockWriteBytes: 4096}
			if err := binary.Write(&row, binary.LittleEndian, &record); err != nil {
				t.Fatal(err)
			}
			object := &reportTestBPF{t: t, rows: []bpf.MapItem{{Value: row.Bytes()}}}
			report, traceErr := collectReport(object, &readerErrorAfterEvent{
				read: test.beforeRead, readErr: test.readErr,
			}, ioConfig{durationSecond: 1, maxProcess: 1, maxFilesPerProcess: 1, maxStack: 2})
			var output bytes.Buffer
			err := writeReport(&jsonWriter{w: &output}, report, traceErr)
			if test.wantReason != "" {
				if !errors.Is(err, readerErr) {
					t.Fatalf("output error = %v, want reader failure", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var got types.IOTracingSnapshot
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatalf("decode actual output: %v", err)
			}
			if got.FailureReason != test.wantReason || len(got.StallStacks) != test.wantStalls {
				t.Fatalf("partial report = %+v", got)
			}
			if len(got.Processes) != 1 || got.Processes[0].TotalDiskWriteBps != 4096 {
				t.Fatalf("completed process IO lost: %+v", got.Processes)
			}
			err = writeReport(&jsonWriter{w: failingReportOutput{outputErr}}, report, traceErr)
			if !errors.Is(err, outputErr) || (traceErr != nil && !errors.Is(err, readerErr)) {
				t.Fatalf("combined output/reader error = %v", err)
			}
		})
	}
}
