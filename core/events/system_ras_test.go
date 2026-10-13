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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The payload structs below mirror the tracepoint records decoded by
// core/events/system_ras.go. They are duplicated here on purpose: production
// declares them locally inside each builder, so the tests must re-state the
// exact wire layout (little-endian, no padding) to craft the records exactly
// the way the kernel would emit them.

type testAcpiPayload struct {
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

type testEdacPayload struct {
	Pad            uint64
	ErrType        uint32
	ErrorMsgOffset uint32
	LabelOffset    uint32
	ErrCount       uint16
	MCIndex        uint8
	TopLayer       int8
	MidLayer       int8
	LowLayer       int8
	ReserveA       [6]uint8
	Addr           uint64
	GrainBits      uint8
	ReserveB       [7]uint8
	Syndrome       uint64
	DriverDetail   uint32
	MsgDetail      [DETAIL_INFO_SIZE_EDAC]byte
}

type testAerPayload struct {
	Pad            uint64
	DevNameOffset  uint32
	Status         uint32
	Severity       uint8
	TLPHeaderValid uint8
	Pattern        [2]uint8
	TLPHeader      [4]uint32
	Msg            [DETAIL_INFO_SIZE_AER]byte
}

type testMcePayload struct {
	Pad       uint64
	MCGCap    uint64
	MCGStatus uint64
	Status    uint64
	Addr      uint64
	Misc      uint64
	Synd      uint64
	IPID      uint64
	IP        uint64
	TSC       uint64
	WallTime  uint64
	CPU       uint32
	CPUID     uint32
	APICID    uint32
	SocketID  uint32
	CS        uint8
	Bank      uint8
	CPUVendor uint8
}

type testArmPayload struct {
	Pad          uint64
	MPIDR        uint64
	MIDR         uint64
	RunningState uint32
	PSCIState    uint32
	Affinity     uint8
}

// JSON views of the structured records the builders emit. Field tags must match
// production exactly, otherwise the assertions would silently inspect zero
// values.
type acpiInfoJSON struct {
	Severity uint8  `json:"severity"`
	SecType  string `json:"sec_type"`
	FRUID    string `json:"fru_id"`
	FRUText  string `json:"fru_text"`
	DataLen  uint32 `json:"data_len"`
	RawData  string `json:"raw_data"`
}

type edacInfoJSON struct {
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

type aerInfoJSON struct {
	DevName   string `json:"dev_name"`
	ErrType   string `json:"err_type"`
	ErrReason string `json:"err_reason"`
	TLPHeader string `json:"tlp_header"`
}

type mceInfoJSON struct {
	Bank uint8  `json:"bank"`
	CPU  uint32 `json:"cpu"`
}

type armGhesInfoJSON struct {
	MPIDR         string `json:"mpidr"`
	MIDR          string `json:"midr"`
	RunningState  string `json:"running_state"`
	PSCIState     string `json:"psci_state"`
	AffinityLevel string `json:"affinity_level"`
}

// mustRasEvent encodes payload little-endian into the 512-byte Info buffer of a
// rasEvent, reproducing the record bpf/system_ras.c writes into the perf event
// buffer.
func mustRasEvent(t *testing.T, evType uint32, payload any) rasEvent {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, payload))
	require.LessOrEqual(t, buf.Len(), RAS_PERFEVENT_INFO_SIZE,
		"payload %T does not fit in the %d-byte tracepoint window", payload, RAS_PERFEVENT_INFO_SIZE)

	var ev rasEvent
	ev.Type = evType
	ev.KernelObservedNS = uint64(5 * time.Second)
	copy(ev.Info[:], buf.Bytes())
	return ev
}

