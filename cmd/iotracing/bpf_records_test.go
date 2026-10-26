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
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/assert"
)

func TestBPFFilesystemIOLayout(t *testing.T) {
	t.Parallel()

	var record bpfFilesystemIO

	assert.Equal(t, uintptr(376), unsafe.Sizeof(record))
	assert.Equal(t, uintptr(0), unsafe.Offsetof(record.TGID))
	assert.Equal(t, uintptr(4), unsafe.Offsetof(record.PathInitialized))
	assert.Equal(t, uintptr(8), unsafe.Offsetof(record.DevID))
	assert.Equal(t, uintptr(16), unsafe.Offsetof(record.FsWriteBytes))
	assert.Equal(t, uintptr(64), unsafe.Offsetof(record.Latency))
	assert.Equal(t, uintptr(104), unsafe.Offsetof(record.Comm))
	assert.Equal(t, uintptr(120), unsafe.Offsetof(record.PathSegs))
}

func TestBPFFilesystemIOPathName(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}

		assert.Empty(t, record.PathName())
	})

	t.Run("captured path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}
		copy(record.PathSegs[0][:], "io")
		copy(record.PathSegs[1][:], "huatuo-iotracing.test")
		copy(record.PathSegs[2][:], "/")

		assert.Equal(t, "/huatuo-iotracing.test/io", record.PathName())
	})
}

var _ bpf.PerfEventReader = (*reportTestReader)(nil)

type reportTestReader struct {
	bpf.PerfEventReader
	readErr       error
	hasRead       bool
	hasSampleLoss bool
}

func (r *reportTestReader) ReadInto(data any) error {
	if r.hasSampleLoss {
		r.hasSampleLoss = false
		return bpf.ErrPerfEventSamplesLost
	}
	if r.hasRead {
		return r.readErr
	}

	r.hasRead = true
	event := data.(*bpfScheduleDelay)
	event.TGID = 1
	event.TID = 2
	event.CPU = 3
	event.DurationNS = 2500
	copy(event.Comm[:], "init")
	return nil
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
		name          string
		beforeRead    bool
		hasSampleLoss bool
		readErr       error
		wantStalls    int
		wantReason    string
	}{
		{name: "error before first event", beforeRead: true, readErr: readerErr, wantReason: types.IOTracingFailureReader},
		{name: "error after event", readErr: readerErr, wantStalls: 1, wantReason: types.IOTracingFailureReader},
		{name: "normal cancellation", readErr: types.ErrExitByCancelCtx, wantStalls: 1},
		{name: "sample loss", hasSampleLoss: true, readErr: types.ErrExitByCancelCtx, wantStalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var row bytes.Buffer
			record := bpfFilesystemIO{
				TGID: 1, FsReadBytes: 8192, FsWriteBytes: 16384,
				BlockReadBytes: 2048, BlockWriteBytes: 4096,
			}
			if err := binary.Write(&row, binary.LittleEndian, &record); err != nil {
				t.Fatal(err)
			}
			object := &reportTestBPF{t: t, rows: []bpf.MapItem{{Value: row.Bytes()}}}
			const sampled = 250 * time.Millisecond
			started := time.Now().Add(-sampled)
			report, traceErr := collectReport(
				object,
				&reportTestReader{
					hasRead: test.beforeRead, hasSampleLoss: test.hasSampleLoss, readErr: test.readErr,
				},
				ioConfig{durationSecond: 10, maxProcess: 1, maxFilesPerProcess: 1, maxStack: 2},
				started,
			)
			elapsed := time.Since(started)
			var output bytes.Buffer
			err := writeReport(&jsonWriter{w: &output}, report, traceErr)
			if test.wantReason != "" {
				if !errors.Is(err, readerErr) {
					t.Fatalf("output error = %v, want reader failure", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
				t.Fatalf("decode actual output fields: %v", err)
			}
			if _, hasReason := fields["failure_reason"]; hasReason != (test.wantReason != "") {
				t.Fatalf("optional failure marker: %s", output.Bytes())
			}
			var got types.IOTracingSnapshot
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatalf("decode actual output: %v", err)
			}
			if got.FailureReason != test.wantReason || len(got.StallStacks) != test.wantStalls {
				t.Fatalf("partial report = %+v", got)
			}
			if test.wantStalls != 0 {
				stall := got.StallStacks[0]
				if stall.PID != 1 || stall.Comm != "init" || stall.ScheduleLatencyUS != 2 ||
					stall.TID != 2 || stall.CPU != 3 {
					t.Fatalf("reported stall = %+v", stall)
				}
			}
			if len(got.Processes) != 1 || len(got.Processes[0].TotalFiles) != 1 {
				t.Fatalf("completed process IO lost: %+v", got.Processes)
			}
			process := got.Processes[0]
			file := process.TotalFiles[0]
			for _, rate := range []struct {
				bytes, process, file uint64
			}{
				{bytes: record.FsReadBytes, process: process.TotalFsReadBps, file: file.FsReadBps},
				{bytes: record.FsWriteBytes, process: process.TotalFsWriteBps, file: file.FsWriteBps},
				{bytes: record.BlockReadBytes, process: process.TotalDiskReadBps, file: file.DiskReadBps},
				{bytes: record.BlockWriteBytes, process: process.TotalDiskWriteBps, file: file.DiskWriteBps},
			} {
				minimum := uint64(float64(rate.bytes) / elapsed.Seconds())
				maximum := uint64(float64(rate.bytes) / sampled.Seconds())
				if rate.process != rate.file || rate.process < minimum || rate.process > maximum {
					t.Fatalf("partial rate = %+v, want [%d, %d] B/s", rate, minimum, maximum)
				}
			}
			err = writeReport(&jsonWriter{w: failingReportOutput{outputErr}}, report, traceErr)
			if !errors.Is(err, outputErr) || (traceErr != nil && !errors.Is(err, readerErr)) {
				t.Fatalf("combined output/reader error = %v", err)
			}
		})
	}
}
