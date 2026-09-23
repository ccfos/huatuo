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
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/timeutil"
)

// acpiDynBase is the offset of the dynamic area inside
// struct trace_event_raw_non_standard_event: trace_entry(8) + sec_type(16) +
// fru_id(16) + __data_loc_fru_text(4) + sev(1) + 3 bytes of padding + len(4) +
// __data_loc_buf(4). The BPF probe copies the record from this offset on, so
// every dynamic descriptor is relative to it.
const acpiDynBase = 56

// acpiInfo mirrors the JSON payload emitted for a non-standard ACPI event.
type acpiInfo struct {
	Severity uint8  `json:"severity"`
	SecType  string `json:"sec_type"`
	FRUID    string `json:"fru_id"`
	FRUText  string `json:"fru_text"`
	DataLen  uint32 `json:"data_len"`
	RawData  string `json:"raw_data"`
}

// rasEventWithInfo builds a rasEvent whose Info field carries the raw
// tracepoint record, with a valid kernel timestamp so the builders under test
// can convert it.
func rasEventWithInfo(t *testing.T, typ uint32, info []byte) *rasEvent {
	t.Helper()

	ns, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatalf("read monotonic clock: %v", err)
	}

	ev := &rasEvent{Type: typ, KernelObservedNS: ns}
	if len(info) > len(ev.Info) {
		t.Fatalf("record is %d bytes, the buffer holds %d", len(info), len(ev.Info))
	}
	copy(ev.Info[:], info)
	return ev
}

// acpiEventWithDyn builds a HW_ERR_ACPI_GHES record. dyn is the captured
// dynamic area; it cannot exceed the DETAIL_INFO_SIZE_ACPI-byte window the BPF
// probe copies out of the tracepoint.
func acpiEventWithDyn(t *testing.T, sev uint8, fruTxtOffset, recordLen, bufOffset uint32, dyn []byte) *rasEvent {
	t.Helper()

	if len(dyn) > DETAIL_INFO_SIZE_ACPI {
		t.Fatalf("dynamic area is %d bytes, the probe captures only %d", len(dyn), DETAIL_INFO_SIZE_ACPI)
	}

	// Fixed-portion offsets, see bpf/include/vmlinux_x86.h:
	// 40 = __data_loc_fru_text, 44 = sev, 48 = len, 52 = __data_loc_buf.
	info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
	binary.LittleEndian.PutUint32(info[40:], fruTxtOffset)
	info[44] = sev
	binary.LittleEndian.PutUint32(info[48:], recordLen)
	binary.LittleEndian.PutUint32(info[52:], bufOffset)
	copy(info[acpiDynBase:], dyn)

	return rasEventWithInfo(t, HW_ERR_ACPI_GHES, info)
}

// decodeAcpiInfo unmarshals the structured Info payload of an ACPI event.
func decodeAcpiInfo(t *testing.T, data *RasTracingData) acpiInfo {
	t.Helper()

	var info acpiInfo
	if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
		t.Fatalf("decode ACPI info %q: %v", data.Info, err)
	}
	return info
}

func TestDynamicWindow(t *testing.T) {
	t.Parallel()

	const maxLen = ^uint32(0)
	window := []byte("0123456789")

	tests := []struct {
		name      string
		dyn       []byte
		rawOffset uint32
		base      uint32
		length    uint32
		want      string
	}{
		{name: "whole window", dyn: window, rawOffset: 0, base: 0, length: maxLen, want: "0123456789"},
		{name: "offset into window", dyn: window, rawOffset: 4, base: 0, length: maxLen, want: "456789"},
		{name: "offset equal to base", dyn: window, rawOffset: 4, base: 4, length: maxLen, want: "0123456789"},
		{name: "length clamped to remainder", dyn: window, rawOffset: 4, base: 0, length: 1000, want: "456789"},
		{name: "length shorter than remainder", dyn: window, rawOffset: 4, base: 0, length: 2, want: "45"},
		{name: "descriptor high bits ignored", dyn: window, rawOffset: 0xffff0000 | 4, base: 0, length: maxLen, want: "456789"},
		{name: "offset below base", dyn: window, rawOffset: 3, base: 4, length: maxLen, want: ""},
		{name: "offset at end of window", dyn: window, rawOffset: 10, base: 0, length: maxLen, want: ""},
		{name: "offset past window", dyn: window, rawOffset: 4096, base: 0, length: maxLen, want: ""},
		{name: "zero length", dyn: window, rawOffset: 4, base: 0, length: 0, want: ""},
		{name: "empty window", dyn: nil, rawOffset: 0, base: 0, length: 4, want: ""},
		{name: "empty window offset at base", dyn: []byte{}, rawOffset: 8, base: 8, length: maxLen, want: ""},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := dynamicWindow(tt.dyn, tt.rawOffset, tt.base, tt.length)
			if string(got) != tt.want {
				t.Fatalf("dynamicWindow(offset=%d, base=%d, len=%d) = %q, want %q", tt.rawOffset, tt.base, tt.length, got, tt.want)
			}
			// Clamping must never hand back bytes that were not captured.
			if len(got) > len(tt.dyn) {
				t.Fatalf("window of %d bytes exceeds the %d captured bytes", len(got), len(tt.dyn))
			}
		})
	}
}

func TestCstringClampsOffset(t *testing.T) {
	t.Parallel()

	dyn := []byte("fru-text\x00rest")

	tests := []struct {
		name      string
		rawOffset uint32
		base      uint32
		want      string
	}{
		{name: "in range", rawOffset: 56, base: 56, want: "fru-text"},
		{name: "descriptor high bits ignored", rawOffset: 0x00080000 | 56, base: 56, want: "fru-text"},
		{name: "offset below base", rawOffset: 55, base: 56, want: ""},
		{name: "offset at end of window", rawOffset: 56 + uint32(len(dyn)), base: 56, want: ""},
		{name: "offset far past window", rawOffset: 0xffff, base: 56, want: ""},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cstring(dyn, tt.rawOffset, tt.base); got != tt.want {
				t.Fatalf("cstring(offset=%d, base=%d) = %q, want %q", tt.rawOffset, tt.base, got, tt.want)
			}
		})
	}
}

