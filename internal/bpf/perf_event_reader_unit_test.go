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

//go:build !didi

package bpf

import (
	"encoding/binary"
	"testing"

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

func TestPerfEventBufferCapacityAndDropCounters(t *testing.T) {
	t.Parallel()

	// Verify buffer capacities: default must be at least 64KB and burst at least 128KB to withstand spike bursts.
	require.GreaterOrEqual(t, DefaultPerfEventBufferBytes, 64*1024)
	require.GreaterOrEqual(t, BurstPerfEventBufferBytes, 128*1024)

	// Verify sample loss error unwrap and string formatting
	lostErr := &PerfEventSamplesLostError{Count: 42}
	require.Equal(t, "bpf: 42 perf event samples lost", lostErr.Error())
	require.ErrorIs(t, lostErr, ErrPerfEventSamplesLost)

	// Verify batch drop accounting
	batch := PerfEventBatch{
		Events:      []any{"event1", "event2"},
		LostSamples: 15,
	}
	require.Equal(t, 2, len(batch.Events))
	require.Equal(t, uint64(15), batch.LostSamples)

	// Verify mock reader tracking TotalLostSamples
	r := &perfEventReader{}
	require.Equal(t, uint64(0), r.TotalLostSamples())
	r.totalLostSamples.Add(10)
	require.Equal(t, uint64(10), r.TotalLostSamples())
	r.totalLostSamples.Add(32)
	require.Equal(t, uint64(42), r.TotalLostSamples())
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
