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

// LibraryVersion returns the MTML library version string.
func LibraryVersion() (string, error) {
	if !IsReady() {
		return "", errNotReady
	}
	if mtmlLibraryGetVersion == nil {
		return "", errNotSupportedSymbol("mtmlLibraryGetVersion")
	}
	return getString("mtmlLibraryGetVersion", libmtml.Lib(), libraryVersionBufferSize,
		func(buf []byte) Return {
			return mtmlLibraryGetVersion(libmtml.Lib(), &buf[0], uint32(len(buf)))
		})
}

// CountDevice returns the number of MTML-visible GPU devices.
func CountDevice() (uint32, error) {
	if !IsReady() {
		return 0, errNotReady
	}
	var count uint32
	if err := checkReturnCode("mtmlLibraryCountDevice", mtmlLibraryCountDevice(libmtml.Lib(), &count)); err != nil {
		return 0, err
	}
	return count, nil
}

// Device is a live MtmlDevice* handle. Close it with Close when done.
type Device struct{ handle uintptr }

// InitDeviceByIndex returns the device at the given index. Close it when done.
func InitDeviceByIndex(index uint32) (*Device, error) {
	if !IsReady() {
		return nil, errNotReady
	}
	var dev uintptr
	if err := checkReturnCode("mtmlLibraryInitDeviceByIndex",
		mtmlLibraryInitDeviceByIndex(libmtml.Lib(), index, &dev)); err != nil {
		return nil, err
	}
	return &Device{handle: dev}, nil
}

// Close frees the device handle (mtmlLibraryFreeDevice).
func (d *Device) Close() error {
	if d.handle == 0 {
		return nil
	}
	err := checkReturnCode("mtmlLibraryFreeDevice", mtmlLibraryFreeDevice(d.handle))
	if err == nil {
		d.handle = 0
	}
	return err
}

// Index returns the device index.
func (d *Device) Index() (uint32, error) {
	if mtmlDeviceGetIndex == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetIndex")
	}
	var idx uint32
	if err := checkReturnCode("mtmlDeviceGetIndex", mtmlDeviceGetIndex(d.handle, &idx)); err != nil {
		return 0, err
	}
	return idx, nil
}

// Name returns the device product name.
func (d *Device) Name() (string, error) {
	if mtmlDeviceGetName == nil {
		return "", errNotSupportedSymbol("mtmlDeviceGetName")
	}
	return getString("mtmlDeviceGetName", d.handle, deviceNameBufferSize,
		func(buf []byte) Return {
			return mtmlDeviceGetName(d.handle, &buf[0], uint32(len(buf)))
		})
}

// UUID returns the device UUID.
func (d *Device) UUID() (string, error) {
	if mtmlDeviceGetUUID == nil {
		return "", errNotSupportedSymbol("mtmlDeviceGetUUID")
	}
	return getString("mtmlDeviceGetUUID", d.handle, deviceUUIDBufferSize,
		func(buf []byte) Return {
			return mtmlDeviceGetUUID(d.handle, &buf[0], uint32(len(buf)))
		})
}

func (d *Device) Brand() (Brand, error) {
	if mtmlDeviceGetBrand == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetBrand")
	}
	var brand uint32
	if err := checkReturnCode("mtmlDeviceGetBrand", mtmlDeviceGetBrand(d.handle, &brand)); err != nil {
		return 0, err
	}
	return Brand(brand), nil
}

// SerialNumber returns the device serial number.
// NOTE: MTML's signature is (dev, length, buf) — length before buffer.
func (d *Device) SerialNumber() (string, error) {
	if mtmlDeviceGetSerialNumber == nil {
		return "", errNotSupportedSymbol("mtmlDeviceGetSerialNumber")
	}
	return getStringSerialLenFirst("mtmlDeviceGetSerialNumber", d.handle, deviceSerialNumberBufferSize,
		func(buf []byte) Return {
			return mtmlDeviceGetSerialNumber(d.handle, uint32(len(buf)), &buf[0])
		})
}

