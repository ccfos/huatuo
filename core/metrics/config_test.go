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

package collector

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ccfos/huatuo/core/metrics/ascend/dcmi"
	testutils "github.com/ccfos/huatuo/internal/testing"
	"github.com/ccfos/huatuo/pkg/metric"
)

func TestConfigCloneDoesNotShareMutableReferences(t *testing.T) {
	source := &Config{}
	testutils.PopulateCloneSource(t, source)

	testutils.AssertDeepClone(t, source, source.Clone())
}

func TestSetPublishesIndependentConfig(t *testing.T) {
	src := &Config{}
	src.NetdevDCB.DeviceList = []string{"eth0"}
	Set(src)
	src.NetdevDCB.DeviceList[0] = "eth1"

	if got := configSnapshot().NetdevDCB.DeviceList[0]; got != "eth0" {
		t.Fatalf("NetdevDCB.DeviceList[0] = %q, want detached value", got)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	type filters struct {
		included string
		excluded string
	}
	pairs := []filters{{"eth0", "lo"}, {"eth1", "docker0"}}
	Set(&Config{})
	valid := map[filters]bool{{}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair filters) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.NetdevStats.DeviceIncluded = pair.included
				cfg.NetdevStats.DeviceExcluded = pair.excluded
				Set(cfg)
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				cfg := configSnapshot()
				got := filters{
					included: cfg.NetdevStats.DeviceIncluded,
					excluded: cfg.NetdevStats.DeviceExcluded,
				}
				if !valid[got] {
					select {
					case errCh <- fmt.Errorf("observed mixed config snapshot: %+v", got):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestAscendCollectionPreservesErrorsAndPartialMetrics(t *testing.T) {
	tests := []struct {
		name    string
		devices []deviceKey
	}{
		{
			name: "one device",
			devices: []deviceKey{
				{
					cardId:   0,
					deviceId: 0,
				},
			},
		},
		{
			name: "independent devices",
			devices: []deviceKey{
				{
					cardId:   0,
					deviceId: 0,
				},
				{
					cardId:   1,
					deviceId: 0,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAscendDCMI(t)
			readErr := errors.New("health read failed")
			dcmi.DcGetDeviceHealth = func(_ context.Context, cardID, _ uint32) (uint32, error) {
				if cardID == 0 {
					return 0, readErr
				}

				return 0, nil
			}

			metrics, err := ascendCollectMetrics(t.Context(), tt.devices)
			if !errors.Is(err, readErr) {
				t.Fatalf("ascendCollectMetrics() error = %v, want health read failure", err)
			}

			for _, device := range tt.devices {
				if !hasAscendMetric(
					metrics,
					"npu_power",
					device.cardId,
					17,
				) {
					t.Fatalf("ascendCollectMetrics() omitted power for card %d", device.cardId)
				}
			}
		})
	}
}

func TestAscendCollectorUpdateRecovery(t *testing.T) {
	dcmiErr := &dcmi.Error{}
	readErr := errors.New("health read failed")
	reinitializeErr := errors.New("native initialization failed")
	retryErr := errors.New("retry read failed")
	tests := []struct {
		name            string
		initialErr      error
		reinitializeErr error
		retryHealthErr  error
		retryPowerErr   error
		wantErr         error
		wantPower       float64
		wantRetries     int
	}{
		{
			name:      "healthy scrape",
			wantPower: 17,
		},
		{
			name:       "ordinary errors do not reinitialize native library",
			initialErr: readErr,
			wantErr:    readErr,
			wantPower:  17,
		},
		{
			name:        "typed error recovers once",
			initialErr:  dcmiErr,
			wantPower:   18,
			wantRetries: 1,
		},
		{
			name:            "reinitialization failure retains successful groups",
			initialErr:      dcmiErr,
			reinitializeErr: reinitializeErr,
			wantErr:         reinitializeErr,
			wantPower:       17,
			wantRetries:     1,
		},
		{
			name:           "retry failure retains error and replaces fresh samples",
			initialErr:     dcmiErr,
			retryHealthErr: retryErr,
			wantErr:        retryErr,
			wantPower:      18,
			wantRetries:    1,
		},
		{
			name:          "retry cannot discard a previously successful group",
			initialErr:    dcmiErr,
			retryPowerErr: retryErr,
			wantErr:       retryErr,
			wantPower:     17,
			wantRetries:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAscendDCMI(t)
			var attempt atomic.Int32
			attempt.Store(1)
			dcmi.DcGetDeviceHealth = func(context.Context, uint32, uint32) (uint32, error) {
				if attempt.Load() == 1 {
					return 0, tt.initialErr
				}

				return 0, tt.retryHealthErr
			}
			dcmi.DcGetDevicePowerInfo = func(context.Context, uint32, uint32) (float32, error) {
				if attempt.Load() == 1 {
					return 17, nil
				}

				return 18, tt.retryPowerErr
			}
			retries := 0
			collector := &ascendNpuCollector{
				reinitializeDCMI: func() error {
					retries++
					attempt.Store(2)
					return tt.reinitializeErr
				},
			}
			collector.cache.Store(&npuCache{devices: []deviceKey{{}}})

			metrics, err := collector.Update()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}

			if tt.wantErr != nil && errors.Is(tt.initialErr, dcmiErr) && !errors.Is(err, dcmiErr) {
				t.Fatalf("Update() error = %v, want original dcmi cause preserved", err)
			}

			if retries != tt.wantRetries {
				t.Fatalf("Update() reinitializations = %d, want %d", retries, tt.wantRetries)
			}

			if !hasAscendMetric(
				metrics,
				"npu_power",
				0,
				tt.wantPower,
			) {
				t.Fatalf("Update() omitted power value %v", tt.wantPower)
			}

			for i, data := range metrics {
				for _, other := range metrics[:i] {
					if data.Name() == other.Name() && maps.Equal(data.Labels(), other.Labels()) {
						t.Fatalf("Update() returned duplicate series %q %v", data.Name(), data.Labels())
					}
				}
			}
		})
	}
}

func TestAscendCollectorRecoversIncompleteTopology(t *testing.T) {
	stubAscendDCMI(t)
	initialErr := &dcmi.Error{}
	enumerationErr := errors.New("second card temporarily unavailable")
	var attempt atomic.Int32
	dcmi.DcGetDeviceHealth = func(_ context.Context, cardID, _ uint32) (uint32, error) {
		if attempt.Load() == 0 && cardID == 0 {
			return 0, initialErr
		}

		return 0, nil
	}
	dcmi.DcGetDevicePowerInfo = func(context.Context, uint32, uint32) (float32, error) {
		return float32(17 + attempt.Load()), nil
	}
	discoveries := 0
	dcmi.DcGetCardList = func(context.Context) (int32, []int32, error) {
		discoveries++
		return 2, []int32{0, 1}, nil
	}
	failEnumeration := true
	dcmi.DcGetDeviceNumInCard = func(_ context.Context, cardID int32) (int32, error) {
		if cardID == 1 && failEnumeration {
			return 0, enumerationErr
		}

		return 1, nil
	}
	reinitializations := 0
	collector := &ascendNpuCollector{
		reinitializeDCMI: func() error {
			reinitializations++
			attempt.Store(1)
			return nil
		},
	}
	collector.cache.Store(&npuCache{devices: []deviceKey{
		{
			cardId:   0,
			deviceId: 0,
		},
		{
			cardId:   1,
			deviceId: 0,
		},
	}})

	metrics, err := collector.Update()
	if !errors.Is(err, enumerationErr) || !errors.Is(err, initialErr) {
		t.Fatalf("incomplete topology error = %v, want enumeration and original DCMI causes", err)
	}

	if !hasAscendMetric(
		metrics,
		"npu_power",
		0,
		18,
	) || !hasAscendMetric(
		metrics,
		"npu_power",
		1,
		17,
	) {
		t.Fatal("incomplete retry lost a healthy or previously collected card")
	}

	failEnumeration = false
	metrics, err = collector.Update()
	if err != nil {
		t.Fatalf("recovered topology: %v", err)
	}

	if discoveries != 2 || reinitializations != 1 {
		t.Fatalf("discoveries=%d reinitializations=%d, want 2 and 1", discoveries, reinitializations)
	}

	if !hasAscendMetric(
		metrics,
		"npu_power",
		1,
		18,
	) {
		t.Fatal("recovered card was omitted from the next scrape")
	}

	if _, err := collector.Update(); err != nil || discoveries != 2 {
		t.Fatalf("complete topology did not remain cached: discoveries=%d error=%v", discoveries, err)
	}
}

func TestAscendCollectorTopologyErrors(t *testing.T) {
	topologyErr := errors.New("topology unavailable")
	metricErr := errors.New("device health unavailable")
	for _, test := range []struct {
		name             string
		isCardListError  bool
		isTypedError     bool
		hasMetricFailure bool
	}{
		{
			name:            "card list error",
			isCardListError: true,
		},
		{name: "card device enumeration error"},
		{
			name:             "topology and metric errors",
			hasMetricFailure: true,
		},
		{
			name:            "typed card list error retries once",
			isCardListError: true,
			isTypedError:    true,
		},
		{
			name:         "typed device enumeration error retries once",
			isTypedError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubAscendDCMI(t)
			var cause error = topologyErr
			if test.isTypedError {
				cause = &dcmi.Error{}
			}

			failing := true
			discoveries := 0
			dcmi.DcGetCardList = func(context.Context) (int32, []int32, error) {
				discoveries++
				if test.isCardListError && failing {
					return 0, nil, cause
				}

				return 2, []int32{0, 1}, nil
			}
			dcmi.DcGetDeviceNumInCard = func(_ context.Context, cardID int32) (int32, error) {
				if !test.isCardListError && failing && cardID == 1 {
					return 0, cause
				}

				return 1, nil
			}
			if test.hasMetricFailure {
				dcmi.DcGetDeviceHealth = func(context.Context, uint32, uint32) (uint32, error) {
					return 0, metricErr
				}
			}

			reinitializations := 0
			collector := &ascendNpuCollector{
				reinitializeDCMI: func() error {
					reinitializations++
					return nil
				},
			}
			metrics, err := collector.Update()
			if !errors.Is(err, cause) || (test.hasMetricFailure && !errors.Is(err, metricErr)) {
				t.Fatalf(
					"topology/collection error = %v, want %v and metric failure=%t",
					err,
					cause,
					test.hasMetricFailure,
				)
			}

			if !test.isCardListError && !hasAscendMetric(
				metrics,
				"npu_power",
				0,
				17,
			) {
				t.Fatal("partial topology lost its available card")
			}

			wantReinitializations := 0
			if test.isTypedError {
				wantReinitializations = 1
			}

			if reinitializations != wantReinitializations {
				t.Fatalf("reinitializations=%d, want %d", reinitializations, wantReinitializations)
			}

			before := discoveries
			failing = false
			metrics, err = collector.Update()
			if !test.hasMetricFailure && err != nil {
				t.Fatalf("recovered topology: %v", err)
			}

			if discoveries != before+1 || !hasAscendMetric(
				metrics,
				"npu_power",
				1,
				17,
			) {
				t.Fatal("failed topology was cached instead of rediscovered")
			}
		})
	}
}

func TestAscendCollectionRetainsIndependentGroupResults(t *testing.T) {
	readErr := errors.New("device read failed")
	tests := []struct {
		name       string
		stub       func()
		wantMetric string
		wantValue  float64
	}{
		{
			name: "utilization",
			stub: func() {
				dcmi.DcGetDeviceUtilizationRate = func(_ context.Context, _, _ uint32, deviceType dcmi.DeviceType) (int32, error) {
					if deviceType.Code == dcmi.DeviceTypeHBM.Code {
						return 0, readErr
					}

					return 55, nil
				}
			},
			wantMetric: "npu_util_rate_ai_core",
			wantValue:  55,
		},
		{
			name: "frequency",
			stub: func() {
				dcmi.DcGetDeviceFrequency = func(_ context.Context, _, _ uint32, deviceType dcmi.DeviceType) (uint32, error) {
					if deviceType.Code == dcmi.FreqTypeCtrlCPU.Code {
						return 0, readErr
					}

					return 1200, nil
				}
			},
			wantMetric: "npu_freq_ai_core",
			wantValue:  1200,
		},
		{
			name: "pcie address",
			stub: func() {
				cfg := configSnapshot().Clone()
				cfg.AscendNPU.EnablePCIe = true
				Set(cfg)
				dcmi.DcGetPCIeBusInfo = func(context.Context, uint32, uint32) (string, error) {
					return "", readErr
				}
			},
			wantMetric: "npu_power",
			wantValue:  17,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAscendDCMI(t)
			tt.stub()

			metrics, err := ascendCollectNpuMetrics(t.Context(), 0, 0)
			if !errors.Is(err, readErr) {
				t.Fatalf("ascendCollectNpuMetrics() error = %v, want read failure", err)
			}

			if !hasAscendMetric(
				metrics,
				tt.wantMetric,
				0,
				tt.wantValue,
			) {
				t.Fatalf("ascendCollectNpuMetrics() omitted successful %q", tt.wantMetric)
			}
		})
	}
}

func TestAscendCollectionReturnsAllGroupErrors(t *testing.T) {
	stubAscendDCMI(t)
	healthErr := errors.New("health read failed")
	powerErr := errors.New("power read failed")
	dcmi.DcGetDeviceHealth = func(context.Context, uint32, uint32) (uint32, error) {
		return 0, healthErr
	}
	dcmi.DcGetDevicePowerInfo = func(context.Context, uint32, uint32) (float32, error) {
		return 0, powerErr
	}

	metrics, err := ascendCollectNpuMetrics(t.Context(), 0, 0)
	if !errors.Is(err, healthErr) || !errors.Is(err, powerErr) {
		t.Fatalf("ascendCollectNpuMetrics() error = %v, want both group failures", err)
	}

	if !hasAscendMetric(
		metrics,
		"npu_temperature",
		0,
		0,
	) {
		t.Fatal("ascendCollectNpuMetrics() omitted successful temperature")
	}
}

func TestAscendPCIeSysfsFailureDoesNotReinitializeDCMI(t *testing.T) {
	stubAscendDCMI(t)
	cfg := configSnapshot().Clone()
	cfg.AscendNPU.EnablePCIe = true
	Set(cfg)
	dcmi.DcGetPCIeBusInfo = func(context.Context, uint32, uint32) (string, error) {
		return "\x00", nil
	}
	reinitializations := 0
	collector := &ascendNpuCollector{
		reinitializeDCMI: func() error {
			reinitializations++
			return nil
		},
	}
	collector.cache.Store(&npuCache{devices: []deviceKey{{}}})

	metrics, err := collector.Update()
	if err == nil || !strings.Contains(err.Error(), "pcie link") {
		t.Fatalf("Update() error = %v, want sysfs link failure", err)
	}

	if reinitializations != 0 {
		t.Fatalf("Update() reinitializations = %d, want none for sysfs errors", reinitializations)
	}

	if !hasAscendMetric(
		metrics,
		"npu_power",
		0,
		17,
	) {
		t.Fatal("Update() omitted successful DCMI power metric")
	}
}

func TestAscendDCMIReinitializeRejectsUnloadedLibrary(t *testing.T) {
	if err := dcmi.DcReinitialize(); err == nil {
		t.Fatal("DcReinitialize() succeeded without an acquired library reference")
	}
}

func TestAscendMetricRetryUsesCompleteSeriesIdentity(t *testing.T) {
	first := []*metric.Data{
		metric.NewGaugeData(
			"power",
			1,
			"power",
			map[string]string{
				"card":   "0",
				"device": "0",
			},
		),
		metric.NewGaugeData(
			"power",
			2,
			"power",
			map[string]string{
				"card":   "1",
				"device": "0",
			},
		),
		metric.NewGaugeData(
			"link",
			3,
			"link",
			map[string]string{"bdf": "0000:00:01.0"},
		),
	}
	retry := []*metric.Data{
		metric.NewGaugeData(
			"power",
			4,
			"power",
			map[string]string{
				"device": "0",
				"card":   "0",
			},
		),
		metric.NewGaugeData(
			"link",
			5,
			"link",
			map[string]string{"bdf": "0000:00:02.0"},
		),
	}

	merged := mergeAscendMetrics(first, retry)
	if len(merged) != 4 {
		t.Fatalf("merged samples = %d, want 4 distinct series", len(merged))
	}

	for i, want := range []float64{4, 2, 3, 5} {
		if merged[i].Value != want {
			t.Fatalf(
				"merged sample %d = %v, want %v",
				i,
				merged[i].Value,
				want,
			)
		}
	}
}

func stubAscendDCMI(t testing.TB) {
	t.Helper()
	previousConfig := configSnapshot()
	previousHealth := dcmi.DcGetDeviceHealth
	previousPower := dcmi.DcGetDevicePowerInfo
	previousTemperature := dcmi.DcGetDeviceTemperature
	previousVoltage := dcmi.DcGetDeviceVoltage
	previousUtilization := dcmi.DcGetDeviceUtilizationRate
	previousFrequency := dcmi.DcGetDeviceFrequency
	previousNetworkHealth := dcmi.DcGetDeviceNetWorkHealth
	previousHBM := dcmi.DcGetHbmInfo
	previousECC := dcmi.DcGetDeviceEccInfo
	previousPCIeAddress := dcmi.DcGetPCIeBusInfo
	previousCards := dcmi.DcGetCardList
	previousDeviceCount := dcmi.DcGetDeviceNumInCard
	t.Cleanup(func() {
		Set(previousConfig)
		dcmi.DcGetDeviceHealth = previousHealth
		dcmi.DcGetDevicePowerInfo = previousPower
		dcmi.DcGetDeviceTemperature = previousTemperature
		dcmi.DcGetDeviceVoltage = previousVoltage
		dcmi.DcGetDeviceUtilizationRate = previousUtilization
		dcmi.DcGetDeviceFrequency = previousFrequency
		dcmi.DcGetDeviceNetWorkHealth = previousNetworkHealth
		dcmi.DcGetHbmInfo = previousHBM
		dcmi.DcGetDeviceEccInfo = previousECC
		dcmi.DcGetPCIeBusInfo = previousPCIeAddress
		dcmi.DcGetCardList = previousCards
		dcmi.DcGetDeviceNumInCard = previousDeviceCount
	})

	cfg := &Config{}
	cfg.AscendNPU.EnableDCMI = true
	Set(cfg)
	dcmi.DcGetDeviceHealth = func(context.Context, uint32, uint32) (uint32, error) {
		return 0, nil
	}
	dcmi.DcGetDevicePowerInfo = func(context.Context, uint32, uint32) (float32, error) {
		return 17, nil
	}
	dcmi.DcGetDeviceTemperature = func(context.Context, uint32, uint32) (int32, error) {
		return 0, nil
	}
	dcmi.DcGetDeviceVoltage = func(context.Context, uint32, uint32) (float32, error) {
		return 0, nil
	}
	dcmi.DcGetDeviceUtilizationRate = func(context.Context, uint32, uint32, dcmi.DeviceType) (int32, error) {
		return 0, nil
	}
	dcmi.DcGetDeviceFrequency = func(context.Context, uint32, uint32, dcmi.DeviceType) (uint32, error) {
		return 0, nil
	}
	dcmi.DcGetDeviceNetWorkHealth = func(context.Context, uint32, uint32) (uint32, error) {
		return 0, nil
	}
	dcmi.DcGetHbmInfo = func(context.Context, uint32, uint32) (*dcmi.HbmInfo, error) {
		return &dcmi.HbmInfo{}, nil
	}
	dcmi.DcGetDeviceEccInfo = func(context.Context, uint32, uint32, dcmi.DcmiDeviceType) (*dcmi.ECCInfo, error) {
		return &dcmi.ECCInfo{}, nil
	}
	dcmi.DcGetCardList = func(context.Context) (int32, []int32, error) {
		return 1, []int32{0}, nil
	}
	dcmi.DcGetDeviceNumInCard = func(context.Context, int32) (int32, error) {
		return 1, nil
	}
}

func hasAscendMetric(metrics []*metric.Data, name string, cardID uint32, value float64) bool {
	for _, data := range metrics {
		if data.Name() == name && data.Labels()["card"] == fmt.Sprint(cardID) && data.Value == value {
			return true
		}
	}

	return false
}

func BenchmarkAscendCollection(b *testing.B) {
	stubAscendDCMI(b)
	devices := []deviceKey{
		{
			cardId:   0,
			deviceId: 0,
		},
		{
			cardId:   1,
			deviceId: 0,
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ascendCollectMetrics(b.Context(), devices); err != nil {
			b.Fatal(err)
		}
	}
}
