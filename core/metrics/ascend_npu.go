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

package collector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"huatuo-bamai/core/metrics/ascend/dcmi"
	"huatuo-bamai/core/metrics/ascend/pcie"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("ascend_npu", newAscendNpuCollector)
}

type ascendNpuCollector struct{}

func newAscendNpuCollector() (*tracing.EventTracingAttr, error) {
	if err := dcmi.DcInit(); err != nil {
		log.Errorf("ascend: DcInit failed: %v", err)
		return nil, types.ErrNotSupported
	}

	return &tracing.EventTracingAttr{
		TracingData: &ascendNpuCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (a *ascendNpuCollector) Update() ([]*metric.Data, error) {
	ctx := context.Background()
	metrics, err := ascendCollectMetrics(ctx)
	if err != nil {
		var dcmiErr *dcmi.Error
		if ok := errors.As(err, &dcmiErr); ok {
			log.Errorf("ascend_npu: dcmi error, re-initing and retrying: %v", err)

			if err := dcmi.DcInit(); err != nil {
				return nil, fmt.Errorf("failed to re-init dcmi: %w", err)
			}
			return ascendCollectMetrics(ctx)
		}

		return nil, err
	}

	return metrics, nil
}

func ascendCollectMetrics(ctx context.Context) ([]*metric.Data, error) {
	_, cardList, err := dcmi.DcGetCardList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get card list: %w", err)
	}

	// Build list of all (cardId, deviceId) pairs
	type device struct {
		cardId, deviceId uint32
	}
	var devices []device
	for _, cardId := range cardList {
		deviceNum, err := dcmi.DcGetDeviceNumInCard(ctx, cardId)
		if err != nil {
			return nil, fmt.Errorf("failed to get device count for card %d: %w", cardId, err)
		}
		for devId := int32(0); devId < deviceNum; devId++ {
			devices = append(devices, device{uint32(cardId), uint32(devId)})
		}
	}

	var (
		metrics []*metric.Data
		mu      sync.Mutex
	)
	eg, subCtx := errgroup.WithContext(ctx)
	for _, dev := range devices {
		eg.Go(func() error {
			npuMetrics, err := ascendCollectNpuMetrics(subCtx, dev.cardId, dev.deviceId)
			if err != nil {
				return fmt.Errorf("failed to collect npu metrics for card %d device %d: %w",
					dev.cardId, dev.deviceId, err)
			}
			mu.Lock()
			metrics = append(metrics, npuMetrics...)
			mu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return metrics, nil
}

func ascendCollectNpuMetrics(ctx context.Context, cardId, deviceId uint32) ([]*metric.Data, error) {
	// Pre-allocate for ~30 metrics per device.
	metrics := make([]*metric.Data, 0, 32)

	npuLabels := map[string]string{
		"card":   strconv.Itoa(int(cardId)),
		"device": strconv.Itoa(int(deviceId)),
	}

	// Phase 1: Mandatory metrics — all parallel, fail-fast on any error.
	phase1Start := time.Now()
	type phase1Result struct {
		health       uint32
		power        float32
		temperature  int32
		voltage      float32
		healthErr    error
		powerErr     error
		tempErr      error
		voltageErr   error
	}
	var p1 phase1Result
	var wg1 sync.WaitGroup
	wg1.Add(4)
	go func() { defer wg1.Done(); p1.health, p1.healthErr = dcmi.DcGetDeviceHealth(ctx, cardId, deviceId) }()
	go func() { defer wg1.Done(); p1.power, p1.powerErr = dcmi.DcGetDevicePowerInfo(ctx, cardId, deviceId) }()
	go func() { defer wg1.Done(); p1.temperature, p1.tempErr = dcmi.DcGetDeviceTemperature(ctx, cardId, deviceId) }()
	go func() { defer wg1.Done(); p1.voltage, p1.voltageErr = dcmi.DcGetDeviceVoltage(ctx, cardId, deviceId) }()
	wg1.Wait()

	if p1.healthErr != nil {
		return nil, fmt.Errorf("failed to get device health: %w", p1.healthErr)
	}
	if p1.powerErr != nil {
		return nil, fmt.Errorf("failed to get device power: %w", p1.powerErr)
	}
	if p1.tempErr != nil {
		return nil, fmt.Errorf("failed to get device temperature: %w", p1.tempErr)
	}
	if p1.voltageErr != nil {
		return nil, fmt.Errorf("failed to get device voltage: %w", p1.voltageErr)
	}

	metrics = append(metrics,
		metric.NewGaugeData("npu_device_health", float64(p1.health),
			"NPU device health status. 0: normal, 1: warning, 2: major, 3: critical, 0xFFFFFFFF: device missing.", npuLabels),
		metric.NewGaugeData("npu_power", float64(p1.power),
			"NPU device power consumption in watts.", npuLabels),
		metric.NewGaugeData("npu_temperature", float64(p1.temperature),
			"NPU device temperature in degrees Celsius.", npuLabels),
		metric.NewGaugeData("npu_voltage", float64(p1.voltage),
			"NPU device voltage in volts.", npuLabels),
	)
	log.Infof("ascend: phase1(mandatory) took %v", time.Since(phase1Start))

	// Phase 2: Optional metrics — best-effort, all groups parallel.
	phase2Start := time.Now()
	type metricResult struct {
		data []*metric.Data
	}
	ch := make(chan metricResult, 6)
	var wg2 sync.WaitGroup

	// Group A: Utilization rates (5 calls in sequence, cheap enough to keep serial).
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		utilTypes := []struct {
			metricName string
			devType    dcmi.DeviceType
		}{
			{"npu_util_rate_hbm", dcmi.DeviceTypeHBM},
			{"npu_util_rate_ai_core", dcmi.DeviceTypeAICore},
			{"npu_util_rate_vector_core", dcmi.DeviceTypeVectorCore},
			{"npu_util_rate_ai_cpu", dcmi.DeviceTypeAICPU},
			{"npu_util_rate_ctrl_cpu", dcmi.DeviceTypeCtrlCPU},
		}
		var data []*metric.Data
		for _, ut := range utilTypes {
			rate, err := dcmi.DcGetDeviceUtilizationRate(ctx, cardId, deviceId, ut.devType)
			if err != nil {
				log.Infof("ascend: utilization %s for card %d device %d failed: %v", ut.devType.Name, cardId, deviceId, err)
				continue
			}
			data = append(data,
				metric.NewGaugeData(ut.metricName, float64(rate),
					"NPU device utilization rate (0-100%).", npuLabels),
			)
		}
		log.Infof("ascend: util took %v", time.Since(t))
		ch <- metricResult{data}
	}()

	// Group B: Frequencies (3 calls in sequence, cheap enough to keep serial).
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		freqTypes := []struct {
			metricName string
			devType    dcmi.DeviceType
		}{
			{"npu_freq_ai_core", dcmi.FreqTypeAICore},
			{"npu_freq_ctrl_cpu", dcmi.FreqTypeCtrlCPU},
			{"npu_freq_ai_core_rated", dcmi.FreqTypeAICoreRated},
		}
		var data []*metric.Data
		for _, ft := range freqTypes {
			freq, err := dcmi.DcGetDeviceFrequency(ctx, cardId, deviceId, ft.devType)
			if err != nil {
				log.Infof("ascend: frequency %s for card %d device %d failed: %v", ft.devType.Name, cardId, deviceId, err)
				continue
			}
			data = append(data,
				metric.NewGaugeData(ft.metricName, float64(freq),
					"NPU device frequency in MHz.", npuLabels),
			)
		}
		log.Infof("ascend: freq took %v", time.Since(t))
		ch <- metricResult{data}
	}()

	// Group C: Network health (has internal 1s timeout).
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		if netHealth, err := dcmi.DcGetDeviceNetWorkHealth(ctx, cardId, deviceId); err != nil {
			log.Infof("ascend: network health for card %d device %d failed: %v", cardId, deviceId, err)
		} else {
			ch <- metricResult{[]*metric.Data{
				metric.NewGaugeData("npu_device_network_health", float64(netHealth),
					"NPU device network health. 0: normal, 1: socket create failed, 2: rx timeout, 3: ip unreachable, 4: probe timeout, 5: probe send failed, 6: probe init, 7: probe create failed, 8: setting probe ip.", npuLabels),
			}}
		}
		log.Infof("ascend: net_health took %v", time.Since(t))
	}()

	// Group D: HBM info.
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		if hbmInfo, err := dcmi.DcGetHbmInfo(ctx, cardId, deviceId); err != nil {
			log.Infof("ascend: HBM info for card %d device %d failed: %v", cardId, deviceId, err)
		} else {
			ch <- metricResult{[]*metric.Data{
				metric.NewGaugeData("npu_hbm_mem_capacity", float64(hbmInfo.MemorySize), "NPU HBM memory capacity in MB.", npuLabels),
				metric.NewGaugeData("npu_hbm_freq", float64(hbmInfo.Frequency), "NPU HBM frequency in MHz.", npuLabels),
				metric.NewGaugeData("npu_freq_hbm", float64(hbmInfo.Frequency), "NPU HBM frequency in MHz.", npuLabels),
				metric.NewGaugeData("npu_hbm_usage", float64(hbmInfo.Usage), "NPU HBM memory usage in MB.", npuLabels),
				metric.NewGaugeData("npu_hbm_temperature", float64(hbmInfo.Temp), "NPU HBM temperature in degrees Celsius.", npuLabels),
				metric.NewGaugeData("npu_hbm_bandwidth_util", float64(hbmInfo.BandWidthUtilRate), "NPU HBM bandwidth utilization (%).", npuLabels),
				metric.NewGaugeData("npu_util_rate_hbm_bw", float64(hbmInfo.BandWidthUtilRate), "NPU HBM bandwidth utilization (%).", npuLabels),
			}}
		}
		log.Infof("ascend: hbm took %v", time.Since(t))
	}()

	// Group E: ECC info.
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		if eccInfo, err := dcmi.DcGetDeviceEccInfo(ctx, cardId, deviceId, dcmi.DcmiDeviceTypeHBM); err != nil {
			log.Infof("ascend: ECC info for card %d device %d failed: %v", cardId, deviceId, err)
		} else {
			ch <- metricResult{[]*metric.Data{
				metric.NewGaugeData("npu_hbm_ecc_enable", float64(eccInfo.EnableFlag), "NPU HBM ECC enable flag.", npuLabels),
				metric.NewCounterData("npu_hbm_single_bit_error_cnt", float64(eccInfo.SingleBitErrorCnt), "NPU HBM current single-bit error count.", npuLabels),
				metric.NewCounterData("npu_hbm_double_bit_error_cnt", float64(eccInfo.DoubleBitErrorCnt), "NPU HBM current double-bit error count.", npuLabels),
				metric.NewCounterData("npu_hbm_total_single_bit_error_cnt", float64(eccInfo.TotalSingleBitErrorCnt), "NPU HBM lifetime single-bit error count.", npuLabels),
				metric.NewCounterData("npu_hbm_total_double_bit_error_cnt", float64(eccInfo.TotalDoubleBitErrorCnt), "NPU HBM lifetime double-bit error count.", npuLabels),
				metric.NewCounterData("npu_hbm_single_bit_isolated_pages_cnt", float64(eccInfo.SingleBitIsolatedPagesCnt), "NPU HBM single-bit error isolated pages count.", npuLabels),
				metric.NewCounterData("npu_hbm_double_bit_isolated_pages_cnt", float64(eccInfo.DoubleBitIsolatedPagesCnt), "NPU HBM double-bit error isolated pages count.", npuLabels),
			}}
		}
		log.Infof("ascend: ecc took %v", time.Since(t))
	}()

	// Group F: PCIe link info (DCMI + lspci).
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		t := time.Now()
		ch <- metricResult{collectPCIeMetrics(ctx, cardId, deviceId)}
		log.Infof("ascend: pcie took %v", time.Since(t))
	}()

	go func() {
		wg2.Wait()
		close(ch)
	}()

	for r := range ch {
		metrics = append(metrics, r.data...)
	}

	log.Infof("ascend: phase2(optional) took %v", time.Since(phase2Start))
	return metrics, nil
}