func TestRasAcpiTracerDataTruncatesOversizedDynamicWindow(t *testing.T) {
	t.Parallel()

	// fru_text is the first dynamic element, so its descriptor points right at
	// the 56-byte base of the record.
	const fruText = "FRU-ACPI"
	// The kernel reports the real CPER section length, which for NVDIMM, CXL and
	// vendor sections is routinely far larger than the 456 bytes the probe
	// captures.
	const oversizedLen = 2048

	tests := []struct {
		name          string
		sev           uint8
		fruTxtOffset  uint32
		recordLen     uint32
		bufOffset     uint32
		dyn           []byte
		wantFRUText   string
		wantDataLen   uint32
		wantRawBytes  int
		wantRawPrefix string
	}{
		{name: "well formed", sev: 1, fruTxtOffset: acpiDynBase, recordLen: 12, bufOffset: acpiDynBase + 9, dyn: append([]byte(fruText+"\x00"), 0xa1, 0xb2, 0xc3, 0xd4), wantFRUText: fruText, wantDataLen: 12, wantRawBytes: 12, wantRawPrefix: "46 52 55 2d 41 43 50 49 00 a1 b2 c3"},
		{name: "len beyond captured window", sev: 2, fruTxtOffset: acpiDynBase, recordLen: oversizedLen, bufOffset: acpiDynBase, dyn: []byte(fruText + "\x00"), wantFRUText: fruText, wantDataLen: oversizedLen, wantRawBytes: DETAIL_INFO_SIZE_ACPI, wantRawPrefix: "46 52 55 2d 41 43 50 49 00"},
		{name: "offset beyond captured window", sev: 1, fruTxtOffset: 4096, recordLen: 16, bufOffset: 64, dyn: []byte(fruText + "\x00"), wantFRUText: "", wantDataLen: 16, wantRawBytes: 0},
		{name: "offset below fixed base", sev: 1, fruTxtOffset: 8, recordLen: 16, bufOffset: 64, dyn: []byte(fruText + "\x00"), wantFRUText: "", wantDataLen: 16, wantRawBytes: 0},
		{name: "zero length payload", sev: 0, fruTxtOffset: acpiDynBase, recordLen: 0, bufOffset: acpiDynBase, dyn: []byte(fruText + "\x00"), wantFRUText: fruText, wantDataLen: 0, wantRawBytes: 0},
		{name: "all descriptor bits set", sev: 0xff, fruTxtOffset: 0xffffffff, recordLen: 0xffffffff, bufOffset: 0xffffffff, dyn: []byte(fruText + "\x00"), wantFRUText: "", wantDataLen: 0xffffffff, wantRawBytes: 0},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildRasAcpiTracerData(acpiEventWithDyn(t, tt.sev, tt.fruTxtOffset, tt.recordLen, tt.bufOffset, tt.dyn))
			if err != nil {
				t.Fatalf("buildRasAcpiTracerData() error = %v", err)
			}

			info := decodeAcpiInfo(t, got)
			if info.FRUText != tt.wantFRUText {
				t.Errorf("fru_text = %q, want %q", info.FRUText, tt.wantFRUText)
			}
			if info.DataLen != tt.wantDataLen {
				t.Errorf("data_len = %d, want %d", info.DataLen, tt.wantDataLen)
			}

			fields := strings.Fields(info.RawData)
			if len(fields) != tt.wantRawBytes {
				t.Fatalf("raw_data holds %d bytes, want %d: %q", len(fields), tt.wantRawBytes, info.RawData)
			}
			if dump := strings.Join(fields, " "); tt.wantRawPrefix != "" && !strings.HasPrefix(dump, tt.wantRawPrefix) {
				t.Errorf("raw_data = %q, want prefix %q", dump, tt.wantRawPrefix)
			}
		})
	}
}

// TestDispatchRasTracerDataToleratesHostileRecords drives every hardware error
// builder with records whose offsets and lengths may point anywhere: an
// all-zero record (the probe zeroes fields a tracepoint does not fill) and an
// all-ones record (every __data_loc descriptor reads 0xffffffff). None of them
// may panic; before the dynamic-array window was bounds-checked the ACPI hex
// dump sliced out of range and killed the agent.
func TestDispatchRasTracerDataToleratesHostileRecords(t *testing.T) {
	t.Parallel()

	records := []struct {
		name string
		fill byte
	}{
		{name: "zeroed record", fill: 0x00},
		{name: "all ones record", fill: 0xff},
	}

	for _, rec := range records {
		for typ, label := range hwErrTypeLabels {
			t.Run(rec.name+"/"+label, func(t *testing.T) {
				t.Parallel()

				info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
				for i := range info {
					info[i] = rec.fill
				}

				data, err := dispatchRasTracerData(rasEventWithInfo(t, uint32(typ), info))
				if err != nil {
					t.Fatalf("dispatchRasTracerData(%s) error = %v", label, err)
				}
				if data == nil {
					t.Fatalf("dispatchRasTracerData(%s) returned no data", label)
				}
			})
		}
	}
}

func TestDispatchRasTracerDataRejectsUnknownType(t *testing.T) {
	t.Parallel()

	info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
	if _, err := dispatchRasTracerData(rasEventWithInfo(t, maxNumHWErrTypes, info)); err == nil {
		t.Fatal("unsupported hardware error type accepted")
	}
}