// BiosVersion returns the device BIOS version.
func (d *Device) BiosVersion() (string, error) {
	if mtmlDeviceGetMtBiosVersion == nil {
		return "", errNotSupportedSymbol("mtmlDeviceGetMtBiosVersion")
	}
	return getString("mtmlDeviceGetMtBiosVersion", d.handle, biosVersionBufferSize,
		func(buf []byte) Return {
			return mtmlDeviceGetMtBiosVersion(d.handle, &buf[0], uint32(len(buf)))
		})
}

// GpuCores returns the number of GPU cores.
func (d *Device) GpuCores() (uint32, error) {
	if mtmlDeviceCountGpuCores == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceCountGpuCores")
	}
	var cores uint32
	if err := checkReturnCode("mtmlDeviceCountGpuCores", mtmlDeviceCountGpuCores(d.handle, &cores)); err != nil {
		return 0, err
	}
	return cores, nil
}

// PciSbdf returns the PCI SBDF string (e.g. "0000:01:00.0").
func (d *Device) PciSbdf() (string, error) {
	if mtmlDeviceGetPciInfo == nil {
		return "", errNotSupportedSymbol("mtmlDeviceGetPciInfo")
	}
	var info mtmlPciInfo
	if err := checkReturnCode("mtmlDeviceGetPciInfo", mtmlDeviceGetPciInfo(d.handle, &info)); err != nil {
		return "", err
	}
	return cString(info.sbdf[:]), nil
}

// PciInfo returns the parsed PCIe link info (current/max speed in GT/s and width in lanes).
func (d *Device) PciInfo() (*PciLinkInfo, error) {
	if mtmlDeviceGetPciInfo == nil {
		return nil, errNotSupportedSymbol("mtmlDeviceGetPciInfo")
	}
	var info mtmlPciInfo
	if err := checkReturnCode("mtmlDeviceGetPciInfo", mtmlDeviceGetPciInfo(d.handle, &info)); err != nil {
		return nil, err
	}
	return &PciLinkInfo{
		CurSpeed: float64(info.pciCurSpeed),
		MaxSpeed: float64(info.pciMaxSpeed),
		CurWidth: info.pciCurWidth,
		MaxWidth: info.pciMaxWidth,
	}, nil
}

// PcieReplayCounter returns the PCIe replay (retransmission) counter.
func (d *Device) PcieReplayCounter() (uint32, error) {
	if mtmlDeviceGetPcieReplayCounter == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetPcieReplayCounter")
	}
	var count uint32
	if err := checkReturnCode("mtmlDeviceGetPcieReplayCounter",
		mtmlDeviceGetPcieReplayCounter(d.handle, &count)); err != nil {
		return 0, err
	}
	return count, nil
}

// PciLinkInfo holds PCIe link speed (GT/s) and width (lanes).
type PciLinkInfo struct {
	CurSpeed float64 // current link speed, GT/s
	MaxSpeed float64 // max link speed, GT/s
	CurWidth uint32  // current link width, lanes
	MaxWidth uint32  // max link width, lanes
}

// PowerUsage returns the device power consumption in milliwatts.
func (d *Device) PowerUsage() (uint32, error) {
	if mtmlDeviceGetPowerUsage == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetPowerUsage")
	}
	var power uint32
	if err := checkReturnCode("mtmlDeviceGetPowerUsage", mtmlDeviceGetPowerUsage(d.handle, &power)); err != nil {
		return 0, err
	}
	return power, nil
}

// PerformanceState returns the device performance state (P-state) as a numeric
// value (0 = P0, etc.).
func (d *Device) PerformanceState() (int32, error) {
	if mtmlDeviceGetPerformanceState == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetPerformanceState")
	}
	var pstate int32
	if err := checkReturnCode("mtmlDeviceGetPerformanceState",
		mtmlDeviceGetPerformanceState(d.handle, &pstate)); err != nil {
		return 0, err
	}
	return pstate, nil
}

