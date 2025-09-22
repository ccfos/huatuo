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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"path/filepath"
	"slices"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/internal/utils/sysfsutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/safchain/ethtool"
)

// currently supports mlx5_core, i40e, ixgbe, bnxt_en; will be removed in future
var netDeviceDriver = []string{"mlx5_core", "i40e", "ixgbe", "bnxt_en", "virtio_net"}

type netdevHw struct {
	prog                bpf.BPF
	data                []*metric.Data
	isTracerRunning     bool
	ifaceSwDropCounters map[string]uint64
	ifaceList           map[string]int
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/netdev_hw.c -o $BPF_DIR/netdev_hw.o
func init() {
	tracing.RegisterEventTracing("netdev_hw", newNetdevHw)
}

func newNetdevHw() (*tracing.EventTracingAttr, error) {
	interfaces, err := sysfsutil.DefaultNetClassDevices()
	if err != nil {
		return nil, err
	}

	log.Infof("processing interfaces: %v", interfaces)

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, err
	}

	ifaceRxDropped := []*metric.Data{}
	ifaceIndex := make(map[string]int)

	for _, iface := range interfaces {
		drvInfo, err := eth.DriverInfo(iface)
		if err != nil {
			continue
		}
		// skip processing if the interface is not in the whitelist or the driver is not allowed
		if !slices.Contains(conf.Get().Tracing.Netdev.Whitelist, iface) ||
			!slices.Contains(netDeviceDriver, drvInfo.Driver) {
			log.Debugf("%s is skipped (not in whitelist or driver not allowed)", iface)
			continue
		}

		ifaceIndex[iface] = len(ifaceRxDropped)

		ifaceRxDropped = append(ifaceRxDropped, metric.NewGaugeData(
			"rx_dropped", 0, "count of packets dropped at hardware level",
			map[string]string{
				"device": iface,
				"driver": drvInfo.Driver,
			},
		))

		log.Debugf("support iface %s [%s] rx_dropped, and metric idx %d", iface, drvInfo.Driver, ifaceIndex[iface])
	}

	return &tracing.EventTracingAttr{
		TracingData: &netdevHw{
			data:                ifaceRxDropped,
			ifaceList:           ifaceIndex,
			ifaceSwDropCounters: make(map[string]uint64),
		},
		Internal: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

// Update the drop statistics metrics
func (netdev *netdevHw) Update() ([]*metric.Data, error) {
	if !netdev.isTracerRunning {
		return nil, nil
	}

	if err := netdev.updateIfaceSwDropCounter(); err != nil {
		return nil, err
	}

	for iface := range netdev.ifaceList {
		counters := map[string]uint64{
			"rx_dropped":       0,
			"rx_missed_errors": 0,
		}

		for name := range counters {
			counters[name], _ = readStat(iface, name)
		}

		count := counters["rx_missed_errors"]
		// 1. no hwdrop or 2. rx_missed_errors is not used.
		if count == 0 {
			// hwdrop = rx_dropped - software_drops
			if sw, ok := netdev.ifaceSwDropCounters[iface]; ok {
				count = counters["rx_dropped"] - sw
			}
		}

		netdev.data[netdev.ifaceList[iface]].Value = float64(count)
	}

	return netdev.data, nil
}

func readStat(iface, stat string) (uint64, error) {
	return parseutil.ReadUint(filepath.Join("/sys/class/net", iface, "statistics", stat))
}

func (netdev *netdevHw) updateIfaceSwDropCounter() error {
	for iface := range netdev.ifaceList {
		_, _ = parseutil.ReadUint("/sys/class/net/" + iface + "/carrier_down_count")
	}

	// dump rx_dropped counters
	items, err := netdev.prog.DumpMapByName("rx_sw_dropped_stats")
	if err != nil {
		return err
	}

	for _, v := range items {
		var (
			ifidx   uint32
			counter uint64
		)

		if err := binary.Read(bytes.NewReader(v.Key), binary.LittleEndian, &ifidx); err != nil {
			return fmt.Errorf("read map key: %w", err)
		}
		if err := binary.Read(bytes.NewReader(v.Value), binary.LittleEndian, &counter); err != nil {
			return fmt.Errorf("read map value: %w", err)
		}

		ifi, err := net.InterfaceByIndex(int(ifidx))
		if err != nil {
			return err
		}

		// iface can be dynamically added while huatuo is running.
		if _, ok := netdev.ifaceSwDropCounters[ifi.Name]; ok {
			log.Debugf("[rx_sw_dropped_stats] %s => %d", ifi.Name, counter)
			netdev.ifaceSwDropCounters[ifi.Name] = counter
		}
	}

	return nil
}

func (netdev *netdevHw) Start(ctx context.Context) error {
	prog, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("LoadBpf %s: %w", bpf.ThisBpfOBJ(), err)
	}
	defer prog.Close()

	if err = prog.Attach(); err != nil {
		return fmt.Errorf("Attach %s: %w", bpf.ThisBpfOBJ(), err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog.WaitDetachByBreaker(childCtx, cancel)

	netdev.prog = prog
	netdev.isTracerRunning = true

	<-childCtx.Done()

	netdev.isTracerRunning = false
	return nil
}