// dispatchWithoutPanic runs the same dispatch the RAS event loop uses and fails
// the test if a crafted record panics. A panic here is exactly the reported
// regression: it escapes rasEventLoop and terminates the agent.
func dispatchWithoutPanic(t *testing.T, ev *rasEvent) *RasTracingData {
	t.Helper()

	var (
		td  *RasTracingData
		err error
	)
	require.NotPanics(t, func() {
		td, err = dispatchRasTracerData(ev)
	}, "malformed hardware record must not panic")

	require.NoError(t, err)
	require.NotNil(t, td)
	return td
}

// decodeInfo unmarshals the JSON record a builder produced into its test view.
func decodeInfo[T any](t *testing.T, td *RasTracingData) T {
	t.Helper()

	var info T
	require.NoError(t, json.Unmarshal([]byte(td.Info), &info))
	return info
}

// writeInto places each string (including its NUL for the ones that carry one)
// at the given offset of buf. Entries never overlap in these tests.
func writeInto(buf []byte, writes map[int]string) {
	for off, s := range writes {
		copy(buf[off:], s)
	}
}

func acpiMsg(writes map[int]string, fill byte) [DETAIL_INFO_SIZE_ACPI]byte {
	var msg [DETAIL_INFO_SIZE_ACPI]byte
	for i := range msg {
		msg[i] = fill
	}
	writeInto(msg[:], writes)
	return msg
}

func edacMsg(writes map[int]string, fill byte) [DETAIL_INFO_SIZE_EDAC]byte {
	var msg [DETAIL_INFO_SIZE_EDAC]byte
	for i := range msg {
		msg[i] = fill
	}
	writeInto(msg[:], writes)
	return msg
}

func aerMsg(writes map[int]string, fill byte) [DETAIL_INFO_SIZE_AER]byte {
	var msg [DETAIL_INFO_SIZE_AER]byte
	for i := range msg {
		msg[i] = fill
	}
	writeInto(msg[:], writes)
	return msg
}

// TestDynamicWindow pins the clamping behaviour of the untrusted-offset
// accessor. Every case below previously produced a slice-bounds panic somewhere
// in the RAS builders when the offset/length pair came straight from the kernel.
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
		{
			name:      "offset at base honours length",
			dyn:       dyn,
			rawOffset: 8,
			base:      4,
			length:    4,
			want:      "4567",
		},
		{
			name:      "length beyond window is clamped",
			dyn:       dyn,
			rawOffset: 8,
			base:      4,
			length:    100,
			want:      "456789",
		},
		{
			name:      "wrapped length does not overflow",
			dyn:       dyn,
			rawOffset: 8,
			base:      4,
			length:    ^uint32(0),
			want:      "456789",
		},
		{
			name:      "offset below base yields nil",
			dyn:       dyn,
			rawOffset: 3,
			base:      4,
			length:    1,
			wantNil:   true,
		},
		{
			name:      "offset at end of window yields nil",
			dyn:       dyn,
			rawOffset: 14,
			base:      4,
			length:    1,
			wantNil:   true,
		},
		{
			name:      "offset past window yields nil",
			dyn:       dyn,
			rawOffset: 0xffff,
			base:      4,
			length:    1,
			wantNil:   true,
		},
		{
			name:      "high bits of offset are masked",
			dyn:       dyn,
			rawOffset: 0x10008,
			base:      4,
			length:    2,
			want:      "45",
		},
		{
			name:      "zero length yields an empty non-nil window",
			dyn:       dyn,
			rawOffset: 8,
			base:      4,
			length:    0,
			want:      "",
		},
		{
			name:      "empty source yields nil",
			dyn:       nil,
			rawOffset: 8,
			base:      4,
			length:    4,
			wantNil:   true,
		},
		{
			name:      "acpi offset one past the fixed window yields nil",
			dyn:       make([]byte, DETAIL_INFO_SIZE_ACPI),
			rawOffset: uint32(56 + DETAIL_INFO_SIZE_ACPI),
			base:      56,
			length:    1,
			wantNil:   true,
		},
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

