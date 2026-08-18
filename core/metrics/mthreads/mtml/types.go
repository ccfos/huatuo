// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package mtml

// Return is the MTML return code type (MtmlReturn).
type Return int32

// Buffer sizes used by the string-returning MTML APIs (from mtml.h macros).
const (
	libraryVersionBufferSize     = 32
	driverVersionBufferSize      = 80
	deviceNameBufferSize         = 32
	deviceUUIDBufferSize         = 48
	biosVersionBufferSize        = 64
	deviceSerialNumberBufferSize = 64
	pciSbdfBufferSize            = 32
	memoryVendorBufferSize       = 64
)

// MTML API raw symbols — C function pointers registered at init time via purego.
//
// The Mtml* handles are opaque C pointers; we carry them as uintptr so the GC
// never inspects C-owned objects.
var (
	// Library lifecycle: mtmlLibraryInit(*MtmlLibrary)
	mtmlLibraryInit func(*uintptr) Return
	// mtmlLibraryShutDown(MtmlLibrary*)
	mtmlLibraryShutDown func(uintptr) Return
	// mtmlLibraryGetVersion(MtmlLibrary*, char* version, unsigned int length)
	mtmlLibraryGetVersion func(uintptr, *byte, uint32) Return

	// Enumeration: mtmlLibraryCountDevice(MtmlLibrary*, unsigned int* count)
	mtmlLibraryCountDevice func(uintptr, *uint32) Return
	// mtmlLibraryInitDeviceByIndex(MtmlLibrary*, unsigned int index, MtmlDevice** dev)
	mtmlLibraryInitDeviceByIndex func(uintptr, uint32, *uintptr) Return
	// mtmlLibraryFreeDevice(MtmlDevice*)
	mtmlLibraryFreeDevice func(uintptr) Return

	// System (for driver version): mtmlLibraryInitSystem(MtmlLibrary*, MtmlSystem** sys)
	mtmlLibraryInitSystem      func(uintptr, *uintptr) Return
	mtmlLibraryFreeSystem      func(uintptr) Return
	mtmlSystemGetDriverVersion func(uintptr, *byte, uint32) Return

	// Device identity.
	// mtmlDeviceGetIndex(MtmlDevice*, unsigned int* index)
	mtmlDeviceGetIndex func(uintptr, *uint32) Return
	// mtmlDeviceGetName(MtmlDevice*, char* name, unsigned int length)
	mtmlDeviceGetName func(uintptr, *byte, uint32) Return
	// mtmlDeviceGetUUID(MtmlDevice*, char* uuid, unsigned int length)
	mtmlDeviceGetUUID func(uintptr, *byte, uint32) Return
	// mtmlDeviceGetBrand(MtmlDevice*, MtmlBrandType* type)
	mtmlDeviceGetBrand func(uintptr, *uint32) Return
	// mtmlDeviceGetSerialNumber(MtmlDevice*, unsigned int length, char* serial)
	mtmlDeviceGetSerialNumber func(uintptr, uint32, *byte) Return
	// mtmlDeviceGetMtBiosVersion(MtmlDevice*, char* version, unsigned int length)
	mtmlDeviceGetMtBiosVersion func(uintptr, *byte, uint32) Return
	// mtmlDeviceCountGpuCores(MtmlDevice*, unsigned int* numCores)
	mtmlDeviceCountGpuCores func(uintptr, *uint32) Return
	// mtmlDeviceGetPciInfo(MtmlDevice*, MtmlPciInfo* pci)
	mtmlDeviceGetPciInfo func(uintptr, *mtmlPciInfo) Return
	// mtmlDeviceGetPowerUsage(MtmlDevice*, unsigned int* power) — milliwatts
	mtmlDeviceGetPowerUsage func(uintptr, *uint32) Return
	// mtmlDeviceGetPcieReplayCounter(MtmlDevice*, unsigned int* replayCounter)
	mtmlDeviceGetPcieReplayCounter func(uintptr, *uint32) Return
	// mtmlDeviceGetPerformanceState(MtmlDevice*, int* pstate)
	mtmlDeviceGetPerformanceState func(uintptr, *int32) Return
	// mtmlDeviceGetMusaComputeCapability(MtmlDevice*, int* major, int* minor)
	mtmlDeviceGetMusaComputeCapability func(uintptr, *int32, *int32) Return

	// mtmlDeviceInitGpu(MtmlDevice*, MtmlGpu** gpu)
	mtmlDeviceInitGpu func(uintptr, *uintptr) Return
	mtmlDeviceFreeGpu func(uintptr) Return
	// mtmlGpuGetTemperature(MtmlGpu*, int* temp) — degrees Celsius
	mtmlGpuGetTemperature func(uintptr, *int32) Return
	// mtmlGpuGetUtilization(MtmlGpu*, unsigned int* utilization) — 0-100
	mtmlGpuGetUtilization func(uintptr, *uint32) Return
	// mtmlGpuGetClock(MtmlGpu*, unsigned int* clockMhz)
	mtmlGpuGetClock    func(uintptr, *uint32) Return
	mtmlGpuGetMaxClock func(uintptr, *uint32) Return
	// mtmlGpuGetVoltage(MtmlGpu*, int* voltage) — millivolts
	mtmlGpuGetVoltage func(uintptr, *int32) Return
	// mtmlGpuGetEnforcedPowerLimit(MtmlGpu*, int* limit) — milliwatts
	mtmlGpuGetEnforcedPowerLimit func(uintptr, *int32) Return
	// mtmlGpuGetPowerManagementDefaultLimit(MtmlGpu*, int* limit) — milliwatts
	mtmlGpuGetPowerManagementDefaultLimit func(uintptr, *int32) Return
	// mtmlGpuGetTemperatureThreshold(MtmlGpu*, int thresholdType, int* temp)
	// thresholdType: 0=shutdown, 1=slowdown (confirmed via cmp $0x1,%esi).
	mtmlGpuGetTemperatureThreshold func(uintptr, int32, *int32) Return

	// Memory sub-object: mtmlDeviceInitMemory(MtmlDevice*, MtmlMemory** mem)
	mtmlDeviceInitMemory func(uintptr, *uintptr) Return
	mtmlDeviceFreeMemory func(uintptr) Return
	// mtmlMemoryGetTotal(MtmlMemory*, unsigned long long* total) — bytes
	mtmlMemoryGetTotal func(uintptr, *uint64) Return
	// mtmlMemoryGetUsed(MtmlMemory*, unsigned long long* used) — bytes
	mtmlMemoryGetUsed func(uintptr, *uint64) Return
	// mtmlMemoryGetUtilization(MtmlMemory*, unsigned int* utilization) — 0-100
	mtmlMemoryGetUtilization func(uintptr, *uint32) Return
	// mtmlMemoryGetTemperature(MtmlMemory*, int* temp) — degrees Celsius
	mtmlMemoryGetTemperature func(uintptr, *int32) Return
	// mtmlMemoryGetClock / GetMaxClock(MtmlMemory*, unsigned int* clockMhz)
	mtmlMemoryGetClock    func(uintptr, *uint32) Return
	mtmlMemoryGetMaxClock func(uintptr, *uint32) Return
	// mtmlMemoryGetBusWidth(MtmlMemory*, unsigned int* busWidth) — bits
	mtmlMemoryGetBusWidth func(uintptr, *uint32) Return
	// mtmlMemoryGetBandwidth(MtmlMemory*, unsigned int* bandwidth) — GB/s
	mtmlMemoryGetBandwidth func(uintptr, *uint32) Return
	// mtmlMemoryGetSpeed(MtmlMemory*, unsigned int* speed) — Mbps
	mtmlMemoryGetSpeed func(uintptr, *uint32) Return
	// mtmlMemoryGetType(MtmlMemory*, MtmlMemoryType* type)
	mtmlMemoryGetType func(uintptr, *uint32) Return
	// mtmlMemoryGetVendor(MtmlMemory*, unsigned int length, char* vendor)
	mtmlMemoryGetVendor func(uintptr, uint32, *byte) Return

	// VPU sub-object: mtmlDeviceInitVpu(MtmlDevice*, MtmlVpu** vpu)
	mtmlDeviceInitVpu func(uintptr, *uintptr) Return
	mtmlDeviceFreeVpu func(uintptr) Return
	// mtmlVpuGetUtilization(MtmlVpu*, MtmlCodecUtil* utilization)
	mtmlVpuGetUtilization func(uintptr, *mtmlCodecUtil) Return
	// mtmlVpuGetClock / GetMaxClock(MtmlVpu*, unsigned int* clockMhz)
	mtmlVpuGetClock    func(uintptr, *uint32) Return
	mtmlVpuGetMaxClock func(uintptr, *uint32) Return

	// Fans: mtmlDeviceCountFan(MtmlDevice*, unsigned int* count)
	mtmlDeviceCountFan func(uintptr, *uint32) Return
	// mtmlDeviceGetFanSpeed(MtmlDevice*, unsigned int index, unsigned int* speed) — percent
	mtmlDeviceGetFanSpeed func(uintptr, uint32, *uint32) Return
	// mtmlDeviceGetFanRpm(MtmlDevice*, unsigned int fanIndex, unsigned int* fanRpm) — RPM
	mtmlDeviceGetFanRpm func(uintptr, uint32, *uint32) Return

	// MtLink: mtmlDeviceGetMtLinkSpec(MtmlDevice*, MtmlMtLinkSpec* spec)
	mtmlDeviceGetMtLinkSpec func(uintptr, *mtmlMtLinkSpec) Return
	// mtmlDeviceGetMtLinkState(MtmlDevice*, unsigned int linkId, MtmlMtLinkState* state)
	// MtmlMtLinkState is an enum (int): 0=DOWN, 1=UP, 2=DOWNGRADE.
	mtmlDeviceGetMtLinkState func(uintptr, uint32, *uint32) Return
)

