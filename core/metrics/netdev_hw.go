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
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/dropcorrelation"
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
	eth                   *ethtool.Ethtool
	running               atomic.Bool
	ifaceSwDroppedCounter map[string]uint64
	ifaceList             map[string]*ethtool.DrvInfo
	sysNetPath            string
	normalizer            *dropcorrelation.CounterNormalizer
	correlator            *dropcorrelation.Correlator
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
		eth.Close()
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
			eth:                   eth,
			normalizer:            dropcorrelation.NewCounterNormalizer(),
			correlator: dropcorrelation.ConfigureDefault(dropcorrelation.Options{
				Window:        time.Duration(cfg.NetdevHW.CorrelationWindowSec) * time.Second,
				PendingLimit:  cfg.NetdevHW.PendingEventLimit,
				IncidentLimit: cfg.NetdevHW.RecentIncidentLimit,
				ReasonLimit:   cfg.NetdevHW.ReasonLabelLimit,
			}),
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

	now := time.Now()
	data := make([]*metric.Data, 0, len(netdev.ifaceList)*12)
	for iface, drv := range netdev.ifaceList {
		sample, absolute, err := netdev.collectHardwareSample(now, iface, drv.Driver)
		if err != nil {
			log.Warnf("netdev_hw: collect %s hardware counters: %v", iface, err)
			continue
		}

		count := absolute["rx_missed_errors"]
		// 1. No packet loss
		// 2. rx_missed_errors of the driver is not used.
		if count == 0 {
			// collectHardwareSample already removes BPF-observed software drops.
			count = absolute["rx_dropped"]
		}

		data = append(data, metric.NewCounterData(
			"rx_dropped_total", float64(count),
			"count of packets dropped at hardware level",
			map[string]string{"device": iface, "driver": drv.Driver},
		))

		incidents := netdev.correlator.ObserveHardware(sample)
		netdev.persistCorrelationIncidents(incidents)
	}

	netdev.persistCorrelationIncidents(netdev.correlator.Flush(now))
	data = append(data, netdev.correlationMetrics()...)

	return data, nil
}

func (netdev *netdevHw) collectHardwareSample(now time.Time, iface, driver string) (
	dropcorrelation.HardwareSample,
	map[string]uint64,
	error,
) {
	names := []string{
		"rx_dropped", "rx_missed_errors", "rx_errors",
		"tx_dropped", "tx_errors", "tx_carrier_errors", "tx_fifo_errors",
	}
	sysCounters := make(map[string]uint64, len(names))
	degraded := false
	for _, name := range names {
		value, err := netdev.readSysNetclassStat(iface, name)
		if err != nil {
			degraded = true
			continue
		}
		sysCounters[name] = value
	}

	// sysfs rx_dropped contains software drops on several drivers. Subtract the
	// BPF-observed software component before it enters hardware correlation.
	if sw, ok := netdev.ifaceSwDroppedCounter[iface]; ok {
		if dropped := sysCounters["rx_dropped"]; dropped >= sw {
			sysCounters["rx_dropped"] = dropped - sw
		} else {
			sysCounters["rx_dropped"] = 0
			degraded = true
		}
	}

	var ethCounters map[string]uint64
	if cfg.NetdevHW.EnableEthtoolStats && netdev.eth != nil {
		var err error
		ethCounters, err = netdev.eth.Stats(iface)
		if err != nil {
			degraded = true
			log.Debugf("netdev_hw: ethtool stats unavailable for %s: %v", iface, err)
		}
	}

	sample, err := netdev.normalizer.Normalize(dropcorrelation.RawSample{
		Timestamp: now,
		Device:    iface,
		Driver:    driver,
		Sysfs:     sysCounters,
		Ethtool:   ethCounters,
	})
	if err != nil {
		return dropcorrelation.HardwareSample{}, nil, err
	}
	sample.ReadDegraded = degraded
	return sample, sysCounters, nil
}

