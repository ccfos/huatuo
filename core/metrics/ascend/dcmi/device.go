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

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// DcGetDeviceHealth returns the health status of a specific NPU device.
func (l *library) DcGetDeviceHealth(ctx context.Context, cardId, deviceId uint32) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var health uint32
	if err := checkReturnCode("dcmi_get_device_health", dcGetDeviceHealth(cardId, deviceId, &health)); err != nil {
		return 0, err
	}

	return health, nil
}

// DcGetCardList returns the list of card IDs present in the system.
func (l *library) DcGetCardList(ctx context.Context) (int32, []int32, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	default:
	}
	const maxCards = 64

	var cNum int32
	ids := make([]int32, maxCards)

	if err := checkReturnCode("dcmi_get_card_list", dcGetCardList(&cNum, &ids[0], maxCards)); err != nil {
		return 0, nil, err
	}

	if cNum <= 0 || cNum > maxCards {
		return 0, nil, fmt.Errorf("invalid card count: %d", cNum)
	}

	result := make([]int32, 0, cNum)
	for i := int32(0); i < cNum; i++ {
		if ids[i] < 0 {
			continue
		}
		result = append(result, ids[i])
	}

	return cNum, result, nil
}

// DcGetDeviceNumInCard returns the number of devices in the specified card.
func (l *library) DcGetDeviceNumInCard(ctx context.Context, cardId int32) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	const maxDevicesPerCard = 4

	if cardId < 0 {
		return 0, fmt.Errorf("invalid card ID: %d", cardId)
	}

	var deviceNum int32
	if err := checkReturnCode("dcmi_get_device_num_in_card", dcGetDeviceNumInCard(cardId, &deviceNum)); err != nil {
		return 0, err
	}

	if deviceNum <= 0 || deviceNum > maxDevicesPerCard {
		return 0, fmt.Errorf("invalid device count in card %d: %d", cardId, deviceNum)
	}

	return deviceNum, nil
}

// DcGetDevicePowerInfo returns the power consumption for a specific NPU device.
// The returned value is in watts with 0.1W precision.
func (l *library) DcGetDevicePowerInfo(ctx context.Context, cardId, deviceId uint32) (float32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var cpower int32
	if err := checkReturnCode("dcmi_get_device_power_info", dcGetDevicePowerInfo(cardId, deviceId, &cpower)); err != nil {
		return 0, err
	}

	if cpower < 0 {
		return 0, fmt.Errorf("invalid power value: %d", cpower)
	}

	return float32(cpower) * 0.1, nil
}

// DcGetDeviceTemperature returns the device temperature in degrees Celsius.
func (l *library) DcGetDeviceTemperature(ctx context.Context, cardId, deviceId uint32) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var temp int32
	if err := checkReturnCode("dcmi_get_device_temperature", dcGetDeviceTemperature(cardId, deviceId, &temp)); err != nil {
		return 0, err
	}

	if temp < -275 {
		return 0, fmt.Errorf("invalid temperature: %d", temp)
	}

	return temp, nil
}

// DcGetDeviceVoltage returns the device voltage in volts with 0.01V precision.
func (l *library) DcGetDeviceVoltage(ctx context.Context, cardId, deviceId uint32) (float32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var vol uint32
	if err := checkReturnCode("dcmi_get_device_voltage", dcGetDeviceVoltage(cardId, deviceId, &vol)); err != nil {
		return 0, err
	}

	if vol >= math.MaxInt32 {
		return 0, fmt.Errorf("voltage value out of range: %d", vol)
	}

	return float32(vol) * 0.01, nil
}

// DcGetDeviceUtilizationRate returns the utilization rate (0-100%) for the specified sub-component.
func (l *library) DcGetDeviceUtilizationRate(ctx context.Context, cardId, deviceId uint32, devType DeviceType) (int32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var rate uint32
	if err := checkReturnCode("dcmi_get_device_utilization_rate",
		dcGetDeviceUtilizationRate(cardId, deviceId, devType.Code, &rate)); err != nil {
		return 0, err
	}

	if rate > 100 {
		return 0, fmt.Errorf("invalid utilization rate (name: %v, code: %d): %d", devType.Name, devType.Code, rate)
	}

	return int32(rate), nil
}

// DcGetDeviceFrequency returns the frequency in MHz for the specified clock domain.
// Ascend910B supports frequency types: 2, 6, 7, 9.
// Ascend910 supports frequency types: 2, 6, 7, 9.
// Ascend310 supports frequency types: 1, 2, 6, 7, 9.
// Ascend310P supports frequency types: 1, 2, 7, 9, 12.
func (l *library) DcGetDeviceFrequency(ctx context.Context, cardId, deviceId uint32, devType DeviceType) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var freq uint32
	if err := checkReturnCode("dcmi_get_device_frequency",
		dcGetDeviceFrequency(cardId, deviceId, devType.Code, &freq)); err != nil {
		return 0, err
	}

	if freq >= math.MaxInt32 {
		return 0, fmt.Errorf("frequency value out of range (name: %v, code: %d): %d", devType.Name, devType.Code, freq)
	}

	return freq, nil
}