// MusaComputeCapability returns the MUSA compute capability major/minor version
// (e.g. major=3, minor=0).
func (d *Device) MusaComputeCapability() (major, minor int32, err error) {
	if mtmlDeviceGetMusaComputeCapability == nil {
		return 0, 0, errNotSupportedSymbol("mtmlDeviceGetMusaComputeCapability")
	}
	if err := checkReturnCode("mtmlDeviceGetMusaComputeCapability",
		mtmlDeviceGetMusaComputeCapability(d.handle, &major, &minor)); err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

// FanCount returns the number of fans on the device.
func (d *Device) FanCount() (uint32, error) {
	if mtmlDeviceCountFan == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceCountFan")
	}
	var count uint32
	if err := checkReturnCode("mtmlDeviceCountFan", mtmlDeviceCountFan(d.handle, &count)); err != nil {
		return 0, err
	}
	return count, nil
}

// FanSpeed returns the fan speed as a percentage of the max noise-tolerance
// speed (may exceed 100%).
func (d *Device) FanSpeed(fanIndex uint32) (uint32, error) {
	if mtmlDeviceGetFanSpeed == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetFanSpeed")
	}
	var speed uint32
	if err := checkReturnCode("mtmlDeviceGetFanSpeed", mtmlDeviceGetFanSpeed(d.handle, fanIndex, &speed)); err != nil {
		return 0, err
	}
	return speed, nil
}

// FanRpm returns the fan speed in RPM.
func (d *Device) FanRpm(fanIndex uint32) (uint32, error) {
	if mtmlDeviceGetFanRpm == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetFanRpm")
	}
	var rpm uint32
	if err := checkReturnCode("mtmlDeviceGetFanRpm", mtmlDeviceGetFanRpm(d.handle, fanIndex, &rpm)); err != nil {
		return 0, err
	}
	return rpm, nil
}

// Gpu is a live MtmlGpu* handle.
type Gpu struct{ handle uintptr }

// InitGpu returns the Gpu sub-object of the device. Close it when done.
func (d *Device) InitGpu() (*Gpu, error) {
	if mtmlDeviceInitGpu == nil {
		return nil, errNotSupportedSymbol("mtmlDeviceInitGpu")
	}
	var gpu uintptr
	if err := checkReturnCode("mtmlDeviceInitGpu", mtmlDeviceInitGpu(d.handle, &gpu)); err != nil {
		return nil, err
	}
	return &Gpu{handle: gpu}, nil
}

// Close frees the gpu handle.
func (g *Gpu) Close() error {
	if g.handle == 0 {
		return nil
	}
	if mtmlDeviceFreeGpu == nil {
		g.handle = 0
		return errNotSupportedSymbol("mtmlDeviceFreeGpu")
	}
	err := checkReturnCode("mtmlDeviceFreeGpu", mtmlDeviceFreeGpu(g.handle))
	g.handle = 0
	return err
}

// Temperature returns the GPU temperature in degrees Celsius.
func (g *Gpu) Temperature() (int32, error) {
	if mtmlGpuGetTemperature == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetTemperature")
	}
	var temp int32
	if err := checkReturnCode("mtmlGpuGetTemperature", mtmlGpuGetTemperature(g.handle, &temp)); err != nil {
		return 0, err
	}
	return temp, nil
}

// Utilization returns the GPU utilization as a percentage (0-100).
func (g *Gpu) Utilization() (uint32, error) {
	if mtmlGpuGetUtilization == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetUtilization")
	}
	var util uint32
	if err := checkReturnCode("mtmlGpuGetUtilization", mtmlGpuGetUtilization(g.handle, &util)); err != nil {
		return 0, err
	}
	return util, nil
}