func (netdev *netdevHw) correlationMetrics() []*metric.Data {
	snapshot := netdev.correlator.Snapshot()
	data := []*metric.Data{
		metric.NewGaugeData("drop_correlation_pending_events", float64(snapshot.Pending),
			"dropwatch events waiting for a hardware sample", nil),
		metric.NewCounterData("drop_correlation_events_total", float64(snapshot.Events),
			"dropwatch events accepted by the correlation engine", nil),
		metric.NewCounterData("drop_correlation_incidents_total", float64(snapshot.Incidents),
			"dropwatch events finalized by the correlation engine", nil),
		metric.NewCounterData("drop_correlation_pending_dropped_total", float64(snapshot.DroppedPending),
			"events force-finalized because the bounded pending queue was full", nil),
		metric.NewCounterData("drop_correlation_counter_resets_total", float64(snapshot.Resets),
			"NIC counter resets excluded from correlation", nil),
		metric.NewCounterData("drop_correlation_degraded_samples_total", float64(snapshot.DegradedSamples),
			"partially unavailable NIC hardware counter samples", nil),
		metric.NewCounterData("drop_correlation_unmatched_hardware_total", float64(snapshot.UnmatchedHardware),
			"hardware loss samples without a matching dropwatch event", nil),
	}

	for _, key := range snapshot.SortedKeys() {
		data = append(data, metric.NewCounterData(
			"drop_correlation_classified_total",
			float64(snapshot.ByKey[key]),
			"correlated packet drops classified by lowest proven layer",
			map[string]string{
				"device":    key.Device,
				"direction": string(key.Direction),
				"layer":     string(key.Layer),
				"reason":    key.Reason,
			},
		))
	}

	devices := make([]string, 0, len(snapshot.LastHardwareByIface))
	for device := range snapshot.LastHardwareByIface {
		devices = append(devices, device)
	}
	sort.Strings(devices)
	for _, device := range devices {
		sample := snapshot.LastHardwareByIface[device]
		for _, counter := range dropcorrelation.AllCounters() {
			data = append(data, metric.NewGaugeData(
				"drop_correlation_hardware_delta",
				float64(sample.Delta[counter]),
				"normalized NIC loss counter delta used by the correlation engine",
				map[string]string{
					"device":  device,
					"driver":  sample.Driver,
					"counter": string(counter),
				},
			))
		}
	}
	return data
}

func (netdev *netdevHw) persistCorrelationIncidents(incidents []dropcorrelation.Incident) {
	for _, incident := range incidents {
		delta := make(map[string]uint64, len(incident.HardwareDelta))
		for counter, value := range incident.HardwareDelta {
			if value > 0 {
				delta[string(counter)] = value
			}
		}
		storageEvent := &types.DropCorrelationIncident{
			EventID:              incident.Event.ID,
			ObservedTimestamp:    incident.Event.Timestamp.UTC().Format(time.RFC3339Nano),
			FinalizedTimestamp:   incident.FinalizedAt.UTC().Format(time.RFC3339Nano),
			Device:               incident.Event.Device,
			IfIndex:              incident.Event.IfIndex,
			Driver:               incident.Driver,
			Direction:            string(incident.Event.Direction),
			Layer:                string(incident.Layer),
			Confidence:           incident.Confidence,
			DropReason:           incident.Event.Reason,
			Protocol:             incident.Event.Protocol,
			ContainerID:          incident.Event.ContainerID,
			PacketLength:         incident.Event.PacketLen,
			CorrelationLagMS:     float64(incident.CorrelationLag) / float64(time.Millisecond),
			Evidence:             append([]string(nil), incident.Evidence...),
			HardwareCounterDelta: delta,
		}
		if !incident.HardwareSampleAt.IsZero() {
			storageEvent.HardwareTimestamp = incident.HardwareSampleAt.UTC().Format(time.RFC3339Nano)
		}
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName:  "dropwatch_correlation",
			ContainerID: incident.Event.ContainerID,
			TracerTime:  incident.FinalizedAt,
			TracerData:  storageEvent,
		}); err != nil {
			log.Warnf("netdev_hw: save correlated drop event %d: %v", incident.Event.ID, err)
		}
	}
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
	if netdev.eth != nil {
		defer netdev.eth.Close()
	}
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
