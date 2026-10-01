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

// mceInfo mirrors the JSON payload emitted for an x86 MCE event.
type mceInfo struct {
	MCGCap    uint64 `json:"mcg_cpu_cap"`
	MCGStatus uint64 `json:"mcg_msr_status"`
	Status    uint64 `json:"banks_msr_status"`
	Addr      uint64 `json:"banks_msr_addr"`
	Misc      uint64 `json:"banks_msr_misc"`
	Synd      uint64 `json:"mca_synd_msr"`
	IPID      uint64 `json:"mca_ipid_msr"`
	IP        uint64 `json:"instr_pointer"`
	TSC       uint64 `json:"tsc_timestamp"`
	WallTime  uint64 `json:"walltime"`
	CPU       uint32 `json:"cpu"`
	CPUID     uint32 `json:"cpuid"`
	APICID    uint32 `json:"apicid"`
	SocketID  uint32 `json:"socketid"`
	CS        uint8  `json:"code_seg"`
	Bank      uint8  `json:"bank"`
	CPUVendor uint8  `json:"cpuvendor"`
}

// edacInfo mirrors the JSON payload emitted for an EDAC mc_event.
type edacInfo struct {
	ErrCount uint16 `json:"err_count"`
	ErrType  string `json:"err_type"`
	Msg      string `json:"err_msg"`
	Label    string `json:"label"`
	MCIndex  uint8  `json:"mc_index"`
	TopLayer int8   `json:"top_layer"`
	MidLayer int8   `json:"mid_layer"`
	LowLayer int8   `json:"low_layer"`
	Addr     uint64 `json:"addr"`
	Grain    uint64 `json:"grain"`
	Syndrome uint64 `json:"syndrome"`
	Driver   string `json:"driver"`
}

// aerInfo mirrors the JSON payload emitted for a PCIe AER event.
type aerInfo struct {
	DevName   string `json:"dev_name"`
	ErrType   string `json:"err_type"`
	ErrReason string `json:"err_reason"`
	TLPHeader string `json:"tlp_header"`
}

// armInfo mirrors the JSON payload emitted for an ARM GHES processor event.
type armInfo struct {
	MPIDR         string `json:"mpidr"`
	MIDR          string `json:"midr"`
	RunningState  string `json:"running_state"`
	PSCIState     string `json:"psci_state"`
	AffinityLevel string `json:"affinity_level"`
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

// edacEventWithDyn builds an HW_ERR_EDAC record. The fixed portion is 60
// bytes: Pad(8) | err_type(4) | msg_offset(4) | label_offset(4) | err_count(2)
// | mc_index(1) | layers(3) | reserve(6) | addr(8) | grain(1) | reserve(7) |
// syndrome(8) | driver_offset(4). dyn is the captured dynamic area; it cannot
// exceed the DETAIL_INFO_SIZE_EDAC-byte window the BPF probe copies.
func edacEventWithDyn(t *testing.T, errType, msgOffset, labelOffset, driverOffset uint32, dyn []byte) *rasEvent {
	t.Helper()

	if len(dyn) > DETAIL_INFO_SIZE_EDAC {
		t.Fatalf("dynamic area is %d bytes, the probe captures only %d", len(dyn), DETAIL_INFO_SIZE_EDAC)
	}

	info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
	binary.LittleEndian.PutUint32(info[8:], errType)
	binary.LittleEndian.PutUint32(info[12:], msgOffset)
	binary.LittleEndian.PutUint32(info[16:], labelOffset)
	binary.LittleEndian.PutUint32(info[56:], driverOffset)
	copy(info[60:], dyn)

	return rasEventWithInfo(t, HW_ERR_EDAC, info)
}

// aerEventWithDyn builds an HW_ERR_PCIE_AER record. The fixed portion is 36
// bytes: Pad(8) | dev_name_offset(4) | status(4) | severity(1) |
// tlp_header_valid(1) | pattern(2) | tlp_header(16). dyn is the captured
// dynamic area; it cannot exceed the DETAIL_INFO_SIZE_AER-byte window.
func aerEventWithDyn(t *testing.T, devNameOffset, status uint32, severity uint8, dyn []byte) *rasEvent {
	t.Helper()

	if len(dyn) > DETAIL_INFO_SIZE_AER {
		t.Fatalf("dynamic area is %d bytes, the probe captures only %d", len(dyn), DETAIL_INFO_SIZE_AER)
	}

	info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
	binary.LittleEndian.PutUint32(info[8:], devNameOffset)
	binary.LittleEndian.PutUint32(info[12:], status)
	info[16] = severity
	copy(info[36:], dyn)

	return rasEventWithInfo(t, HW_ERR_PCIE_AER, info)
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

// decodeEdacInfo unmarshals the structured Info payload of an EDAC event.
func decodeEdacInfo(t *testing.T, data *RasTracingData) edacInfo {
	t.Helper()

	var info edacInfo
	if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
		t.Fatalf("decode EDAC info %q: %v", data.Info, err)
	}
	return info
}

// decodeAerInfo unmarshals the structured Info payload of a PCIe AER event.
func decodeAerInfo(t *testing.T, data *RasTracingData) aerInfo {
	t.Helper()

	var info aerInfo
	if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
		t.Fatalf("decode AER info %q: %v", data.Info, err)
	}
	return info
}