// DcGetDeviceNetWorkHealth returns the network health status for a device.
// This call includes a 1-second timeout to prevent blocking on dropped cards.
func (l *library) DcGetDeviceNetWorkHealth(ctx context.Context, cardId, deviceId uint32) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	type result struct {
		health uint32
		err    error
	}

	ch := make(chan result, 1)
	go func() {
		var health uint32
		ret := dcGetDeviceNetWorkHealth(cardId, deviceId, &health)
		ch <- result{health, checkReturnCode("dcmi_get_device_network_health", ret)}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			return 0, res.err
		}
		if res.health > math.MaxInt8 {
			return 0, fmt.Errorf("invalid network health code: %d", res.health)
		}
		return res.health, nil
	case <-time.After(1 * time.Second):
		return 0, fmt.Errorf("dcmi_get_device_network_health timeout for card %d device %d", cardId, deviceId)
	}
}

// DcGetHbmInfo returns HBM memory information for a device. A310/A310P not support.
func (l *library) DcGetHbmInfo(ctx context.Context, cardId, deviceId uint32) (*HbmInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var info dcmiHbmInfo
	if err := checkReturnCode("dcmi_get_device_hbm_info",
		dcGetDeviceHbmInfo(cardId, deviceId, &info)); err != nil {
		return nil, err
	}

	if info.temp < 0 {
		return nil, fmt.Errorf("invalid HBM temperature: %d", info.temp)
	}

	return &HbmInfo{
		MemorySize:        info.memorySize,
		Frequency:         info.freq,
		Usage:             info.memoryUsage,
		Temp:              info.temp,
		BandWidthUtilRate: info.bandWidthUtilRate,
	}, nil
}

// DcGetDeviceEccInfo returns ECC error information for a device component type.
func (l *library) DcGetDeviceEccInfo(ctx context.Context, cardId, deviceId uint32, deviceType DcmiDeviceType) (*ECCInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var info dcmiEccInfo
	if err := checkReturnCode("dcmi_get_device_ecc_info",
		dcGetDeviceEccInfo(cardId, deviceId, int32(deviceType), &info)); err != nil {
		return nil, err
	}

	return &ECCInfo{
		EnableFlag:                info.enableFlag,
		SingleBitErrorCnt:         int64(info.singleBitErrorCnt),
		DoubleBitErrorCnt:         int64(info.doubleBitErrorCnt),
		TotalSingleBitErrorCnt:    int64(info.totalSingleBitErrorCnt),
		TotalDoubleBitErrorCnt:    int64(info.totalDoubleBitErrorCnt),
		SingleBitIsolatedPagesCnt: int64(info.singleBitIsolatedPagesCnt),
		DoubleBitIsolatedPagesCnt: int64(info.doubleBitIsolatedPagesCnt),
	}, nil
}

// DcGetPCIeBusInfo returns the PCIe BDF string (e.g. "0000:01:00.0") for a device.
func (l *library) DcGetPCIeBusInfo(ctx context.Context, cardId, deviceId uint32) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	var info dcmiPcieInfoAll
	if err := checkReturnCode("dcmi_get_device_pcie_info_v2",
		dcGetDevicePcieInfoV2(cardId, deviceId, &info)); err != nil {
		return "", err
	}

	bdf := fmt.Sprintf("%04X:%02X:%02X.%-4X",
		info.domain, info.bdfBusid, info.bdfDeviceid, info.bdfFuncid)

	return strings.TrimRight(bdf, " "), nil
}

// DcGetDeviceLogicID returns the logic ID for a given card and device.
func (l *library) DcGetDeviceLogicID(cardId, deviceId int32) (int32, error) {
	var logicID int32
	if err := checkReturnCode("dcmi_get_device_logic_id",
		dcGetDeviceLogicID(&logicID, cardId, deviceId)); err != nil {
		return 0, err
	}
	if logicID < 0 {
		return 0, fmt.Errorf("invalid logic ID: %d", logicID)
	}
	return logicID, nil
}

// DcGetPhysicIDFromLogicID returns the physical ID for a given logic ID.
func (l *library) DcGetPhysicIDFromLogicID(logicID uint32) (int32, error) {
	var phyID uint32
	if err := checkReturnCode("dcmi_get_device_phyid_from_logicid",
		dcGetPhysicIDFromLogicID(logicID, &phyID)); err != nil {
		return 0, err
	}
	if phyID >= math.MaxInt32 {
		return 0, fmt.Errorf("invalid physical ID: %d", phyID)
	}
	return int32(phyID), nil
}
