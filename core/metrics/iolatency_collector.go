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
	"fmt"
	"strconv"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
)

// DiskEntry stores disk latency histogram buckets and freeze counts.
type DiskEntry struct {
	Disk     uint64
	Major    uint32
	Minor    uint32
	FreezeNr uint64
	Q2CZone  [6]uint64
	D2CZone  [6]uint64
}

// BlkgqEntry stores latency histogram buckets for a block cgroup queue.
type BlkgqEntry struct {
	Blkgq   uint64
	Cgroup  uint64
	Disk    uint64
	Q2CZone [6]uint64
	D2CZone [6]uint64
}

func (c *iolatencyTracing) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	diskMetrics, err := c.getDiskIOLatencyMetrics()
	if err != nil {
		return nil, err
	}

	containerMetrics, err := c.getContainerIOLatencyMetrics()
	if err != nil {
		return nil, err
	}

	return append(diskMetrics, containerMetrics...), nil
}

func (c *iolatencyTracing) getContainerIOLatencyMetrics() ([]*metric.Data, error) {
	c.dataLock.RLock()

	currentContainerLatencyData := make([]BlkgqEntry, len(c.containerLatencyData))
	copy(currentContainerLatencyData, c.containerLatencyData)

	currentBlkgqContainerMap := make(map[uint64]*pod.Container, len(c.blkcgContainerMap))
	for blkgq, container := range c.blkcgContainerMap {
		currentBlkgqContainerMap[blkgq] = container
	}

	c.dataLock.RUnlock()

	var containerMetrics []*metric.Data

	for i := range currentContainerLatencyData {
		blkcg := &currentContainerLatencyData[i]
		container, ok := currentBlkgqContainerMap[blkcg.Blkgq]
		if !ok {
			continue
		}

		for zone, cnt := range blkcg.Q2CZone {
			if cnt == 0 {
				continue
			}

			containerMetrics = append(containerMetrics, metric.NewContainerGaugeData(
				container,
				"q2c",
				float64(cnt),
				"container q2c latency",
				map[string]string{"zone": strconv.Itoa(zone)},
			))
		}

		for zone, cnt := range blkcg.D2CZone {
			if cnt == 0 {
				continue
			}

			containerMetrics = append(containerMetrics, metric.NewContainerGaugeData(
				container,
				"d2c",
				float64(cnt),
				"container d2c latency",
				map[string]string{"zone": strconv.Itoa(zone)},
			))
		}
	}

	return containerMetrics, nil
}

func (c *iolatencyTracing) getDiskIOLatencyMetrics() ([]*metric.Data, error) {
	c.dataLock.RLock()
	currentDiskLatencyData := make([]DiskEntry, len(c.diskLatencyData))
	copy(currentDiskLatencyData, c.diskLatencyData)
	c.dataLock.RUnlock()

	var diskMetrics []*metric.Data

	for i := range currentDiskLatencyData {
		diskInfo := &currentDiskLatencyData[i]
		diskDev := fmt.Sprintf("%d:%d", diskInfo.Major, diskInfo.Minor)

		for zone, cnt := range diskInfo.Q2CZone {
			if cnt == 0 {
				continue
			}

			diskMetrics = append(diskMetrics, metric.NewGaugeData(
				"disk_q2c",
				float64(cnt),
				"disk q2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}

		for zone, cnt := range diskInfo.D2CZone {
			if cnt == 0 {
				continue
			}

			diskMetrics = append(diskMetrics, metric.NewGaugeData(
				"disk_d2c",
				float64(cnt),
				"disk d2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}

		if diskInfo.FreezeNr > 0 {
			diskMetrics = append(diskMetrics, metric.NewGaugeData(
				"disk_freeze",
				float64(diskInfo.FreezeNr),
				"disk freeze count",
				map[string]string{"disk": diskDev},
			))
		}
	}

	return diskMetrics, nil
}
