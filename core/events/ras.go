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
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/ras.c -o $BPF_DIR/ras.o
type rasTracing struct {
	count uint64
}

const (
	HW_ERR_MCE          = 0
	HW_ERR_EDAC         = 1
	HW_ERR_NON_STANDARD = 2
	HW_ERR_AER_EVENT    = 3
)

var (
	Corrected   = "CORRECTED"
	Uncorrected = "UNCORRECTED"
	RecovPanic  = "RECOVERABLE/PANIC"
	Fatal       = "FATAL"
)

// The dynamic_array info is just at the very last place of the event
// struct. We don't know the exact length of the info because it depends
// on the driver. Just read the whole 512 bytes of the perf event output
// Info.
//
// The length of the other part besids data[] are:
// struct trace_event_raw_mc_event: 64 - 4 = 60
// struct trace_event_raw_non_standard_event: 60 - 4 = 56
// struct trace_event_raw_aer_event: 40 - 4 = 36
const (
	RAS_PERFEVENT_INFO_SIZE       = 512
	DETAIL_INFO_SIZE_EDAC         = RAS_PERFEVENT_INFO_SIZE - 60
	DETAIL_INFO_SIZE_NON_STANDARD = RAS_PERFEVENT_INFO_SIZE - 56
	DETAIL_INFO_SIZE_AER          = RAS_PERFEVENT_INFO_SIZE - 36
)

type rasPerfEvent struct {
	Type      uint32
	Corrected uint32
	Timestamp uint64
	Info      [RAS_PERFEVENT_INFO_SIZE]byte
}

type RasTracingData struct {
	Device    string `json:"dev"`
	Event     string `json:"event"`
	ErrType   string `json:"errtype"`
	Timestamp uint64 `json:"timestamp"`
	Info      string `json:"info"`
}

var (
	interruptsPath        = "/proc/interrupts"
	thresholdCount uint64 = 0
)

func init() {
	tracing.RegisterEventTracing("ras", newRasTracing)
}

func newRasTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &rasTracing{},
		Internal:    60,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func CopyFromOffset(src []byte, offset, length int) ([]byte, error) {
	if offset < 0 || offset >= len(src) {
		return nil, fmt.Errorf("offset out of bounds")
	}
	if length <= 0 || offset+length > len(src) {
		return nil, fmt.Errorf("invalid length parameter")
	}

	dst := make([]byte, length)
	if n := copy(dst, src[offset:offset+length]); n != length {
		return nil, fmt.Errorf("incomplete copy")
	}
	return dst, nil
}

const (
	// Correctable errors status
	PciErrCorRcvr     uint32 = 0x00000001 /* Receiver Error Status */
	PciErrCorBadTlp   uint32 = 0x00000040 /* Bad TLP Status */
	PciErrCorBadDllp  uint32 = 0x00000080 /* Bad DLLP Status */
	PciErrCorRepRoll  uint32 = 0x00000100 /* REPLAY_NUM Rollover */
	PciErrCorRepTimer uint32 = 0x00001000 /* Replay Timer Timeout */
	PciErrCorAdvNfat  uint32 = 0x00002000 /* Advisory Non-Fatal */
	PciErrCorInternal uint32 = 0x00004000 /* Corrected Internal */
	PciErrCorLogOver  uint32 = 0x00008000 /* Header Log Overflow */

	// Uncorrectable errors status
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
	PciErrUncEcrc      uint32 = 0x00080000 /* ECRC Error Status */
	PciErrUncUnsup     uint32 = 0x00100000 /* Unsupported Request */
	PciErrUncAscv      uint32 = 0x00200000 /* ACS Violation */
	PciErrUncIntn      uint32 = 0x00400000 /* internal error */
	PciErrUncMcptlp    uint32 = 0x00800000 /* MC blocked TLP */
	PciErrUncAtomeg    uint32 = 0x01000000 /* Atomic egress blocked */
	PciErrUncTlpPre    uint32 = 0x02000000 /* TLP prefix blocked */
)

