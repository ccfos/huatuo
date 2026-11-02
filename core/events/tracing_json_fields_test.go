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

package events

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestEventJSONFieldNames(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected []string
	}{
		{
			name: "scheduler tick",
			value: SchedTickTracingData{
				TickIntervalNS:          20,
				TickIntervalThresholdNS: 10,
			},
			expected: []string{"tick_interval_ns", "tick_interval_threshold_ns"},
		},
		{
			name: "memory reclaim",
			value: MemoryReclaimTracingData{
				PID:               10,
				TID:               11,
				ReclaimDurationNS: 20,
			},
			expected: []string{"pid", "tid", "reclaim_duration_ns"},
		},
		{
			name:     "hung task",
			value:    HungTaskTracerData{TID: 11},
			expected: []string{"tid"},
		},
		{
			name:     "oom actor",
			value:    OOMActor{PID: 10},
			expected: []string{"pid"},
		},
		{
			name: "network receive latency",
			value: NetTracingData{
				LatencyMS:          20,
				LatencyThresholdMS: 10,
				PacketLenBytes:     64,
			},
			expected: []string{
				"latency_stage",
				"latency_ms",
				"latency_threshold_ms",
				"packet_len_bytes",
				"net_namespace_inum",
				"net_namespace_cookie",
			},
		},
		{
			name:     "dropwatch",
			value:    types.DropWatchTracing{PacketLenBytes: 64},
			expected: []string{"packet_len_bytes"},
		},
		{
			name:     "ras",
			value:    RasTracingData{Device: "cpu"},
			expected: []string{"dev"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(tt.value)
			require.NoError(t, err)

			fields := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal(encoded, &fields))
			for _, name := range tt.expected {
				assert.Contains(t, fields, name)
			}
		})
	}
}

// rasAcpiPayload mirrors struct trace_event_raw_non_standard_event as
// bpf/system_ras.c reads it. Production declares the same layout inside
// buildRasAcpiTracerData, so the test restates the wire format here.
type rasAcpiPayload struct {
	Pad          uint64
	SecType      [16]uint8
	FRUID        [16]uint8
	FRUTxtOffset uint32
	Sev          uint8
	Pattern      [3]uint8
	Len          uint32
	BufOffset    uint32
	Msg          [DETAIL_INFO_SIZE_ACPI]byte
}

type rasAcpiInfo struct {
	Severity uint8  `json:"severity"`
	SecType  string `json:"sec_type"`
	FRUID    string `json:"fru_id"`
	FRUText  string `json:"fru_text"`
	DataLen  uint32 `json:"data_len"`
	RawData  string `json:"raw_data"`
}

// rasAcpiRecord builds one non-standard ACPI record the way the kernel emits
// it: the fixed payload followed by the copied dynamic window.
func rasAcpiRecord(t *testing.T, msg []byte, fruTxtOffset, bufOffset, sectionLen uint32) rasEvent {
	t.Helper()
	require.LessOrEqual(t, len(msg), DETAIL_INFO_SIZE_ACPI)

	payload := rasAcpiPayload{
		FRUTxtOffset: fruTxtOffset,
		Sev:          2,
		Len:          sectionLen,
		BufOffset:    bufOffset,
	}
	copy(payload.Msg[:], msg)

	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, payload))

	var ev rasEvent
	ev.Type = HW_ERR_ACPI_GHES
	ev.KernelObservedNS = uint64(5 * time.Second)
	copy(ev.Info[:], buf.Bytes())
	return ev
}

