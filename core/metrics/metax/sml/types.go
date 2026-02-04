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

package sml

import (
	"huatuo-bamai/core/metrics/metax/sml/device"
	"huatuo-bamai/core/metrics/metax/sml/gpu"
)

// MetaX SML RETURN CODE
type Return int32

// MetaX SML API Struct
type (
	PcieInfo             = device.PcieLinkInfo
	MetaXLinkAer         = device.MetaXLinkAerInfo
	PcieThroughput       = device.PcieThroughputInfo
	SingleMetaXLinkInfo  = device.MetaXLinkLinkInfo
	BoardWayElectricInfo = device.BoardWayElectricInfo
	EccErrorCount        = device.DieEccMemoryInfo
)

// MetaXLinkTrafficStat describes MetaXLink traffic statistics.
type MetaXLinkTrafficStat struct {
	RequestTrafficStat  int64 // requestTrafficStat in bytes.
	ResponseTrafficStat int64 // responseTrafficStat in bytes.
}

// MetaXLinkBandwidth describes MetaXLink bandwidth information.
type MetaXLinkBandwidth struct {
	RequestBandwidth  int32 // requestBandwidth in MB/s.
	ResponseBandwidth int32 // responseBandwidth in MB/s.
}

// DeviceUnavailableReasonInfo describes device unavailable reason.
type DeviceUnavailableReasonInfo struct {
	unavailableCode int32
	_               [64]byte // unavailableReason, not used yet.
}

// MemoryInfo describes device memory usage.
type MemoryInfo struct {
	_         int64 // visVramTotal in KB, not used yet.
	_         int64 // visVramUse in KB, not used yet.
	vramTotal int64 // vramTotal in KB.
	vramUse   int64 // vramUse in KB.
	_         int64 // xttTotal in KB, not used yet.
	_         int64 // xttUse in KB, not used yet.
}

// MetaX SML API RAW SYMBOLS
var (
	// Error and initialization symbols
	mxSmlInit           func() Return
	mxSmlGetErrorString func(Return) string

	// MACA module symbols
	mxSmlGetMacaVersion func(*byte, *uint32) Return

	// Device symbols
	mxSmlGetDeviceCount    func() uint32
	mxSmlGetPfDeviceCount  func() uint32
	mxSmlGetDeviceInfo     func(uint32, *device.Info) Return
	mxSmlGetDeviceDieCount func(uint32, *uint32) Return
	mxSmlGetDeviceVersion  func(uint32, device.DeviceVersionUnit, *byte, *uint32) Return

	// Board power symbols
	mxSmlGetBoardPowerInfo func(uint32, *uint32, *BoardWayElectricInfo) Return

	// PCIe symbols
	mxSmlGetPcieInfo       func(uint32, *PcieInfo) Return
	mxSmlGetPcieThroughput func(uint32, *PcieThroughput) Return

	// MetaXLink symbols
	mxSmlGetMetaXLinkInfo_v2     func(uint32, *uint32, *SingleMetaXLinkInfo) Return
	mxSmlGetMetaXLinkBandwidth   func(uint32, device.MetaXLinkType, *uint32, *MetaXLinkBandwidth) Return
	mxSmlGetMetaXLinkTrafficStat func(uint32, device.MetaXLinkType, *uint32, *MetaXLinkTrafficStat) Return
	mxSmlGetMetaXLinkAer         func(uint32, *uint32, *MetaXLinkAer) Return

	// Die symbols
	mxSmlGetDieUnavailableReason           func(uint32, uint32, *DeviceUnavailableReasonInfo) Return
	mxSmlGetDieTemperatureInfo             func(uint32, uint32, gpu.TemperatureSensor, *int32) Return
	mxSmlGetDieIpUsage                     func(uint32, uint32, gpu.UsageIp, *int32) Return
	mxSmlGetDieMemoryInfo                  func(uint32, uint32, *MemoryInfo) Return
	mxSmlGetDieClocks                      func(uint32, uint32, gpu.ClockIp, *uint32, *uint32) Return
	mxSmlGetDieCurrentClocksThrottleReason func(uint32, uint32, *uint64) Return
	mxSmlGetCurrentDieDpmIpPerfLevel       func(uint32, uint32, gpu.DpmIp, *uint32) Return
	mxSmlGetDieTotalEccErrors              func(uint32, uint32, *EccErrorCount) Return
)