var aerCorrectablErrors = map[uint32]string{
	PciErrCorRcvr:     "Receiver Error",
	PciErrCorBadTlp:   "Bad TLP",
	PciErrCorBadDllp:  "PciErrCorBadDllp",
	PciErrCorRepRoll:  "RELAY_NUM Rollover",
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

func getPciErr(key uint32, isCorrectable bool) (string, error) {
	if isCorrectable {
		if val, exists := aerCorrectablErrors[key]; exists {
			return val, nil
		}
	} else {
		if val, exists := aerUncorrectableErrors[key]; exists {
			return val, nil
		}
	}

	return "", fmt.Errorf("key not found")
}

func (ras *rasTracing) Start(ctx context.Context) (err error) {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "ras_event_map", 8192)
	if err != nil {
		return fmt.Errorf("attach and event pipe: %w", err)
	}
	defer reader.Close()

	thresholdCount, err = getThrInfo()
	if err != nil {
		return err
	}

	for {
		select {
		case <-childCtx.Done():
			log.Info("ras tracing is stopped.")
			return nil
		default:
			var data rasPerfEvent
			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("read ras perf event fail: %w", err)
			}

			atomic.AddUint64(&ras.count, 1)
			tracerData := &RasTracingData{
				Timestamp: data.Timestamp,
			}

			switch data.Type {
			case HW_ERR_MCE:
				tracerData.Device = "CPU/MEM"
				tracerData.Event = "MCE"
				if data.Corrected == 0 {
					tracerData.ErrType = Uncorrected
				} else {
					tracerData.ErrType = Corrected
				}

				type tpMceRecord struct {
					Pad       uint64
					Mcgcap    uint64
					McgStatus uint64
					Status    uint64
					Addr      uint64
					Misc      uint64
					Synd      uint64
					Ipid      uint64
					Ip        uint64
					Tsc       uint64
					Walltime  uint64
					Cpu       uint32
					Cpuid     uint32
					Apicid    uint32
					Socketid  uint32
					Cs        uint8
					Bank      uint8
					Cpuvendor uint8
				}
				mceRecord := &tpMceRecord{}

				reader := bytes.NewReader(data.Info[:])
				err := binary.Read(reader, binary.LittleEndian, mceRecord)
				if err != nil {
					return fmt.Errorf("parse mce detail info error: %w", err)
				}

				tracerData.Info = fmt.Sprintf("CPU: %d, MCGc/s: %x/%x, MC%d: %016x, "+
					"IPID: %016x, ADDR/MISC/SYND: %016x/%016x/%016x, "+
					"RIP: %02x:<%016x>, TSC: %x, PROCESSOR: %x:%x, "+
					"TIME: %d, SOCKET: %x, APIC: %x",
					mceRecord.Cpu, mceRecord.Mcgcap, mceRecord.McgStatus,
					mceRecord.Bank, mceRecord.Status,
					mceRecord.Ipid, mceRecord.Addr, mceRecord.Misc,
					mceRecord.Synd, mceRecord.Cs, mceRecord.Ip,
					mceRecord.Tsc, mceRecord.Cpuvendor,
					mceRecord.Cpuid, mceRecord.Walltime,
					mceRecord.Socketid, mceRecord.Apicid)

			case HW_ERR_EDAC:
				tracerData.Device = "MEM"
				tracerData.Event = "EDAC"
				if data.Corrected == 0 {
					tracerData.ErrType = Uncorrected
				} else {
					tracerData.ErrType = Corrected
				}

				type tpEdacRecord struct {
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

				edacRecord := &tpEdacRecord{}

				reader := bytes.NewReader(data.Info[:])
				err := binary.Read(reader, binary.LittleEndian, edacRecord)
				if err != nil {
					return fmt.Errorf("parse edac detail info error: %w", err)
				}

				msgDetail := edacRecord.MsgDetail[:]

				// Get the detailed message string base on the offsets
				// Error message
				errMsgOffset := edacRecord.ErrorMsgOffset&0xffff - 60
				strErrorMsgEnd := bytes.IndexByte(msgDetail[errMsgOffset:], 0)
				strErrorMsg := string(msgDetail[errMsgOffset : int(errMsgOffset)+strErrorMsgEnd])

				// Label
				labelOffset := edacRecord.LabelOffset&0xffff - 60
				strLabelEnd := bytes.IndexByte(msgDetail[labelOffset:], 0)
				strLabel := string(msgDetail[labelOffset : int(labelOffset)+strLabelEnd])

				// Driver detail info
				driverDetail := edacRecord.DriverDetail&0xffff - 60
				strDriverDetailEnd := bytes.IndexByte(msgDetail[driverDetail:], 0)
				strDriverDetail := string(msgDetail[driverDetail : int(driverDetail)+strDriverDetailEnd])

				tracerData.Info = fmt.Sprintf("%d %s err: %s on %s "+
					"(mc: %d location:%d:%d:%d "+
					"address: %#x grain:%d syndrome:%#x %s)",
					edacRecord.ErrCount,
					tracerData.ErrType,
					strErrorMsg,
					strLabel,
					edacRecord.McIndex,
					edacRecord.TopLayer,
					edacRecord.MidLayer,
					edacRecord.LowLayer,
					edacRecord.Addr,
					1<<edacRecord.GrainBits,
					edacRecord.Syndrome,
					strDriverDetail,
				)
			case HW_ERR_NON_STANDARD:
				tracerData.Device = "ACPI"
				tracerData.Event = "NON_STANDARD"

				type tpAcpiNonStandardRecord struct {
					Pad          uint64
					SecType      [16]uint8
					FruID        [16]uint8
					FruTxtOffset uint32
					Sev          uint8
					Pattern      [3]uint8
					Len          uint32
					BufOffset    uint32
					Msg          [DETAIL_INFO_SIZE_NON_STANDARD]byte
				}
				nonStandardRecord := &tpAcpiNonStandardRecord{}

				reader := bytes.NewReader(data.Info[:])
				err := binary.Read(reader, binary.LittleEndian, nonStandardRecord)
				if err != nil {
					return fmt.Errorf("parse acpi non_standard detail info error: %w", err)
				}

				if nonStandardRecord.Sev < 2 {
					tracerData.ErrType = Corrected
				} else {
					tracerData.ErrType = RecovPanic
				}

				// get fruTxt
				fruTxt := nonStandardRecord.Msg[:]
				fruTxtOffset := nonStandardRecord.FruTxtOffset&0xffff - 56
				strFruTxtEnd := bytes.IndexByte(fruTxt[fruTxtOffset:], 0)
				strFruTxt := string(fruTxt[fruTxtOffset : int(fruTxtOffset)+strFruTxtEnd])

				rawData, _ := CopyFromOffset(nonStandardRecord.Msg[:], int(fruTxtOffset), int(nonStandardRecord.Len))

				tracerData.Info = fmt.Sprintf("severity: %d; "+
					"sec type:%x; FRU: %x%s; "+
					"data len:%d; raw data:% x",
					nonStandardRecord.Sev,
					nonStandardRecord.SecType,
					nonStandardRecord.FruID,
					strFruTxt,
					nonStandardRecord.Len,
					rawData,
				)
			case HW_ERR_AER_EVENT:
				var strSeverity string
				var strErrDetail string
				var strTlpHeader string

				tracerData.Event = "AER"

				type tpAerEventRecord struct {
					Pad            uint64
					DevNameOffset  uint32
					Status         uint32
					Severity       uint8
					TlpHeaderValid uint8
					Pattern        [2]uint8
					TlpHeader      [4]uint32
					Msg            [DETAIL_INFO_SIZE_AER]byte
				}
				aerEventRecord := &tpAerEventRecord{}

				reader := bytes.NewReader(data.Info[:])
				err := binary.Read(reader, binary.LittleEndian, aerEventRecord)
				if err != nil {
					return fmt.Errorf("parse PCIe detail info error: %w", err)
				}

				// get Device Name
				msg := aerEventRecord.Msg[:]
				devNameOffset := aerEventRecord.DevNameOffset&0xffff - 36
				strDevNameEnd := bytes.IndexByte(msg[devNameOffset:], 0)
				strDevName := string(msg[devNameOffset : int(devNameOffset)+strDevNameEnd])

				if aerEventRecord.Severity == 2 {
					var err error
					strSeverity = "Corrected"
					tracerData.ErrType = Corrected
					strErrDetail, err = getPciErr(aerEventRecord.Status, true)
					if err != nil {
						return fmt.Errorf("parse PCIe correctable error status error: %w", err)
					}
				} else {
					var err error
					if aerEventRecord.Severity == 1 {
						strSeverity = "Fatal"
						tracerData.ErrType = Fatal
					} else {
						strSeverity = "Uncorrected, non-fatal"
						tracerData.ErrType = Uncorrected
					}
					strErrDetail, err = getPciErr(aerEventRecord.Status, false)
					if err != nil {
						return fmt.Errorf("parse PCIe uncorrectable error status error: %w", err)
					}
				}

				if aerEventRecord.TlpHeaderValid != 0 {
					strTlpHeader = fmt.Sprintf("{%#x,%#x,%#x,%#x}",
						aerEventRecord.TlpHeader[0],
						aerEventRecord.TlpHeader[1],
						aerEventRecord.TlpHeader[2],
						aerEventRecord.TlpHeader[3])
				} else {
					strTlpHeader = "Not available"
				}

				tracerData.Device = fmt.Sprintf("PCIe %s", strDevName)

				tracerData.Info = fmt.Sprintf("%s "+
					"PCIe Bus Error: severity=%s, "+
					"%s, TLP Header=%s",
					strDevName, strSeverity, strErrDetail, strTlpHeader)
			}

			storage.Save("ras", "", time.Now(), tracerData)
		}
	}
}

