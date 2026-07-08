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

	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
)

func (c *iolatencyTracing) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	containers, _ := c.fetchContainerIOlatency()

	blkio, err := c.fetchBlkDiskIOlatency()
	if err != nil {
		return containers, err
	}

	return append(containers, blkio...), nil
}

func (c *iolatencyTracing) fetchContainerIOlatency() ([]*metric.Data, error) {
	var metrics []*metric.Data

	containers, err := pod.Containers()
	if err != nil {
		return nil, err
	}

	cssContainers := pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO)

	containersIOdata, err := c.dumpContainerLatency()
	if err != nil {
		return nil, err
	}

	for _, blkcg := range containersIOdata {
		diskDev := fmt.Sprintf("%d:%d", blkcg.Major, blkcg.Minor)

		for zone, cnt := range blkcg.Q2CZone {
			if cnt == 0 {
				continue
			}

			container, ok := cssContainers[blkcg.Blkgq]
			if !ok {
				continue
			}

			metrics = append(metrics, metric.NewContainerGaugeData(
				container, "blkdisk_q2c", float64(cnt),
				"container blkio q2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}

		for zone, cnt := range blkcg.D2CZone {
			if cnt == 0 {
				continue
			}

			container, ok := cssContainers[blkcg.Blkgq]
			if !ok {
				continue
			}

			metrics = append(metrics, metric.NewContainerGaugeData(
				container, "blkdisk_d2c", float64(cnt),
				"container blkio d2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}
	}

	return metrics, nil
}

func (c *iolatencyTracing) fetchBlkDiskIOlatency() ([]*metric.Data, error) {
	var metrics []*metric.Data

	blkIOdata, err := c.dumpBlkdiskLatency()
	if err != nil {
		return nil, err
	}

	for _, disk := range blkIOdata {
		diskDev := fmt.Sprintf("%d:%d", disk.Major, disk.Minor)

		for zone, cnt := range disk.Q2CZone {
			metrics = append(metrics, metric.NewGaugeData(
				"blkdisk_q2c", float64(cnt),
				"the disk q2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}

		for zone, cnt := range disk.D2CZone {
			metrics = append(metrics, metric.NewGaugeData(
				"blkdisk_d2c", float64(cnt),
				"the disk d2c latency",
				map[string]string{"disk": diskDev, "zone": strconv.Itoa(zone)},
			))
		}

		metrics = append(metrics, metric.NewGaugeData(
			"blkdisk_freeze", float64(disk.FreezeNr),
			"the disk freeze event count",
			map[string]string{"disk": diskDev},
		))
	}

	return metrics, nil
}
