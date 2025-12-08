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
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"

	"github.com/ebitengine/purego"
	"golang.org/x/sync/errgroup"

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

	// SDK version
	if sdkVersion, err := metaxGetSdkVersion(); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get sdk version: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("sdk_info", 1, "GPU SDK info.", map[string]string{
				"version": sdkVersion,
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
	const pfGpuIndexOffset = 100
	for i := pfGpuIndexOffset; i < pfGpuIndexOffset+pfGpuCount; i++ {
		gpus = append(gpus, i)
	}

	// Driver version
	if len(gpus) > 0 {
		if driverVersion, err := metaxGetGpuVersion(uint32(gpus[0]), metaxSmlDeviceVersionUnitDriver); metaxIsSmlOperationNotSupportedError(err) {

		} else if err != nil {
			return nil, fmt.Errorf("failed to get driver version: %v", err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("driver_info", 1, "GPU driver info.", map[string]string{
					"version": driverVersion,
				}),
			)
		}
	}

	// Metrics
	eg, ctx := errgroup.WithContext(context.Background())
	var mu sync.Mutex
	for _, gpu := range gpus {
		eg.Go(func() error {
			gpuMetrics, err := metaxCollectGpuMetrics(ctx, uint32(gpu))
			if err != nil {
				return fmt.Errorf("failed to collect gpu %d metrics: %v", gpu, err)
			}
			mu.Lock()
			metrics = append(metrics, gpuMetrics...)
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return metrics, nil
}

/*
   GPU metrics
*/

func metaxCollectGpuMetrics(ctx context.Context, gpu uint32) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// GPU info
	gpuInfo, err := metaxGetGpuInfo(gpu)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu info: %v", err)
	}
	metrics = append(metrics,
		metric.NewGaugeData("info", 1, "GPU info.", map[string]string{
			"gpu":          strconv.Itoa(int(gpu)),
			"model":        gpuInfo.model,
			"uuid":         gpuInfo.uuid,
			"bios_version": gpuInfo.biosVersion,
			"bdf":          gpuInfo.bdf,
			"mode":         string(gpuInfo.mode),
			"die_count":    strconv.Itoa(int(gpuInfo.dieCount)),
		}),
	)

	// Board electric
	if boardWayElectricInfos, err := metaxListGpuBoardWayElectricInfos(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to list board way electric infos: %v", err)
	} else {
		var totalPower float64
		for _, info := range boardWayElectricInfos {
			totalPower += float64(info.power)
		}

		metrics = append(metrics,
			metric.NewGaugeData("board_power_watts", totalPower/1000, "GPU board power.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
		)
	}

	// PCIe link
	if pcieLinkInfo, err := metaxGetGpuPcieLinkInfo(gpu); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get pcie link info: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("pcie_link_speed_gt_per_second", float64(pcieLinkInfo.speed), "GPU PCIe current link speed.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
			}),
			metric.NewGaugeData("pcie_link_width_lanes", float64(pcieLinkInfo.width), "GPU PCIe current link width.", map[string]string{
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
				metric.NewGaugeData("metaxlink_link_speed_gt_per_second", float64(info.speed), "GPU MetaXLink current link speed.", map[string]string{
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
	eg, _ := errgroup.WithContext(ctx)
	var mu sync.Mutex
	for die := 0; die < int(gpuInfo.dieCount); die++ {
		eg.Go(func() error {
			dieMetrics, err := metaxCollectDieMetrics(gpu, uint32(die), gpuInfo.series)
			if err != nil {
				return fmt.Errorf("failed to collect die %d metrics: %v", die, err)
			}
			mu.Lock()
			metrics = append(metrics, dieMetrics...)
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return metrics, nil
}

/*
   Die metrics
*/

var (
	metaxGpuUtilizationIpMap = map[string]metaxSmlUsageIp{
		"encoder": metaxSmlUsageIpVpue,
		"decoder": metaxSmlUsageIpVpud,
		"xcore":   metaxSmlUsageIpXcore,
	}
	metaxGpuClockIpMap = map[string]metaxSmlClockIp{
		"encoder": metaxSmlClockIpVpue,
		"decoder": metaxSmlClockIpVpud,
		"xcore":   metaxSmlClockIpXcore,
		"memory":  metaxSmlClockIpMc0,
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

func metaxCollectDieMetrics(gpu, die uint32, series metaxGpuSeries) ([]*metric.Data, error) {
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
	if value, err := metaxGetDieTemperature(gpu, die, metaxSmlTemperatureSensorHotspot); metaxIsSmlOperationNotSupportedError(err) {

	} else if err != nil {
		return nil, fmt.Errorf("failed to get temperature: %v", err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("temperature_celsius", value, "GPU temperature.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
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
				metric.NewGaugeData("clock_mhz", float64(values[0]), "GPU clock.", map[string]string{
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
			bit := i + 1

			if bit > len(metaxGpuClocksThrottleBitReasonMap) {
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
					"reason": metaxGpuClocksThrottleBitReasonMap[bit],
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

/*
   MetaX SML API
*/

var (
	mxSmlGetErrorString                    func(metaxSmlReturnCode) string
	mxSmlInit                              func() metaxSmlReturnCode
	mxSmlGetMacaVersion                    func(*byte, *uint32) metaxSmlReturnCode
	mxSmlGetDeviceCount                    func() uint32
	mxSmlGetPfDeviceCount                  func() uint32
	mxSmlGetDeviceInfo                     func(uint32, *metaxSmlDeviceInfo) metaxSmlReturnCode
	mxSmlGetDeviceDieCount                 func(uint32, *uint32) metaxSmlReturnCode
	mxSmlGetDeviceVersion                  func(uint32, metaxSmlDeviceVersionUnit, *byte, *uint32) metaxSmlReturnCode
	mxSmlGetBoardPowerInfo                 func(uint32, *uint32, *metaxSmlBoardWayElectricInfo) metaxSmlReturnCode
	mxSmlGetPcieInfo                       func(uint32, *metaxSmlPcieInfo) metaxSmlReturnCode
	mxSmlGetPcieThroughput                 func(uint32, *metaxSmlPcieThroughput) metaxSmlReturnCode
	mxSmlGetMetaXLinkInfo_v2               func(uint32, *uint32, *metaxSmlSingleMetaxlinkInfo) metaxSmlReturnCode
	mxSmlGetMetaXLinkBandwidth             func(uint32, metaxSmlMetaxlinkType, *uint32, *metaxSmlMetaXLinkBandwidth) metaxSmlReturnCode
	mxSmlGetMetaXLinkTrafficStat           func(uint32, metaxSmlMetaxlinkType, *uint32, *metaxSmlMetaxlinkTrafficStat) metaxSmlReturnCode
	mxSmlGetMetaXLinkAer                   func(uint32, *uint32, *metaxSmlMetaxlinkAer) metaxSmlReturnCode
	mxSmlGetDieUnavailableReason           func(uint32, uint32, *metaxSmlDeviceUnavailableReasonInfo) metaxSmlReturnCode
	mxSmlGetDieTemperatureInfo             func(uint32, uint32, metaxSmlTemperatureSensor, *int) metaxSmlReturnCode
	mxSmlGetDieIpUsage                     func(uint32, uint32, metaxSmlUsageIp, *int) metaxSmlReturnCode
	mxSmlGetDieMemoryInfo                  func(uint32, uint32, *metaxSmlMemoryInfo) metaxSmlReturnCode
	mxSmlGetDieClocks                      func(uint32, uint32, metaxSmlClockIp, *uint32, *uint32) metaxSmlReturnCode
	mxSmlGetDieCurrentClocksThrottleReason func(uint32, uint32, *uint64) metaxSmlReturnCode
	mxSmlGetCurrentDieDpmIpPerfLevel       func(uint32, uint32, metaxSmlDpmIp, *uint32) metaxSmlReturnCode
	mxSmlGetDieTotalEccErrors              func(uint32, uint32, *metaxSmlEccErrorCount) metaxSmlReturnCode
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
	purego.RegisterLibFunc(&mxSmlGetDeviceVersion, libc, "mxSmlGetDeviceVersion")
	purego.RegisterLibFunc(&mxSmlGetBoardPowerInfo, libc, "mxSmlGetBoardPowerInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieInfo, libc, "mxSmlGetPcieInfo")
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
   MetaX SML API call
*/

type metaxSmlReturnCode uint32

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

func metaxCheckSmlReturnCode(operation string, returnCode metaxSmlReturnCode) error {
	switch returnCode {
	case metaxSmlReturnCodeSuccess:
		return nil
	case metaxSmlReturnCodeOperationNotSupported:
		return metaxSmlOperationNotSupportedErr
	default:
		return fmt.Errorf("%s failed: %s", operation, metaxGetSmlReturnCodeDescription(returnCode))
	}
}

/*
   Init
*/

func metaxInitSml() error {
	return metaxCheckSmlReturnCode("mxSmlInit", mxSmlInit())
}

/*
   Basic
*/

func metaxGetSdkVersion() (string, error) {
	var (
		size uint32 = 128
		buf         = make([]byte, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMacaVersion", mxSmlGetMacaVersion(&buf[0], &size)); err != nil {
		return "", err
	}

	return cString(buf), nil
}

func metaxGetNativeAndVfGpuCount() uint32 {
	return mxSmlGetDeviceCount()
}

func metaxGetPfGpuCount() uint32 {
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
	series      metaxGpuSeries
	model       string
	uuid        string
	biosVersion string
	bdf         string
	mode        metaxGpuMode
	dieCount    uint32
}

type metaxSmlDeviceBrand uint32

const (
	metaxSmlDeviceBrandUnknown metaxSmlDeviceBrand = iota
	metaxSmlDeviceBrandN
	metaxSmlDeviceBrandC
	metaxSmlDeviceBrandG
)

type metaxSmlDeviceVirtualizationMode uint32

const (
	metaxSmlDeviceVirtualizationModeNone metaxSmlDeviceVirtualizationMode = iota
	metaxSmlDeviceVirtualizationModePf
	metaxSmlDeviceVirtualizationModeVf
)

type metaxSmlDeviceInfo struct {
	deviceId   uint32
	typ        uint32 // DEPRECATED
	bdfId      [32]byte
	gpuId      uint32
	nodeId     uint32
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

func metaxGetGpuInfo(gpu uint32) (metaxGpuInfo, error) {
	var info metaxSmlDeviceInfo
	if err := metaxCheckSmlReturnCode("mxSmlGetDeviceInfo", mxSmlGetDeviceInfo(gpu, &info)); err != nil {
		return metaxGpuInfo{}, err
	}

	series, ok := metaxGpuSeriesMap[info.brand]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu series: %v", info.brand)
	}

	biosVersion, err := metaxGetGpuVersion(gpu, metaxSmlDeviceVersionUnitBios)
	if metaxIsSmlOperationNotSupportedError(err) {
		biosVersion = "none"
	} else if err != nil {
		return metaxGpuInfo{}, fmt.Errorf("failed to get bios version: %v", err)
	}

	mode, ok := metaxGpuModeMap[info.mode]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu mode: %v", info.mode)
	}

	var dieCount uint32
	if err := metaxCheckSmlReturnCode("mxSmlGetDeviceDieCount", mxSmlGetDeviceDieCount(gpu, &dieCount)); err != nil {
		return metaxGpuInfo{}, err
	}

	return metaxGpuInfo{
		series:      series,
		model:       cString(info.deviceName[:]),
		uuid:        cString(info.uuid[:]),
		biosVersion: biosVersion,
		bdf:         cString(info.bdfId[:]),
		mode:        mode,
		dieCount:    dieCount,
	}, nil
}

type metaxSmlDeviceVersionUnit uint32

const (
	metaxSmlDeviceVersionUnitBios metaxSmlDeviceVersionUnit = iota
	metaxSmlDeviceVersionUnitDriver
)

func metaxGetGpuVersion(gpu uint32, unit metaxSmlDeviceVersionUnit) (string, error) {
	const versionMaximumSize = 64

	var (
		size uint32 = versionMaximumSize
		buf         = make([]byte, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetDeviceVersion", mxSmlGetDeviceVersion(gpu, unit, &buf[0], &size)); err != nil {
		return "", err
	}

	return cString(buf), nil
}

type metaxGpuBoardWayElectricInfo struct {
	voltage uint32 // voltage in mV.
	current uint32 // current in mA.
	power   uint32 // power in mW.
}

type metaxSmlBoardWayElectricInfo metaxGpuBoardWayElectricInfo

func metaxListGpuBoardWayElectricInfos(gpu uint32) ([]metaxGpuBoardWayElectricInfo, error) {
	const maxBoardWays = 3

	var (
		size uint32 = maxBoardWays
		arr         = make([]metaxSmlBoardWayElectricInfo, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetBoardPowerInfo", mxSmlGetBoardPowerInfo(gpu, &size, &arr[0])); err != nil {
		return nil, err
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
	speed float32 // speed in GT/s.
	width uint32  // width in lanes.
}

type metaxSmlPcieInfo metaxGpuPcieLinkInfo

func metaxGetGpuPcieLinkInfo(gpu uint32) (metaxGpuPcieLinkInfo, error) {
	var obj metaxSmlPcieInfo
	if err := metaxCheckSmlReturnCode("mxSmlGetPcieInfo", mxSmlGetPcieInfo(gpu, &obj)); err != nil {
		return metaxGpuPcieLinkInfo{}, err
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

func metaxGetGpuPcieThroughputInfo(gpu uint32) (metaxGpuPcieThroughputInfo, error) {
	var obj metaxSmlPcieThroughput
	if err := metaxCheckSmlReturnCode("mxSmlGetPcieThroughput", mxSmlGetPcieThroughput(gpu, &obj)); err != nil {
		return metaxGpuPcieThroughputInfo{}, err
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

type metaxSmlMetaxlinkType uint32

const (
	metaxSmlMetaxlinkTypeReceive metaxSmlMetaxlinkType = iota
	metaxSmlMetaxlinkTypeTransmit
)

type metaxGpuMetaxlinkLinkInfo struct {
	speed float32 // speed in GT/s.
	width uint32  // width in lanes.
}

type metaxSmlSingleMetaxlinkInfo metaxGpuMetaxlinkLinkInfo

func metaxListGpuMetaxlinkLinkInfos(gpu uint32) ([]metaxGpuMetaxlinkLinkInfo, error) {
	var (
		size uint32 = metaxSmlMetaxlinkMaxNumber
		arr         = make([]metaxSmlSingleMetaxlinkInfo, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMetaXLinkInfo_v2", mxSmlGetMetaXLinkInfo_v2(gpu, &size, &arr[0])); err != nil {
		return nil, err
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

func metaxListGpuMetaxlinkThroughputInfos(gpu uint32) ([]metaxGpuMetaxlinkThroughputInfo, error) {
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

func metaxListGpuMetaxlinkThroughputParts(gpu uint32, typ metaxSmlMetaxlinkType) ([]int, error) {
	var (
		size uint32 = metaxSmlMetaxlinkMaxNumber
		arr         = make([]metaxSmlMetaXLinkBandwidth, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMetaXLinkBandwidth", mxSmlGetMetaXLinkBandwidth(gpu, typ, &size, &arr[0])); err != nil {
		return nil, err
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

func metaxListGpuMetaxlinkTrafficStatInfos(gpu uint32) ([]metaxGpuMetaxlinkTrafficStatInfo, error) {
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

func metaxListGpuMetaxlinkTrafficStatParts(gpu uint32, typ metaxSmlMetaxlinkType) ([]int64, error) {
	var (
		size uint32 = metaxSmlMetaxlinkMaxNumber
		arr         = make([]metaxSmlMetaxlinkTrafficStat, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMetaXLinkTrafficStat", mxSmlGetMetaXLinkTrafficStat(gpu, typ, &size, &arr[0])); err != nil {
		return nil, err
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

func metaxListGpuMetaxlinkAerErrorsInfos(gpu uint32) ([]metaxGpuMetaxlinkAerErrorsInfo, error) {
	var (
		size uint32 = metaxSmlMetaxlinkMaxNumber
		arr         = make([]metaxSmlMetaxlinkAer, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMetaXLinkAer", mxSmlGetMetaXLinkAer(gpu, &size, &arr[0])); err != nil {
		return nil, err
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

func metaxGetDieStatus(gpu, die uint32) (int, error) {
	var obj metaxSmlDeviceUnavailableReasonInfo
	if err := metaxCheckSmlReturnCode("mxSmlGetDieUnavailableReason", mxSmlGetDieUnavailableReason(gpu, die, &obj)); err != nil {
		return 0, err
	}

	return obj.unavailableCode, nil
}

type metaxSmlTemperatureSensor uint32

const (
	metaxSmlTemperatureSensorHotspot metaxSmlTemperatureSensor = iota
)

// metaxGetDieTemperature in ℃.
func metaxGetDieTemperature(gpu, die uint32, sensor metaxSmlTemperatureSensor) (float64, error) {
	var value int
	if err := metaxCheckSmlReturnCode("mxSmlGetDieTemperatureInfo", mxSmlGetDieTemperatureInfo(gpu, die, sensor, &value)); err != nil {
		return 0, err
	}

	return float64(value) / 100, nil
}

type metaxSmlUsageIp uint32

const (
	metaxSmlUsageIpDla metaxSmlUsageIp = iota // metaxSmlUsageIpDla only valid for metaxSmlDeviceBrandN.
	metaxSmlUsageIpVpue
	metaxSmlUsageIpVpud
	metaxSmlUsageIpG2d   // metaxSmlUsageIpG2d only valid for metaxSmlDeviceBrandN.
	metaxSmlUsageIpXcore // metaxSmlUsageIpXcore only valid for metaxSmlDeviceBrandC.
)

// metaxGetDieUtilization in [0, 100].
func metaxGetDieUtilization(gpu, die uint32, ip metaxSmlUsageIp) (int, error) {
	var value int
	if err := metaxCheckSmlReturnCode("mxSmlGetDieIpUsage", mxSmlGetDieIpUsage(gpu, die, ip, &value)); err != nil {
		return 0, err
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

func metaxGetDieMemoryInfo(gpu, die uint32) (metaxDieMemoryInfo, error) {
	var obj metaxSmlMemoryInfo
	if err := metaxCheckSmlReturnCode("mxSmlGetDieMemoryInfo", mxSmlGetDieMemoryInfo(gpu, die, &obj)); err != nil {
		return metaxDieMemoryInfo{}, err
	}

	return metaxDieMemoryInfo{
		total: obj.vramTotal,
		used:  obj.vramUse,
	}, nil
}

type metaxSmlClockIp uint32

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
func metaxListDieClocks(gpu, die uint32, ip metaxSmlClockIp) ([]uint32, error) {
	const maxClocksSize = 8

	var (
		size uint32 = maxClocksSize
		arr         = make([]uint32, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetDieClocks", mxSmlGetDieClocks(gpu, die, ip, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]uint32, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i]
	}

	return result, nil
}

func metaxGetDieClocksThrottleStatus(gpu, die uint32) (uint64, error) {
	var value uint64
	if err := metaxCheckSmlReturnCode("mxSmlGetDieCurrentClocksThrottleReason", mxSmlGetDieCurrentClocksThrottleReason(gpu, die, &value)); err != nil {
		return 0, err
	}

	return value, nil
}

type metaxSmlDpmIp uint32

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

func metaxGetDieDpmPerformanceLevel(gpu, die uint32, ip metaxSmlDpmIp) (uint32, error) {
	var value uint32
	if err := metaxCheckSmlReturnCode("mxSmlGetCurrentDieDpmIpPerfLevel", mxSmlGetCurrentDieDpmIpPerfLevel(gpu, die, ip, &value)); err != nil {
		return 0, err
	}

	return value, nil
}

type metaxDieEccMemoryInfo struct {
	sramCorrectableErrorsCount   uint32
	sramUncorrectableErrorsCount uint32
	dramCorrectableErrorsCount   uint32
	dramUncorrectableErrorsCount uint32
	retiredPagesCount            uint32
}

type metaxSmlEccErrorCount metaxDieEccMemoryInfo

func metaxGetDieEccMemoryInfo(gpu, die uint32) (metaxDieEccMemoryInfo, error) {
	var obj metaxSmlEccErrorCount
	if err := metaxCheckSmlReturnCode("mxSmlGetDieTotalEccErrors", mxSmlGetDieTotalEccErrors(gpu, die, &obj)); err != nil {
		return metaxDieEccMemoryInfo{}, err
	}

	return metaxDieEccMemoryInfo{
		sramCorrectableErrorsCount:   obj.sramCorrectableErrorsCount,
		sramUncorrectableErrorsCount: obj.sramUncorrectableErrorsCount,
		dramCorrectableErrorsCount:   obj.dramCorrectableErrorsCount,
		dramUncorrectableErrorsCount: obj.dramUncorrectableErrorsCount,
		retiredPagesCount:            obj.retiredPagesCount,
	}, nil
}

/*
   Util
*/

func cString(bs []byte) string {
	return string(bytes.TrimRight(bs, "\x00"))
}

func getBitsFromLsbToMsb(x uint64) []uint8 {
	bits := make([]uint8, 64)
	for i := 0; i < 64; i++ {
		bits[i] = uint8((x >> i) & 1)
	}
	return bits
}
