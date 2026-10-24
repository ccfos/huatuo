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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/safchain/ethtool"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/qdisc"
	"github.com/ccfos/huatuo/pkg/metric"
)

func TestQdiscCollectorUpdate(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = `^eth[01]$`
	testConfig.Qdisc.DeviceExcluded = `^eth1$`
	Set(testConfig)

	attr, err := newQdiscCollector()
	if err != nil {
		t.Fatalf("create qdisc collector: %v", err)
	}
	collector, ok := attr.TracingData.(*qdiscCollector)
	if !ok {
		t.Fatalf("tracing data type = %T, want *qdiscCollector", attr.TracingData)
	}
	collector.readStats = func() ([]qdisc.Stats, error) {
		return []qdisc.Stats{
			{
				Netdev:       "eth0",
				Parent:       math.MaxUint32,
				Kind:         "fq_codel",
				Bytes:        1,
				Packets:      2,
				Drops:        3,
				Requeues:     4,
				Overlimits:   5,
				QueueLength:  6,
				BacklogBytes: 7,
			},
			{Netdev: "eth1", Parent: math.MaxUint32, Kind: "fq"},
			{Netdev: "eth0", Parent: 1, Kind: "fq"},
			{Netdev: "eth0", Parent: math.MaxUint32, Kind: "noqueue"},
			{Netdev: "lo", Parent: math.MaxUint32, Kind: "fq"},
		}, nil
	}

	metrics, err := collector.Update()
	if err != nil {
		t.Fatalf("update qdisc metrics: %v", err)
	}
	want := []qdiscMetric{
		{Name: "bytes_total", Value: 1, Device: "eth0", Kind: "fq_codel"},
		{Name: "packets_total", Value: 2, Device: "eth0", Kind: "fq_codel"},
		{Name: "drops_total", Value: 3, Device: "eth0", Kind: "fq_codel"},
		{Name: "requeues_total", Value: 4, Device: "eth0", Kind: "fq_codel"},
		{Name: "overlimits_total", Value: 5, Device: "eth0", Kind: "fq_codel"},
		{Name: "current_queue_length", Value: 6, Device: "eth0", Kind: "fq_codel"},
		{Name: "backlog", Value: 7, Device: "eth0", Kind: "fq_codel"},
	}
	if diff := cmp.Diff(want, summarizeQdiscMetrics(metrics)); diff != "" {
		t.Fatalf("qdisc metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestQdiscCollectorUpdateReadError(t *testing.T) {
	wantErr := errors.New("netlink unavailable")
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return nil, wantErr },
	}

	_, err := collector.Update()
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapping %v", err, wantErr)
	}
}

func TestNewQdiscCollectorRejectsInvalidDeviceFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = "["
	Set(testConfig)

	_, err := newQdiscCollector()
	if err == nil || !strings.Contains(err.Error(), "qdisc device filter") {
		t.Fatalf("error = %v, want invalid qdisc device filter", err)
	}
}

func BenchmarkQdiscCollectorUpdate(b *testing.B) {
	stats := make([]qdisc.Stats, 128)
	for i := range stats {
		stats[i] = qdisc.Stats{
			Netdev:       "eth0",
			Parent:       math.MaxUint32,
			Kind:         "fq_codel",
			Bytes:        1_024,
			Packets:      128,
			Drops:        1,
			QueueLength:  2,
			BacklogBytes: 4_096,
		}
	}
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return stats, nil },
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := collector.Update(); err != nil {
			b.Fatalf("update qdisc metrics: %v", err)
		}
	}
}

type qdiscMetric struct {
	Name   string
	Value  float64
	Device string
	Kind   string
}

func summarizeQdiscMetrics(metrics []*metric.Data) []qdiscMetric {
	summary := make([]qdiscMetric, 0, len(metrics))
	for _, data := range metrics {
		labels := data.Labels()
		summary = append(summary, qdiscMetric{
			Name:   data.Name(),
			Value:  data.Value,
			Device: labels["device"],
			Kind:   labels["kind"],
		})
	}
	return summary
}

// Map-boundary fakes let the hardware-drop cases run without physical NICs.
func TestDiscoverNetdevHwDevices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		driver    string
		pattern   string
		driverErr error
		want      bool
	}{
		{
			name:    "mlx5",
			driver:  "mlx5_core",
			pattern: "eth.*",
			want:    true,
		},
		{
			name:    "i40e",
			driver:  "i40e",
			pattern: "eth.*",
			want:    true,
		},
		{
			name:    "ixgbe",
			driver:  "ixgbe",
			pattern: "eth.*",
			want:    true,
		},
		{
			name:    "bnxt",
			driver:  "bnxt_en",
			pattern: "eth.*",
			want:    true,
		},
		{
			name:    "virtio",
			driver:  "virtio_net",
			pattern: "eth.*",
			want:    true,
		},
		{
			name:    "unsupported driver",
			driver:  "veth",
			pattern: "eth.*",
		},
		{
			name:    "excluded device",
			driver:  "mlx5_core",
			pattern: "ens.*",
		},
		{
			name:   "empty whitelist",
			driver: "mlx5_core",
		},
		{
			name:      "driver query failure",
			pattern:   "eth.*",
			driverErr: errors.New("ioctl unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			devices, err := matcher.NewListMatcher([]string{tt.pattern})
			if err != nil {
				t.Fatal(err)
			}

			got := discoverNetdevHwDevices([]string{"eth0"}, devices, func(string) (ethtool.DrvInfo, error) {
				return ethtool.DrvInfo{Driver: tt.driver}, tt.driverErr
			})

			driver, found := got["eth0"]
			if found != tt.want {
				t.Fatalf("selected devices = %v, want eth0 selected=%t", got, tt.want)
			}

			if found && driver.Driver != tt.driver {
				t.Errorf("selected driver = %q, want %q", driver.Driver, tt.driver)
			}
		})
	}
}

