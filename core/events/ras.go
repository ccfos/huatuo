// Copyright 2025 The HuaTuo Authors
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
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	"github.com/cloudflare/backoff"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/ras.c -o $BPF_DIR/ras.o

// Hardware error type identifiers — must stay in sync with bpf/ras.c.
const (
	HW_ERR_MCE       = 0
	HW_ERR_EDAC      = 1
	HW_ERR_ACPI_GHES = 2
	HW_ERR_PCIE_AER  = 3
	HW_ERR_THR       = 4 // MCE threshold (local-APIC) interrupt
)

// maxNumHWErrTypes is the total number of distinct hardware error source types.
// Any new HW_ERR_* constant must increment this value and extend hwErrTypeLabels.
const maxNumHWErrTypes = 5

// hwErrTypeLabels maps each HW_ERR_* index to its Prometheus "type" label value.
// Index must align 1:1 with the HW_ERR_* constants above.
var hwErrTypeLabels = [maxNumHWErrTypes]string{
	HW_ERR_MCE:       "mce",
	HW_ERR_EDAC:      "edac",
	HW_ERR_ACPI_GHES: "acpi",
	HW_ERR_PCIE_AER:  "aer",
	HW_ERR_THR:       "thr",
}

const labelType = "type"

// Error severity labels written into RasTracingData.ErrType.
const (
	ErrTypeCorrected              = "Corrected"
	ErrTypeUncorrectedRecoverable = "UncorrectedRecoverable"
	ErrTypeUncorrectedDeferred    = "UncorrectedDeferred"
	ErrTypeUncorrectedFatal       = "UncorrectedFatal"
	ErrTypeInfo                   = "Info"
	ErrTypeUnknown                = "unknown"
)

// The dynamic_array info always lives at the very end of each tracepoint
// event struct.  We read the full 512-byte buffer so the kernel never needs
// to truncate the payload.
//
// Fixed-portion byte counts (the static fields, excluding the 4-byte
// __data_loc field that precedes the dynamic area):
//
//	trace_event_raw_mc_event:           64 − 4 = 60
//	trace_event_raw_non_standard_event: 60 − 4 = 56
//	trace_event_raw_aer_event:          40 − 4 = 36
const (
	RAS_PERFEVENT_INFO_SIZE = 512
	DETAIL_INFO_SIZE_EDAC   = RAS_PERFEVENT_INFO_SIZE - 60
	DETAIL_INFO_SIZE_ACPI   = RAS_PERFEVENT_INFO_SIZE - 56
	DETAIL_INFO_SIZE_AER    = RAS_PERFEVENT_INFO_SIZE - 36
)

// rasEvent mirrors the BPF-side struct event layout.
type rasEvent struct {
	Type      uint32
	Pad0      uint32
	Timestamp uint64
	Info      [RAS_PERFEVENT_INFO_SIZE]byte
}

// RasTracingData is the structured record persisted by tracing.Save.
type RasTracingData struct {
	Device    string `json:"dev"`
	Event     string `json:"event"`
	ErrType   string `json:"type"`
	Timestamp uint64 `json:"timestamp"`
	Info      string `json:"info"`
}

const defaultThrEventBackoff = 30 * time.Minute

type rasTracing struct {
	counts     [maxNumHWErrTypes]atomic.Uint64
	thrBackoff *backoff.Backoff // THR event save cooldown
}

func init() {
	tracing.RegisterEventTracing("ras", newRasTracing)
}