// Clock returns the GPU clock frequency in MHz.
func (g *Gpu) Clock() (uint32, error) {
	if mtmlGpuGetClock == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlGpuGetClock", mtmlGpuGetClock(g.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// MaxClock returns the GPU maximum clock frequency in MHz.
func (g *Gpu) MaxClock() (uint32, error) {
	if mtmlGpuGetMaxClock == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetMaxClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlGpuGetMaxClock", mtmlGpuGetMaxClock(g.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// Voltage returns the GPU voltage in millivolts.
func (g *Gpu) Voltage() (int32, error) {
	if mtmlGpuGetVoltage == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetVoltage")
	}
	var v int32
	if err := checkReturnCode("mtmlGpuGetVoltage", mtmlGpuGetVoltage(g.handle, &v)); err != nil {
		return 0, err
	}
	return v, nil
}

// EnforcedPowerLimit returns the enforced GPU power limit in milliwatts.
func (g *Gpu) EnforcedPowerLimit() (int32, error) {
	if mtmlGpuGetEnforcedPowerLimit == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetEnforcedPowerLimit")
	}
	var limit int32
	if err := checkReturnCode("mtmlGpuGetEnforcedPowerLimit",
		mtmlGpuGetEnforcedPowerLimit(g.handle, &limit)); err != nil {
		return 0, err
	}
	return limit, nil
}

// PowerManagementDefaultLimit returns the default GPU power management limit in milliwatts.
func (g *Gpu) PowerManagementDefaultLimit() (int32, error) {
	if mtmlGpuGetPowerManagementDefaultLimit == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetPowerManagementDefaultLimit")
	}
	var limit int32
	if err := checkReturnCode("mtmlGpuGetPowerManagementDefaultLimit",
		mtmlGpuGetPowerManagementDefaultLimit(g.handle, &limit)); err != nil {
		return 0, err
	}
	return limit, nil
}

// TemperatureThreshold returns a GPU temperature threshold in degrees Celsius.
// thresholdType is a TempThreshold* constant (0=shutdown, 1=slowdown).
func (g *Gpu) TemperatureThreshold(thresholdType int32) (int32, error) {
	if mtmlGpuGetTemperatureThreshold == nil {
		return 0, errNotSupportedSymbol("mtmlGpuGetTemperatureThreshold")
	}
	var temp int32
	if err := checkReturnCode("mtmlGpuGetTemperatureThreshold",
		mtmlGpuGetTemperatureThreshold(g.handle, thresholdType, &temp)); err != nil {
		return 0, err
	}
	return temp, nil
}

// Memory is a live MtmlMemory* handle.
type Memory struct{ handle uintptr }

// InitMemory returns the Memory sub-object of the device. Close it when done.
func (d *Device) InitMemory() (*Memory, error) {
	if mtmlDeviceInitMemory == nil {
		return nil, errNotSupportedSymbol("mtmlDeviceInitMemory")
	}
	var mem uintptr
	if err := checkReturnCode("mtmlDeviceInitMemory", mtmlDeviceInitMemory(d.handle, &mem)); err != nil {
		return nil, err
	}
	return &Memory{handle: mem}, nil
}

// Close frees the memory handle.
func (m *Memory) Close() error {
	if m.handle == 0 {
		return nil
	}
	if mtmlDeviceFreeMemory == nil {
		m.handle = 0
		return errNotSupportedSymbol("mtmlDeviceFreeMemory")
	}
	err := checkReturnCode("mtmlDeviceFreeMemory", mtmlDeviceFreeMemory(m.handle))
	m.handle = 0
	return err
}

// Total returns the total VRAM in bytes.
func (m *Memory) Total() (uint64, error) {
	if mtmlMemoryGetTotal == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetTotal")
	}
	var total uint64
	if err := checkReturnCode("mtmlMemoryGetTotal", mtmlMemoryGetTotal(m.handle, &total)); err != nil {
		return 0, err
	}
	return total, nil
}

// Used returns the used VRAM in bytes.
func (m *Memory) Used() (uint64, error) {
	if mtmlMemoryGetUsed == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetUsed")
	}
	var used uint64
	if err := checkReturnCode("mtmlMemoryGetUsed", mtmlMemoryGetUsed(m.handle, &used)); err != nil {
		return 0, err
	}
	return used, nil
}

// Utilization returns the memory utilization as a percentage (0-100).
func (m *Memory) Utilization() (uint32, error) {
	if mtmlMemoryGetUtilization == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetUtilization")
	}
	var util uint32
	if err := checkReturnCode("mtmlMemoryGetUtilization", mtmlMemoryGetUtilization(m.handle, &util)); err != nil {
		return 0, err
	}
	return util, nil
}

