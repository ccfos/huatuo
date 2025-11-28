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

package collector

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"

	"github.com/ebitengine/purego"

	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type metaxGpuCollector struct{}

func init() {
	tracing.RegisterEventTracing("metax_gpu", newMetaxGpuCollector)
}

func newMetaxGpuCollector() (*tracing.EventTracingAttr, error) {
	// Load MetaX SML library
	if _, err := metaxLoadSmlLibrary(); err != nil {
		return nil, fmt.Errorf("failed to load sml library: %v", err)
	}

	// Init MetaX SML
	if err := metaxInitSml(); err != nil {
		return nil, fmt.Errorf("failed to init sml: %v", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &metaxGpuCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (m *metaxGpuCollector) Update() ([]*metric.Data, error) {
	var metrics []*metric.Data

	// MACA version
	if macaVersion, err := metaxGetMacaVersion(); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get maca version: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("maca_sdk_info", 1, "GPU MACA SDK info.", map[string]string{
				"version": macaVersion,
			}),
		)
	}

	var gpus []int

	// Native and VF GPUs
	nativeAndVfGpuCount := int(metaxGetNativeAndVfGpuCount())
	for i := 0; i < nativeAndVfGpuCount; i++ {
		gpus = append(gpus, i)
	}

	// PF GPUs
	pfGpuCount := int(metaxGetPfGpuCount())
	for i := 100; i < 100+pfGpuCount; i++ {
		gpus = append(gpus, i)
	}

	// Metrics
	for _, gpu := range gpus {
		gpuMetrics, err := metaxGetGpuMetrics(uint(gpu))
		if err != nil {
			return nil, fmt.Errorf("failed to get gpu %d metrics: %v", gpu, err)
		}
		metrics = append(metrics, gpuMetrics...)
	}

	return metrics, nil
}

/*
   GPU metrics
*/

func metaxGetGpuMetrics(gpu uint) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// GPU info
	gpuInfo, err := metaxGetGpuInfo(gpu)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu info: %v", err)
	}
	metrics = append(metrics,
		metric.NewGaugeData("info", 1, "GPU info.", map[string]string{
			"gpu":       strconv.Itoa(int(gpu)),
			"series":    string(gpuInfo.series),
			"model":     gpuInfo.model,
			"uuid":      gpuInfo.uuid,
			"bdf":       gpuInfo.bdf,
			"mode":      string(gpuInfo.mode),
			"die_count": strconv.Itoa(int(gpuInfo.dieCount)),
		}),
	)

	// GPU status
	if gpuStatus, err := metaxGetGpuStatus(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get gpu status: %v", err)
	} else {
		metric.NewGaugeData("available", float64(gpuStatus), "GPU availability, 0 means not available, 1 means available.", map[string]string{
			"gpu": strconv.Itoa(int(gpu)),
		})
	}

	// Board electric
	if boardWayElectricInfos, err := metaxListGpuBoardWayElectricInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list board way electric infos: %v", err)
	} else {
		for i, info := range boardWayElectricInfos {
			metrics = append(metrics,
				metric.NewGaugeData("board_voltage_volts", float64(info.voltage)/1000, "Voltage of each power supply of the GPU board.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"way": strconv.Itoa(i),
				}),
				metric.NewGaugeData("board_current_amperes", float64(info.current)/1000, "Current of each power supply of the GPU board.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"way": strconv.Itoa(i),
				}),
				metric.NewGaugeData("board_power_watts", float64(info.power)/1000, "Power of each power supply of the GPU board.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"way": strconv.Itoa(i),
				}),
			)
		}
	}

	// PCIe link
	if pcieLinkInfo, err := metaxGetGpuPcieLinkInfo(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get pcie link info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("pcie_link_speed_transfers_per_second", pcieLinkInfo.speed*1000*1000*1000, "GPU PCIe current link speed.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
			metric.NewGaugeData("pcie_link_width_lanes", float64(pcieLinkInfo.width), "GPU PCIe current link width.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
		)
	}

	// PCIe link max
	if pcieLinkMaxInfo, err := metaxGetGpuPcieLinkMaxInfo(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get pcie link mx info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("pcie_link_speed_max_transfers_per_second", pcieLinkMaxInfo.speed*1000*1000*1000, "GPU PCIe max link speed.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
			metric.NewGaugeData("pcie_link_width_max_lanes", float64(pcieLinkMaxInfo.width), "GPU PCIe max link width.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
		)
	}

	// PCIe throughput
	if pcieThroughputInfo, err := metaxGetGpuPcieThroughputInfo(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get pcie throughput info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("pcie_receive_bytes_per_second", float64(pcieThroughputInfo.receiveRate)*1000*1000, "GPU PCIe receive throughput.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
			metric.NewGaugeData("pcie_transmit_bytes_per_second", float64(pcieThroughputInfo.transmitRate)*1000*1000, "GPU PCIe transmit throughput.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
		)
	}

	// MetaXLink link
	if metaxlinkLinkInfos, err := metaxListGpuMetaxlinkLinkInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink link infos: %v", err)
	} else {
		for i, info := range metaxlinkLinkInfos {
			metrics = append(metrics,
				metric.NewGaugeData("metaxlink_link_speed_transfers_per_second", info.speed*1000*1000*1000, "GPU MetaXLink current link speed.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
				metric.NewGaugeData("metaxlink_link_width_lanes", float64(info.width), "GPU MetaXLink current link width.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
			)
		}
	}

	// MetaXLink throughput
	if metaxlinkThroughputInfos, err := metaxListGpuMetaxlinkThroughputInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink throughput infos: %v", err)
	} else {
		for i, info := range metaxlinkThroughputInfos {
			metrics = append(metrics,
				metric.NewGaugeData("metaxlink_receive_bytes_per_second", float64(info.receiveRate)*1000*1000, "GPU MetaXLink receive throughput.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
				metric.NewGaugeData("metaxlink_transmit_bytes_per_second", float64(info.transmitRate)*1000*1000, "GPU MetaXLink transmit throughput.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
			)
		}
	}

	// MetaXLink traffic stat
	if metaxlinkTrafficStatInfos, err := metaxListGpuMetaxlinkTrafficStatInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink traffic stat infos: %v", err)
	} else {
		for i, info := range metaxlinkTrafficStatInfos {
			metrics = append(metrics,
				metric.NewCounterData("metaxlink_receive_bytes_total", float64(info.receive), "GPU MetaXLink receive data size.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
				metric.NewCounterData("metaxlink_transmit_bytes_total", float64(info.transmit), "GPU MetaXLink transmit data size.", map[string]string{
					"gpu":       strconv.Itoa(int(gpu)),
					"metaxlink": strconv.Itoa(i + 1),
				}),
			)
		}
	}

	// MetaXLink AER errors
	if metaxlinkAerErrorsInfos, err := metaxListGpuMetaxlinkAerErrorsInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink aer errors infos: %v", err)
	} else {
		for i, info := range metaxlinkAerErrorsInfos {
			metrics = append(metrics,
				metric.NewCounterData("metaxlink_aer_errors_total", float64(info.correctableErrorsCount), "GPU MetaXLink AER errors count.", map[string]string{
					"gpu":        strconv.Itoa(int(gpu)),
					"metaxlink":  strconv.Itoa(i + 1),
					"error_type": "ce",
				}),
				metric.NewCounterData("metaxlink_aer_errors_total", float64(info.uncorrectableErrorsCount), "GPU MetaXLink AER errors count.", map[string]string{
					"gpu":        strconv.Itoa(int(gpu)),
					"metaxlink":  strconv.Itoa(i + 1),
					"error_type": "ue",
				}),
			)
		}
	}

	// Die
	for die := 0; die < int(gpuInfo.dieCount); die++ {
		dieMetrics, err := metaxGetDieMetrics(gpu, uint(die), gpuInfo.series)
		if err != nil {
			return nil, fmt.Errorf("failed to get die %d metrics: %v", die, err)
		}
		metrics = append(metrics, dieMetrics...)
	}

	return metrics, nil
}

/*
   Die metrics
*/

var (
	metaxGpuTemperatureSensorMap = map[string]metaxSmlTemperatureSensor{
		"chip_hotspot": metaxSmlTemperatureSensorHotspot,
	}
	metaxGpuUtilizationIpMap = map[string]metaxSmlUsageIp{
		"vpue":  metaxSmlUsageIpVpue,
		"vpud":  metaxSmlUsageIpVpud,
		"xcore": metaxSmlUsageIpXcore,
	}
	metaxGpuClockIpMap = map[string]metaxSmlClockIp{
		"vpue":   metaxSmlClockIpVpue,
		"vpud":   metaxSmlClockIpVpud,
		"xcore":  metaxSmlClockIpXcore,
		"memory": metaxSmlClockIpMc0,
	}
	metaxGpuDpmIpMap = map[string]metaxSmlDpmIp{
		"xcore": metaxSmlDpmIpXcore,
	}
)

var metaxGpuClocksThrottleBitReasonMap = map[int]string{
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

func metaxGetDieMetrics(gpu, die uint, series metaxGpuSeries) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// Die status
	if dieStatus, err := metaxGetDieStatus(gpu, die); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get die status: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("status", float64(dieStatus), "GPU status, 0 means normal, other values means abnormal. Check the documentation to see the exceptions corresponding to each value.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
	}

	// Temperature
	for sensor, sensorC := range metaxGpuTemperatureSensorMap {
		if value, err := metaxGetDieTemperature(gpu, die, sensorC); metaxIsSmlOperationNotSupportedError(err) {

		} else if err != nil {
			return nil, fmt.Errorf("failed to get %s temperature: %v", sensor, err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("temperature_celsius", value, "Temperature of each GPU sensor.", map[string]string{
					"gpu":    strconv.Itoa(int(gpu)),
					"die":    strconv.Itoa(int(die)),
					"sensor": sensor,
				}),
			)
		}
	}

	// Utilization
	for ip, ipC := range metaxGpuUtilizationIpMap {
		if value, err := metaxGetDieUtilization(gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {

		} else if err != nil {
			return nil, fmt.Errorf("failed to get %s utilization: %v", ip, err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("utilization_percent", float64(value), "GPU utilization, ranging from 0 to 100.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"die": strconv.Itoa(int(die)),
					"ip":  ip,
				}),
			)
		}
	}

	// Memory
	if memoryInfo, err := metaxGetDieMemoryInfo(gpu, die); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get memory info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("memory_total_bytes", float64(memoryInfo.total)*1000, "Total vram.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
			metric.NewGaugeData("memory_used_bytes", float64(memoryInfo.used)*1000, "Used vram.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
	}

	// Clock
	for ip, ipC := range metaxGpuClockIpMap {
		// SPECIAL >>
		if ip == "memory" && series == metaxGpuSeriesN {
			ipC = metaxSmlClockIpMc
		}
		// << END

		if values, err := metaxListDieClocks(gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {

		} else if err != nil {
			return nil, fmt.Errorf("failed to list %s clocks: %v", ip, err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("clock_hertz", float64(values[0])*1000*1000, "GPU clock.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"die": strconv.Itoa(int(die)),
					"ip":  ip,
				}),
			)
		}
	}

	// Clocks throttle status
	if clocksThrottleStatus, err := metaxGetDieClocksThrottleStatus(gpu, die); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get clocks throttle status: %v", err)
	} else {
		bits := getBitsFromLsbToMsb(clocksThrottleStatus)

		for i, v := range bits {
			if i > len(metaxGpuClocksThrottleBitReasonMap) {
				break
			}

			if v == 0 {
				// Metrics are not exported when not throttling.
				continue
			}

			metrics = append(metrics,
				metric.NewGaugeData("clocks_throttling", float64(v), "Reason(s) for GPU clocks throttling.", map[string]string{
					"gpu":    strconv.Itoa(int(gpu)),
					"die":    strconv.Itoa(int(die)),
					"reason": metaxGpuClocksThrottleBitReasonMap[i],
				}),
			)
		}
	}

	// DPM performance level
	for ip, ipC := range metaxGpuDpmIpMap {
		if value, err := metaxGetDieDpmPerformanceLevel(gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {

		} else if err != nil {
			return nil, fmt.Errorf("failed to get %s dpm performance level: %v", ip, err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("dpm_performance_level", float64(value), "GPU DPM performance level.", map[string]string{
					"gpu": strconv.Itoa(int(gpu)),
					"die": strconv.Itoa(int(die)),
					"ip":  ip,
				}),
			)
		}
	}

	// Ecc memory
	if eccMemoryInfo, err := metaxGetDieEccMemoryInfo(gpu, die); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get ecc memory info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewCounterData("ecc_memory_errors_total", float64(eccMemoryInfo.sramCorrectableErrorsCount), "GPU ECC memory errors count.", map[string]string{
				"gpu":         strconv.Itoa(int(gpu)),
				"die":         strconv.Itoa(int(die)),
				"memory_type": "sram",
				"error_type":  "ce",
			}),
			metric.NewCounterData("ecc_memory_errors_total", float64(eccMemoryInfo.sramUncorrectableErrorsCount), "GPU ECC memory errors count.", map[string]string{
				"gpu":         strconv.Itoa(int(gpu)),
				"die":         strconv.Itoa(int(die)),
				"memory_type": "sram",
				"error_type":  "ue",
			}),
			metric.NewCounterData("ecc_memory_errors_total", float64(eccMemoryInfo.dramCorrectableErrorsCount), "GPU ECC memory errors count.", map[string]string{
				"gpu":         strconv.Itoa(int(gpu)),
				"die":         strconv.Itoa(int(die)),
				"memory_type": "dram",
				"error_type":  "ce",
			}),
			metric.NewCounterData("ecc_memory_errors_total", float64(eccMemoryInfo.dramUncorrectableErrorsCount), "GPU ECC memory errors count.", map[string]string{
				"gpu":         strconv.Itoa(int(gpu)),
				"die":         strconv.Itoa(int(die)),
				"memory_type": "dram",
				"error_type":  "ue",
			}),
			metric.NewCounterData("ecc_memory_retired_pages_total", float64(eccMemoryInfo.retiredPagesCount), "GPU ECC memory retired pages count.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
	}

	return metrics, nil
}

func getBitsFromLsbToMsb(x uint64) []uint8 {
	bits := make([]uint8, 64)
	for i := 0; i < 64; i++ {
		bits[i] = uint8((x >> i) & 1)
	}
	return bits
}

/*
   MetaX SML API
*/

var (
	mxSmlGetErrorString                    func(metaxSmlReturnCode) string
	mxSmlInit                              func() metaxSmlReturnCode
	mxSmlGetMacaVersion                    func(*byte, *uint) metaxSmlReturnCode
	mxSmlGetDeviceCount                    func() uint
	mxSmlGetPfDeviceCount                  func() uint
	mxSmlGetDeviceInfo                     func(uint, *metaxSmlDeviceInfo) metaxSmlReturnCode
	mxSmlGetDeviceDieCount                 func(uint, *uint) metaxSmlReturnCode
	mxSmlGetDeviceState                    func(uint, *int) metaxSmlReturnCode
	mxSmlGetBoardPowerInfo                 func(uint, *uint, *metaxSmlBoardWayElectricInfo) metaxSmlReturnCode
	mxSmlGetPcieInfo                       func(uint, *metaxSmlPcieInfo) metaxSmlReturnCode
	mxSmlGetPcieMaxLinkInfo                func(uint, *metaxSmlPcieInfo) metaxSmlReturnCode
	mxSmlGetPcieThroughput                 func(uint, *metaxSmlPcieThroughput) metaxSmlReturnCode
	mxSmlGetMetaXLinkInfo_v2               func(uint, *uint, *metaxSmlSingleMetaxlinkInfo) metaxSmlReturnCode
	mxSmlGetMetaXLinkBandwidth             func(uint, metaxSmlMetaxlinkType, *uint, *metaxSmlMetaXLinkBandwidth) metaxSmlReturnCode
	mxSmlGetMetaXLinkTrafficStat           func(uint, metaxSmlMetaxlinkType, *uint, *metaxSmlMetaxlinkTrafficStat) metaxSmlReturnCode
	mxSmlGetMetaXLinkAer                   func(uint, *uint, *metaxSmlMetaxlinkAer) metaxSmlReturnCode
	mxSmlGetDieUnavailableReason           func(uint, uint, *metaxSmlDeviceUnavailableReasonInfo) metaxSmlReturnCode
	mxSmlGetDieTemperatureInfo             func(uint, uint, metaxSmlTemperatureSensor, *int) metaxSmlReturnCode
	mxSmlGetDieIpUsage                     func(uint, uint, metaxSmlUsageIp, *int) metaxSmlReturnCode
	mxSmlGetDieMemoryInfo                  func(uint, uint, *metaxSmlMemoryInfo) metaxSmlReturnCode
	mxSmlGetDieClocks                      func(uint, uint, metaxSmlClockIp, *uint, *uint) metaxSmlReturnCode
	mxSmlGetDieCurrentClocksThrottleReason func(uint, uint, *uint64) metaxSmlReturnCode
	mxSmlGetCurrentDieDpmIpPerfLevel       func(uint, uint, metaxSmlDpmIp, *uint) metaxSmlReturnCode
	mxSmlGetDieTotalEccErrors              func(uint, uint, *metaxSmlEccErrorCount) metaxSmlReturnCode
)

func metaxGetSmlLibraryPath() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return "/opt/mxdriver/lib/libmxsml.so", nil
	default:
		return "", fmt.Errorf("GOOS=%s is not supported", runtime.GOOS)
	}
}

func metaxLoadSmlLibrary() (uintptr, error) {
	path, err := metaxGetSmlLibraryPath()
	if err != nil {
		return 0, fmt.Errorf("failed to get sml library path: %v", err)
	}

	libc, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("failed to open sml library: %v", err)
	}

	metaxRegisterSmlLibraryFunctions(libc)

	return libc, nil
}

func metaxUnloadSmlLibrary(libc uintptr) error {
	return purego.Dlclose(libc)
}

func metaxRegisterSmlLibraryFunctions(libc uintptr) {
	purego.RegisterLibFunc(&mxSmlGetErrorString, libc, "mxSmlGetErrorString")
	purego.RegisterLibFunc(&mxSmlInit, libc, "mxSmlInit")
	purego.RegisterLibFunc(&mxSmlGetMacaVersion, libc, "mxSmlGetMacaVersion")
	purego.RegisterLibFunc(&mxSmlGetDeviceCount, libc, "mxSmlGetDeviceCount")
	purego.RegisterLibFunc(&mxSmlGetPfDeviceCount, libc, "mxSmlGetPfDeviceCount")
	purego.RegisterLibFunc(&mxSmlGetDeviceInfo, libc, "mxSmlGetDeviceInfo")
	purego.RegisterLibFunc(&mxSmlGetDeviceDieCount, libc, "mxSmlGetDeviceDieCount")
	purego.RegisterLibFunc(&mxSmlGetDeviceState, libc, "mxSmlGetDeviceState")
	purego.RegisterLibFunc(&mxSmlGetBoardPowerInfo, libc, "mxSmlGetBoardPowerInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieInfo, libc, "mxSmlGetPcieInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieMaxLinkInfo, libc, "mxSmlGetPcieMaxLinkInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieThroughput, libc, "mxSmlGetPcieThroughput")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkInfo_v2, libc, "mxSmlGetMetaXLinkInfo_v2")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkBandwidth, libc, "mxSmlGetMetaXLinkBandwidth")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkTrafficStat, libc, "mxSmlGetMetaXLinkTrafficStat")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkAer, libc, "mxSmlGetMetaXLinkAer")
	purego.RegisterLibFunc(&mxSmlGetDieUnavailableReason, libc, "mxSmlGetDieUnavailableReason")
	purego.RegisterLibFunc(&mxSmlGetDieTemperatureInfo, libc, "mxSmlGetDieTemperatureInfo")
	purego.RegisterLibFunc(&mxSmlGetDieIpUsage, libc, "mxSmlGetDieIpUsage")
	purego.RegisterLibFunc(&mxSmlGetDieMemoryInfo, libc, "mxSmlGetDieMemoryInfo")
	purego.RegisterLibFunc(&mxSmlGetDieClocks, libc, "mxSmlGetDieClocks")
	purego.RegisterLibFunc(&mxSmlGetDieCurrentClocksThrottleReason, libc, "mxSmlGetDieCurrentClocksThrottleReason")
	purego.RegisterLibFunc(&mxSmlGetCurrentDieDpmIpPerfLevel, libc, "mxSmlGetCurrentDieDpmIpPerfLevel")
	purego.RegisterLibFunc(&mxSmlGetDieTotalEccErrors, libc, "mxSmlGetDieTotalEccErrors")
}

/*
   MetaX SML call
*/

type metaxSmlReturnCode uint

const (
	metaxSmlReturnCodeSuccess metaxSmlReturnCode = iota
	metaxSmlReturnCodeFailure
	metaxSmlReturnCodeNoDevice
	metaxSmlReturnCodeOperationNotSupported
)

var metaxSmlOperationNotSupportedErr = errors.New("sml operation not supported on specified device")

func metaxIsSmlOperationNotSupportedError(err error) bool {
	return errors.Is(err, metaxSmlOperationNotSupportedErr)
}

func metaxGetSmlReturnCodeDescription(returnCode metaxSmlReturnCode) string {
	return mxSmlGetErrorString(returnCode)
}

/*
   Init
*/

func metaxInitSml() error {
	if returnCode := mxSmlInit(); returnCode != metaxSmlReturnCodeSuccess {
		return fmt.Errorf("mxSmlInit failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return nil
}

/*
   Basic
*/

func metaxGetMacaVersion() (string, error) {
	var (
		size uint = 128
		buf       = make([]byte, size)
	)
	if returnCode := mxSmlGetMacaVersion(&buf[0], &size); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return "", metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return "", fmt.Errorf("mxSmlGetMacaVersion failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return string(buf), nil
}

func metaxGetNativeAndVfGpuCount() uint {
	return mxSmlGetDeviceCount()
}

func metaxGetPfGpuCount() uint {
	return mxSmlGetPfDeviceCount()
}

/*
   GPU
*/

type metaxGpuSeries string

const (
	metaxGpuSeriesUnknown metaxGpuSeries = "unknown"
	metaxGpuSeriesN       metaxGpuSeries = "mxn"
	metaxGpuSeriesC       metaxGpuSeries = "mxc"
	metaxGpuSeriesG       metaxGpuSeries = "mxg"
)

type metaxGpuMode string

const (
	metaxGpuModeNative metaxGpuMode = "native"
	metaxGpuModePf     metaxGpuMode = "pf"
	metaxGpuModeVf     metaxGpuMode = "vf"
)

type metaxGpuInfo struct {
	series   metaxGpuSeries
	model    string
	uuid     string
	bdf      string
	mode     metaxGpuMode
	dieCount uint
}

type metaxSmlDeviceBrand uint

const (
	metaxSmlDeviceBrandUnknown metaxSmlDeviceBrand = iota
	metaxSmlDeviceBrandN
	metaxSmlDeviceBrandC
	metaxSmlDeviceBrandG
)

type metaxSmlDeviceVirtualizationMode uint

const (
	metaxSmlDeviceVirtualizationModeNone metaxSmlDeviceVirtualizationMode = iota
	metaxSmlDeviceVirtualizationModePf
	metaxSmlDeviceVirtualizationModeVf
)

type metaxSmlDeviceInfo struct {
	deviceId   uint
	typ        uint // DEPRECATED
	bdfId      [32]byte
	gpuId      uint
	nodeId     uint
	uuid       [96]byte
	brand      metaxSmlDeviceBrand
	mode       metaxSmlDeviceVirtualizationMode
	deviceName [32]byte
}

var metaxGpuSeriesMap = map[metaxSmlDeviceBrand]metaxGpuSeries{
	metaxSmlDeviceBrandUnknown: metaxGpuSeriesUnknown,
	metaxSmlDeviceBrandN:       metaxGpuSeriesN,
	metaxSmlDeviceBrandC:       metaxGpuSeriesC,
	metaxSmlDeviceBrandG:       metaxGpuSeriesG,
}

var metaxGpuModeMap = map[metaxSmlDeviceVirtualizationMode]metaxGpuMode{
	metaxSmlDeviceVirtualizationModeNone: metaxGpuModeNative,
	metaxSmlDeviceVirtualizationModePf:   metaxGpuModePf,
	metaxSmlDeviceVirtualizationModeVf:   metaxGpuModeVf,
}

func metaxGetGpuInfo(gpu uint) (metaxGpuInfo, error) {
	var info metaxSmlDeviceInfo
	if returnCode := mxSmlGetDeviceInfo(gpu, &info); returnCode != metaxSmlReturnCodeSuccess {
		return metaxGpuInfo{}, fmt.Errorf("mxSmlGetDeviceInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	series, ok := metaxGpuSeriesMap[info.brand]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu series: %v", info.brand)
	}

	mode, ok := metaxGpuModeMap[info.mode]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu mode: %v", info.mode)
	}

	var dieCount uint
	if returnCode := mxSmlGetDeviceDieCount(gpu, &dieCount); returnCode != metaxSmlReturnCodeSuccess {
		return metaxGpuInfo{}, fmt.Errorf("mxSmlGetDeviceDieCount failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxGpuInfo{
		series:   series,
		model:    string(info.deviceName[:]),
		uuid:     string(info.uuid[:]),
		bdf:      string(info.bdfId[:]),
		mode:     mode,
		dieCount: dieCount,
	}, nil
}

// metaxGetGpuStatus
// 0: not available
// 1: available
func metaxGetGpuStatus(gpu uint) (int, error) {
	var value int
	if returnCode := mxSmlGetDeviceState(gpu, &value); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetDeviceState failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return value, nil
}

type metaxGpuBoardWayElectricInfo struct {
	voltage uint // voltage in mV.
	current uint // current in mA.
	power   uint // power in mW.
}

type metaxSmlBoardWayElectricInfo metaxGpuBoardWayElectricInfo

func metaxListGpuBoardWayElectricInfos(gpu uint) ([]metaxGpuBoardWayElectricInfo, error) {
	const maxBoardWays = 3

	var (
		size uint = maxBoardWays
		arr       = make([]metaxSmlBoardWayElectricInfo, size)
	)
	if returnCode := mxSmlGetBoardPowerInfo(gpu, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetBoardPowerInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]metaxGpuBoardWayElectricInfo, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = metaxGpuBoardWayElectricInfo{
			voltage: arr[i].voltage,
			current: arr[i].current,
			power:   arr[i].power,
		}
	}

	return result, nil
}

/*
   PCIe
*/

type metaxGpuPcieLinkInfo struct {
	speed float64 // speed in GT/s.
	width uint    // width in lanes.
}

type metaxSmlPcieInfo metaxGpuPcieLinkInfo

func metaxGetGpuPcieLinkInfo(gpu uint) (metaxGpuPcieLinkInfo, error) {
	var obj metaxSmlPcieInfo
	if returnCode := mxSmlGetPcieInfo(gpu, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return metaxGpuPcieLinkInfo{}, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return metaxGpuPcieLinkInfo{}, fmt.Errorf("mxSmlGetPcieInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxGpuPcieLinkInfo{
		speed: obj.speed,
		width: obj.width,
	}, nil
}

func metaxGetGpuPcieLinkMaxInfo(gpu uint) (metaxGpuPcieLinkInfo, error) {
	var obj metaxSmlPcieInfo
	if returnCode := mxSmlGetPcieMaxLinkInfo(gpu, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return metaxGpuPcieLinkInfo{}, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return metaxGpuPcieLinkInfo{}, fmt.Errorf("mxSmlGetPcieMaxLinkInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxGpuPcieLinkInfo{
		speed: obj.speed,
		width: obj.width,
	}, nil
}

type metaxGpuPcieThroughputInfo struct {
	receiveRate  int // receiveRate in MB/s.
	transmitRate int // transmitRate in MB/s.
}

type metaxSmlPcieThroughput metaxGpuPcieThroughputInfo

func metaxGetGpuPcieThroughputInfo(gpu uint) (metaxGpuPcieThroughputInfo, error) {
	var obj metaxSmlPcieThroughput
	if returnCode := mxSmlGetPcieThroughput(gpu, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return metaxGpuPcieThroughputInfo{}, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return metaxGpuPcieThroughputInfo{}, fmt.Errorf("mxSmlGetPcieThroughput failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxGpuPcieThroughputInfo{
		receiveRate:  obj.receiveRate,
		transmitRate: obj.transmitRate,
	}, nil
}

/*
   MetaXLink
*/

const metaxSmlMetaxlinkMaxNumber = 7

type metaxSmlMetaxlinkType uint

const (
	metaxSmlMetaxlinkTypeReceive metaxSmlMetaxlinkType = iota
	metaxSmlMetaxlinkTypeTransmit
)

type metaxGpuMetaxlinkLinkInfo struct {
	speed float64 // speed in GT/s.
	width uint    // width in lanes.
}

type metaxSmlSingleMetaxlinkInfo metaxGpuMetaxlinkLinkInfo

func metaxListGpuMetaxlinkLinkInfos(gpu uint) ([]metaxGpuMetaxlinkLinkInfo, error) {
	var (
		size uint = metaxSmlMetaxlinkMaxNumber
		arr       = make([]metaxSmlSingleMetaxlinkInfo, size)
	)
	if returnCode := mxSmlGetMetaXLinkInfo_v2(gpu, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetMetaXLinkInfo_v2 failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]metaxGpuMetaxlinkLinkInfo, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = metaxGpuMetaxlinkLinkInfo{
			speed: arr[i].speed,
			width: arr[i].width,
		}
	}

	return result, nil
}

type metaxGpuMetaxlinkThroughputInfo struct {
	receiveRate  int // receiveRate in MB/s.
	transmitRate int // transmitRate in MB/s.
}

func metaxListGpuMetaxlinkThroughputInfos(gpu uint) ([]metaxGpuMetaxlinkThroughputInfo, error) {
	receiveRates, err := metaxListGpuMetaxlinkThroughputParts(gpu, metaxSmlMetaxlinkTypeReceive)
	if metaxIsSmlOperationNotSupportedError(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink receive rates: %v", err)
	}

	transmitRates, err := metaxListGpuMetaxlinkThroughputParts(gpu, metaxSmlMetaxlinkTypeTransmit)
	if metaxIsSmlOperationNotSupportedError(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink transmit rates: %v", err)
	}

	if len(receiveRates) != len(transmitRates) {
		return nil, fmt.Errorf("receive and transmit array length mismatch")
	}

	result := make([]metaxGpuMetaxlinkThroughputInfo, len(receiveRates))

	for i := 0; i < len(result); i++ {
		result[i] = metaxGpuMetaxlinkThroughputInfo{
			receiveRate:  receiveRates[i],
			transmitRate: transmitRates[i],
		}
	}

	return result, nil
}

type metaxSmlMetaXLinkBandwidth struct {
	requestBandwidth  int // requestBandwidth in MB/s.
	responseBandwidth int // responseBandwidth in MB/s.
}

func metaxListGpuMetaxlinkThroughputParts(gpu uint, typ metaxSmlMetaxlinkType) ([]int, error) {
	var (
		size uint = metaxSmlMetaxlinkMaxNumber
		arr       = make([]metaxSmlMetaXLinkBandwidth, size)
	)
	if returnCode := mxSmlGetMetaXLinkBandwidth(gpu, typ, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetMetaXLinkBandwidth failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]int, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i].requestBandwidth
	}

	return result, nil
}

type metaxGpuMetaxlinkTrafficStatInfo struct {
	receive  int64 // receive in bytes.
	transmit int64 // transmit in bytes.
}

func metaxListGpuMetaxlinkTrafficStatInfos(gpu uint) ([]metaxGpuMetaxlinkTrafficStatInfo, error) {
	receives, err := metaxListGpuMetaxlinkTrafficStatParts(gpu, metaxSmlMetaxlinkTypeReceive)
	if metaxIsSmlOperationNotSupportedError(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink receives: %v", err)
	}

	transmits, err := metaxListGpuMetaxlinkTrafficStatParts(gpu, metaxSmlMetaxlinkTypeTransmit)
	if metaxIsSmlOperationNotSupportedError(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to list metaxlink transmits: %v", err)
	}

	if len(receives) != len(transmits) {
		return nil, fmt.Errorf("receive and transmit array length mismatch")
	}

	result := make([]metaxGpuMetaxlinkTrafficStatInfo, len(receives))

	for i := 0; i < len(result); i++ {
		result[i] = metaxGpuMetaxlinkTrafficStatInfo{
			receive:  receives[i],
			transmit: transmits[i],
		}
	}

	return result, nil
}

type metaxSmlMetaxlinkTrafficStat struct {
	requestTrafficStat  int64 // requestTrafficStat in bytes.
	responseTrafficStat int64 // responseTrafficStat in bytes.
}

func metaxListGpuMetaxlinkTrafficStatParts(gpu uint, typ metaxSmlMetaxlinkType) ([]int64, error) {
	var (
		size uint = metaxSmlMetaxlinkMaxNumber
		arr       = make([]metaxSmlMetaxlinkTrafficStat, size)
	)
	if returnCode := mxSmlGetMetaXLinkTrafficStat(gpu, typ, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetMetaXLinkTrafficStat failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]int64, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i].requestTrafficStat
	}

	return result, nil
}

type metaxGpuMetaxlinkAerErrorsInfo struct {
	correctableErrorsCount   int
	uncorrectableErrorsCount int
}

type metaxSmlMetaxlinkAer metaxGpuMetaxlinkAerErrorsInfo

func metaxListGpuMetaxlinkAerErrorsInfos(gpu uint) ([]metaxGpuMetaxlinkAerErrorsInfo, error) {
	var (
		size uint = metaxSmlMetaxlinkMaxNumber
		arr       = make([]metaxSmlMetaxlinkAer, size)
	)
	if returnCode := mxSmlGetMetaXLinkAer(gpu, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetMetaXLinkAer failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]metaxGpuMetaxlinkAerErrorsInfo, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = metaxGpuMetaxlinkAerErrorsInfo{
			correctableErrorsCount:   arr[i].correctableErrorsCount,
			uncorrectableErrorsCount: arr[i].uncorrectableErrorsCount,
		}
	}

	return result, nil
}

/*
   Die
*/

type metaxSmlDeviceUnavailableReasonInfo struct {
	unavailableCode   int
	unavailableReason [64]byte
}

func metaxGetDieStatus(gpu, die uint) (int, error) {
	var obj metaxSmlDeviceUnavailableReasonInfo
	if returnCode := mxSmlGetDieUnavailableReason(gpu, die, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetDieUnavailableReason failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return obj.unavailableCode, nil
}

type metaxSmlTemperatureSensor uint

const (
	metaxSmlTemperatureSensorHotspot metaxSmlTemperatureSensor = iota
)

// metaxGetDieTemperature in ℃.
func metaxGetDieTemperature(gpu, die uint, sensor metaxSmlTemperatureSensor) (float64, error) {
	var value int
	if returnCode := mxSmlGetDieTemperatureInfo(gpu, die, sensor, &value); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetDieTemperatureInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return float64(value) / 100, nil
}

type metaxSmlUsageIp uint

const (
	metaxSmlUsageIpDla metaxSmlUsageIp = iota // metaxSmlUsageIpDla only valid for metaxSmlDeviceBrandN.
	metaxSmlUsageIpVpue
	metaxSmlUsageIpVpud
	metaxSmlUsageIpG2d   // metaxSmlUsageIpG2d only valid for metaxSmlDeviceBrandN.
	metaxSmlUsageIpXcore // metaxSmlUsageIpXcore only valid for metaxSmlDeviceBrandC.
)

// metaxGetDieUtilization in [0, 100].
func metaxGetDieUtilization(gpu, die uint, ip metaxSmlUsageIp) (int, error) {
	var value int
	if returnCode := mxSmlGetDieIpUsage(gpu, die, ip, &value); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetDieIpUsage failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return value, nil
}

type metaxDieMemoryInfo struct {
	total int64 // total in KB.
	used  int64 // used in KB.
}

type metaxSmlMemoryInfo struct {
	visVramTotal int64 // visVramTotal in KB.
	visVramUse   int64 // visVramUse in KB.
	vramTotal    int64 // vramTotal in KB.
	vramUse      int64 // vramUse in KB.
	xttTotal     int64 // xttTotal in KB.
	xttUse       int64 // xttUse in KB.
}

func metaxGetDieMemoryInfo(gpu, die uint) (metaxDieMemoryInfo, error) {
	var obj metaxSmlMemoryInfo
	if returnCode := mxSmlGetDieMemoryInfo(gpu, die, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return metaxDieMemoryInfo{}, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return metaxDieMemoryInfo{}, fmt.Errorf("mxSmlGetDieMemoryInfo failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxDieMemoryInfo{
		total: obj.vramTotal,
		used:  obj.vramUse,
	}, nil
}

type metaxSmlClockIp uint

const (
	metaxSmlClockIpCsc metaxSmlClockIp = iota
	metaxSmlClockIpDla
	metaxSmlClockIpMc
	metaxSmlClockIpMc0
	metaxSmlClockIpMc1
	metaxSmlClockIpVpue
	metaxSmlClockIpVpud
	metaxSmlClockIpSoc
	metaxSmlClockIpDnoc
	metaxSmlClockIpG2d
	metaxSmlClockIpCcx
	metaxSmlClockIpXcore
)

// metaxListDieClocks in MHz.
func metaxListDieClocks(gpu, die uint, ip metaxSmlClockIp) ([]uint, error) {
	const maxClocksSize = 8

	var (
		size uint = maxClocksSize
		arr       = make([]uint, size)
	)
	if returnCode := mxSmlGetDieClocks(gpu, die, ip, &size, &arr[0]); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return nil, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return nil, fmt.Errorf("mxSmlGetDieClocks failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	actualSize := int(size)
	result := make([]uint, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i]
	}

	return result, nil
}

func metaxGetDieClocksThrottleStatus(gpu, die uint) (uint64, error) {
	var value uint64
	if returnCode := mxSmlGetDieCurrentClocksThrottleReason(gpu, die, &value); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetDieCurrentClocksThrottleReason failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return value, nil
}

type metaxSmlDpmIp uint

const (
	metaxSmlDpmIpDla metaxSmlDpmIp = iota
	metaxSmlDpmIpXcore
	metaxSmlDpmIpMc
	metaxSmlDpmIpSoc
	metaxSmlDpmIpDnoc
	metaxSmlDpmIpVpue
	metaxSmlDpmIpVpud
	metaxSmlDpmIpHbm
	metaxSmlDpmIpG2d
	metaxSmlDpmIpHbmPower
	metaxSmlDpmIpCcx
	metaxSmlDpmIpIpGroup
	metaxSmlDpmIpDma
	metaxSmlDpmIpCsc
	metaxSmlDpmIpEth
	metaxSmlDpmIpDidt
	metaxSmlDpmIpReserved
)

func metaxGetDieDpmPerformanceLevel(gpu, die uint, ip metaxSmlDpmIp) (uint, error) {
	var value uint
	if returnCode := mxSmlGetCurrentDieDpmIpPerfLevel(gpu, die, ip, &value); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return 0, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return 0, fmt.Errorf("mxSmlGetCurrentDieDpmIpPerfLevel failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return value, nil
}

type metaxDieEccMemoryInfo struct {
	sramCorrectableErrorsCount   uint
	sramUncorrectableErrorsCount uint
	dramCorrectableErrorsCount   uint
	dramUncorrectableErrorsCount uint
	retiredPagesCount            uint
}

type metaxSmlEccErrorCount metaxDieEccMemoryInfo

func metaxGetDieEccMemoryInfo(gpu, die uint) (metaxDieEccMemoryInfo, error) {
	var obj metaxSmlEccErrorCount
	if returnCode := mxSmlGetDieTotalEccErrors(gpu, die, &obj); returnCode == metaxSmlReturnCodeOperationNotSupported {
		return metaxDieEccMemoryInfo{}, metaxSmlOperationNotSupportedErr
	} else if returnCode != metaxSmlReturnCodeSuccess {
		return metaxDieEccMemoryInfo{}, fmt.Errorf("mxSmlGetDieTotalEccErrors failed: %s", metaxGetSmlReturnCodeDescription(returnCode))
	}

	return metaxDieEccMemoryInfo{
		sramCorrectableErrorsCount:   obj.sramCorrectableErrorsCount,
		sramUncorrectableErrorsCount: obj.sramUncorrectableErrorsCount,
		dramCorrectableErrorsCount:   obj.dramCorrectableErrorsCount,
		dramUncorrectableErrorsCount: obj.dramUncorrectableErrorsCount,
		retiredPagesCount:            obj.retiredPagesCount,
	}, nil
}