// TestBuildRasAcpiTracerDataRawWindow is the regression for the reported panic
// and its follow-up: raw_data used the FRU text offset and trusted the
// kernel-reported section length. The tracepoint keeps __data_loc_fru_text and
// __data_loc_buf as separate fields, and only the copied part of the fixed
// window is payload; the rest is padding.
func TestBuildRasAcpiTracerDataRawWindow(t *testing.T) {
	t.Parallel()

	const (
		base      = 56 // fixed portion of trace_event_raw_non_standard_event
		fruOffset = base + 8
		bufOffset = base + 32
		captured  = 48
	)

	msg := make([]byte, DETAIL_INFO_SIZE_ACPI)
	for i := 0; i < captured; i++ {
		msg[i] = 0xAA
	}
	copy(msg[fruOffset-base:], "DIMM_A1\x00")

	raw := func(n int) string { return fmt.Sprintf("% x", msg[bufOffset-base:bufOffset-base+n]) }

	tests := []struct {
		name       string
		fruTxt     uint32
		bufLoc     uint32
		sectionLen uint32
		wantText   string
		wantRaw    string
	}{
		{
			name:       "raw data starts at BufOffset, not at the FRU text",
			fruTxt:     fruOffset,
			bufLoc:     bufOffset | (16 << 16),
			sectionLen: 16,
			wantText:   "DIMM_A1",
			wantRaw:    raw(16),
		},
		{
			name:       "missing FRU text still keeps the raw data",
			fruTxt:     0,
			bufLoc:     bufOffset | (8 << 16),
			sectionLen: 8,
			wantText:   "",
			wantRaw:    raw(8),
		},
		{
			name:       "section length shorter than the capture wins",
			fruTxt:     fruOffset,
			bufLoc:     bufOffset | (32 << 16),
			sectionLen: 4,
			wantText:   "DIMM_A1",
			wantRaw:    raw(4),
		},
		{
			name:       "section length past the window is clamped",
			fruTxt:     fruOffset,
			bufLoc:     bufOffset | (32 << 16),
			sectionLen: 1 << 20,
			wantText:   "DIMM_A1",
			wantRaw:    raw(32),
		},
		{
			name:       "zero padding in the fixed window is not payload",
			fruTxt:     fruOffset,
			bufLoc:     bufOffset | (4 << 16),
			sectionLen: uint32(DETAIL_INFO_SIZE_ACPI),
			wantText:   "DIMM_A1",
			wantRaw:    raw(4),
		},
		{
			name:       "BufOffset past the window yields no raw data",
			fruTxt:     fruOffset,
			bufLoc:     uint32(base+DETAIL_INFO_SIZE_ACPI) | (8 << 16),
			sectionLen: 8,
			wantText:   "DIMM_A1",
			wantRaw:    "",
		},
		{
			name:       "BufOffset below the base yields no raw data",
			fruTxt:     fruOffset,
			bufLoc:     (base - 4) | (8 << 16),
			sectionLen: 8,
			wantText:   "DIMM_A1",
			wantRaw:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := rasAcpiRecord(t, msg, tc.fruTxt, tc.bufLoc, tc.sectionLen)

			var (
				td  *RasTracingData
				err error
			)
			require.NotPanics(t, func() {
				td, err = dispatchRasTracerData(&ev)
			}, "a malformed hardware record must not panic")
			require.NoError(t, err)
			require.NotNil(t, td)

			var info rasAcpiInfo
			require.NoError(t, json.Unmarshal([]byte(td.Info), &info))
			assert.Equal(t, tc.wantText, info.FRUText)
			assert.Equal(t, tc.wantRaw, info.RawData)
			assert.Equal(t, tc.sectionLen, info.DataLen,
				"data_len must keep reporting the raw kernel length even when truncated")
		})
	}
}

// TestDynamicWindow pins the clamping contract of the untrusted-range helper:
// offsets are 16-bit __data_loc values, offsets outside the copied window
// yield nil, and lengths never wrap or slice past the end.
func TestDynamicWindow(t *testing.T) {
	t.Parallel()

	dyn := []byte("0123456789")

	tests := []struct {
		name      string
		dyn       []byte
		rawOffset uint32
		base      uint32
		length    uint32
		wantNil   bool
		want      string
	}{
		{name: "offset at base honours length", dyn: dyn, rawOffset: 8, base: 4, length: 4, want: "4567"},
		{name: "length beyond the window is clamped", dyn: dyn, rawOffset: 8, base: 4, length: 100, want: "456789"},
		{name: "wrapped length does not overflow", dyn: dyn, rawOffset: 8, base: 4, length: ^uint32(0), want: "456789"},
		{name: "offset below base yields nil", dyn: dyn, rawOffset: 3, base: 4, length: 1, wantNil: true},
		{name: "offset at the end yields nil", dyn: dyn, rawOffset: 14, base: 4, length: 1, wantNil: true},
		{name: "offset past the window yields nil", dyn: dyn, rawOffset: 0xffff, base: 4, length: 1, wantNil: true},
		{name: "high bits of the offset are masked", dyn: dyn, rawOffset: 0x10008, base: 4, length: 2, want: "45"},
		{name: "zero length yields an empty window", dyn: dyn, rawOffset: 8, base: 4, length: 0, want: ""},
		{name: "empty source yields nil", dyn: nil, rawOffset: 8, base: 4, length: 4, wantNil: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := dynamicWindow(tc.dyn, tc.rawOffset, tc.base, tc.length)
			if tc.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}
