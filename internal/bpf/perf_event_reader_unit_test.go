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

package bpf

import (
	"encoding/binary"
	"testing"

	"github.com/cilium/ebpf/perf"
	"github.com/stretchr/testify/require"
)

func TestPerfEventSamplesLostError(t *testing.T) {
	t.Parallel()

	err := &PerfEventSamplesLostError{Count: 7}
	require.ErrorIs(t, err, ErrPerfEventSamplesLost)

	var lostErr *PerfEventSamplesLostError
	require.ErrorAs(t, err, &lostErr)
	require.Equal(t, uint64(7), lostErr.Count)
}

func TestDecodePerfEvent(t *testing.T) {
	t.Parallel()

	type event struct {
		PID   uint32
		Value uint64
	}

	sample := make([]byte, 12)
	binary.NativeEndian.PutUint32(sample, 42)
	binary.NativeEndian.PutUint64(sample[4:], 99)

	var got event
	require.NoError(t, decodePerfEvent(sample, &got))
	require.Equal(t, event{PID: 42, Value: 99}, got)
	require.Error(t, decodePerfEvent(sample[:4], &got))
}

func BenchmarkDecodePerfEvent(b *testing.B) {
	type event struct {
		PID   uint32
		Value uint64
	}

	sample := make([]byte, 12)
	binary.NativeEndian.PutUint32(sample, 42)
	binary.NativeEndian.PutUint64(sample[4:], 99)

	b.ReportAllocs()
	for b.Loop() {
		var dst event
		if err := decodePerfEvent(sample, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNormalizePerfEventReaderOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      PerfEventReaderOptions
		pageSize  int
		wantBytes int
		wantErr   string
	}{
		{
			name: "one page",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 1,
				WatermarkBytes:    1,
			},
			pageSize:  4096,
			wantBytes: 4096,
		},
		{
			name: "round pages to power of two",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 4097,
				WatermarkBytes:    4096,
			},
			pageSize:  4096,
			wantBytes: 8192,
		},
		{
			name: "already normalized",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 524288,
				WatermarkBytes:    8192,
			},
			pageSize:  4096,
			wantBytes: 524288,
		},
		{
			name: "missing buffer",
			opts: PerfEventReaderOptions{
				WatermarkBytes: 1,
			},
			pageSize: 4096,
			wantErr:  "buffer size must be positive",
		},
		{
			name: "missing watermark",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 4096,
			},
			pageSize: 4096,
			wantErr:  "watermark must be positive",
		},
		{
			name: "watermark equals effective capacity",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 4097,
				WatermarkBytes:    8192,
			},
			pageSize: 4096,
			wantErr:  "must be smaller",
		},
		{
			name: "invalid page size",
			opts: PerfEventReaderOptions{
				PerCPUBufferBytes: 4096,
				WatermarkBytes:    1,
			},
			wantErr: "invalid page size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizePerfEventReaderOptions(tt.opts, tt.pageSize)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantBytes, got)
		})
	}
}

func TestSetPerfEventRawRecord(t *testing.T) {
	t.Parallel()

	t.Run("sample", func(t *testing.T) {
		t.Parallel()

		sample := []byte{1, 2, 3, 4, 5, 6, 7, 8}
		source := &perf.Record{CPU: 3, RawSample: sample, Remaining: 96}
		var got PerfEventRawRecord
		setPerfEventRawRecord(&got, source)

		require.Equal(t, 3, got.CPU)
		require.Equal(t, sample, got.RawSample)
		require.Zero(t, got.LostSamples)
		require.Equal(t, 96, got.RemainingBytes)
		require.Equal(t, 20, got.PerfRecordSize)
	})

	t.Run("lost", func(t *testing.T) {
		t.Parallel()

		source := &perf.Record{CPU: 7, LostSamples: 11, Remaining: 48}
		var got PerfEventRawRecord
		setPerfEventRawRecord(&got, source)

		require.Equal(t, 7, got.CPU)
		require.Empty(t, got.RawSample)
		require.Equal(t, uint64(11), got.LostSamples)
		require.Equal(t, 48, got.RemainingBytes)
		require.Equal(t, perfEventLostRecordSize, got.PerfRecordSize)
	})
}

func BenchmarkSetPerfEventRawRecord(b *testing.B) {
	source := &perf.Record{CPU: 3, RawSample: make([]byte, 1048), Remaining: 96}
	var dst PerfEventRawRecord

	b.ReportAllocs()
	for b.Loop() {
		setPerfEventRawRecord(&dst, source)
	}
}