// mtmlPciInfo mirrors the C struct MtmlPciInfo layout for purego.
// sbdf is a fixed char[32]; followed by unsigned ints, two floats (speed), and
// rsvd[6] int padding. Field order/type must match the C definition exactly.
type mtmlPciInfo struct {
	sbdf           [pciSbdfBufferSize]byte
	segment        uint32
	bus            uint32
	device         uint32
	pciDeviceId    uint32
	pciSubsystemId uint32
	busWidth       uint32 // @deprecated, always 0
	pciMaxSpeed    float32
	pciCurSpeed    float32
	pciMaxWidth    uint32
	pciCurWidth    uint32
	pciMaxGen      uint32
	pciCurGen      uint32
	rsvd           [6]int32
}

// mtmlCodecUtil mirrors the C struct MtmlCodecUtil (VPU utilization).
type mtmlCodecUtil struct {
	util    uint32 // overall utilization rate over the sampling period (%)
	period  uint32 // sampling period, microseconds
	encUtil uint32 // encoder utilization rate (%)
	decUtil uint32 // decoder utilization rate (%)
	rsvd    [2]int32
}

// mtmlMtLinkSpec mirrors the C struct MtmlMtLinkSpec.
type mtmlMtLinkSpec struct {
	version   uint32 // 10000*major + 100*minor + patch
	bandWidth uint32 // bandwidth per link in GB/s
	linkNum   uint32 // max number of supported links
	rsvd      [4]uint32
}