// TestDynamicWindowAliasesSource pins the documented contract that the returned
// window aliases the input instead of copying it: callers must keep the source
// alive while they use the result, and any byte they write lands in the source.
func TestDynamicWindowAliasesSource(t *testing.T) {
	t.Parallel()

	src := []byte("0123456789")
	win := dynamicWindow(src, 8, 4, 3) // resolves to src[4:7] == "456"
	require.NotNil(t, win)
	require.Equal(t, "456", string(win))

	win[0] = 'X'
	assert.Equal(t, byte('X'), src[4], "window must alias the source, not copy it")
	assert.Equal(t, "0123X56789", string(src))
}

func TestCstring(t *testing.T) {
	t.Parallel()

	buf := []byte("CPU0_DIMM_A1\x00ZZZZ") // NUL at index 12, len 17

	tests := []struct {
		name      string
		buf       []byte
		rawOffset uint32
		base      uint32
		want      string
	}{
		{
			name: "reads up to the NUL terminator",
			buf:  buf,
			want: "CPU0_DIMM_A1",
		},
		{
			name:      "offset inside the buffer",
			buf:       buf,
			rawOffset: 5,
			want:      "DIMM_A1",
		},
		{
			name: "no NUL returns the rest of the window",
			buf:  []byte("ABCDEFG"),
			want: "ABCDEFG",
		},
		{
			name: "empty buffer",
			buf:  nil,
			want: "",
		},
		{
			name:      "offset below base",
			buf:       buf,
			rawOffset: 2,
			base:      8,
			want:      "",
		},
		{
			name:      "offset past the end",
			buf:       buf,
			rawOffset: 100,
			base:      8,
			want:      "",
		},
		{
			name:      "offset exactly at the end",
			buf:       buf,
			rawOffset: uint32(len(buf)) + 8,
			base:      8,
			want:      "",
		},
		{
			name:      "high bits of offset are masked",
			buf:       buf,
			rawOffset: 0x10005,
			want:      "DIMM_A1",
		},
		{
			name:      "NUL at the offset yields empty",
			buf:       buf,
			rawOffset: 12,
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, cstring(tc.buf, tc.rawOffset, tc.base))
		})
	}
}

// TestBuildRasAcpiTracerDataFRUWindow is the regression for the reported panic.
// The kernel reports the CPER section length in payload.Len, which is larger
// than the fixed window for many real sections (NVDIMM, CXL, vendor payloads).
// Before the fix the raw hex dump sliced payload.Msg with that untrusted length
// and panicked in the RAS event loop.
func TestBuildRasAcpiTracerDataFRUWindow(t *testing.T) {
	t.Parallel()

	const base = 56 // nonStandardBase: fixed portion of trace_event_raw_non_standard_event

	fullWindow := make([]byte, DETAIL_INFO_SIZE_ACPI)
	for i := range fullWindow {
		fullWindow[i] = byte(i)
	}

	tests := []struct {
		name         string
		fruTxtOffset uint32
		length       uint32
		wantRaw      []byte
	}{
		{
			name:         "length equals the window",
			fruTxtOffset: base,
			length:       uint32(DETAIL_INFO_SIZE_ACPI),
			wantRaw:      fullWindow,
		},
		{
			// Regression: one byte past the window used to panic in
			// payload.Msg[off : off+payload.Len].
			name:         "length one past the window truncates",
			fruTxtOffset: base,
			length:       uint32(DETAIL_INFO_SIZE_ACPI + 1),
			wantRaw:      fullWindow,
		},
		{
			name:         "length far past the window truncates",
			fruTxtOffset: base,
			length:       1 << 20,
			wantRaw:      fullWindow,
		},
		{
			// Regression: off+Len wrapped so the high bound fell below the low
			// bound, panicking with "slice bounds out of range".
			name:         "wrapped length is clamped",
			fruTxtOffset: base,
			length:       ^uint32(0),
			wantRaw:      fullWindow,
		},
		{
			// Regression: the low bound alone was already past the window.
			name:         "offset past the window yields no raw data",
			fruTxtOffset: uint32(base + DETAIL_INFO_SIZE_ACPI),
			length:       64,
			wantRaw:      nil,
		},
		{
			name:         "offset 0xffff yields no raw data",
			fruTxtOffset: 0xffff,
			length:       64,
			wantRaw:      nil,
		},
		{
			name:         "offset below base yields no raw data",
			fruTxtOffset: base - 8,
			length:       64,
			wantRaw:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testAcpiPayload{
				FRUTxtOffset: tc.fruTxtOffset,
				Sev:          0x2,
				Len:          tc.length,
			}
			copy(payload.Msg[:], fullWindow)

			ev := mustRasEvent(t, HW_ERR_ACPI_GHES, payload)
			td := dispatchWithoutPanic(t, &ev)

			info := decodeInfo[acpiInfoJSON](t, td)
			assert.Equal(t, tc.length, info.DataLen,
				"data_len must keep reporting the raw kernel length even when truncated")
			assert.Equal(t, fmt.Sprintf("% x", tc.wantRaw), info.RawData)
		})
	}
}

