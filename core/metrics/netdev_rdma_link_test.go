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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// TestRdmaPortMetricsKeepPortIdentity is the regression for multi-port RDMA
// devices: RdmaStatistic returns one counter set per port, so dropping the
// port index collapses every port onto the same series and loses values.
func TestRdmaPortMetricsKeepPortIdentity(t *testing.T) {
	t.Parallel()

	link := &netlink.RdmaLink{
		Attrs: netlink.RdmaLinkAttrs{
			Index:    7,
			Name:     "mlx5_0",
			NodeGuid: "0a:0b:0c:0d:0e:0f:10:11",
			NumPorts: 2,
		},
	}
	stats := &netlink.RdmaDeviceStatistic{
		RdmaPortStatistics: []*netlink.RdmaPortStatistic{
			{PortIndex: 1, Statistics: map[string]uint64{"rx_packets": 10}},
			{PortIndex: 2, Statistics: map[string]uint64{"rx_packets": 20}},
		},
	}

	metrics := rdmaPortMetrics(link, stats)
	require.Len(t, metrics, 2)

	byPort := make(map[string]map[string]string, len(metrics))
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		labels := data.Labels()
		port := labels["port"]
		require.NotContains(t, byPort, port, "port %q must identify a single series", port)
		byPort[port] = labels
		values[port] = data.Value

		assert.Equal(t, "mlx5_0", labels["device"])
		assert.Equal(t, "0a:0b:0c:0d:0e:0f:10:11", labels["nodeguid"])
		assert.Equal(t, "7", labels["index"])
		assert.Equal(t, "2", labels["num_ports"])
	}

	require.Contains(t, byPort, "1")
	require.Contains(t, byPort, "2")
	assert.Equal(t, float64(10), values["1"])
	assert.Equal(t, float64(20), values["2"])
}

func TestRdmaPortMetricsWithoutPorts(t *testing.T) {
	t.Parallel()

	link := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "mlx5_0"}}
	assert.Empty(t, rdmaPortMetrics(link, &netlink.RdmaDeviceStatistic{}))
}
