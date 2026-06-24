// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package dcmi

// Return is the DCMI return code type.
type Return int32

// DeviceType specifies a device sub-component for utilization and frequency queries.
type DeviceType struct {
	Code int32
	Name string
}

// Predefined DeviceType values for dcmi_get_device_utilization_rate.
var (
	DeviceTypeAICore     = DeviceType{Code: 2, Name: "aicore"}
	DeviceTypeAICPU      = DeviceType{Code: 3, Name: "aicpu"}
	DeviceTypeCtrlCPU    = DeviceType{Code: 4, Name: "ctrl_cpu"}
	DeviceTypeHBM        = DeviceType{Code: 6, Name: "hbm"}
	DeviceTypeVectorCore = DeviceType{Code: 12, Name: "vector_core"}
)

// Predefined DeviceType values for dcmi_get_device_frequency.
var (
	FreqTypeCtrlCPU     = DeviceType{Code: 2, Name: "ctrl_cpu"}
	FreqTypeAICore      = DeviceType{Code: 7, Name: "aicore"}
	FreqTypeAICoreRated = DeviceType{Code: 9, Name: "aicore_rated"}
)

// DCMI API raw symbols — C function pointers registered at init time.
var (
	// Initialization
	dcmiInit func() Return

	// Device health: dcmi_get_device_health(card_id, device_id, *health)
	dcGetDeviceHealth func(uint32, uint32, *uint32) Return

	// Device enumeration
	// dcmi_get_card_list(*card_num, *card_list, list_len)
	dcGetCardList func(*int32, *int32, int32) Return
	// dcmi_get_device_num_in_card(card_id, *device_num)
	dcGetDeviceNumInCard func(int32, *int32) Return

	// Device power: dcmi_get_device_power_info(card_id, device_id, *power)
	dcGetDevicePowerInfo func(uint32, uint32, *int32) Return

	// Device temperature: dcmi_get_device_temperature(card_id, device_id, *temperature)
	dcGetDeviceTemperature func(uint32, uint32, *int32) Return

	// Device voltage: dcmi_get_device_voltage(card_id, device_id, *voltage)
	dcGetDeviceVoltage func(uint32, uint32, *uint32) Return

	// Device utilization: dcmi_get_device_utilization_rate(card_id, device_id, input_type, *rate)
	dcGetDeviceUtilizationRate func(uint32, uint32, int32, *uint32) Return

	// Device frequency: dcmi_get_device_frequency(card_id, device_id, freq_type, *freq)
	dcGetDeviceFrequency func(uint32, uint32, int32, *uint32) Return

	// Device network health: dcmi_get_device_network_health(card_id, device_id, *result)
	dcGetDeviceNetWorkHealth func(uint32, uint32, *uint32) Return

	// HBM info: dcmi_get_device_hbm_info(card_id, device_id, *hbm_info)
	dcGetDeviceHbmInfo func(uint32, uint32, *dcmiHbmInfo) Return

	// ECC info: dcmi_get_device_ecc_info(card_id, device_id, device_type, *ecc_info)
	dcGetDeviceEccInfo func(uint32, uint32, int32, *dcmiEccInfo) Return

	// PCIe info: dcmi_get_device_pcie_info_v2(card_id, device_id, *pcie_info)
	dcGetDevicePcieInfoV2 func(uint32, uint32, *dcmiPcieInfoAll) Return

	// ID mapping: dcmi_get_device_logic_id(*logic_id, card_id, device_id)
	dcGetDeviceLogicID func(*int32, int32, int32) Return
	// ID mapping: dcmi_get_device_phyid_from_logicid(logic_id, *phyid)
	dcGetPhysicIDFromLogicID func(uint32, *uint32) Return
)

// DcmiDeviceType specifies the component type for ECC queries.
type DcmiDeviceType int32

const (
	DcmiDeviceTypeDDR  DcmiDeviceType = 0
	DcmiDeviceTypeSRAM DcmiDeviceType = 1
	DcmiDeviceTypeHBM  DcmiDeviceType = 2
	DcmiDeviceTypeNPU  DcmiDeviceType = 3
	DcmiDeviceTypeNONE DcmiDeviceType = 0xff
)

// HbmInfo describes HBM memory information returned by dcmi_get_device_hbm_info.
type HbmInfo struct {
	MemorySize        uint64 // total size, MB
	Frequency         uint32 // frequency, MHz
	Usage             uint64 // memory usage, MB
	Temp              int32  // temperature, degrees Celsius
	BandWidthUtilRate uint32 // bandwidth utilization, %
}

// ECCInfo describes ECC error information returned by dcmi_get_device_ecc_info.
type ECCInfo struct {
	EnableFlag                int32
	SingleBitErrorCnt         int64
	DoubleBitErrorCnt         int64
	TotalSingleBitErrorCnt    int64
	TotalDoubleBitErrorCnt    int64
	SingleBitIsolatedPagesCnt int64
	DoubleBitIsolatedPagesCnt int64
}

// dcmiHbmInfo matches the C struct dcmi_hbm_info layout for purego.
type dcmiHbmInfo struct {
	memorySize        uint64 // unsigned long long
	freq              uint32 // unsigned int
	_                 uint32 // padding
	memoryUsage       uint64 // unsigned long long
	temp              int32  // int
	bandWidthUtilRate uint32 // unsigned int
}

// dcmiPcieInfoAll matches the C struct dcmi_pcie_info_all layout for purego.
// Total size must be 64 bytes.
type dcmiPcieInfoAll struct {
	_           uint32   // venderid (unsigned int)
	_           uint32   // subvenderid (unsigned int)
	_           uint32   // deviceid (unsigned int)
	_           uint32   // subdeviceid (unsigned int)
	domain      int32    // int
	bdfBusid    uint32   // unsigned int
	bdfDeviceid uint32   // unsigned int
	bdfFuncid   uint32   // unsigned int
	_           [32]byte // reserve
}

// dcmiEccInfo matches the C struct dcmi_ecc_info layout for purego.
type dcmiEccInfo struct {
	enableFlag                int32  // int
	singleBitErrorCnt         uint32 // unsigned int
	doubleBitErrorCnt         uint32 // unsigned int
	totalSingleBitErrorCnt    uint32 // unsigned int
	totalDoubleBitErrorCnt    uint32 // unsigned int
	singleBitIsolatedPagesCnt uint32 // unsigned int
	doubleBitIsolatedPagesCnt uint32 // unsigned int
	_                         uint32 // single_bit_next_isolated_pages_cnt
	_                         uint32 // double_bit_next_isolated_pages_cnt
}