func TestBuildRasAcpiTracerDataFRUText(t *testing.T) {
	t.Parallel()

	const base = 56

	tests := []struct {
		name         string
		fruTxtOffset uint32
		msg          [DETAIL_INFO_SIZE_ACPI]byte
		want         string
	}{
		{
			name:         "text at base",
			fruTxtOffset: base,
			msg:          acpiMsg(map[int]string{0: "CPU0_DIMM_A1\x00"}, 0),
			want:         "CPU0_DIMM_A1",
		},
		{
			name:         "text at an inner offset",
			fruTxtOffset: base + 16,
			msg:          acpiMsg(map[int]string{16: "PCIe card 0\x00"}, 0),
			want:         "PCIe card 0",
		},
		{
			name:         "offset below base is ignored",
			fruTxtOffset: base - 4,
			msg:          acpiMsg(map[int]string{0: "ignored\x00"}, 0),
			want:         "",
		},
		{
			name:         "offset past the window is ignored",
			fruTxtOffset: uint32(base + DETAIL_INFO_SIZE_ACPI),
			msg:          acpiMsg(map[int]string{0: "ignored\x00"}, 0),
			want:         "",
		},
		{
			name:         "high bits of the offset are masked",
			fruTxtOffset: 0x10000 | base,
			msg:          acpiMsg(map[int]string{0: "CPU0_DIMM_A1\x00"}, 0),
			want:         "CPU0_DIMM_A1",
		},
		{
			name:         "no NUL returns the rest of the window",
			fruTxtOffset: base,
			msg:          acpiMsg(nil, 'Z'),
			want:         strings.Repeat("Z", DETAIL_INFO_SIZE_ACPI),
		},
		{
			name:         "leading NUL yields empty text",
			fruTxtOffset: base,
			msg:          acpiMsg(map[int]string{0: "\x00rest"}, 0),
			want:         "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testAcpiPayload{
				FRUTxtOffset: tc.fruTxtOffset,
				Len:          16,
				Msg:          tc.msg,
			}
			ev := mustRasEvent(t, HW_ERR_ACPI_GHES, payload)
			td := dispatchWithoutPanic(t, &ev)

			info := decodeInfo[acpiInfoJSON](t, td)
			assert.Equal(t, tc.want, info.FRUText)
		})
	}
}