// decodeArmInfo unmarshals the structured Info payload of an ARM GHES event.
func decodeArmInfo(t *testing.T, data *RasTracingData) armInfo {
	t.Helper()

	var info armInfo
	if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
		t.Fatalf("decode ARM info %q: %v", data.Info, err)
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

// TestRasMceTracerDataDecodesHostileStatus locks the MCE error-class mapping
// for hostile status bitfields. MCE records carry no dynamic elements, so the
// only kernel-supplied value that influences decoding is the MCi_STATUS
// bitfield: bit 44 (deferred) and bit 61 (uncorrectable) decide the reported
// class, and the all-ones record must decode as the deferred class instead of
// producing a bogus value.
func TestRasMceTracerDataDecodesHostileStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fill     byte
		wantType string
	}{
		{name: "zeroed record", fill: 0x00, wantType: ErrTypeCorrected},
		{name: "all ones record", fill: 0xff, wantType: ErrTypeUncorrectedDeferred},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
			for i := range info {
				info[i] = tt.fill
			}

			got, err := buildRasMceTracerData(rasEventWithInfo(t, HW_ERR_MCE, info))
			if err != nil {
				t.Fatalf("buildRasMceTracerData() error = %v", err)
			}
			if got.ErrType != tt.wantType {
				t.Errorf("type = %q, want %q", got.ErrType, tt.wantType)
			}

			var payload mceInfo
			if err := json.Unmarshal([]byte(got.Info), &payload); err != nil {
				t.Fatalf("decode MCE info %q: %v", got.Info, err)
			}
			if tt.fill == 0xff && payload.Status != ^uint64(0) {
				t.Errorf("banks_msr_status = %#x, want all ones", payload.Status)
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

// TestRasEdacTracerDataClampsDescriptors drives the EDAC builder with
// kernel-supplied __data_loc descriptors pointing before, at and past the
// captured window. The three descriptors (error message, DIMM label, driver
// detail) all resolve through cstring(), so an out-of-window offset must
// decode to an empty string instead of panicking the RAS event loop.
func TestRasEdacTracerDataClampsDescriptors(t *testing.T) {
	t.Parallel()

	// The dynamic area starts after the 60-byte fixed portion. Layout:
	// "mem-row-hammered\x00" (17) | "CPU0_DIMM_A1\x00" (13) |
	// "sb_edac\x00" (8) | two junk bytes.
	const edacBase = uint32(60)
	dyn := append(append(
		append([]byte("mem-row-hammered\x00"), []byte("CPU0_DIMM_A1\x00")...),
		[]byte("sb_edac\x00")...), 0xa1, 0xb2)

	tests := []struct {
		name         string
		errType      uint32
		msgOffset    uint32
		labelOffset  uint32
		driverOffset uint32
		wantType     string
		wantMsg      string
		wantLabel    string
		wantDriver   string
	}{
		{name: "well formed", errType: 0x04, msgOffset: edacBase, labelOffset: edacBase + 17, driverOffset: edacBase + 30, wantType: ErrTypeInfo, wantMsg: "mem-row-hammered", wantLabel: "CPU0_DIMM_A1", wantDriver: "sb_edac"},
		{name: "offsets past window", errType: 0x04, msgOffset: 0xffff, labelOffset: 0xffff, driverOffset: 0xffff, wantType: ErrTypeInfo, wantMsg: "", wantLabel: "", wantDriver: ""},
		{name: "offsets below base", errType: 0x04, msgOffset: 8, labelOffset: 8, driverOffset: 8, wantType: ErrTypeInfo, wantMsg: "", wantLabel: "", wantDriver: ""},
		{name: "offsets at end of window", errType: 0x04, msgOffset: edacBase + uint32(len(dyn)), labelOffset: edacBase + uint32(len(dyn)), driverOffset: edacBase + uint32(len(dyn)), wantType: ErrTypeInfo, wantMsg: "", wantLabel: "", wantDriver: ""},
		{name: "driver offset past window", errType: 0x04, msgOffset: edacBase, labelOffset: edacBase + 17, driverOffset: 0xffff, wantType: ErrTypeInfo, wantMsg: "mem-row-hammered", wantLabel: "CPU0_DIMM_A1", wantDriver: ""},
		{name: "unknown err type", errType: 0xff, msgOffset: edacBase, labelOffset: edacBase + 17, driverOffset: edacBase + 30, wantType: ErrTypeUnknown, wantMsg: "mem-row-hammered", wantLabel: "CPU0_DIMM_A1", wantDriver: "sb_edac"},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildRasEdacTracerData(edacEventWithDyn(t, tt.errType, tt.msgOffset, tt.labelOffset, tt.driverOffset, dyn))
			if err != nil {
				t.Fatalf("buildRasEdacTracerData() error = %v", err)
			}
			if got.ErrType != tt.wantType {
				t.Errorf("type = %q, want %q", got.ErrType, tt.wantType)
			}

			info := decodeEdacInfo(t, got)
			if info.Msg != tt.wantMsg {
				t.Errorf("err_msg = %q, want %q", info.Msg, tt.wantMsg)
			}
			if info.Label != tt.wantLabel {
				t.Errorf("label = %q, want %q", info.Label, tt.wantLabel)
			}
			if info.Driver != tt.wantDriver {
				t.Errorf("driver = %q, want %q", info.Driver, tt.wantDriver)
			}
		})
	}
}

