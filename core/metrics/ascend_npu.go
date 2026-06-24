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
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"huatuo-bamai/core/metrics/ascend/dcmi"
	"huatuo-bamai/core/metrics/ascend/hccn"
	"huatuo-bamai/core/metrics/ascend/pcie"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("ascend_npu", newAscendNpuCollector)
}

// deviceKey identifies a single NPU device.
type deviceKey struct {
	cardId, deviceId uint32
}

// npuCache holds topology data that is static after DcInit.
// It is invalidated and rebuilt when DcInit is re-called.
type npuCache struct {
	devices []deviceKey
}

type ascendNpuCollector struct {
	cache atomic.Pointer[npuCache]
}

func newAscendNpuCollector() (*tracing.EventTracingAttr, error) {
	if err := dcmi.DcInit(); err != nil {
		log.Errorf("ascend: DcInit failed: %v", err)
		return nil, types.ErrNotSupported
	}

	c := &ascendNpuCollector{}
	c.refreshCache()
	return &tracing.EventTracingAttr{
		TracingData: c,
		Flag:        tracing.FlagMetric,
	}, nil
}

func (a *ascendNpuCollector) Update() ([]*metric.Data, error) {
	ctx := context.Background()
	metrics, err := ascendCollectMetrics(ctx, a.getDevices())
	if err != nil {
		var dcmiErr *dcmi.Error
		if ok := errors.As(err, &dcmiErr); ok {
			log.Errorf("ascend_npu: dcmi error, re-initing and retrying: %v", err)

			if err := dcmi.DcInit(); err != nil {
				return nil, fmt.Errorf("failed to re-init dcmi: %w", err)
			}
			a.cache.Store(nil)
			return ascendCollectMetrics(ctx, a.getDevices())
		}

		return nil, err
	}

	return metrics, nil
}

func (a *ascendNpuCollector) getDevices() []deviceKey {
	if c := a.cache.Load(); c != nil {
		return c.devices
	}
	return a.refreshCache()
}

func (a *ascendNpuCollector) refreshCache() []deviceKey {
	ctx := context.Background()
	_, cardList, err := dcmi.DcGetCardList(ctx)
	if err != nil {
		log.Errorf("ascend: failed to get card list for cache: %v", err)
		return nil
	}

	var devices []deviceKey
	for _, cardId := range cardList {
		deviceNum, err := dcmi.DcGetDeviceNumInCard(ctx, cardId)
		if err != nil {
			log.Errorf("ascend: failed to get device count for card %d: %v", cardId, err)
			continue
		}
		for devId := int32(0); devId < deviceNum; devId++ {
			devices = append(devices, deviceKey{uint32(cardId), uint32(devId)})
		}
	}

	a.cache.Store(&npuCache{devices: devices})
	return devices
}