// Temperature returns the memory temperature in degrees Celsius.
func (m *Memory) Temperature() (int32, error) {
	if mtmlMemoryGetTemperature == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetTemperature")
	}
	var temp int32
	if err := checkReturnCode("mtmlMemoryGetTemperature", mtmlMemoryGetTemperature(m.handle, &temp)); err != nil {
		return 0, err
	}
	return temp, nil
}

// Clock returns the memory clock frequency in MHz.
func (m *Memory) Clock() (uint32, error) {
	if mtmlMemoryGetClock == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlMemoryGetClock", mtmlMemoryGetClock(m.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// MaxClock returns the memory maximum clock frequency in MHz.
func (m *Memory) MaxClock() (uint32, error) {
	if mtmlMemoryGetMaxClock == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetMaxClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlMemoryGetMaxClock", mtmlMemoryGetMaxClock(m.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// BusWidth returns the memory bus width in bits.
func (m *Memory) BusWidth() (uint32, error) {
	if mtmlMemoryGetBusWidth == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetBusWidth")
	}
	var width uint32
	if err := checkReturnCode("mtmlMemoryGetBusWidth", mtmlMemoryGetBusWidth(m.handle, &width)); err != nil {
		return 0, err
	}
	return width, nil
}

// Bandwidth returns the memory bandwidth in GB/s.
func (m *Memory) Bandwidth() (uint32, error) {
	if mtmlMemoryGetBandwidth == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetBandwidth")
	}
	var bw uint32
	if err := checkReturnCode("mtmlMemoryGetBandwidth", mtmlMemoryGetBandwidth(m.handle, &bw)); err != nil {
		return 0, err
	}
	return bw, nil
}

// Speed returns the memory speed in Mbps.
func (m *Memory) Speed() (uint32, error) {
	if mtmlMemoryGetSpeed == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetSpeed")
	}
	var speed uint32
	if err := checkReturnCode("mtmlMemoryGetSpeed", mtmlMemoryGetSpeed(m.handle, &speed)); err != nil {
		return 0, err
	}
	return speed, nil
}

// MemoryType returns the memory type code (0 = LPDDR4, 1 = GDDR6).
func (m *Memory) MemoryType() (uint32, error) {
	if mtmlMemoryGetType == nil {
		return 0, errNotSupportedSymbol("mtmlMemoryGetType")
	}
	var typ uint32
	if err := checkReturnCode("mtmlMemoryGetType", mtmlMemoryGetType(m.handle, &typ)); err != nil {
		return 0, err
	}
	return typ, nil
}

// Vendor returns the memory vendor string.
// NOTE: MTML's signature is (mem, length, buf) — length before buffer.
func (m *Memory) Vendor() (string, error) {
	if mtmlMemoryGetVendor == nil {
		return "", errNotSupportedSymbol("mtmlMemoryGetVendor")
	}
	return getStringSerialLenFirst("mtmlMemoryGetVendor", m.handle, memoryVendorBufferSize,
		func(buf []byte) Return {
			return mtmlMemoryGetVendor(m.handle, uint32(len(buf)), &buf[0])
		})
}

// Vpu is a live MtmlVpu* handle.
type Vpu struct{ handle uintptr }

// InitVpu returns the VPU sub-object of the device. Close it when done.
func (d *Device) InitVpu() (*Vpu, error) {
	if mtmlDeviceInitVpu == nil {
		return nil, errNotSupportedSymbol("mtmlDeviceInitVpu")
	}
	var vpu uintptr
	if err := checkReturnCode("mtmlDeviceInitVpu", mtmlDeviceInitVpu(d.handle, &vpu)); err != nil {
		return nil, err
	}
	return &Vpu{handle: vpu}, nil
}

// Close frees the vpu handle.
func (v *Vpu) Close() error {
	if v.handle == 0 {
		return nil
	}
	if mtmlDeviceFreeVpu == nil {
		v.handle = 0
		return errNotSupportedSymbol("mtmlDeviceFreeVpu")
	}
	err := checkReturnCode("mtmlDeviceFreeVpu", mtmlDeviceFreeVpu(v.handle))
	v.handle = 0
	return err
}

