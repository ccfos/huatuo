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

package device

// Brand defines the GPU vendor.
type Brand uint32

const (
	BrandUnknown Brand = iota
	BrandN
	BrandC
	BrandG
)

// VirtualizationMode defines the virtualization mode.
type VirtualizationMode uint32

const (
	VirtualizationModeNone VirtualizationMode = iota // None.
	VirtualizationModePf                             // Physical Function.
	VirtualizationModeVf                             // Virtual Function.
)

// Info describes basic device information.
type Info struct {
	DeviceId   uint32             // Device ID.
	_          uint32             // DEPRECATED.
	BDFId      [32]byte           // PCI BDF.
	GpuId      uint32             // GPU index.
	NodeId     uint32             // Node ID.
	UUID       [96]byte           // Device UUID.
	Brand      Brand              // Device brand.
	Mode       VirtualizationMode // Virtualization mode.
	DeviceName [32]byte           // Device name.
}

// DeviceVersionUnit defines version type.
type DeviceVersionUnit uint32

const (
	DeviceVersionUnitBios   DeviceVersionUnit = iota // BIOS.
	DeviceVersionUnitDriver                          // Driver.
)

// BoardWayElectricInfo describes board electrical data.
type BoardWayElectricInfo struct {
	Voltage uint32 // Voltage in mV.
	Current uint32 // Current in mA.
	Power   uint32 // Power in mW.
}

// PcieLinkInfo describes PCIe link.
type PcieLinkInfo struct {
	Speed float32 // Speed in GT/s.
	Width uint32  // Lane width.
}

// PcieThroughputInfo describes PCIe throughput.
type PcieThroughputInfo struct {
	ReceiveRate  int32 // RX MB/s.
	TransmitRate int32 // TX MB/s.
}

// MetaXLink describes MetaX interconnect links.
const MetaXLinkMaxNumber = 7 // Max link count.

type MetaXLinkType uint32

const (
	MetaXLinkTypeReceive  MetaXLinkType = iota // RX.
	MetaXLinkTypeTransmit                      // TX.
)

// MetaXLinkLinkInfo describes link capability.
type MetaXLinkLinkInfo struct {
	Speed float32 // Speed in GT/s.
	Width uint32  // Lane width.
}

// MetaXLinkThroughputInfo describes throughput.
type MetaXLinkThroughputInfo struct {
	ReceiveRate  int32 // RX MB/s.
	TransmitRate int32 // TX MB/s.
}

// MetaXLinkTrafficStatInfo describes traffic counters.
type MetaXLinkTrafficStatInfo struct {
	Receive  int64 // RX bytes.
	Transmit int64 // TX bytes.
}

// MetaXLinkAerInfo describes AER errors.
type MetaXLinkAerInfo struct {
	CorrectableErrorsCount   int32 // Correctable errors.
	UncorrectableErrorsCount int32 // Uncorrectable errors.
}

// DieMemoryInfo describes die memory usage.
type DieMemoryInfo struct {
	Total int64 // Total KB.
	Used  int64 // Used KB.
}

// DieEccMemoryInfo describes die ECC errors.
type DieEccMemoryInfo struct {
	SramCorrectableErrorsCount   uint32 // SRAM CE.
	SramUncorrectableErrorsCount uint32 // SRAM UE.
	DramCorrectableErrorsCount   uint32 // DRAM CE.
	DramUncorrectableErrorsCount uint32 // DRAM UE.
	RetiredPagesCount            uint32 // Retired pages.
}