func ascendCollectMetrics(ctx context.Context, devices []deviceKey) ([]*metric.Data, error) {
	if len(devices) == 0 {
		return nil, fmt.Errorf("no npu devices found")
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

// ascendCollectNpuMetrics collects all metrics for a single NPU device.
// All groups run in parallel and are best-effort — individual failures
// do not block other groups.
func ascendCollectNpuMetrics(ctx context.Context, cardId, deviceId uint32) ([]*metric.Data, error) {
	metrics := make([]*metric.Data, 0, 32)

	npuLabels := map[string]string{
		"card":   strconv.Itoa(int(cardId)),
		"device": strconv.Itoa(int(deviceId)),
	}

	t := time.Now()
	type metricResult struct {
		data []*metric.Data
	}
	ch := make(chan metricResult, 11)
	var wg sync.WaitGroup

	if cfg.AscendNPU.EnableDCMI {
		// Health
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h, err := dcmi.DcGetDeviceHealth(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: health for card %d device %d failed: %v", cardId, deviceId, err)
				}
			} else {
				ch <- metricResult{[]*metric.Data{
					metric.NewGaugeData("npu_device_health", float64(h),
						"NPU device health status.", npuLabels),
				}}
			}
		}()

		// Power
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := dcmi.DcGetDevicePowerInfo(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: power for card %d device %d failed: %v", cardId, deviceId, err)
				}
			} else {
				ch <- metricResult{[]*metric.Data{
					metric.NewGaugeData("npu_power", float64(p),
						"NPU device power consumption in watts.", npuLabels),
				}}
			}
		}()

		// Temperature
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tmp, err := dcmi.DcGetDeviceTemperature(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: temperature for card %d device %d failed: %v", cardId, deviceId, err)
				}
			} else {
				ch <- metricResult{[]*metric.Data{
					metric.NewGaugeData("npu_temperature", float64(tmp),
						"NPU device temperature in degrees Celsius.", npuLabels),
				}}
			}
		}()

		// Voltage
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := dcmi.DcGetDeviceVoltage(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: voltage for card %d device %d failed: %v", cardId, deviceId, err)
				}
			} else {
				ch <- metricResult{[]*metric.Data{
					metric.NewGaugeData("npu_voltage", float64(v),
						"NPU device voltage in volts.", npuLabels),
				}}
			}
		}()

		// Utilization rates
		wg.Add(1)
		go func() {
			defer wg.Done()
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
					if !dcmi.IsNotSupported(err) {
						log.Debugf("ascend: utilization %s for card %d device %d failed: %v", ut.devType.Name, cardId, deviceId, err)
					}
					continue
				}
				data = append(
					data,
					metric.NewGaugeData(ut.metricName, float64(rate),
						"NPU device utilization rate (0-100%).", npuLabels),
				)
			}
			ch <- metricResult{data}
		}()

		// Frequencies
		wg.Add(1)
		go func() {
			defer wg.Done()
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
					if !dcmi.IsNotSupported(err) {
						log.Debugf("ascend: frequency %s for card %d device %d failed: %v", ft.devType.Name, cardId, deviceId, err)
					}
					continue
				}
				data = append(
					data,
					metric.NewGaugeData(ft.metricName, float64(freq),
						"NPU device frequency in MHz.", npuLabels),
				)
			}
			ch <- metricResult{data}
		}()

		// Network health
		wg.Add(1)
		go func() {
			defer wg.Done()
			if netHealth, err := dcmi.DcGetDeviceNetWorkHealth(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: network health for card %d device %d failed: %v", cardId, deviceId, err)
				}
			} else {
				ch <- metricResult{[]*metric.Data{
					metric.NewGaugeData("npu_device_network_health", float64(netHealth),
						"NPU device network health.", npuLabels),
				}}
			}
		}()

		// HBM info
		wg.Add(1)
		go func() {
			defer wg.Done()
			if hbmInfo, err := dcmi.DcGetHbmInfo(ctx, cardId, deviceId); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: HBM info for card %d device %d failed: %v", cardId, deviceId, err)
				}
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
		}()

		// ECC info
		wg.Add(1)
		go func() {
			defer wg.Done()
			if eccInfo, err := dcmi.DcGetDeviceEccInfo(ctx, cardId, deviceId, dcmi.DcmiDeviceTypeHBM); err != nil {
				if !dcmi.IsNotSupported(err) {
					log.Debugf("ascend: ECC info for card %d device %d failed: %v", cardId, deviceId, err)
				}
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
		}()
	}

	// PCIe link info
	if cfg.AscendNPU.EnablePCIe {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch <- metricResult{collectPCIeMetrics(ctx, cardId, deviceId)}
		}()
	}

	// HCCN network metrics
	if cfg.AscendNPU.EnableHCCN {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch <- metricResult{collectHccnMetrics(int32(cardId), int32(deviceId), npuLabels)}
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for r := range ch {
		metrics = append(metrics, r.data...)
	}

	log.Debugf("ascend: card %d device %d took %v", cardId, deviceId, time.Since(t))
	return metrics, nil
}