// EDAC shares cstring with ACPI, so its dynamic offsets are covered with the
// same in-window/below-base/past-window matrix.
func TestBuildRasEdacTracerDataDynamicOffsets(t *testing.T) {
	t.Parallel()

	const base = 60

	validMsg := edacMsg(map[int]string{
		0:  "CE memory read error\x00",
		32: "CPU_SrcID#0_Ha#0_Chan#0_DIMM#0\x00",
		64: "amd64_edac\x00",
	}, 0)

	tests := []struct {
		name         string
		msgOffset    uint32
		labelOffset  uint32
		driverOffset uint32
		wantMsg      string
		wantLabel    string
		wantDriver   string
	}{
		{
			name:         "valid offsets",
			msgOffset:    base,
			labelOffset:  base + 32,
			driverOffset: base + 64,
			wantMsg:      "CE memory read error",
			wantLabel:    "CPU_SrcID#0_Ha#0_Chan#0_DIMM#0",
			wantDriver:   "amd64_edac",
		},
		{
			name:         "offsets below base are ignored",
			msgOffset:    base - 1,
			labelOffset:  base - 2,
			driverOffset: base - 3,
		},
		{
			name:         "offsets past the window are ignored",
			msgOffset:    uint32(base + DETAIL_INFO_SIZE_EDAC),
			labelOffset:  ^uint32(0),
			driverOffset: 0xffff,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testEdacPayload{
				ErrType:        0x03,
				ErrorMsgOffset: tc.msgOffset,
				LabelOffset:    tc.labelOffset,
				ErrCount:       7,
				MCIndex:        1,
				GrainBits:      3,
				DriverDetail:   tc.driverOffset,
				MsgDetail:      validMsg,
			}
			ev := mustRasEvent(t, HW_ERR_EDAC, payload)
			td := dispatchWithoutPanic(t, &ev)

			info := decodeInfo[edacInfoJSON](t, td)
			assert.Equal(t, "MEM", td.Device)
			assert.Equal(t, ErrTypeUncorrectedFatal, info.ErrType)
			assert.Equal(t, tc.wantMsg, info.Msg)
			assert.Equal(t, tc.wantLabel, info.Label)
			assert.Equal(t, tc.wantDriver, info.Driver)
			assert.Equal(t, uint16(7), info.ErrCount)
			assert.Equal(t, uint64(8), info.Grain)
		})
	}
}

func TestBuildRasAerTracerDataDynamicOffsets(t *testing.T) {
	t.Parallel()

	const base = 36

	tests := []struct {
		name        string
		devOffset   uint32
		wantDevName string
	}{
		{
			name:        "valid device name",
			devOffset:   base,
			wantDevName: "0000:03:00.0",
		},
		{
			name:        "offset below base is ignored",
			devOffset:   base - 4,
			wantDevName: "",
		},
		{
			name:        "offset past the window is ignored",
			devOffset:   0xffff,
			wantDevName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testAerPayload{
				DevNameOffset:  tc.devOffset,
				Status:         PciErrCorBadTlp,
				Severity:       2,
				TLPHeaderValid: 1,
				TLPHeader:      [4]uint32{0x1, 0x2, 0x3, 0x4},
				Msg:            aerMsg(map[int]string{0: "0000:03:00.0\x00"}, 0),
			}
			ev := mustRasEvent(t, HW_ERR_PCIE_AER, payload)
			td := dispatchWithoutPanic(t, &ev)

			info := decodeInfo[aerInfoJSON](t, td)
			assert.Equal(t, "PCIe "+tc.wantDevName, td.Device)
			assert.Equal(t, tc.wantDevName, info.DevName)
			assert.Equal(t, ErrTypeCorrected, info.ErrType)
			assert.Equal(t, "Bad TLP", info.ErrReason)
			assert.Equal(t, "{0x1,0x2,0x3,0x4}", info.TLPHeader)
		})
	}
}

