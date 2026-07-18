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
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/procfs/sysfs"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	"github.com/safchain/ethtool"
)

// currently supports mlx5_core, i40e, ixgbe, bnxt_en; will be removed in future
var deviceDriverList = []string{"mlx5_core", "i40e", "ixgbe", "bnxt_en", "virtio_net"}

type netdevHw struct {
	prog                  bpf.BPF
	running               atomic.Bool
	ifaceSwDroppedCounter map[string]uint64
	// ifaceHwDroppedCounter holds the last emitted cumulative hardware drop
	// count per interface, used to derive the per-interval delta emitted as a
	// dropwatch tracing event (see emitHwDropDelta).
	ifaceHwDroppedCounter map[string]uint64
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
	ifaceHwCounter := make(map[string]uint64)

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
		// ifaceHwCounter is intentionally NOT pre-populated: the first
		// emitHwDropDelta sample baselines the cumulative counter so the
		// boot-time accumulation is not mistaken for fresh drops.
		log.Debugf("support iface %s [%s] hardware rx_dropped", iface, drv.Driver)
	}

	return &tracing.EventTracingAttr{
		TracingData: &netdevHw{
			ifaceList:             ifaceList,
			ifaceSwDroppedCounter: ifaceSwCounter,
			ifaceHwDroppedCounter: ifaceHwCounter,
			sysNetPath:            sysfs.Path("class/net"),
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

// Update the drop statistics metrics
func (netdev *netdevHw) Update() ([]*metric.Data, error) {
	if !netdev.running.Load() {
		return nil, nil
	}

	// avoid data race
	netdev.mutex.Lock()
	defer netdev.mutex.Unlock()

	if err := netdev.updateIfaceSwDroppedStat(); err != nil {
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
				count = counters["rx_dropped"] - sw
			}
		}

		data = append(data, metric.NewCounterData(
			"rx_dropped_total", float64(count),
			"count of packets dropped at hardware level",
			map[string]string{"device": iface, "driver": drv.Driver},
		))

		// Correlate hardware drops with kernel dropwatch events: emit the
		// per-interval delta as a dropwatch tracing event tagged
		// DropLayerHardware so both sources share one schema/stream and can be
		// sliced by drop_layer on one timeline. Must be called under mutex.
		netdev.emitHwDropDelta(iface, count)
	}

	return data, nil
}

// emitHwDropDelta tracks the cumulative hardware-drop counter for iface and,
// when it grows, emits a dropwatch tracing event carrying the delta. The first
// observed sample baselines the counter (no event) so the boot-time cumulative
// count is not mistaken for fresh drops. A decrease (driver reset / counter
// wraparound) is treated as a fresh baseline via hwDropDelta.
func (netdev *netdevHw) emitHwDropDelta(iface string, cur uint64) {
	prev, exists := netdev.ifaceHwDroppedCounter[iface]
	netdev.ifaceHwDroppedCounter[iface] = cur
	if !exists {
		return // first sample: baseline, nothing to report
	}

	delta := hwDropDelta(prev, cur)
	if delta == 0 {
		return
	}

	ev := &types.DropWatchTracing{
		ObservedTimestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Source:            types.DropSourceTypesMetric,
		Type:              "hw_drop",
		NetdevName:        iface,
		DropLayer:         types.DropLayerHardware,
		DropCount:         delta,
	}
	if err := tracing.Save(&tracing.WriteRequest{
		TracerName: "dropwatch",
		TracerTime: time.Now(),
		TracerData: ev,
	}); err != nil {
		log.Warnf("netdev_hw: save hardware drop event for %s: %v", iface, err)
	}
}

// hwDropDelta returns the positive difference between two cumulative hardware
// drop counters. A decrease (counter reset or wraparound) reports the current
// value as-is so a reset never surfaces as a huge spurious delta.
func hwDropDelta(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

func (netdev *netdevHw) readSysNetclassStat(iface, stat string) (uint64, error) {
	return parseutil.ReadUint(filepath.Join(netdev.sysNetPath, iface, "statistics", stat))
}

// store the software counter netdev.rx_dropped to bpf map.
func (netdev *netdevHw) updateIfaceSwDroppedStat() error {
	for iface := range netdev.ifaceList {
		_, _ = parseutil.ReadUint(filepath.Join(netdev.sysNetPath, iface, "carrier_down_count"))
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
		if _, ok := netdev.ifaceSwDroppedCounter[ifi.Name]; ok {
			log.Debugf("[rx_sw_dropped_stats] %s => %d", ifi.Name, counter)
			netdev.ifaceSwDroppedCounter[ifi.Name] = counter
		}
	}

	return nil
}

func (netdev *netdevHw) Start(ctx context.Context) error {
	prog, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer prog.Close()

	if err := prog.Attach(); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog.WaitDetachByBreaker(childCtx, cancel)

	netdev.prog = prog
	netdev.running.Store(true)

	<-childCtx.Done()

	netdev.running.Store(false)
	return nil
}
