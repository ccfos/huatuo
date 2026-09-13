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

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/procfs/sysfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"

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
	// interfaceByIndex is a seam for tests; production code uses
	// net.InterfaceByIndex.
	interfaceByIndex func(int) (*net.Interface, error)
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

	cfg := configSnapshot()
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
			interfaceByIndex:      net.InterfaceByIndex,
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

	return netdev.collectData(), nil
}

// collectData builds one sample per tracked interface. A failed sysfs read
// skips the interface instead of exporting zero: the metric is a counter,
// and a fabricated reset makes Prometheus rate() report a phantom spike.
func (netdev *netdevHw) collectData() []*metric.Data {
	data := []*metric.Data{}
	for iface, drv := range netdev.ifaceList {
		rxDropped, err := netdev.readSysNetclassStat(iface, "rx_dropped")
		if err != nil {
			log.Debugf("skip %s sample: read rx_dropped: %v", iface, err)
			continue
		}
		rxMissed, err := netdev.readSysNetclassStat(iface, "rx_missed_errors")
		if err != nil {
			log.Debugf("skip %s sample: read rx_missed_errors: %v", iface, err)
			continue
		}

		count := rxMissed
		// 1. No packet loss
		// 2. rx_missed_errors of the driver is not used.
		if count == 0 {
			// hardware drop = rx_dropped - software_drops
			if sw, ok := netdev.ifaceSwDroppedCounter[iface]; ok && rxDropped >= sw {
				count = rxDropped - sw
			}
		}

		data = append(data, metric.NewCounterData(
			"rx_dropped_total", float64(count),
			"count of packets dropped at hardware level",
			map[string]string{"device": iface, "driver": drv.Driver},
		))
	}

	return data
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

	return netdev.applySwDroppedItems(items)
}

// applySwDroppedItems consumes one dumped rx_sw_dropped_stats map item per
// call slice entry, keyed by ifindex.
func (netdev *netdevHw) applySwDroppedItems(items []bpf.MapItem) error {
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

		ifi, err := netdev.interfaceByIndex(int(ifidx))
		if err != nil {
			// The interface may have been removed while its key is still
			// in the BPF map; skip it so one stale key cannot abort the
			// whole scrape.
			log.Debugf("[rx_sw_dropped_stats] skip ifindex %d: %v", ifidx, err)
			continue
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
