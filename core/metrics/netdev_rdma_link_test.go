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
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestRdmaLinkUpdateRefreshesDeviceList(t *testing.T) {
	updates := [][]*netlink.RdmaLink{
		{{Attrs: netlink.RdmaLinkAttrs{Index: 1, Name: "rdma0", NumPorts: 1}}},
		{{Attrs: netlink.RdmaLinkAttrs{Index: 2, Name: "rdma1", NumPorts: 1}}},
	}
	call := 0
	collector := &rdmaLink{
		listLinks: func() ([]*netlink.RdmaLink, error) {
			links := updates[call]
			call++
			return links, nil
		},
		statistics: func(link *netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error) {
			return &netlink.RdmaDeviceStatistic{
				RdmaPortStatistics: []*netlink.RdmaPortStatistic{
					{PortIndex: 1, Statistics: map[string]uint64{"packets": uint64(link.Attrs.Index)}},
				},
			}, nil
		},
	}

	for i, wantDevice := range []string{"rdma0", "rdma1"} {
		metrics, err := collector.Update()
		if err != nil {
			t.Fatalf("Update() call %d error = %v", i+1, err)
		}
		if len(metrics) != 1 {
			t.Fatalf("Update() call %d returned %d metrics, want 1", i+1, len(metrics))
		}
		if got := metrics[0].Labels()["device"]; got != wantDevice {
			t.Errorf("Update() call %d device = %q, want %q", i+1, got, wantDevice)
		}
	}
}

func TestRdmaLinkUpdateReturnsDiscoveryError(t *testing.T) {
	collector := &rdmaLink{
		listLinks: func() ([]*netlink.RdmaLink, error) {
			return nil, errors.New("netlink unavailable")
		},
	}

	_, err := collector.Update()
	if err == nil || !strings.Contains(err.Error(), "list RDMA links") {
		t.Fatalf("Update() error = %v, want RDMA discovery context", err)
	}
}
