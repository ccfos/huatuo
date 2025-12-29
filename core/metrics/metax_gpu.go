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
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"

	"github.com/ebitengine/purego"
	"golang.org/x/sync/errgroup"

	"huatuo-bamai/internal/log"
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
		return nil, fmt.Errorf("failed to load sml library: %w", err)
	}

	// Init MetaX SML
	if err := metaxInitSml(); err != nil {
		return nil, fmt.Errorf("failed to init sml: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &metaxGpuCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (m *metaxGpuCollector) Update() ([]*metric.Data, error) {
	metrics, err := metaxCollectMetrics(context.Background())
	if err != nil {
		var smlError *metaxSmlError
		if errors.As(err, &smlError) {
			log.Errorf("re-initing sml and retrying because sml error: %v", err)

			if err := metaxInitSml(); err != nil {
				return nil, fmt.Errorf("failed to re-init sml: %w", err)
			}

			return metaxCollectMetrics(context.Background())
		}

		return nil, err
	}

	return metrics, nil
}

func metaxCollectMetrics(ctx context.Context) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// SDK version
	operationGetSdkVersion := "get sdk version"
	if sdkVersion, err := metaxGetSdkVersion(ctx); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported", operationGetSdkVersion)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetSdkVersion, err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("sdk_info", 1, "GPU SDK info.", map[string]string{
				"version": sdkVersion,
			}),
		)
	}

	var gpus []uint32

	// Native and VF GPUs
	nativeAndVfGpuCount := metaxGetNativeAndVfGpuCount()
	for i := uint32(0); i < nativeAndVfGpuCount; i++ {
		gpus = append(gpus, i)
	}

	// PF GPUs
	pfGpuCount := metaxGetPfGpuCount()
	const pfGpuIndexOffset = uint32(100)
	for i := pfGpuIndexOffset; i < pfGpuIndexOffset+pfGpuCount; i++ {
		gpus = append(gpus, i)
	}

	// Driver version
	if len(gpus) > 0 {
		operationGetDriverVersion := "get driver version"
		if driverVersion, err := metaxGetGpuVersion(ctx, gpus[0], metaxSmlDeviceVersionUnitDriver); metaxIsSmlOperationNotSupportedError(err) {
			log.Debugf("operation %s not supported on gpu 0", operationGetDriverVersion)
		} else if err != nil {
			return nil, fmt.Errorf("failed to %s: %w", operationGetDriverVersion, err)
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("driver_info", 1, "GPU driver info.", map[string]string{
					"version": driverVersion,
				}),
			)
		}
	}

	// GPU
	eg, subCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	for _, gpu := range gpus {
		eg.Go(func() error {
			gpuMetrics, err := metaxCollectGpuMetrics(subCtx, gpu)
			if err != nil {
				return fmt.Errorf("failed to collect gpu %d metrics: %w", gpu, err)
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
	gpuInfo, err := metaxGetGpuInfo(ctx, gpu)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu info: %w", err)
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
	operationListBoardWayElectricInfos := "list board way electric infos"
	if boardWayElectricInfos, err := metaxListGpuBoardWayElectricInfos(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListBoardWayElectricInfos, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListBoardWayElectricInfos, err)
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
	operationGetPcieLinkInfo := "get pcie link info"
	if pcieLinkInfo, err := metaxGetGpuPcieLinkInfo(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationGetPcieLinkInfo, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetPcieLinkInfo, err)
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
	operationGetPcieThroughputInfo := "get pcie throughput info"
	if pcieThroughputInfo, err := metaxGetGpuPcieThroughputInfo(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationGetPcieThroughputInfo, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetPcieThroughputInfo, err)
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
	operationListMetaxlinkLinkInfos := "list metaxlink link infos"
	if metaxlinkLinkInfos, err := metaxListGpuMetaxlinkLinkInfos(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkLinkInfos, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkLinkInfos, err)
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
	operationListMetaxlinkThroughputInfos := "list metaxlink throughput infos"
	if metaxlinkThroughputInfos, err := metaxListGpuMetaxlinkThroughputInfos(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkThroughputInfos, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkThroughputInfos, err)
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
	operationListMetaxlinkTrafficStatInfos := "list metaxlink traffic stat infos"
	if metaxlinkTrafficStatInfos, err := metaxListGpuMetaxlinkTrafficStatInfos(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkTrafficStatInfos, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkTrafficStatInfos, err)
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
	operationListMetaxlinkAerErrorsInfos := "list metaxlink aer errors infos"
	if metaxlinkAerErrorsInfos, err := metaxListGpuMetaxlinkAerErrorsInfos(ctx, gpu); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkAerErrorsInfos, gpu)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkAerErrorsInfos, err)
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
	eg, subCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	for die := uint32(0); die < gpuInfo.dieCount; die++ {
		eg.Go(func() error {
			dieMetrics, err := metaxCollectDieMetrics(subCtx, gpu, die, gpuInfo.series)
			if err != nil {
				return fmt.Errorf("failed to collect die %d metrics: %w", die, err)
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

func metaxCollectDieMetrics(ctx context.Context, gpu, die uint32, series metaxGpuSeries) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// Die status
	operationGetDieStatus := "get die status"
	if dieStatus, err := metaxGetDieStatus(ctx, gpu, die); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d die %d", operationGetDieStatus, gpu, die)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetDieStatus, err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("status", float64(dieStatus), "GPU status, 0 means normal, other values means abnormal. Check the documentation to see the exceptions corresponding to each value.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
	}

	// Temperature
	operationGetTemperature := "get temperature"
	if value, err := metaxGetDieTemperature(ctx, gpu, die, metaxSmlTemperatureSensorHotspot); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d die %d", operationGetTemperature, gpu, die)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetTemperature, err)
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
		operationGetUtilization := fmt.Sprintf("get %s utilization", ip)
		if value, err := metaxGetDieUtilization(ctx, gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {
			log.Debugf("operation %s not supported on gpu %d die %d", operationGetUtilization, gpu, die)
		} else if err != nil {
			return nil, fmt.Errorf("failed to %s: %w", operationGetUtilization, err)
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
	operationGetMemoryInfo := "get memory info"
	if memoryInfo, err := metaxGetDieMemoryInfo(ctx, gpu, die); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d die %d", operationGetMemoryInfo, gpu, die)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetMemoryInfo, err)
	} else {
		metrics = append(metrics,
			metric.NewGaugeData("memory_total_bytes", float64(memoryInfo.total)*1024, "Total vram.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
			metric.NewGaugeData("memory_used_bytes", float64(memoryInfo.used)*1024, "Used vram.", map[string]string{
				"gpu": strconv.Itoa(int(gpu)),
				"die": strconv.Itoa(int(die)),
			}),
		)
	}

	// Clock
	for ip, ipC := range metaxGpuClockIpMap {
		// For metaxGpuSeriesN, use metaxSmlClockIpMc instead of metaxSmlClockIpMc0 for memory clock
		if ip == "memory" && series == metaxGpuSeriesN {
			ipC = metaxSmlClockIpMc
		}

		operationListClocks := fmt.Sprintf("list %s clocks", ip)
		if values, err := metaxListDieClocks(ctx, gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {
			log.Debugf("operation %s not supported on gpu %d die %d", operationListClocks, gpu, die)
		} else if err != nil {
			return nil, fmt.Errorf("failed to %s: %w", operationListClocks, err)
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
	operationGetClocksThrottleStatus := "get clocks throttle status"
	if clocksThrottleStatus, err := metaxGetDieClocksThrottleStatus(ctx, gpu, die); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d die %d", operationGetClocksThrottleStatus, gpu, die)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetClocksThrottleStatus, err)
	} else {
		bits := getBitsFromLsbToMsb(clocksThrottleStatus)

		for i, v := range bits {
			if v == 0 {
				// Metrics are not exported when not throttling.
				continue
			}

			bit := i + 1

			if _, ok := metaxGpuClocksThrottleBitReasonMap[bit]; !ok {
				log.Warnf("gpu %d die %d is clocks throttling for unknown reason bit %d", gpu, die, bit)
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
		operationGetDpmPerformanceLevel := fmt.Sprintf("get %s dpm performance level", ip)
		if value, err := metaxGetDieDpmPerformanceLevel(ctx, gpu, die, ipC); metaxIsSmlOperationNotSupportedError(err) {
			log.Debugf("operation %s not supported on gpu %d die %d", operationGetDpmPerformanceLevel, gpu, die)
		} else if err != nil {
			return nil, fmt.Errorf("failed to %s: %w", operationGetDpmPerformanceLevel, err)
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
	operationGetEccMemoryInfo := "get ecc memory info"
	if eccMemoryInfo, err := metaxGetDieEccMemoryInfo(ctx, gpu, die); metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d die %d", operationGetEccMemoryInfo, gpu, die)
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationGetEccMemoryInfo, err)
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
	mxSmlGetDieTemperatureInfo             func(uint32, uint32, metaxSmlTemperatureSensor, *int32) metaxSmlReturnCode
	mxSmlGetDieIpUsage                     func(uint32, uint32, metaxSmlUsageIp, *int32) metaxSmlReturnCode
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
		return 0, fmt.Errorf("failed to get sml library path: %w", err)
	}

	libc, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("failed to open sml library: %w", err)
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

func metaxGetSmlReturnCodeDescription(returnCode metaxSmlReturnCode) string {
	return mxSmlGetErrorString(returnCode)
}

type metaxSmlError struct {
	operation string
	code      metaxSmlReturnCode
	message   string
}

func (e *metaxSmlError) Error() string {
	return fmt.Sprintf("%s failed: %s", e.operation, e.message)
}

func metaxIsSmlOperationNotSupportedError(err error) bool {
	var smlError *metaxSmlError
	if errors.As(err, &smlError) {
		return smlError.code == metaxSmlReturnCodeOperationNotSupported
	}

	return false
}

func metaxCheckSmlReturnCode(operation string, returnCode metaxSmlReturnCode) error {
	if returnCode == metaxSmlReturnCodeSuccess {
		return nil
	}

	return &metaxSmlError{
		operation: operation,
		code:      returnCode,
		message:   metaxGetSmlReturnCodeDescription(returnCode),
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

func metaxGetSdkVersion(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

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

func metaxGetGpuInfo(ctx context.Context, gpu uint32) (metaxGpuInfo, error) {
	select {
	case <-ctx.Done():
		return metaxGpuInfo{}, ctx.Err()
	default:
	}

	var info metaxSmlDeviceInfo
	if err := metaxCheckSmlReturnCode("mxSmlGetDeviceInfo", mxSmlGetDeviceInfo(gpu, &info)); err != nil {
		return metaxGpuInfo{}, err
	}

	series, ok := metaxGpuSeriesMap[info.brand]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu series: %d", info.brand)
	}

	operationGetBiosVersion := "get bios version"
	biosVersion, err := metaxGetGpuVersion(ctx, gpu, metaxSmlDeviceVersionUnitBios)
	if metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationGetBiosVersion, gpu)
		biosVersion = ""
	} else if err != nil {
		return metaxGpuInfo{}, fmt.Errorf("failed to %s: %w", operationGetBiosVersion, err)
	}

	mode, ok := metaxGpuModeMap[info.mode]
	if !ok {
		return metaxGpuInfo{}, fmt.Errorf("invalid gpu mode: %d", info.mode)
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

func metaxGetGpuVersion(ctx context.Context, gpu uint32, unit metaxSmlDeviceVersionUnit) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

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

func metaxListGpuBoardWayElectricInfos(ctx context.Context, gpu uint32) ([]metaxGpuBoardWayElectricInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

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

func metaxGetGpuPcieLinkInfo(ctx context.Context, gpu uint32) (metaxGpuPcieLinkInfo, error) {
	select {
	case <-ctx.Done():
		return metaxGpuPcieLinkInfo{}, ctx.Err()
	default:
	}

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
	receiveRate  int32 // receiveRate in MB/s.
	transmitRate int32 // transmitRate in MB/s.
}

type metaxSmlPcieThroughput metaxGpuPcieThroughputInfo

func metaxGetGpuPcieThroughputInfo(ctx context.Context, gpu uint32) (metaxGpuPcieThroughputInfo, error) {
	select {
	case <-ctx.Done():
		return metaxGpuPcieThroughputInfo{}, ctx.Err()
	default:
	}

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

func metaxListGpuMetaxlinkLinkInfos(ctx context.Context, gpu uint32) ([]metaxGpuMetaxlinkLinkInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

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
	receiveRate  int32 // receiveRate in MB/s.
	transmitRate int32 // transmitRate in MB/s.
}

func metaxListGpuMetaxlinkThroughputInfos(ctx context.Context, gpu uint32) ([]metaxGpuMetaxlinkThroughputInfo, error) {
	operationListMetaxlinkReceiveRates := "list metaxlink receive rates"
	receiveRates, err := metaxListGpuMetaxlinkThroughputParts(ctx, gpu, metaxSmlMetaxlinkTypeReceive)
	if metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkReceiveRates, gpu)
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkReceiveRates, err)
	}

	operationListMetaxlinkTransmitRates := "list metaxlink transmit rates"
	transmitRates, err := metaxListGpuMetaxlinkThroughputParts(ctx, gpu, metaxSmlMetaxlinkTypeTransmit)
	if metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkTransmitRates, gpu)
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkTransmitRates, err)
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
	requestBandwidth  int32 // requestBandwidth in MB/s.
	responseBandwidth int32 // responseBandwidth in MB/s.
}

func metaxListGpuMetaxlinkThroughputParts(ctx context.Context, gpu uint32, typ metaxSmlMetaxlinkType) ([]int32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var (
		size uint32 = metaxSmlMetaxlinkMaxNumber
		arr         = make([]metaxSmlMetaXLinkBandwidth, size)
	)
	if err := metaxCheckSmlReturnCode("mxSmlGetMetaXLinkBandwidth", mxSmlGetMetaXLinkBandwidth(gpu, typ, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]int32, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i].requestBandwidth
	}

	return result, nil
}

type metaxGpuMetaxlinkTrafficStatInfo struct {
	receive  int64 // receive in bytes.
	transmit int64 // transmit in bytes.
}

func metaxListGpuMetaxlinkTrafficStatInfos(ctx context.Context, gpu uint32) ([]metaxGpuMetaxlinkTrafficStatInfo, error) {
	operationListMetaxlinkReceives := "list metaxlink receives"
	receives, err := metaxListGpuMetaxlinkTrafficStatParts(ctx, gpu, metaxSmlMetaxlinkTypeReceive)
	if metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkReceives, gpu)
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkReceives, err)
	}

	operationListMetaxlinkTransmits := "list metaxlink transmits"
	transmits, err := metaxListGpuMetaxlinkTrafficStatParts(ctx, gpu, metaxSmlMetaxlinkTypeTransmit)
	if metaxIsSmlOperationNotSupportedError(err) {
		log.Debugf("operation %s not supported on gpu %d", operationListMetaxlinkTransmits, gpu)
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkTransmits, err)
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

func metaxListGpuMetaxlinkTrafficStatParts(ctx context.Context, gpu uint32, typ metaxSmlMetaxlinkType) ([]int64, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

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
	correctableErrorsCount   int32
	uncorrectableErrorsCount int32
}

type metaxSmlMetaxlinkAer metaxGpuMetaxlinkAerErrorsInfo

func metaxListGpuMetaxlinkAerErrorsInfos(ctx context.Context, gpu uint32) ([]metaxGpuMetaxlinkAerErrorsInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

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
	unavailableCode   int32
	unavailableReason [64]byte
}

func metaxGetDieStatus(ctx context.Context, gpu, die uint32) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

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
func metaxGetDieTemperature(ctx context.Context, gpu, die uint32, sensor metaxSmlTemperatureSensor) (float64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value int32
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
func metaxGetDieUtilization(ctx context.Context, gpu, die uint32, ip metaxSmlUsageIp) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value int32
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

func metaxGetDieMemoryInfo(ctx context.Context, gpu, die uint32) (metaxDieMemoryInfo, error) {
	select {
	case <-ctx.Done():
		return metaxDieMemoryInfo{}, ctx.Err()
	default:
	}

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
func metaxListDieClocks(ctx context.Context, gpu, die uint32, ip metaxSmlClockIp) ([]uint32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

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

func metaxGetDieClocksThrottleStatus(ctx context.Context, gpu, die uint32) (uint64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

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

func metaxGetDieDpmPerformanceLevel(ctx context.Context, gpu, die uint32, ip metaxSmlDpmIp) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

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

func metaxGetDieEccMemoryInfo(ctx context.Context, gpu, die uint32) (metaxDieEccMemoryInfo, error) {
	select {
	case <-ctx.Done():
		return metaxDieEccMemoryInfo{}, ctx.Err()
	default:
	}

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
	for i, b := range bs {
		if b == 0 {
			return string(bs[:i])
		}
	}
	return string(bs)
}

func getBitsFromLsbToMsb(x uint64) []uint8 {
	size := 64
	bits := make([]uint8, size)
	for i := 0; i < size; i++ {
		bits[i] = uint8((x >> i) & 1)
	}
	return bits
}
