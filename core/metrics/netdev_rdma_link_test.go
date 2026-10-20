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
	"errors"
	"testing"

	"github.com/vishvananda/netlink"
)

func rdmaTestLink(name string, index uint32) *netlink.RdmaLink {
	return &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: name, Index: index, NumPorts: 1, NodeGuid: "guid-" + name}}
}

func rdmaTestStats(*netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error) {
	return &netlink.RdmaDeviceStatistic{RdmaPortStatistics: []*netlink.RdmaPortStatistic{
		{PortIndex: 1, Statistics: map[string]uint64{"rx_packets": 42}},
	}}, nil
}

func TestRDMARefreshesLinkDiscovery(t *testing.T) {
	var links []*netlink.RdmaLink
	collector := &rdmaLink{listLinks: func() ([]*netlink.RdmaLink, error) { return links, nil }, readStats: rdmaTestStats}
	for _, current := range [][]*netlink.RdmaLink{
		nil,
		{rdmaTestLink("rdma0", 1)},
		{rdmaTestLink("rdma0", 1), rdmaTestLink("rdma1", 2)},
		{rdmaTestLink("replacement", 7)},
		nil,
	} {
		links = current
		data, err := collector.Update()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != len(current) {
			t.Fatalf("metrics = %d, want %d", len(data), len(current))
		}
		for i, link := range current {
			labels := data[i].Labels()
			if labels["device"] != link.Attrs.Name || labels["nodeguid"] != link.Attrs.NodeGuid {
				t.Fatalf("stale link labels: %v, want attributes %+v", labels, link.Attrs)
			}
			if data[i].Name() != "rx_packets" || data[i].Value != 42 {
				t.Fatalf("metric = %+v", data[i])
			}
		}
	}
}

func TestRDMADiscoveryFailure(t *testing.T) {
	expected := errors.New("netlink discovery failed")
	collector := &rdmaLink{
		listLinks: func() ([]*netlink.RdmaLink, error) { return nil, expected },
		readStats: func(*netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error) {
			t.Fatal("statistics read after failed discovery")
			return nil, nil
		},
	}
	data, err := collector.Update()
	if !errors.Is(err, expected) || len(data) != 0 {
		t.Fatalf("Update = (%v,%v), want discovery failure", data, err)
	}
}
