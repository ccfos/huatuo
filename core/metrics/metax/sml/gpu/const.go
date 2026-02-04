// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The MetaX Authors
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

package gpu

import (
	"huatuo-bamai/core/metrics/metax/sml/device"
)

// Series represents device product series.
type Series string

const (
	Unknown Series = "unknown" // Unknown series.
	SeriesN Series = "mxn"     // N series.
	SeriesC Series = "mxc"     // C series.
	SeriesG Series = "mxg"     // G series.
)

// Mode represents device virtualization mode.
type Mode string

const (
	ModeNative Mode = "native" // Native (non-virtualized).
	ModePf     Mode = "pf"     // PCIe Physical Function.
	ModeVf     Mode = "vf"     // PCIe Virtual Function.
)

// Info describes basic device metadata.
type Info struct {
	Series      Series // Product series.
	Model       string // Device model.
	UUID        string // Device UUID.
	BiosVersion string // BIOS version.
	BDF         string // PCI BDF.
	Mode        Mode   // Virtualization mode.
	DieCount    uint32 // Number of dies.
}

// SeriesMap maps device brand to series.
var SeriesMap = map[device.Brand]Series{
	device.BrandUnknown: Unknown,
	device.BrandN:       SeriesN,
	device.BrandC:       SeriesC,
	device.BrandG:       SeriesG,
}

// ModeMap maps virtualization mode to string mode.
var ModeMap = map[device.VirtualizationMode]Mode{
	device.VirtualizationModeNone: ModeNative,
	device.VirtualizationModePf:   ModePf,
	device.VirtualizationModeVf:   ModeVf,
}

// TemperatureSensor represents temperature sensor type.
type TemperatureSensor uint32

const (
	TemperatureSensorHotspot TemperatureSensor = iota // Hotspot sensor.
)

// UsageIp represents IP utilization domain.
type UsageIp uint32

const (
	UsageIpDla   UsageIp = iota // DLA.
	UsageIpVpue                 // Video encoder.
	UsageIpVpud                 // Video decoder.
	UsageIpG2d                  // 2D graphics.
	UsageIpXcore                // XCore.
)

// ClockIp represents clock domain.
type ClockIp uint32

const (
	ClockIpCsc   ClockIp = iota // CSC.
	ClockIpDla                  // DLA.
	ClockIpMc                   // Memory controller.
	ClockIpMc0                  // Memory controller 0.
	ClockIpMc1                  // Memory controller 1.
	ClockIpVpue                 // Video encoder.
	ClockIpVpud                 // Video decoder.
	ClockIpSoc                  // SoC.
	ClockIpDnoc                 // DNOC.
	ClockIpG2d                  // 2D graphics.
	ClockIpCcx                  // CCX.
	ClockIpXcore                // XCore.
)

// DpmIp represents DPM domain.
type DpmIp uint32

const (
	DpmIpDla      DpmIp = iota // DLA.
	DpmIpXcore                 // XCore.
	DpmIpMc                    // Memory controller.
	DpmIpSoc                   // SoC.
	DpmIpDnoc                  // DNOC.
	DpmIpVpue                  // Video encoder.
	DpmIpVpud                  // Video decoder.
	DpmIpHbm                   // HBM.
	DpmIpG2d                   // 2D graphics.
	DpmIpHbmPower              // HBM power.
	DpmIpCcx                   // CCX.
	DpmIpIpGroup               // IP group.
	DpmIpDma                   // DMA.
	DpmIpCsc                   // CSC.
	DpmIpEth                   // Ethernet.
	DpmIpDidt                  // DIDT.
	DpmIpReserved              // Reserved.
)

var (
	// UtilizationIpMap maps logical name to usage IP.
	UtilizationIpMap = map[string]UsageIp{
		"encoder": UsageIpVpue,
		"decoder": UsageIpVpud,
		"xcore":   UsageIpXcore,
	}

	// ClockIpMap maps logical name to clock IP.
	ClockIpMap = map[string]ClockIp{
		"encoder": ClockIpVpue,
		"decoder": ClockIpVpud,
		"xcore":   ClockIpXcore,
		"memory":  ClockIpMc0,
	}

	// DpmIpMap maps logical name to DPM IP.
	DpmIpMap = map[string]DpmIp{
		"xcore": DpmIpXcore,
	}
)

// ClocksThrottleBitReasonMap maps throttle reason bit to description.
var ClocksThrottleBitReasonMap = map[int]string{
	1:  "idle",
	2:  "application_limit",
	3:  "over_power",
	4:  "chip_overheated",
	5:  "vr_overheated",
	6:  "hbm_overheated",
	7:  "thermal_overheated",
	8:  "pcc",
	9:  "power_brake",
	10: "didt",
	11: "low_usage",
	12: "other",
}
