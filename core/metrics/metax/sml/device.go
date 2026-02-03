// Copyright 2026 The HuaTuo Authors
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
	"context"
	"fmt"

	"huatuo-bamai/core/metrics/metax/device"
	"huatuo-bamai/core/metrics/metax/gpu"
)

// getSDKVersion returns the SDK version
func (l *library) getSdkVersion(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	var (
		size uint32 = 128
		buf         = make([]byte, size)
	)
	if err := checkReturnCode("mxSmlGetMacaVersion", mxSmlGetMacaVersion(&buf[0], &size)); err != nil {
		return "", err
	}

	return cString(buf), nil
}

// getNativeAndVFGPUCount returns the number of native and VF GPUs
func (l *library) getNativeAndVfGpuCount() uint32 {
	return mxSmlGetDeviceCount()
}

// getPFGPUCount returns the number of PF GPUs
func (l *library) getPfGpuCount() uint32 {
	return mxSmlGetPfDeviceCount()
}

// getGpuInfo returns the GPU info message
func (l *library) getGpuInfo(ctx context.Context, gpuId uint32) (gpu.Info, error) {
	select {
	case <-ctx.Done():
		return gpu.Info{}, ctx.Err()
	default:
	}

	var info device.Info
	if err := checkReturnCode("mxSmlGetDeviceInfo", mxSmlGetDeviceInfo(gpuId, &info)); err != nil {
		return gpu.Info{}, err
	}

	series, ok := gpu.SeriesMap[info.Brand]
	if !ok {
		return gpu.Info{}, fmt.Errorf("invalid gpu series: %d", info.Brand)
	}

	operationGetBiosVersion := "get bios version"
	biosVersion, err := GetGPUVersion(ctx, gpuId, device.DeviceVersionUnitBios)
	if IsNotSupported(err) {
		// Logging is handled in the caller
		biosVersion = ""
	} else if err != nil {
		return gpu.Info{}, fmt.Errorf("failed to %s: %w", operationGetBiosVersion, err)
	}

	mode, ok := gpu.ModeMap[info.Mode]
	if !ok {
		return gpu.Info{}, fmt.Errorf("invalid gpu mode: %d", info.Mode)
	}

	var dieCount uint32
	if err := checkReturnCode("mxSmlGetDeviceDieCount", mxSmlGetDeviceDieCount(gpuId, &dieCount)); err != nil {
		return gpu.Info{}, err
	}

	return gpu.Info{
		Series:      series,
		Model:       cString(info.DeviceName[:]),
		UUID:        cString(info.UUID[:]),
		BiosVersion: biosVersion,
		BDF:         cString(info.BDFId[:]),
		Mode:        mode,
		DieCount:    dieCount,
	}, nil
}

// getGPUVersion returns the BIOS or driver version for a GPU
func (l *library) getGpuVersion(ctx context.Context, gpuId uint32, unit device.DeviceVersionUnit) (string, error) {
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
	if err := checkReturnCode("mxSmlGetDeviceVersion", mxSmlGetDeviceVersion(gpuId, unit, &buf[0], &size)); err != nil {
		return "", err
	}

	return cString(buf), nil
}

// listGpuBoardWayElectricInfos returns board power information for a GPU
func (l *library) listGpuBoardWayElectricInfos(ctx context.Context, gpuId uint32) ([]device.BoardWayElectricInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	const maxBoardWays = 3

	var (
		size uint32 = maxBoardWays
		arr         = make([]SmlBoardWayElectricInfo, size)
	)
	if err := checkReturnCode("mxSmlGetBoardPowerInfo", mxSmlGetBoardPowerInfo(gpuId, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]device.BoardWayElectricInfo, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = device.BoardWayElectricInfo(arr[i])
	}

	return result, nil
}

// getGpuPcieLinkInfo returns PCIe link information for a GPU
func (l *library) getGpuPcieLinkInfo(ctx context.Context, gpuId uint32) (device.PcieLinkInfo, error) {
	select {
	case <-ctx.Done():
		return device.PcieLinkInfo{}, ctx.Err()
	default:
	}

	var obj SmlPcieInfo
	if err := checkReturnCode("mxSmlGetPcieInfo", mxSmlGetPcieInfo(gpuId, &obj)); err != nil {
		return device.PcieLinkInfo{}, err
	}

	return device.PcieLinkInfo(obj), nil
}

// getGpuPcieThroughputInfo returns PCIe throughput information for a GPU
func (l *library) getGpuPcieThroughputInfo(ctx context.Context, gpuId uint32) (device.PcieThroughputInfo, error) {
	select {
	case <-ctx.Done():
		return device.PcieThroughputInfo{}, ctx.Err()
	default:
	}

	var obj SmlPcieThroughput
	if err := checkReturnCode("mxSmlGetPcieThroughput", mxSmlGetPcieThroughput(gpuId, &obj)); err != nil {
		return device.PcieThroughputInfo{}, err
	}

	return device.PcieThroughputInfo(obj), nil
}

