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

func TestRDMAStatisticsFailuresPreserveHealthyData(t *testing.T) {
	firstErr := errors.New("netlink permission denied")
	secondErr := errors.New("device statistics unavailable")
	for _, tc := range []struct {
		name     string
		failures map[uint32]error
		devices  int
		wantData int
	}{
		{"all successful", nil, 3, 3},
		{"mixed", map[uint32]error{2: firstErr}, 3, 2},
		{"all failed", map[uint32]error{1: firstErr, 2: secondErr}, 2, 0},
		{"empty", nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links := make([]*netlink.RdmaLink, tc.devices)
			for i := range links {
				links[i] = &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "rdma-device", Index: uint32(i + 1), NumPorts: 1}}
			}
			collector := &rdmaLink{rdmaList: links, readStats: func(link *netlink.RdmaLink) (*netlink.RdmaDeviceStatistic, error) {
				if err := tc.failures[link.Attrs.Index]; err != nil {
					return nil, err
				}
				return &netlink.RdmaDeviceStatistic{RdmaPortStatistics: []*netlink.RdmaPortStatistic{{PortIndex: 1, Statistics: map[string]uint64{"rx_packets": 42}}}}, nil
			}}
			data, err := collector.Update()
			if len(data) != tc.wantData {
				t.Fatalf("metrics = %d, want %d", len(data), tc.wantData)
			}
			for _, sample := range data {
				if sample.Name() != "rx_packets" || sample.Value != 42 {
					t.Fatalf("healthy metric changed: %+v", sample)
				}
			}
			if len(tc.failures) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("statistics failures reported as a successful scrape")
			}
			if !strings.Contains(err.Error(), "rdma-device") {
				t.Fatalf("error lacks device identity: %v", err)
			}
			for _, cause := range tc.failures {
				if !errors.Is(err, cause) {
					t.Fatalf("error %v lost cause %v", err, cause)
				}
			}
		})
	}
}