func getThrInfo() (uint64, error) {
	file, err := os.Open(interruptsPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open interrupts: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "THR") {
			var nums []uint64
			var sum uint64

			for _, field := range strings.Fields(line) {
				if num, err := strconv.ParseUint(field, 10, 64); err == nil {
					nums = append(nums, num)
					sum += num
				}
			}

			if len(nums) == 0 {
				return 0, fmt.Errorf("failed to find nums")
			}
			return sum, nil
		}
	}
	return 0, fmt.Errorf("didn't find interrupts info")
}

func (ras *rasTracing) Update() ([]*metric.Data, error) {
	count, err := getThrInfo()
	if err != nil {
		return nil, err
	}

	if thresholdCount < count {
		delta := count - thresholdCount
		thresholdCount = count
		atomic.AddUint64(&ras.count, 1)

		tracerData := &RasTracingData{}

		tracerData.Device = "ACPI"
		tracerData.Event = "Threshold APIC interrupts"
		tracerData.ErrType = Corrected
		tracerData.Info = fmt.Sprintf("%d threshold interrupts occurred, totaling %d", delta, thresholdCount)

		storage.Save("ras", "", time.Now(), tracerData)
	}

	return []*metric.Data{
		metric.NewGaugeData("hw_counter", float64(atomic.LoadUint64(&ras.count)),
			"ras counter", nil),
	}, nil
}