// MtmlGpuEngine enum values (for mtmlGpuGetEngineUtilization).
const (
	GpuEngineGeometry = 0
	GpuEngine2D       = 1
	GpuEngine3D       = 2
	GpuEngineCompute  = 3
)

// MtmlMemoryType enum values (for mtmlMemoryGetType).
const (
	MemoryTypeLPDDR4 = 0
	MemoryTypeGDDR6  = 1
)

// Temperature threshold types for mtmlGpuGetTemperatureThreshold.
const (
	TempThresholdShutdown = 0
	TempThresholdSlowdown = 1
)

// MtLink state enum values (MtmlMtLinkState) for mtmlDeviceGetMtLinkState.
const (
	MtLinkStateDown      = 0
	MtLinkStateUp        = 1
	MtLinkStateDowngrade = 2
)

// MTML return codes (MtmlReturn, from mtml.h).
const (
	Success                 Return = 0
	ErrorDriverNotLoaded    Return = 1
	ErrorDriverFailure      Return = 2
	ErrorInvalidArgument    Return = 3
	ErrorNotSupported       Return = 4
	ErrorNoPermission       Return = 5
	ErrorInsufficientSize   Return = 6
	ErrorNotFound           Return = 7
	ErrorInsufficientMemory Return = 8
	ErrorDriverTooOld       Return = 9
	ErrorDriverTooNew       Return = 10
	ErrorTimeout            Return = 11
	ErrorResourceIsBusy     Return = 12
	ErrorUnknown            Return = 999
)
