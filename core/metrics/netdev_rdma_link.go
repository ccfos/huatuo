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

	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/vishvananda/netlink"
)

func init() {
	tracing.RegisterEventTracing("netdev_rdma_link", newRdmaLink)
}

type rdmaLink struct {
	listLinks  func() ([]*netlink.RdmaLink, error)
	statistics func(*netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error)
}

func newRdmaLink() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &rdmaLink{
			listLinks:  netlink.RdmaLinkList,
			statistics: netlink.RdmaStatistic,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (r *rdmaLink) Update() ([]*metric.Data, error) {
	rdmaList, err := r.listLinks()
	if err != nil {
		return nil, fmt.Errorf("list RDMA links: %w", err)
	}

	var data []*metric.Data

	for _, rdma := range rdmaList {
		stats, err := r.statistics(rdma)
		if err != nil {
			continue
		}

		tags := map[string]string{
			"device":    rdma.Attrs.Name,
			"nodeguid":  rdma.Attrs.NodeGuid,
			"index":     strconv.FormatUint(uint64(rdma.Attrs.Index), 10),
			"num_ports": strconv.FormatUint(uint64(rdma.Attrs.NumPorts), 10),
		}

		for _, s := range stats.RdmaPortStatistics {
			for lable, val := range s.Statistics {
				data = append(data, metric.NewCounterData(lable, float64(val),
					fmt.Sprintf("rdma device statistic %s.", lable), tags))
			}
		}
	}

	return data, nil
}