type netdevHwBPFStub struct {
	bpf.BPF
	dumpErr error
	items   []bpf.MapItem
}

func (s *netdevHwBPFStub) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name != "rx_sw_dropped_stats" {
		return nil, fmt.Errorf("unexpected map %q", name)
	}

	return s.items, s.dumpErr
}

func (s *netdevHwBPFStub) Close() error { return nil }

func TestNetdevHwUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dropped  uint64
		missed   uint64
		software uint64
		want     float64
	}{
		{
			name:     "missed takes precedence",
			dropped:  100,
			missed:   7,
			software: 30,
			want:     7,
		},
		{
			name:     "subtract software drops",
			dropped:  100,
			software: 30,
			want:     70,
		},
		{
			name:     "only software drops",
			dropped:  30,
			software: 30,
		},
		{
			name:     "software exceeds total",
			dropped:  20,
			software: 30,
		},
		{name: "no drops"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			stats := filepath.Join(root, "eth0", "statistics")
			if err := os.MkdirAll(stats, 0o755); err != nil {
				t.Fatal(err)
			}

			for name, value := range map[string]uint64{"rx_dropped": tt.dropped, "rx_missed_errors": tt.missed} {
				if err := os.WriteFile(filepath.Join(stats, name), fmt.Appendf(nil, "%d\n", value), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			collector := &netdevHw{
				ifaceList:             map[string]*ethtool.DrvInfo{"eth0": {Driver: "mlx5_core"}},
				ifaceSwDroppedCounter: map[string]uint64{"eth0": tt.software},
				sysNetPath:            root,
			}
			if err := collector.prog.Publish(&netdevHwBPFStub{}); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if err := collector.prog.UnPublish(); err != nil {
					t.Error(err)
				}
			})

			metrics, err := collector.Update()
			if err != nil {
				t.Fatal(err)
			}

			if len(metrics) != 1 {
				t.Fatalf("Update returned %d metrics, want 1", len(metrics))
			}

			got := metrics[0]
			if got.Name() != "rx_dropped_total" || got.Value != tt.want {
				t.Errorf(
					"metric = %s %v, want rx_dropped_total %v",
					got.Name(),
					got.Value,
					tt.want,
				)
			}

			labels := got.Labels()
			if labels["device"] != "eth0" || labels["driver"] != "mlx5_core" {
				t.Errorf("labels = %v, want device=eth0 driver=mlx5_core", labels)
			}
		})
	}
}

func TestNetdevHwUpdateMapFailure(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("software-drop map unavailable")
	collector := &netdevHw{}
	if err := collector.prog.Publish(&netdevHwBPFStub{dumpErr: expectedErr}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := collector.prog.UnPublish(); err != nil {
			t.Error(err)
		}
	})

	metrics, err := collector.Update()
	if !errors.Is(err, expectedErr) || len(metrics) != 0 {
		t.Fatalf(
			"Update() = (%v, %v), want no metrics and %v",
			metrics,
			err,
			expectedErr,
		)
	}
}

func TestNetdevHwBPFDropSnapshots(t *testing.T) {
	t.Parallel()

	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	stats := filepath.Join(root, iface.Name, "statistics")
	if err := os.MkdirAll(stats, 0o755); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{"rx_dropped": "100\n", "rx_missed_errors": "0\n"} {
		if err := os.WriteFile(filepath.Join(stats, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	key, value := make([]byte, 4), make([]byte, 8)
	binary.LittleEndian.PutUint32(key, uint32(iface.Index))
	object := &netdevHwBPFStub{
		items: []bpf.MapItem{
			{
				Key:   key,
				Value: value,
			},
		},
	}
	collector := &netdevHw{
		ifaceList:             map[string]*ethtool.DrvInfo{iface.Name: {Driver: "mlx5_core"}},
		ifaceSwDroppedCounter: map[string]uint64{iface.Name: 999},
		sysNetPath:            root,
	}
	if err := collector.prog.Publish(object); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := collector.prog.UnPublish(); err != nil {
			t.Error(err)
		}
	})

	for _, snapshot := range []struct {
		name     string
		software uint64
		want     float64
	}{
		{
			name:     "first snapshot replaces stale cache",
			software: 30,
			want:     70,
		},
		{
			name:     "next snapshot updates the metric",
			software: 45,
			want:     55,
		},
	} {
		t.Run(snapshot.name, func(t *testing.T) {
			binary.LittleEndian.PutUint64(value, snapshot.software)

			metrics, err := collector.Update()
			if err != nil {
				t.Fatal(err)
			}

			if len(metrics) != 1 {
				t.Fatalf("Update returned %d metrics, want 1", len(metrics))
			}

			if metrics[0].Value != snapshot.want {
				t.Errorf(
					"BPF software drops=%d: metric=%v, want %v",
					snapshot.software,
					metrics[0].Value,
					snapshot.want,
				)
			}
		})
	}
}