// TestRasAerTracerDataClampsDevNameOffset drives the PCIe AER builder with
// kernel-supplied dev_name offsets pointing before, at and past the captured
// window. DevNameOffset is the only dynamic descriptor of the record, so an
// out-of-window offset must decode to an empty device name instead of
// panicking the RAS event loop.
func TestRasAerTracerDataClampsDevNameOffset(t *testing.T) {
	t.Parallel()

	// The dynamic area starts after the 36-byte fixed portion.
	const aerBase = uint32(36)
	dyn := []byte("0000:03:00.0\x00")

	tests := []struct {
		name        string
		devOffset   uint32
		severity    uint8
		wantType    string
		wantDevName string
	}{
		{name: "well formed", devOffset: aerBase, severity: 2, wantType: ErrTypeCorrected, wantDevName: "0000:03:00.0"},
		{name: "offset past window", devOffset: 0xffff, severity: 2, wantType: ErrTypeCorrected, wantDevName: ""},
		{name: "offset below base", devOffset: 8, severity: 2, wantType: ErrTypeCorrected, wantDevName: ""},
		{name: "offset at end of window", devOffset: aerBase + uint32(len(dyn)), severity: 2, wantType: ErrTypeCorrected, wantDevName: ""},
		{name: "unknown severity", devOffset: aerBase, severity: 0xff, wantType: ErrTypeUnknown, wantDevName: "0000:03:00.0"},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildRasAerTracerData(aerEventWithDyn(t, tt.devOffset, 0, tt.severity, dyn))
			if err != nil {
				t.Fatalf("buildRasAerTracerData() error = %v", err)
			}
			if got.ErrType != tt.wantType {
				t.Errorf("type = %q, want %q", got.ErrType, tt.wantType)
			}

			info := decodeAerInfo(t, got)
			if info.DevName != tt.wantDevName {
				t.Errorf("dev_name = %q, want %q", info.DevName, tt.wantDevName)
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

// TestRasArmGhesTracerDataDecodesHostileFields locks the decoding of ARM GHES
// optional CPER fields for hostile inputs. ARM records are a fixed layout
// without dynamic elements; the kernel fills fields it could not capture with
// all-ones, which must render as N/A instead of a bogus value. The zeroed
// record pins the stopped-state rendering.
func TestRasArmGhesTracerDataDecodesHostileFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		fill              byte
		wantRunningState  string
		wantPSCIState     string
		wantAffinityLevel string
	}{
		{name: "zeroed record", fill: 0x00, wantRunningState: "stopped", wantPSCIState: "0x0", wantAffinityLevel: "0"},
		{name: "all ones record", fill: 0xff, wantRunningState: "N/A", wantPSCIState: "N/A", wantAffinityLevel: "N/A"},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := make([]byte, RAS_PERFEVENT_INFO_SIZE)
			for i := range info {
				info[i] = tt.fill
			}

			got, err := buildRasArmGhesTracerData(rasEventWithInfo(t, HW_ERR_ARM_GHES, info))
			if err != nil {
				t.Fatalf("buildRasArmGhesTracerData() error = %v", err)
			}

			payload := decodeArmInfo(t, got)
			if payload.RunningState != tt.wantRunningState {
				t.Errorf("running_state = %q, want %q", payload.RunningState, tt.wantRunningState)
			}
			if payload.PSCIState != tt.wantPSCIState {
				t.Errorf("psci_state = %q, want %q", payload.PSCIState, tt.wantPSCIState)
			}
			if payload.AffinityLevel != tt.wantAffinityLevel {
				t.Errorf("affinity_level = %q, want %q", payload.AffinityLevel, tt.wantAffinityLevel)
			}
		})
	}
}