func collectPCIeMetrics(ctx context.Context, cardId, deviceId uint32) []*metric.Data {
	bdf, err := dcmi.DcGetPCIeBusInfo(ctx, cardId, deviceId)
	if err != nil {
		if !dcmi.IsNotSupported(err) {
			log.Debugf("ascend: PCIe BDF for card %d device %d failed: %v", cardId, deviceId, err)
		}
		return nil
	}
	linkInfo, err := pcie.GetPCIeLinkInfo(bdf)
	if err != nil {
		log.Debugf("ascend: PCIe link info for %s failed: %v", bdf, err)
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

// hccnStatMetrics maps hccn_tool stat keys to Prometheus metric names.
var hccnStatMetrics = map[string]string{
	"mac_tx_mac_pause_num":  "npu_mac_tx_mac_pause_num",
	"mac_rx_mac_pause_num":  "npu_mac_rx_mac_pause_num",
	"mac_tx_pfc_pkt_num":    "npu_mac_tx_pfc_pkt_num",
	"mac_rx_pfc_pkt_num":    "npu_mac_rx_pfc_pkt_num",
	"mac_tx_bad_pkt_num":    "npu_mac_tx_bad_pkt_num",
	"mac_rx_bad_pkt_num":    "npu_mac_rx_bad_pkt_num",
	"roce_tx_err_pkt_num":   "npu_roce_tx_err_pkt_num",
	"roce_rx_err_pkt_num":   "npu_roce_rx_err_pkt_num",
	"roce_tx_all_pkt_num":   "npu_roce_tx_all_pkt_num",
	"roce_rx_all_pkt_num":   "npu_roce_rx_all_pkt_num",
	"roce_new_pkt_rty_num":  "npu_roce_new_pkt_rty_num",
	"roce_out_of_order_num": "npu_roce_out_of_order_num",
	"roce_rx_cnp_pkt_num":   "npu_roce_rx_cnp_pkt_num",
	"roce_tx_cnp_pkt_num":   "npu_roce_tx_cnp_pkt_num",
}

// hccnOptMetrics maps hccn_tool optical keys to Prometheus metric names.
var hccnOptMetrics = map[string]string{
	"Temperature":     "npu_opt_temperature",
	"Temp_High_Thres": "npu_opt_temperature_high_thres",
	"Temp_Low_Thres":  "npu_opt_temperature_low_thres",
	"Vcc":             "npu_opt_voltage",
	"Vcc_High_Thres":  "npu_opt_voltage_high_thres",
	"Vcc_Low_Thres":   "npu_opt_voltage_low_thres",
	"Tx_Power0":       "npu_opt_tx_power_lane0",
	"Tx_Power1":       "npu_opt_tx_power_lane1",
	"Tx_Power2":       "npu_opt_tx_power_lane2",
	"Tx_Power3":       "npu_opt_tx_power_lane3",
	"Rx_Power0":       "npu_opt_rx_power_lane0",
	"Rx_Power1":       "npu_opt_rx_power_lane1",
	"Rx_Power2":       "npu_opt_rx_power_lane2",
	"Rx_Power3":       "npu_opt_rx_power_lane3",
	"Tx_Bias0":        "npu_opt_tx_bias_lane0",
	"Tx_Bias1":        "npu_opt_tx_bias_lane1",
	"Tx_Bias2":        "npu_opt_tx_bias_lane2",
	"Tx_Bias3":        "npu_opt_tx_bias_lane3",
	"Tx_Los_Flag":     "npu_opt_tx_los",
	"Rx_Los_Flag":     "npu_opt_rx_los",
	"Media_SNR_Lane0": "npu_opt_media_snr_lane0",
	"Media_SNR_Lane1": "npu_opt_media_snr_lane1",
	"Media_SNR_Lane2": "npu_opt_media_snr_lane2",
	"Media_SNR_Lane3": "npu_opt_media_snr_lane3",
}

func collectHccnMetrics(cardId, deviceId int32, npuLabels map[string]string) []*metric.Data {
	phyID := cardId
	if logicID, err := dcmi.DcGetDeviceLogicID(cardId, deviceId); err != nil {
		if !dcmi.IsNotSupported(err) {
			log.Debugf("ascend: DcGetDeviceLogicID(%d, %d) failed, using cardId as phyID: %v", cardId, deviceId, err)
		}
	} else if phy, err := dcmi.DcGetPhysicIDFromLogicID(uint32(logicID)); err != nil {
		if !dcmi.IsNotSupported(err) {
			log.Debugf("ascend: DcGetPhysicIDFromLogicID(%d) failed, using cardId as phyID: %v", logicID, err)
		}
	} else {
		phyID = phy
	}

	type hccnResult struct {
		data []*metric.Data
	}
	ch := make(chan hccnResult, 4)
	var wg sync.WaitGroup

	// Link status
	wg.Add(1)
	go func() {
		defer wg.Done()
		status, err := hccn.GetLinkStatus(phyID)
		if err != nil {
			log.Debugf("ascend: hccn link status for phy %d failed: %v", phyID, err)
			return
		}
		linkVal := float64(0)
		if status == "UP" {
			linkVal = 1
		}
		ch <- hccnResult{[]*metric.Data{
			metric.NewGaugeData("npu_network_port_link_status", linkVal,
				"NPU network port link status. 0: DOWN, 1: UP.", npuLabels),
		}}
	}()

	// Bandwidth
	wg.Add(1)
	go func() {
		defer wg.Done()
		tx, rx, err := hccn.GetInterfaceTraffic(phyID)
		if err != nil {
			log.Debugf("ascend: hccn bandwidth for phy %d failed: %v", phyID, err)
			return
		}
		ch <- hccnResult{[]*metric.Data{
			metric.NewGaugeData("npu_roce_tx_rate", tx, "NPU RoCE transmit rate in MB/sec.", npuLabels),
			metric.NewGaugeData("npu_roce_rx_rate", rx, "NPU RoCE receive rate in MB/sec.", npuLabels),
		}}
	}()

	// Stat counters
	wg.Add(1)
	go func() {
		defer wg.Done()
		statInfo, err := hccn.GetStatInfo(phyID)
		if err != nil {
			log.Debugf("ascend: hccn stat for phy %d failed: %v", phyID, err)
			return
		}
		var data []*metric.Data
		for key, metricName := range hccnStatMetrics {
			if val, ok := statInfo[key]; ok {
				data = append(
					data,
					metric.NewCounterData(metricName, float64(val), "NPU network stat counter.", npuLabels),
				)
			}
		}
		ch <- hccnResult{data}
	}()

	// Optical info
	wg.Add(1)
	go func() {
		defer wg.Done()
		optInfo, err := hccn.GetOpticalInfo(phyID)
		if err != nil {
			log.Debugf("ascend: hccn optical for phy %d failed: %v", phyID, err)
			return
		}
		var data []*metric.Data
		for key, metricName := range hccnOptMetrics {
			if val, ok := optInfo[key]; ok {
				data = append(
					data,
					metric.NewGaugeData(metricName, val, "NPU optical module info.", npuLabels),
				)
			}
		}
		ch <- hccnResult{data}
	}()

	go func() {
		wg.Wait()
		close(ch)
	}()

	var metrics []*metric.Data
	for r := range ch {
		metrics = append(metrics, r.data...)
	}

	return metrics
}