func newRasTracing() (*tracing.EventTracingAttr, error) {
	if !hasRasTracepoint() {
		log.Infof("ras: kernel tracepoint subsystem absent, disabling")
		return nil, types.ErrNotSupported
	}

	backoffDur := defaultThrEventBackoff
	if cfg.Ras.MceThrBackoff > 0 {
		backoffDur = time.Duration(cfg.Ras.MceThrBackoff) * time.Second
	}

	// max == interval so Duration() always returns a flat, non-exponential value.
	thrBO := backoff.NewWithoutJitter(backoffDur, backoffDur)
	thrBO.SetDecay(backoffDur)

	return &tracing.EventTracingAttr{
		TracingData: &rasTracing{thrBackoff: thrBO},
		Interval:    60,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

// hasRasTracepoint reports whether the running kernel exposes the ras:*
// tracepoint family. Without it, CO-RE relocations against
// trace_event_raw_mc_event resolve to a poisoned helper id and BPF load
// fails at the verifier — common on stripped VM kernels (e.g. OrbStack).
func hasRasTracepoint() bool {
	for _, p := range []string{
		"/sys/kernel/tracing/events/ras",
		"/sys/kernel/debug/tracing/events/ras",
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func decodePayload[T any](info []byte) (*T, error) {
	var payload T
	if err := binary.Read(bytes.NewReader(info), binary.LittleEndian, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func cstring(buf []byte, rawOffset, base uint32) string {
	absOff := rawOffset & 0xffff
	if absOff < base {
		return ""
	}
	off := int(absOff - base)
	if off >= len(buf) {
		return ""
	}
	if end := bytes.IndexByte(buf[off:], 0); end >= 0 {
		return string(buf[off : off+end])
	}
	return string(buf[off:])
}

// Bank's MCi_STATUS MSR
//
// #define MCI_STATUS_DEFERRED     BIT_ULL(44)  /* uncorrected error, deferred exception */
// #define MCI_STATUS_UC           BIT_ULL(61)  /* uncorrected error */

const (
	MCI_STATUS_DEFERRED = 1 << 44
	MCI_STATUS_UC       = 1 << 61
)

func mceErrType(status uint64) string {
	if status&MCI_STATUS_DEFERRED != 0 {
		return ErrTypeUncorrectedDeferred
	}
	if status&MCI_STATUS_UC != 0 {
		return ErrTypeUncorrectedRecoverable
	}
	return ErrTypeCorrected
}

// copy from linux kernel include/linux/edac.h
//
//   - enum hw_event_mc_err_type - type of the detected error
//   - @HW_EVENT_ERR_CORRECTED:     Corrected Error
//   - @HW_EVENT_ERR_UNCORRECTED:   Uncorrected Error (non-fatal)
//   - @HW_EVENT_ERR_DEFERRED:      Deferred Error (uncorrectable but not urgent)
//   - @HW_EVENT_ERR_FATAL:         Fatal Error (uncorrectable, unrecoverable)
//   - @HW_EVENT_ERR_INFO:          Informational (CPER informational logs)
func edacErrType(errType uint32) string {
	switch errType {
	case 0x0:
		return ErrTypeCorrected
	case 0x01:
		return ErrTypeUncorrectedRecoverable
	case 0x02:
		return ErrTypeUncorrectedDeferred
	case 0x03:
		return ErrTypeUncorrectedFatal
	case 0x04:
		return ErrTypeInfo
	default:
		return ErrTypeUnknown
	}
}

// acpiErrType maps an ACPI non-standard event severity to an error type.
//
// linux kernel include/acpi/ghes.h
//
//	enum {
//	        GHES_SEV_NO = 0x0,
//	        GHES_SEV_CORRECTED = 0x1,
//	        GHES_SEV_RECOVERABLE = 0x2,
//	        GHES_SEV_PANIC = 0x3,
//	};
//
// ghes_edac_report_mem_error()
//
// GHES_SEV_CORRECTED  → HW_EVENT_ERR_CORRECTED
// GHES_SEV_RECOVERABLE → HW_EVENT_ERR_UNCORRECTED
// GHES_SEV_PANIC      → HW_EVENT_ERR_FATAL
// GHES_SEV_NO         → HW_EVENT_ERR_INFO
func acpiErrType(sev uint8) string {
	switch sev {
	case 0x0:
		return ErrTypeInfo
	case 0x1:
		return ErrTypeCorrected
	case 0x2:
		return ErrTypeUncorrectedRecoverable
	case 0x3:
		return ErrTypeUncorrectedFatal
	default:
		return ErrTypeUnknown
	}
}

// aerErrType maps a PCIe AER severity value to an error type.
//
// linux kernel include/linux/aer.h
//
// AER_CORRECTABLE 2
// AER_FATAL 1
// AER_NONFATAL 0
func aerErrType(severity uint8) string {
	switch severity {
	case 2:
		return ErrTypeCorrected
	case 1:
		return ErrTypeUncorrectedFatal
	case 0:
		return ErrTypeUncorrectedRecoverable
	default:
		return ErrTypeUnknown
	}
}

// ---------------------------------------------------------------------------
// PCI AER error status bits (PCIe Base Spec §7.8.4)
// ---------------------------------------------------------------------------

// Correctable error status bits.
const (
	PciErrCorRcvr     uint32 = 0x00000001 /* Receiver Error */
	PciErrCorBadTlp   uint32 = 0x00000040 /* Bad TLP */
	PciErrCorBadDllp  uint32 = 0x00000080 /* Bad DLLP */
	PciErrCorRepRoll  uint32 = 0x00000100 /* REPLAY_NUM Rollover */
	PciErrCorRepTimer uint32 = 0x00001000 /* Replay Timer Timeout */
	PciErrCorAdvNfat  uint32 = 0x00002000 /* Advisory Non-Fatal */
	PciErrCorInternal uint32 = 0x00004000 /* Corrected Internal */
	PciErrCorLogOver  uint32 = 0x00008000 /* Header Log Overflow */
)

// Uncorrectable error status bits.
const (
	PciErrUncUnd       uint32 = 0x00000001 /* Undefined */
	PciErrUncDlp       uint32 = 0x00000010 /* Data Link Protocol */
	PciErrUncSurpdn    uint32 = 0x00000020 /* Surprise Down */
	PciErrUncPoisonTlp uint32 = 0x00001000 /* Poisoned TLP */
	PciErrUncFcp       uint32 = 0x00002000 /* Flow Control Protocol */
	PciErrUncCompTime  uint32 = 0x00004000 /* Completion Timeout */
	PciErrUncCompAbort uint32 = 0x00008000 /* Completer Abort */
	PciErrUncUnxComp   uint32 = 0x00010000 /* Unexpected Completion */
	PciErrUncRxOver    uint32 = 0x00020000 /* Receiver Overflow */
	PciErrUncMalfTlp   uint32 = 0x00040000 /* Malformed TLP */
	PciErrUncEcrc      uint32 = 0x00080000 /* ECRC Error */
	PciErrUncUnsup     uint32 = 0x00100000 /* Unsupported Request */
	PciErrUncAscv      uint32 = 0x00200000 /* ACS Violation */
	PciErrUncIntn      uint32 = 0x00400000 /* Uncorrectable Internal */
	PciErrUncMcptlp    uint32 = 0x00800000 /* MC Blocked TLP */
	PciErrUncAtomeg    uint32 = 0x01000000 /* AtomicOp Egress Blocked */
	PciErrUncTlpPre    uint32 = 0x02000000 /* TLP Prefix Blocked */
)

var aerCorrectableErrors = map[uint32]string{
	PciErrCorRcvr:     "Receiver Error",
	PciErrCorBadTlp:   "Bad TLP",
	PciErrCorBadDllp:  "Bad DLLP",
	PciErrCorRepRoll:  "REPLAY_NUM Rollover",
	PciErrCorRepTimer: "Replay Timer Timeout",
	PciErrCorAdvNfat:  "Advisory Non-Fatal Error",
	PciErrCorInternal: "Corrected Internal Error",
	PciErrCorLogOver:  "Header Log Overflow",
}

var aerUncorrectableErrors = map[uint32]string{
	PciErrUncUnd:       "Undefined",
	PciErrUncDlp:       "Data Link Protocol Error",
	PciErrUncSurpdn:    "Surprise Down Error",
	PciErrUncPoisonTlp: "Poisoned TLP",
	PciErrUncFcp:       "Flow Control Protocol Error",
	PciErrUncCompTime:  "Completion Timeout",
	PciErrUncCompAbort: "Completer Abort",
	PciErrUncUnxComp:   "Unexpected Completion",
	PciErrUncRxOver:    "Receiver Overflow",
	PciErrUncMalfTlp:   "Malformed TLP",
	PciErrUncEcrc:      "ECRC Error",
	PciErrUncUnsup:     "Unsupported Request Error",
	PciErrUncAscv:      "ACS Violation",
	PciErrUncIntn:      "Uncorrectable Internal Error",
	PciErrUncMcptlp:    "MC Blocked TLP",
	PciErrUncAtomeg:    "AtomicOp Egress Blocked",
	PciErrUncTlpPre:    "TLP Prefix Blocked Error",
}

func pciErrReason(status uint32, correctable bool) string {
	m := aerUncorrectableErrors
	if correctable {
		m = aerCorrectableErrors
	}
	if name, ok := m[status]; ok {
		return name
	}
	return "unknown"
}

func newRasTracingData[T any](ev *rasEvent, device, event, errType string, info T) (*RasTracingData, error) {
	b, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal %s info: %w", event, err)
	}
	return &RasTracingData{
		Timestamp: ev.Timestamp,
		Device:    device,
		Event:     event,
		ErrType:   errType,
		Info:      string(b),
	}, nil
}

// ---------------------------------------------------------------------------
// Per-event-type builder functions
// ---------------------------------------------------------------------------

func buildRasMceTracerData(data *rasEvent) (*RasTracingData, error) {
	// tracepointMcePayload mirrors struct trace_event_raw_mce_record.
	// https://git.kernel.org/pub/scm/linux/kernel/git/netdev/net-next.git/tree/arch/x86/include/uapi/asm/mce.h
	type tracepointMcePayload struct {
		Pad       uint64 `json:"-"`
		Mcgcap    uint64 `json:"mcg_cpu_cap"`
		McgStatus uint64 `json:"mcg_msr_status"`
		Status    uint64 `json:"banks_msr_status"`
		Addr      uint64 `json:"banks_msr_addr"`
		Misc      uint64 `json:"banks_msr_misc"`
		Synd      uint64 `json:"mca_synd_msr"`
		Ipid      uint64 `json:"mca_ipid_msr"`
		Ip        uint64 `json:"instr_pointer"`
		Tsc       uint64 `json:"tsc_timestamp"`
		Walltime  uint64 `json:"walltime"`
		Cpu       uint32 `json:"cpu"`
		Cpuid     uint32 `json:"cpuid"`
		Apicid    uint32 `json:"apicid"`
		Socketid  uint32 `json:"socketid"`
		Cs        uint8  `json:"code_seg"`
		Bank      uint8  `json:"bank"`
		Cpuvendor uint8  `json:"cpuvendor"`
	}

	payload, err := decodePayload[tracepointMcePayload](data.Info[:])
	if err != nil {
		return nil, fmt.Errorf("parse MCE payload: %w", err)
	}
	return newRasTracingData(data, "CPU/MEM", "MCE", mceErrType(payload.Status), payload)
}

func buildRasEdacTracerData(data *rasEvent) (*RasTracingData, error) {
	// tracepointEdacPayload mirrors struct trace_event_raw_mc_event.
	type tracepointEdacPayload struct {
		Pad            uint64
		ErrType        uint32
		ErrorMsgOffset uint32
		LabelOffset    uint32
		ErrCount       uint16
		McIndex        uint8
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

	payload, err := decodePayload[tracepointEdacPayload](data.Info[:])
	if err != nil {
		return nil, fmt.Errorf("parse EDAC payload: %w", err)
	}

	const edacBase uint32 = 60
	dyn := payload.MsgDetail[:]
	errType := edacErrType(payload.ErrType)

	return newRasTracingData(data, "MEM", "EDAC", errType, struct {
		ErrCount uint16 `json:"err_count"`
		ErrType  string `json:"err_type"`
		Msg      string `json:"err_msg"`
		Label    string `json:"label"`
		McIndex  uint8  `json:"mc_index"`
		TopLayer int8   `json:"top_layer"`
		MidLayer int8   `json:"mid_layer"`
		LowLayer int8   `json:"low_layer"`
		Addr     uint64 `json:"addr"`
		Grain    uint64 `json:"grain"`
		Syndrome uint64 `json:"syndrome"`
		Driver   string `json:"driver"`
	}{
		ErrCount: payload.ErrCount,
		ErrType:  errType,
		Msg:      cstring(dyn, payload.ErrorMsgOffset, edacBase),
		Label:    cstring(dyn, payload.LabelOffset, edacBase),
		McIndex:  payload.McIndex,
		TopLayer: payload.TopLayer,
		MidLayer: payload.MidLayer,
		LowLayer: payload.LowLayer,
		Addr:     payload.Addr,
		Grain:    uint64(1) << payload.GrainBits,
		Syndrome: payload.Syndrome,
		Driver:   cstring(dyn, payload.DriverDetail, edacBase),
	})
}

func buildRasAcpiTracerData(data *rasEvent) (*RasTracingData, error) {
	// tracepointAcpiNonStandardPayload mirrors
	// struct trace_event_raw_non_standard_event.
	type tracepointAcpiNonStandardPayload struct {
		Pad          uint64
		SecType      [16]uint8
		FruID        [16]uint8
		FruTxtOffset uint32
		Sev          uint8
		Pattern      [3]uint8
		Len          uint32
		BufOffset    uint32
		Msg          [DETAIL_INFO_SIZE_ACPI]byte
	}

	payload, err := decodePayload[tracepointAcpiNonStandardPayload](data.Info[:])
	if err != nil {
		return nil, fmt.Errorf("parse ACPI non-standard payload: %w", err)
	}

	const nonStandardBase uint32 = 56
	fru := cstring(payload.Msg[:], payload.FruTxtOffset, nonStandardBase)

	// Extract raw bytes at the FRU text location for the hex dump.
	var rawData []byte
	if absOff := payload.FruTxtOffset & 0xffff; absOff >= nonStandardBase {
		rawData = bytes.Clone(payload.Msg[absOff-nonStandardBase : absOff-nonStandardBase+payload.Len])
	}

	return newRasTracingData(data, "ACPI", "NON_STANDARD", acpiErrType(payload.Sev), struct {
		Severity uint8  `json:"severity"`
		SecType  string `json:"sec_type"`
		FruID    string `json:"fru_id"`
		FruText  string `json:"fru_text"`
		DataLen  uint32 `json:"data_len"`
		RawData  string `json:"raw_data"`
	}{
		Severity: payload.Sev,
		SecType:  fmt.Sprintf("%x", payload.SecType),
		FruID:    fmt.Sprintf("%x", payload.FruID),
		FruText:  fru,
		DataLen:  payload.Len,
		RawData:  fmt.Sprintf("% x", rawData),
	})
}

func buildRasAerTracerData(data *rasEvent) (*RasTracingData, error) {
	// tracepointAerEventPayload mirrors struct trace_event_raw_aer_event.
	type tracepointAerEventPayload struct {
		Pad            uint64
		DevNameOffset  uint32 // __data_loc_dev_name
		Status         uint32
		Severity       uint8
		TlpHeaderValid uint8
		Pattern        [2]uint8
		TlpHeader      [4]uint32
		Msg            [DETAIL_INFO_SIZE_AER]byte
	}

	payload, err := decodePayload[tracepointAerEventPayload](data.Info[:])
	if err != nil {
		return nil, fmt.Errorf("parse PCIe AER payload: %w", err)
	}

	const aerBase uint32 = 36
	dev := cstring(payload.Msg[:], payload.DevNameOffset, aerBase)

	errType := aerErrType(payload.Severity)
	errReason := pciErrReason(payload.Status, payload.Severity == 2)

	tlpHeader := "not available"
	if payload.TlpHeaderValid != 0 {
		tlpHeader = fmt.Sprintf("{%#x,%#x,%#x,%#x}",
			payload.TlpHeader[0], payload.TlpHeader[1],
			payload.TlpHeader[2], payload.TlpHeader[3])
	}

	return newRasTracingData(data, "PCIe "+dev, "AER", errType, struct {
		DevName   string `json:"dev_name"`
		ErrType   string `json:"err_type"`
		ErrReason string `json:"err_reason"`
		TlpHeader string `json:"tlp_header"`
	}{
		DevName:   dev,
		ErrType:   errType,
		ErrReason: errReason,
		TlpHeader: tlpHeader,
	})
}

func buildRasThrTracerData(data *rasEvent) (*RasTracingData, error) {
	// tracepointThrPayload mirrors BPF-side struct thr_info stored in event->info.
	type tracepointThrPayload struct {
		Vector uint32 `json:"vector"`
		CPU    uint32 `json:"cpu"`
	}
	payload, err := decodePayload[tracepointThrPayload](data.Info[:])
	if err != nil {
		return nil, fmt.Errorf("parse THR payload: %w", err)
	}
	return newRasTracingData(data, "CPU", "MCE_THRESHOLD", ErrTypeCorrected, payload)
}

func dispatchRasTracerData(data *rasEvent) (*RasTracingData, error) {
	switch data.Type {
	case HW_ERR_MCE:
		return buildRasMceTracerData(data)
	case HW_ERR_EDAC:
		return buildRasEdacTracerData(data)
	case HW_ERR_ACPI_GHES:
		return buildRasAcpiTracerData(data)
	case HW_ERR_PCIE_AER:
		return buildRasAerTracerData(data)
	case HW_ERR_THR:
		return buildRasThrTracerData(data)
	default:
		return nil, fmt.Errorf("unsupported hardware error type %d", data.Type)
	}
}

func (ras *rasTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "ras_event_map", 8192)
	if err != nil {
		return fmt.Errorf("attach ras event pipe: %w", err)
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	return ras.rasEventLoop(childCtx, reader)
}

func (ras *rasTracing) rasEventLoop(ctx context.Context, reader bpf.PerfEventReader) error {
	var nextThrAllowed time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			var ev rasEvent
			if err := reader.ReadInto(&ev); err != nil {
				return fmt.Errorf("read ras event: %w", err)
			}

			if int(ev.Type) < maxNumHWErrTypes {
				ras.counts[ev.Type].Add(1)
			}

			// THR: backoff to suppress interrupt storms.
			if ev.Type == HW_ERR_THR {
				now := time.Now()
				if now.Before(nextThrAllowed) {
					continue
				}
				nextThrAllowed = now.Add(ras.thrBackoff.Duration())
			}

			tracerData, err := dispatchRasTracerData(&ev)
			if err != nil {
				continue
			}

			if err := tracing.Save(&tracing.WriteRequest{
				TracerName: "ras",
				TracerTime: time.Now(),
				TracerData: tracerData,
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

func (ras *rasTracing) Update() ([]*metric.Data, error) {
	metrics := make([]*metric.Data, maxNumHWErrTypes)
	for i, typeLabel := range hwErrTypeLabels {
		metrics[i] = metric.NewCounterData(
			"hw_err_total",
			float64(ras.counts[i].Load()),
			"total RAS hardware error events by source type",
			map[string]string{labelType: typeLabel},
		)
	}
	return metrics, nil
}
