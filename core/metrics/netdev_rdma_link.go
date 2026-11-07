// Copyright 2025, 2026 The HuaTuo Authors
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

	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/vishvananda/netlink"
)

func init() {
	tracing.RegisterEventTracing("netdev_rdma_link", newRdmaLink)
}

type rdmaLink struct {
	rdmaList []*netlink.RdmaLink
}

func newRdmaLink() (*tracing.EventTracingAttr, error) {
	lists, err := netlink.RdmaLinkList()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &rdmaLink{rdmaList: lists},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (r *rdmaLink) Update() ([]*metric.Data, error) {
	var data []*metric.Data

	for _, rdma := range r.rdmaList {
		stats, err := netlink.RdmaStatistic(rdma)
		if err != nil {
			continue
		}
		data = append(data, rdmaPortMetrics(rdma, stats)...)
	}

	return data, nil
}

// rdmaPortMetrics converts one device's per-port counters into metric series.
// RdmaStatistic returns one entry per port, so the port index is part of the
// series identity: without it, two ports on the same device emit the same
// metric name and labels, and one port's values overwrite the other.
func rdmaPortMetrics(rdma *netlink.RdmaLink, stats *netlink.RdmaDeviceStatistic) []*metric.Data {
	var data []*metric.Data

	for _, port := range stats.RdmaPortStatistics {
		tags := map[string]string{
			"device":    rdma.Attrs.Name,
			"nodeguid":  rdma.Attrs.NodeGuid,
			"index":     strconv.FormatUint(uint64(rdma.Attrs.Index), 10),
			"num_ports": strconv.FormatUint(uint64(rdma.Attrs.NumPorts), 10),
			"port":      strconv.FormatUint(uint64(port.PortIndex), 10),
		}

		for name, value := range port.Statistics {
			data = append(data, metric.NewCounterData(name, float64(value),
				fmt.Sprintf("rdma device statistic %s.", name), tags))
		}
	}

	return data
}
