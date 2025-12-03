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

// ref: https://github.com/prometheus/node_exporter/tree/master/collector
//	- netdev_common.go
//	- netdev_linuxt.go

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/jsimonetti/rtnetlink"
	"github.com/mdlayher/netlink"
	"github.com/prometheus/procfs"
)

type (
	netdevStats     map[string]map[string]uint64
	netdevCollector struct{}
)

func init() {
	tracing.RegisterEventTracing("netdev", newNetdevCollector)
}

func newNetdevCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netdevCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *netdevCollector) Update() ([]*metric.Data, error) {
	filter := newFieldFilter(conf.Get().MetricCollector.NetdevStats.DeviceExcluded,
		conf.Get().MetricCollector.NetdevStats.DeviceIncluded)

	log.Debugf("Updating netdev metrics by filter: %v", filter)

	// normal containers
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("GetNormalContainers: %w", err)
	}

	// support the empty container
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}
	// append host into containers
	containers[""] = nil

	var metrics []*metric.Data
	for _, container := range containers {
		devStats, err := c.getStats(container, filter)
		if err != nil {
			return nil, fmt.Errorf("couldn't get netdev statistic for container %v: %w", container, err)
		}

		for dev, stats := range devStats {
			for key, val := range stats {
				tags := map[string]string{"device": dev}
				if container != nil {
					metrics = append(metrics,
						metric.NewContainerCounterData(container, key+"_total", float64(val), fmt.Sprintf("Network device statistic %s.", key), tags))
				} else {
					metrics = append(metrics,
						metric.NewCounterData(key+"_total", float64(val), fmt.Sprintf("Network device statistic %s.", key), tags))
				}
			}
		}
	}

	log.Debugf("Updated netdev metrics by filter %v: %v", filter, metrics)
	return metrics, nil
}

func (c *netdevCollector) getStats(container *pod.Container, filter *fieldFilter) (netdevStats, error) {
	if conf.Get().MetricCollector.NetdevStats.EnableNetlink {
		return c.netlinkStats(container, filter)
	}
	return c.procStats(container, filter)
}