func collectPCIeMetrics(ctx context.Context, cardId, deviceId uint32) []*metric.Data {
	bdf, err := dcmi.DcGetPCIeBusInfo(ctx, cardId, deviceId)
	if err != nil {
		log.Infof("ascend: PCIe BDF for card %d device %d failed: %v", cardId, deviceId, err)
		return nil
	}
	linkInfo, err := pcie.GetPCIeLinkInfo(bdf)
	if err != nil {
		log.Infof("ascend: PCIe link info for %s failed: %v", bdf, err)
		return nil
	}
	labels := map[string]string{
		"card":   strconv.Itoa(int(cardId)),
		"device": strconv.Itoa(int(deviceId)),
		"bdf":    bdf,
	}
	return []*metric.Data{
		metric.NewGaugeData("npu_link_cap_speed", linkInfo.CapSpeed,
			"NPU PCIe link maximum speed in GT/s.", labels),
		metric.NewGaugeData("npu_link_cap_width", float64(linkInfo.CapWidth),
			"NPU PCIe link maximum width in lanes.", labels),
		metric.NewGaugeData("npu_link_status_speed", linkInfo.StatusSpeed,
			"NPU PCIe link current speed in GT/s.", labels),
		metric.NewGaugeData("npu_link_status_width", float64(linkInfo.StatusWidth),
			"NPU PCIe link current width in lanes.", labels),
	}
}
