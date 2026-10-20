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
	"errors"
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
	rdmaList  []*netlink.RdmaLink
	readStats func(*netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error)
}

func newRdmaLink() (*tracing.EventTracingAttr, error) {
	lists, err := netlink.RdmaLinkList()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &rdmaLink{rdmaList: lists, readStats: netlink.RdmaStatistic},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (r *rdmaLink) Update() ([]*metric.Data, error) {
	var data []*metric.Data
	var failures []error

	for _, rdma := range r.rdmaList {
		stats, err := r.readStats(rdma)
		if err != nil {
			failures = append(failures, fmt.Errorf("read RDMA statistics for device %q (index %d): %w", rdma.Attrs.Name, rdma.Attrs.Index, err))
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

	return data, errors.Join(failures...)
}