// listGpuMetaxlinkLinkInfos returns MetaXLink link information for a GPU
func (l *library) listGpuMetaxlinkLinkInfos(ctx context.Context, gpuId uint32) ([]device.MetaXLinkLinkInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var (
		size uint32 = device.MetaXLinkMaxNumber
		arr         = make([]SmlSingleMetaXLinkInfo, size)
	)
	if err := checkReturnCode("mxSmlGetMetaXLinkInfo_v2", mxSmlGetMetaXLinkInfo_v2(gpuId, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]device.MetaXLinkLinkInfo, actualSize)

	for i := range actualSize {
		result[i] = device.MetaXLinkLinkInfo(arr[i])
	}

	return result, nil
}

// listGpuMetaxlinkThroughputInfos returns MetaXLink throughput information for a GPU
func (l *library) listGpuMetaxlinkThroughputInfos(ctx context.Context, gpuId uint32) ([]device.MetaXLinkThroughputInfo, error) {
	operationListMetaxlinkReceiveRates := "list metaxlink receive rates"
	receiveRates, err := l.listGpuMetaxlinkThroughputParts(ctx, gpuId, device.MetaXLinkTypeReceive)
	if IsNotSupported(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkReceiveRates, err)
	}

	operationListMetaxlinkTransmitRates := "list metaxlink transmit rates"
	transmitRates, err := l.listGpuMetaxlinkThroughputParts(ctx, gpuId, device.MetaXLinkTypeTransmit)
	if IsNotSupported(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkTransmitRates, err)
	}

	if len(receiveRates) != len(transmitRates) {
		return nil, fmt.Errorf("receive and transmit array length mismatch")
	}

	result := make([]device.MetaXLinkThroughputInfo, len(receiveRates))

	for i := 0; i < len(result); i++ {
		result[i] = device.MetaXLinkThroughputInfo{
			ReceiveRate:  receiveRates[i],
			TransmitRate: transmitRates[i],
		}
	}

	return result, nil
}

// listGpuMetaxlinkThroughputParts returns MetaXLink throughput data for a specific type
func (l *library) listGpuMetaxlinkThroughputParts(ctx context.Context, gpuId uint32, typ device.MetaXLinkType) ([]int32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var (
		size uint32 = device.MetaXLinkMaxNumber
		arr         = make([]SmlMetaXLinkBandwidth, size)
	)
	if err := checkReturnCode("mxSmlGetMetaXLinkBandwidth", mxSmlGetMetaXLinkBandwidth(gpuId, typ, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]int32, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i].RequestBandwidth
	}

	return result, nil
}

// listGpuMetaxlinkTrafficStatInfos returns MetaXLink traffic statistics for a GPU
func (l *library) listGpuMetaxlinkTrafficStatInfos(ctx context.Context, gpuId uint32) ([]device.MetaXLinkTrafficStatInfo, error) {
	operationListMetaxlinkReceives := "list metaxlink receives"
	receives, err := l.listGpuMetaxlinkTrafficStatParts(ctx, gpuId, device.MetaXLinkTypeReceive)
	if IsNotSupported(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkReceives, err)
	}

	operationListMetaxlinkTransmits := "list metaxlink transmits"
	transmits, err := l.listGpuMetaxlinkTrafficStatParts(ctx, gpuId, device.MetaXLinkTypeTransmit)
	if IsNotSupported(err) {
		return nil, err
	} else if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", operationListMetaxlinkTransmits, err)
	}

	if len(receives) != len(transmits) {
		return nil, fmt.Errorf("receive and transmit array length mismatch")
	}

	result := make([]device.MetaXLinkTrafficStatInfo, len(receives))

	for i := 0; i < len(result); i++ {
		result[i] = device.MetaXLinkTrafficStatInfo{
			Receive:  receives[i],
			Transmit: transmits[i],
		}
	}

	return result, nil
}

// listGpuMetaxlinkTrafficStatParts returns MetaXLink traffic statistics for a specific type
func (l *library) listGpuMetaxlinkTrafficStatParts(ctx context.Context, gpuId uint32, typ device.MetaXLinkType) ([]int64, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var (
		size uint32 = device.MetaXLinkMaxNumber
		arr         = make([]SmlMetaXLinkTrafficStat, size)
	)
	if err := checkReturnCode("mxSmlGetMetaXLinkTrafficStat", mxSmlGetMetaXLinkTrafficStat(gpuId, typ, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]int64, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i].RequestTrafficStat
	}

	return result, nil
}

