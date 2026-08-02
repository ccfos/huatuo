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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"sync"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/procfs/sysfs"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/safchain/ethtool"
)

// currently supports mlx5_core, i40e, ixgbe, bnxt_en; will be removed in future
var deviceDriverList = []string{"mlx5_core", "i40e", "ixgbe", "bnxt_en", "virtio_net"}

type netdevHw struct {
	prog                  bpf.Reference
	ifaceSwDroppedCounter map[string]uint64
	ifaceList             map[string]*ethtool.DrvInfo
	sysNetPath            string
	mutex                 sync.Mutex
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/netdev_hw.c -o $BPF_DIR/netdev_hw.o
func init() {
	tracing.RegisterEventTracing("netdev_hw", newNetdevHw)
}

func newNetdevHw() (*tracing.EventTracingAttr, error) {
	ifaces, err := sysfs.DefaultNetClassDevices()
	if err != nil {
		return nil, err
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, err
	}

	deviceMatcher, err := matcher.NewListMatcher(cfg.NetdevHW.DeviceList)
	if err != nil {
		return nil, fmt.Errorf("netdev hw device list: %w", err)
	}

	ifaceList := make(map[string]*ethtool.DrvInfo)
	ifaceSwCounter := make(map[string]uint64)

	log.Infof("processing interfaces: %v", ifaces)
	for _, iface := range ifaces {
		drv, err := eth.DriverInfo(iface)
		if err != nil {
			continue
		}

		// skip processing if the interface is not in the whitelist or the driver is not allowed
		if !deviceMatcher.Match(iface) ||
			!slices.Contains(deviceDriverList, drv.Driver) {
			log.Debugf("%s is skipped (not in whitelist or driver not allowed)", iface)
			continue
		}

		ifaceList[iface] = &drv
		ifaceSwCounter[iface] = 0
		log.Debugf("support iface %s [%s] hardware rx_dropped", iface, drv.Driver)
	}

	return &tracing.EventTracingAttr{
		TracingData: &netdevHw{
			ifaceList:             ifaceList,
			ifaceSwDroppedCounter: ifaceSwCounter,
			sysNetPath:            sysfs.Path("class/net"),
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

// Update the drop statistics metrics
func (netdev *netdevHw) Update() ([]*metric.Data, error) {
	lease, ok := netdev.prog.Acquire()
	if !ok {
		return nil, nil
	}
	defer lease.Release()

	// avoid data race
	netdev.mutex.Lock()
	defer netdev.mutex.Unlock()

	if err := netdev.updateIfaceSwDroppedStat(lease.BPF); err != nil {
		return nil, err
	}

	data := []*metric.Data{}
	for iface, drv := range netdev.ifaceList {
		counters := map[string]uint64{
			"rx_dropped":       0,
			"rx_missed_errors": 0,
		}

		for name := range counters {
			counters[name], _ = netdev.readSysNetclassStat(iface, name)
		}

		count := counters["rx_missed_errors"]
		// 1. No packet loss
		// 2. rx_missed_errors of the driver is not used.
		if count == 0 {
			// hardware drop = rx_dropped - software_drops
			if sw, ok := netdev.ifaceSwDroppedCounter[iface]; ok {
				if counters["rx_dropped"] >= sw {
					count = counters["rx_dropped"] - sw
				}
			}
		}

		data = append(data, metric.NewCounterData(
			"rx_dropped_total", float64(count),
			"count of packets dropped at hardware level",
			map[string]string{"device": iface, "driver": drv.Driver},
		))
	}

	return data, nil
}

func (netdev *netdevHw) readSysNetclassStat(iface, stat string) (uint64, error) {
	return parseutil.ReadUint(filepath.Join(netdev.sysNetPath, iface, "statistics", stat))
}

// store the software counter netdev.rx_dropped to bpf map.
func (netdev *netdevHw) updateIfaceSwDroppedStat(object bpf.BPF) error {
	for iface := range netdev.ifaceList {
		_, _ = parseutil.ReadUint(filepath.Join(netdev.sysNetPath, iface, "carrier_down_count"))
	}

	// dump rx_dropped counters
	items, err := object.DumpMapByName("rx_sw_dropped_stats")
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
		if _, ok := netdev.ifaceSwDroppedCounter[ifi.Name]; ok {
			log.Debugf("[rx_sw_dropped_stats] %s => %d", ifi.Name, counter)
			netdev.ifaceSwDroppedCounter[ifi.Name] = counter
		}
	}

	return nil
}

func (netdev *netdevHw) Start(ctx context.Context) (retErr error) {
	object, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}

	if err := object.Attach(); err != nil {
		return errors.Join(err, object.Close())
	}
	if err := netdev.prog.Publish(object); err != nil {
		return errors.Join(err, object.Close())
	}
	defer func() {
		retErr = errors.Join(retErr, netdev.prog.UnPublish())
	}()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	object.DetachOnContextDone(childCtx, cancel)

	<-childCtx.Done()
	return nil
}
