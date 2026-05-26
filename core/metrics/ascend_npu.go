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

	"huatuo-bamai/core/metrics/ascend/dcmi"
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
			log.Errorf("re-initing dcmi and retrying because dcmi error: %v", err)

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
	var metrics []*metric.Data

	_, cardList, err := dcmi.DcGetCardList()
	if err != nil {
		return nil, fmt.Errorf("failed to get card list: %w", err)
	}

	for _, cardId := range cardList {
		deviceNum, err := dcmi.DcGetDeviceNumInCard(cardId)
		if err != nil {
			return nil, fmt.Errorf("failed to get device count for card %d: %w", cardId, err)
		}

		for deviceId := int32(0); deviceId < deviceNum; deviceId++ {
			npuMetrics, err := ascendCollectNpuMetrics(ctx, uint32(cardId), uint32(deviceId))
			if err != nil {
				return nil, fmt.Errorf("failed to collect npu metrics for card %d device %d: %w",
					cardId, deviceId, err)
			}
			metrics = append(metrics, npuMetrics...)
		}
	}

	return metrics, nil
}

func ascendCollectNpuMetrics(ctx context.Context, cardId, deviceId uint32) ([]*metric.Data, error) {
	var metrics []*metric.Data

	// Device health
	health, err := dcmi.DcGetDeviceHealth(ctx, cardId, deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get device health: %w", err)
	}
	metrics = append(metrics,
		metric.NewGaugeData("health_status", float64(health),
			"NPU health status, 0 means healthy, other values indicate abnormal.",
			map[string]string{
				"card":   strconv.Itoa(int(cardId)),
				"device": strconv.Itoa(int(deviceId)),
			}),
	)

	return metrics, nil
}
