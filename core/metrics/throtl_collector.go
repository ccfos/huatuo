// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file decodes throttle wait counters and builds scope metrics.
// Count and wait share one aligned 64-bit word per CPU, read once to keep
// each IO's count paired with its wait time. The 26/38-bit split uses 10us
// wait units to keep snapshots compact: each wait loses less than 10us,
// and intervals exceeding either field's capacity may be undercounted.
// Raw CSS deltas are preserved until attribution through the shared pod CSS
// mapping is complete. Kernel CSS serials identify cumulative counters;
// container labels follow the current CSS lookup and its update latency.

package collector

import (
	"encoding/binary"
	"fmt"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
)

const (
	throtlWaitValueSize    = 8
	throtlDelayedCountBits = 26
	throtlWait10USBits     = 38
	throtlDelayedCountMask = uint64(1<<throtlDelayedCountBits) - 1
	throtlWait10USMask     = uint64(1<<throtlWait10USBits) - 1
	throtlWait10USToMS     = 0.01
	throtlDelayedCountName = "delayed_io_count"
	throtlAverageWaitName  = "average_wait_milliseconds"
	throtlHostScope        = "host"
	throtlDelayedCountHelp = "Number of blk-throttle delayed IO episodes " +
		"released between consecutive successful blk_throtl collections."
	throtlAverageWaitHelp = "Average blk-throttle queue wait in milliseconds " +
		"for delayed IO episodes released between consecutive successful " +
		"blk_throtl collections."
)

type throtlWaitKey struct {
	TD        uint64
	BLKG      uint64
	CSS       uint64
	CSSSerial uint64
	Operation uint32
	_         uint32 // C tail padding; part of the map key ABI.
}

type throtlWaitCounters struct {
	DelayedCount uint64
	Wait10US     uint64
}

type throtlWaitInterval struct {
	device    string
	operation string
	counters  throtlWaitCounters
}

type throtlHostKey struct {
	device    string
	operation string
}

func decodeThrotlWaitCounters(
	data []byte,
	possibleCPUs int,
) ([]throtlWaitCounters, error) {
	if possibleCPUs <= 0 {
		return nil, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/throtlWaitValueSize {
		return nil, fmt.Errorf("possible CPU count is too large: %d",
			possibleCPUs)
	}
	expectedSize := possibleCPUs * throtlWaitValueSize
	if len(data) != expectedSize {
		return nil, fmt.Errorf("data size %d, want %d for %d possible CPUs",
			len(data), expectedSize, possibleCPUs)
	}

	counters := make([]throtlWaitCounters, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * throtlWaitValueSize
		packed := binary.LittleEndian.Uint64(
			data[offset : offset+throtlWaitValueSize])
		counters[cpu] = decodeThrotlWaitCounter(packed)
	}
	return counters, nil
}

func decodeThrotlWaitCounter(packed uint64) throtlWaitCounters {
	return throtlWaitCounters{
		DelayedCount: (packed >> throtlWait10USBits) &
			throtlDelayedCountMask,
		Wait10US: packed & throtlWait10USMask,
	}
}

func addThrotlWaitCounters(
	left throtlWaitCounters,
	right throtlWaitCounters,
) throtlWaitCounters {
	return throtlWaitCounters{
		DelayedCount: left.DelayedCount + right.DelayedCount,
		Wait10US:     left.Wait10US + right.Wait10US,
	}
}

func appendThrotlHostMetrics(
	metrics []*metric.Data,
	host map[throtlHostKey]throtlWaitCounters,
) []*metric.Data {
	return appendThrotlScopeMetrics(metrics, host, throtlHostScope)
}

func appendThrotlScopeMetrics(
	metrics []*metric.Data,
	intervals map[throtlHostKey]throtlWaitCounters,
	scope string,
) []*metric.Data {
	for key, counters := range intervals {
		averageMS := float64(0)
		if counters.DelayedCount != 0 {
			averageMS = float64(counters.Wait10US) *
				throtlWait10USToMS /
				float64(counters.DelayedCount)
		}
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
			"scope":     scope,
		}
		metrics = append(metrics,
			metric.NewGaugeData(
				throtlDelayedCountName,
				float64(counters.DelayedCount),
				throtlDelayedCountHelp,
				labels,
			),
			metric.NewGaugeData(
				throtlAverageWaitName,
				averageMS,
				throtlAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}

const throtlOtherScope = "other"

type throtlContainerKey struct {
	labels    ioControlContainerLabels
	device    string
	operation string
}

type throtlContainerInterval struct {
	container *pod.Container
	counters  throtlWaitCounters
}

type throtlAttributedIntervals struct {
	host       map[throtlHostKey]throtlWaitCounters
	other      map[throtlHostKey]throtlWaitCounters
	containers map[throtlContainerKey]throtlContainerInterval
}

func aggregateThrotlAttributedIntervals(
	raw map[throtlWaitKey]throtlWaitInterval,
	containers map[uint64]*pod.Container,
) *throtlAttributedIntervals {
	result := &throtlAttributedIntervals{
		host:       make(map[throtlHostKey]throtlWaitCounters),
		other:      make(map[throtlHostKey]throtlWaitCounters),
		containers: make(map[throtlContainerKey]throtlContainerInterval),
	}
	for key, interval := range raw {
		hostKey := throtlHostKey{
			device:    interval.device,
			operation: interval.operation,
		}
		hostCounters := addThrotlWaitCounters(
			result.host[hostKey],
			interval.counters,
		)
		result.host[hostKey] = hostCounters

		container, labels := ioControlContainerAttribution(containers, key.CSS)
		if container == nil {
			combined := addThrotlWaitCounters(
				result.other[hostKey],
				interval.counters,
			)
			result.other[hostKey] = combined
		} else {
			containerKey := throtlContainerKey{
				labels:    labels,
				device:    interval.device,
				operation: interval.operation,
			}
			combined := addThrotlWaitCounters(
				result.containers[containerKey].counters,
				interval.counters,
			)
			result.containers[containerKey] = throtlContainerInterval{
				container: container,
				counters:  combined,
			}
		}
	}
	return result
}

func appendThrotlAttributedMetrics(
	metrics []*metric.Data,
	intervals *throtlAttributedIntervals,
) []*metric.Data {
	metrics = appendThrotlHostMetrics(metrics, intervals.host)
	metrics = appendThrotlScopeMetrics(
		metrics,
		intervals.other,
		throtlOtherScope,
	)

	for key, interval := range intervals.containers {
		averageMS := float64(0)
		if interval.counters.DelayedCount != 0 {
			averageMS = float64(interval.counters.Wait10US) *
				throtlWait10USToMS /
				float64(interval.counters.DelayedCount)
		}
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
		}
		metrics = append(metrics,
			metric.NewContainerGaugeData(
				interval.container,
				throtlDelayedCountName,
				float64(interval.counters.DelayedCount),
				throtlDelayedCountHelp,
				labels,
			),
			metric.NewContainerGaugeData(
				interval.container,
				throtlAverageWaitName,
				averageMS,
				throtlAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}