func (c *netdevCollector) netlinkStats(container *pod.Container, filter *fieldFilter) (netdevStats, error) {
	pid := 1 // host
	if container != nil {
		pid = container.InitPid
	}

	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "ns/net"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	conn, err := rtnetlink.Dial(&netlink.Config{NetNS: int(file.Fd())})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	links, err := conn.Link.List()
	if err != nil {
		return nil, err
	}

	metrics := netdevStats{}
	for _, msg := range links {
		if msg.Attributes == nil {
			log.Debug("No netlink attributes, skipping")
			continue
		}
		name := msg.Attributes.Name
		stats := msg.Attributes.Stats64
		if stats32 := msg.Attributes.Stats; stats == nil && stats32 != nil {
			stats = &rtnetlink.LinkStats64{
				RXPackets:          uint64(stats32.RXPackets),
				TXPackets:          uint64(stats32.TXPackets),
				RXBytes:            uint64(stats32.RXBytes),
				TXBytes:            uint64(stats32.TXBytes),
				RXErrors:           uint64(stats32.RXErrors),
				TXErrors:           uint64(stats32.TXErrors),
				RXDropped:          uint64(stats32.RXDropped),
				TXDropped:          uint64(stats32.TXDropped),
				Multicast:          uint64(stats32.Multicast),
				Collisions:         uint64(stats32.Collisions),
				RXLengthErrors:     uint64(stats32.RXLengthErrors),
				RXOverErrors:       uint64(stats32.RXOverErrors),
				RXCRCErrors:        uint64(stats32.RXCRCErrors),
				RXFrameErrors:      uint64(stats32.RXFrameErrors),
				RXFIFOErrors:       uint64(stats32.RXFIFOErrors),
				RXMissedErrors:     uint64(stats32.RXMissedErrors),
				TXAbortedErrors:    uint64(stats32.TXAbortedErrors),
				TXCarrierErrors:    uint64(stats32.TXCarrierErrors),
				TXFIFOErrors:       uint64(stats32.TXFIFOErrors),
				TXHeartbeatErrors:  uint64(stats32.TXHeartbeatErrors),
				TXWindowErrors:     uint64(stats32.TXWindowErrors),
				RXCompressed:       uint64(stats32.RXCompressed),
				TXCompressed:       uint64(stats32.TXCompressed),
				RXNoHandler:        uint64(stats32.RXNoHandler),
				RXOtherhostDropped: 0,
			}
		}

		if filter.ignored(name) {
			log.Debugf("Ignoring device: %s", name)
			continue
		}

		// Make sure we don't panic when accessing `stats` attributes below.
		if stats == nil {
			log.Debug("No netlink stats, skipping")
			continue
		}

		// https://github.com/torvalds/linux/blob/master/include/uapi/linux/if_link.h#L42-L246
		metrics[name] = map[string]uint64{
			"receive_packets":  stats.RXPackets,
			"transmit_packets": stats.TXPackets,
			"receive_bytes":    stats.RXBytes,
			"transmit_bytes":   stats.TXBytes,
			"receive_errors":   stats.RXErrors,
			"transmit_errors":  stats.TXErrors,
			"receive_dropped":  stats.RXDropped,
			"transmit_dropped": stats.TXDropped,
			"multicast":        stats.Multicast,
			"collisions":       stats.Collisions,

			// detailed rx_errors
			"receive_length_errors": stats.RXLengthErrors,
			"receive_over_errors":   stats.RXOverErrors,
			"receive_crc_errors":    stats.RXCRCErrors,
			"receive_frame_errors":  stats.RXFrameErrors,
			"receive_fifo_errors":   stats.RXFIFOErrors,
			"receive_missed_errors": stats.RXMissedErrors,

			// detailed tx_errors
			"transmit_aborted_errors":   stats.TXAbortedErrors,
			"transmit_carrier_errors":   stats.TXCarrierErrors,
			"transmit_fifo_errors":      stats.TXFIFOErrors,
			"transmit_heartbeat_errors": stats.TXHeartbeatErrors,
			"transmit_window_errors":    stats.TXWindowErrors,

			// for cslip etc
			"receive_compressed":  stats.RXCompressed,
			"transmit_compressed": stats.TXCompressed,
			"receive_nohandler":   stats.RXNoHandler,
		}
	}

	return metrics, nil
}

func (c *netdevCollector) procStats(container *pod.Container, filter *fieldFilter) (netdevStats, error) {
	pid := 1 // host
	if container != nil {
		pid = container.InitPid
	}

	fs, err := procfs.NewProc(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to open procfs: %w", err)
	}

	netdev, err := fs.NetDev()
	if err != nil {
		return nil, fmt.Errorf("failed to parse /proc/[%d]/net/dev: %w", pid, err)
	}

	metrics := netdevStats{}
	for name := range netdev {
		stats := netdev[name]

		if filter.ignored(name) {
			log.Debugf("Ignoring device: %s", name)
			continue
		}

		metrics[name] = map[string]uint64{
			"receive_bytes":       stats.RxBytes,
			"receive_packets":     stats.RxPackets,
			"receive_errors":      stats.RxErrors,
			"receive_dropped":     stats.RxDropped,
			"receive_fifo":        stats.RxFIFO,
			"receive_frame":       stats.RxFrame,
			"receive_compressed":  stats.RxCompressed,
			"receive_multicast":   stats.RxMulticast,
			"transmit_bytes":      stats.TxBytes,
			"transmit_packets":    stats.TxPackets,
			"transmit_errors":     stats.TxErrors,
			"transmit_dropped":    stats.TxDropped,
			"transmit_fifo":       stats.TxFIFO,
			"transmit_colls":      stats.TxCollisions,
			"transmit_carrier":    stats.TxCarrier,
			"transmit_compressed": stats.TxCompressed,
		}
	}

	return metrics, nil
}