// CodecUtil returns the VPU (codec) utilization. encUtil/decUtil are the
// encoder/decoder utilization rates as percentages.
type CodecUtil struct {
	Util    uint32 // overall utilization, %
	Period  uint32 // sampling period, microseconds
	EncUtil uint32 // encoder utilization, %
	DecUtil uint32 // decoder utilization, %
}

// Utilization returns the VPU codec utilization.
func (v *Vpu) Utilization() (*CodecUtil, error) {
	if mtmlVpuGetUtilization == nil {
		return nil, errNotSupportedSymbol("mtmlVpuGetUtilization")
	}
	var raw mtmlCodecUtil
	if err := checkReturnCode("mtmlVpuGetUtilization", mtmlVpuGetUtilization(v.handle, &raw)); err != nil {
		return nil, err
	}
	return &CodecUtil{
		Util:    raw.util,
		Period:  raw.period,
		EncUtil: raw.encUtil,
		DecUtil: raw.decUtil,
	}, nil
}

// Clock returns the VPU clock frequency in MHz.
func (v *Vpu) Clock() (uint32, error) {
	if mtmlVpuGetClock == nil {
		return 0, errNotSupportedSymbol("mtmlVpuGetClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlVpuGetClock", mtmlVpuGetClock(v.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// MaxClock returns the VPU maximum clock frequency in MHz.
func (v *Vpu) MaxClock() (uint32, error) {
	if mtmlVpuGetMaxClock == nil {
		return 0, errNotSupportedSymbol("mtmlVpuGetMaxClock")
	}
	var clock uint32
	if err := checkReturnCode("mtmlVpuGetMaxClock", mtmlVpuGetMaxClock(v.handle, &clock)); err != nil {
		return 0, err
	}
	return clock, nil
}

// MtLinkSpec holds MtLink specifications.
type MtLinkSpec struct {
	Version   uint32 // 10000*major + 100*minor + patch
	BandWidth uint32 // Gb/s per link
	LinkNum   uint32 // max number of supported links
}

// MtLinkSpec returns the device's MtLink specifications.
func (d *Device) MtLinkSpec() (*MtLinkSpec, error) {
	if mtmlDeviceGetMtLinkSpec == nil {
		return nil, errNotSupportedSymbol("mtmlDeviceGetMtLinkSpec")
	}
	var spec mtmlMtLinkSpec
	if err := checkReturnCode("mtmlDeviceGetMtLinkSpec",
		mtmlDeviceGetMtLinkSpec(d.handle, &spec)); err != nil {
		return nil, err
	}
	return &MtLinkSpec{
		Version:   spec.version,
		BandWidth: spec.bandWidth,
		LinkNum:   spec.linkNum,
	}, nil
}

// MtLinkState returns the state of MtLink link linkId: 0=DOWN, 1=UP, 2=DOWNGRADE.
func (d *Device) MtLinkState(linkId uint32) (uint32, error) {
	if mtmlDeviceGetMtLinkState == nil {
		return 0, errNotSupportedSymbol("mtmlDeviceGetMtLinkState")
	}
	var state uint32
	if err := checkReturnCode("mtmlDeviceGetMtLinkState",
		mtmlDeviceGetMtLinkState(d.handle, linkId, &state)); err != nil {
		return 0, err
	}
	return state, nil
}

// getString allocates a buffer, invokes fn to fill it, and returns the C string
// truncated at the first NUL byte. For APIs with signature (handle, *byte, length).
func getString(symbol string, _ uintptr, size int, fn func(buf []byte) Return) (string, error) {
	buf := make([]byte, size)
	clear(buf)
	if err := checkReturnCode(symbol, fn(buf)); err != nil {
		return "", err
	}
	return cString(buf), nil
}

// getStringSerialLenFirst is like getString but for APIs with the unusual
// signature (handle, length, *byte) — e.g. mtmlDeviceGetSerialNumber,
// mtmlMemoryGetVendor.
func getStringSerialLenFirst(symbol string, _ uintptr, size int, fn func(buf []byte) Return) (string, error) {
	return getString(symbol, 0, size, fn)
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