func TestBuildRasMceTracerData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      uint64
		wantErrType string
	}{
		{name: "corrected", status: 0, wantErrType: ErrTypeCorrected},
		{name: "uncorrected recoverable", status: MCI_STATUS_UC, wantErrType: ErrTypeUncorrectedRecoverable},
		{name: "uncorrected deferred", status: MCI_STATUS_DEFERRED, wantErrType: ErrTypeUncorrectedDeferred},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testMcePayload{Status: tc.status, Bank: 3, CPU: 7, CPUVendor: 1}
			ev := mustRasEvent(t, HW_ERR_MCE, payload)
			td := dispatchWithoutPanic(t, &ev)

			assert.Equal(t, "CPU/MEM", td.Device)
			assert.Equal(t, "MCE", td.Event)
			assert.Equal(t, tc.wantErrType, td.ErrType)

			info := decodeInfo[mceInfoJSON](t, td)
			assert.Equal(t, uint8(3), info.Bank)
			assert.Equal(t, uint32(7), info.CPU)
		})
	}
}

func TestBuildRasArmGhesTracerData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		runningState uint32
		psciState    uint32
		affinity     uint8
		wantRunning  string
		wantPSCI     string
		wantAffinity string
	}{
		{
			name:         "running",
			runningState: 1,
			wantRunning:  "running",
			wantPSCI:     "N/A",
			wantAffinity: "0",
		},
		{
			name:         "stopped with psci state",
			runningState: 0,
			psciState:    0x3010000,
			affinity:     2,
			wantRunning:  "stopped",
			wantPSCI:     "0x3010000",
			wantAffinity: "2",
		},
		{
			name:         "absent running state",
			runningState: ^uint32(0),
			affinity:     ^uint8(0),
			wantRunning:  "N/A",
			wantPSCI:     "N/A",
			wantAffinity: "N/A",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testArmPayload{
				MPIDR:        0x81000000,
				MIDR:         0x410fd034,
				RunningState: tc.runningState,
				PSCIState:    tc.psciState,
				Affinity:     tc.affinity,
			}
			ev := mustRasEvent(t, HW_ERR_ARM_GHES, payload)
			td := dispatchWithoutPanic(t, &ev)

			info := decodeInfo[armGhesInfoJSON](t, td)
			assert.Equal(t, "CPU", td.Device)
			assert.Equal(t, "ARM_GHES", td.Event)
			assert.Equal(t, "0x81000000", info.MPIDR)
			assert.Equal(t, "0x410fd034", info.MIDR)
			assert.Equal(t, tc.wantRunning, info.RunningState)
			assert.Equal(t, tc.wantPSCI, info.PSCIState)
			assert.Equal(t, tc.wantAffinity, info.AffinityLevel)
		})
	}
}

// TestDispatchRasTracerDataSurvivesMalformedWindows drives the dispatcher the
// way rasEventLoop does with hostile offset/length values in every event type,
// guarding the "a single malformed hardware event must not kill the agent"
// invariant.
func TestDispatchRasTracerDataSurvivesMalformedWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  uint32
		body any
	}{
		{
			name: "acpi max offset and wrapped length",
			typ:  HW_ERR_ACPI_GHES,
			body: testAcpiPayload{FRUTxtOffset: 0xffff, Sev: 0x3, Len: ^uint32(0)},
		},
		{
			name: "acpi offset at window end",
			typ:  HW_ERR_ACPI_GHES,
			body: testAcpiPayload{FRUTxtOffset: uint32(56 + DETAIL_INFO_SIZE_ACPI), Len: 8},
		},
		{
			name: "edac out-of-range offsets",
			typ:  HW_ERR_EDAC,
			body: testEdacPayload{ErrorMsgOffset: 0xffff, LabelOffset: 0xffff, DriverDetail: 0xffff},
		},
		{
			name: "aer out-of-range offset",
			typ:  HW_ERR_PCIE_AER,
			body: testAerPayload{DevNameOffset: 0xffff},
		},
		{
			name: "mce",
			typ:  HW_ERR_MCE,
			body: testMcePayload{Bank: 1},
		},
		{
			name: "arm ghes",
			typ:  HW_ERR_ARM_GHES,
			body: testArmPayload{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := mustRasEvent(t, tc.typ, tc.body)
			dispatchWithoutPanic(t, &ev)
		})
	}
}