// listGpuMetaxlinkAerErrorsInfos returns MetaXLink AER error information for a GPU
func (l *library) listGpuMetaxlinkAerErrorsInfos(ctx context.Context, gpuId uint32) ([]device.MetaXLinkAerInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var (
		size uint32 = device.MetaXLinkMaxNumber
		arr         = make([]SmlMetaXLinkAer, size)
	)
	if err := checkReturnCode("mxSmlGetMetaXLinkAer", mxSmlGetMetaXLinkAer(gpuId, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]device.MetaXLinkAerInfo, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = device.MetaXLinkAerInfo(arr[i])
	}

	return result, nil
}

// getDieStatus returns the status of a specific die
func (l *library) getDieStatus(ctx context.Context, gpuId, die uint32) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var obj SmlDeviceUnavailableReasonInfo
	if err := checkReturnCode("mxSmlGetDieUnavailableReason", mxSmlGetDieUnavailableReason(gpuId, die, &obj)); err != nil {
		return 0, err
	}

	return obj.unavailableCode, nil
}

// getDieTemperature returns the temperature of a specific die
func (l *library) getDieTemperature(ctx context.Context, gpu, die uint32, sensor gpu.TemperatureSensor) (float64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value int32
	if err := checkReturnCode("mxSmlGetDieTemperatureInfo", mxSmlGetDieTemperatureInfo(gpu, die, sensor, &value)); err != nil {
		return 0, err
	}

	return float64(value) / 100, nil
}

// getDieUtilization collects and reports utilization per GPU die and hardware IP.
func (l *library) getDieUtilization(ctx context.Context, gpuId, dieId uint32, ip gpu.UsageIp) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value int32
	if err := checkReturnCode("mxSmlGetDieIpUsage", mxSmlGetDieIpUsage(gpuId, dieId, ip, &value)); err != nil {
		return 0, err
	}

	return value, nil
}

// getDieMemoryInfo returns memory information for a specific die
func (l *library) getDieMemoryInfo(ctx context.Context, gpuId, dieId uint32) (device.DieMemoryInfo, error) {
	select {
	case <-ctx.Done():
		return device.DieMemoryInfo{}, ctx.Err()
	default:
	}

	var obj SmlMemoryInfo
	if err := checkReturnCode("mxSmlGetDieMemoryInfo", mxSmlGetDieMemoryInfo(gpuId, dieId, &obj)); err != nil {
		return device.DieMemoryInfo{}, err
	}

	return device.DieMemoryInfo{
		Total: obj.vramTotal,
		Used:  obj.vramUse,
	}, nil
}

// listDieClocks collects clock frequency per GPU die and IP.
func (l *library) listDieClocks(ctx context.Context, gpuId, dieId uint32, ip gpu.ClockIp) ([]uint32, error) {
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
	if err := checkReturnCode("mxSmlGetDieClocks", mxSmlGetDieClocks(gpuId, dieId, ip, &size, &arr[0])); err != nil {
		return nil, err
	}

	actualSize := int(size)
	result := make([]uint32, actualSize)

	for i := 0; i < actualSize; i++ {
		result[i] = arr[i]
	}

	return result, nil
}

// getDieClocksThrottleStatus returns the clocks throttle status for a die
func (l *library) getDieClocksThrottleStatus(ctx context.Context, gpuId, dieId uint32) (uint64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value uint64
	if err := checkReturnCode("mxSmlGetDieCurrentClocksThrottleReason", mxSmlGetDieCurrentClocksThrottleReason(gpuId, dieId, &value)); err != nil {
		return 0, err
	}

	return value, nil
}

// getDieDpmPerformanceLevel collects current DPM performance level per GPU die and hardware IP, and exports it as a metric.
func (l *library) getDieDpmPerformanceLevel(ctx context.Context, gpuId, dieId uint32, ip gpu.DpmIp) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var value uint32
	if err := checkReturnCode("mxSmlGetCurrentDieDpmIpPerfLevel", mxSmlGetCurrentDieDpmIpPerfLevel(gpuId, dieId, ip, &value)); err != nil {
		return 0, err
	}

	return value, nil
}

// getDieEccMemoryInfo returns ECC memory information for a specific die
func (l *library) getDieEccMemoryInfo(ctx context.Context, gpuId, dieId uint32) (device.DieEccMemoryInfo, error) {
	select {
	case <-ctx.Done():
		return device.DieEccMemoryInfo{}, ctx.Err()
	default:
	}

	var obj SmlEccErrorCount
	if err := checkReturnCode("mxSmlGetDieTotalEccErrors", mxSmlGetDieTotalEccErrors(gpuId, dieId, &obj)); err != nil {
		return device.DieEccMemoryInfo{}, err
	}

	return device.DieEccMemoryInfo(obj), nil
}

func (l *library) getErrorString(code Return) string {
	return mxSmlGetErrorString(code)
}

func cString(bs []byte) string {
	for i, b := range bs {
		if b == 0 {
			return string(bs[:i])
		}
	}
	return string(bs)
}
