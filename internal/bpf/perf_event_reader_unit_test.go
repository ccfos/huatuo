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
