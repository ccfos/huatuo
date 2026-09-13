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

package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/linkstatus"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type netdevInfo struct {
	flags           uint32
	driver          string
	driverVersion   string
	firmwareVersion string
}

// driverInfoSource provides ethtool driver metadata for netdev interfaces.
type driverInfoSource interface {
	DriverInfo(ifname string) (ethtool.DrvInfo, error)
}

type netdevTracing struct {
	name                  string
	linkUpdateCh          chan netlink.LinkUpdate
	linkDoneCh            chan struct{}
	mu                    sync.Mutex
	netdevInfoStore       map[string]*netdevInfo              // [ifname]ifinfomsg::netdevInfo
	linkStatusEventCounts map[linkstatus.Types]map[string]int // [netdevEventType][ifname]count
	deviceMatcher         *matcher.ListMatcher                // device whitelist for interfaces first seen after start
	eth                   driverInfoSource                    // driver metadata for interfaces first seen after start
}

type netdevEventData struct {
	linkFlags       uint32
	flagsChange     uint32
	Ifname          string `json:"ifname"`
	Index           int    `json:"index"`
	LinkStatus      string `json:"linkstatus"`
	Mac             string `json:"mac"`
	IsAtStart       bool   `json:"start"` // true: be scanned at start, false: event trigger
	Driver          string `json:"driver"`
	DriverVersion   string `json:"driver_version"`
	FirmwareVersion string `json:"firmware_version"`
}

func init() {
	tracing.RegisterEventTracing("netdev_events", newNetdevTracing)
}

func newNetdevTracing() (*tracing.EventTracingAttr, error) {
	initMap := make(map[linkstatus.Types]map[string]int)
	for i := linkstatus.Unknown; i < linkstatus.MaxTypeNums; i++ {
		initMap[i] = make(map[string]int)
	}

	return &tracing.EventTracingAttr{
		TracingData: &netdevTracing{
			netdevInfoStore:       make(map[string]*netdevInfo),
			linkStatusEventCounts: initMap,
			name:                  "netdev_events",
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (netdev *netdevTracing) Start(ctx context.Context) error {
	eth, err := ethtool.NewEthtool()
	if err != nil {
		return err
	}
	netdev.eth = eth
	defer eth.Close()

	if err := netdev.checkAndInitLinkStatus(); err != nil {
		return err
	}

	// Create new channels because linkDoneCh and linkUpdateCh
	// cannot be reused after stop/restart.
	netdev.linkUpdateCh = make(chan netlink.LinkUpdate)
	netdev.linkDoneCh = make(chan struct{})

	if err := netlink.LinkSubscribe(netdev.linkUpdateCh, netdev.linkDoneCh); err != nil {
		return err
	}
	defer netdev.close()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case update, ok := <-netdev.linkUpdateCh:
			if !ok {
				return nil
			}
			switch update.Header.Type {
			case unix.NLMSG_ERROR:
				return fmt.Errorf("NLMSG_ERROR")
			case unix.RTM_NEWLINK, unix.RTM_DELLINK:
				netdev.handleEvent(&update)
			}
		}
	}
}

// Update implement Collector
func (netdev *netdevTracing) Update() ([]*metric.Data, error) {
	netdev.mu.Lock()
	defer netdev.mu.Unlock()

	var metrics []*metric.Data

	for typ, value := range netdev.linkStatusEventCounts {
		for ifname, count := range value {
			info, exists := netdev.netdevInfoStore[ifname]
			if !exists {
				continue
			}
			metrics = append(metrics, metric.NewCounterData(
				typ.String()+"_total", float64(count), typ.String(),
				map[string]string{
					"device":           ifname,
					"driver":           info.driver,
					"driver_version":   info.driverVersion,
					"firmware_version": info.firmwareVersion,
				},
			))
		}
	}

	return metrics, nil
}

func (netdev *netdevTracing) checkAndInitLinkStatus() error {
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}

	cfg := configSnapshot()
	netdev.deviceMatcher, err = matcher.NewListMatcher(cfg.Netdev.DeviceList)
	if err != nil {
		return fmt.Errorf("netdev device list: %w", err)
	}

	for _, link := range links {
		ifname := link.Attrs().Name
		if !netdev.deviceMatcher.Match(ifname) {
			continue
		}

		drvInfo, err := netdev.eth.DriverInfo(ifname)
		if err != nil {
			continue
		}

		flags := link.Attrs().RawFlags
		netdev.setInfo(ifname, &netdevInfo{
			flags:           flags,
			driver:          drvInfo.Driver,
			driverVersion:   drvInfo.Version,
			firmwareVersion: drvInfo.FwVersion,
		})

		data := &netdevEventData{
			linkFlags:       flags,
			Ifname:          ifname,
			Index:           link.Attrs().Index,
			Mac:             link.Attrs().HardwareAddr.String(),
			IsAtStart:       true,
			Driver:          drvInfo.Driver,
			DriverVersion:   drvInfo.Version,
			FirmwareVersion: drvInfo.FwVersion,
		}
		netdev.updateAndSaveEvent(data)
	}

	return nil
}

func (netdev *netdevTracing) updateAndSaveEvent(data *netdevEventData) {
	changed := linkstatus.Changed(data.linkFlags, data.flagsChange)

	netdev.mu.Lock()
	for _, status := range changed {
		netdev.linkStatusEventCounts[status][data.Ifname]++
	}
	netdev.mu.Unlock()

	for _, status := range changed {
		if data.LinkStatus == "" {
			data.LinkStatus = status.String()
		} else {
			data.LinkStatus = data.LinkStatus + ", " + status.String()
		}
	}

	if !data.IsAtStart && data.LinkStatus != "" {
		log.Infof("%s %+v", data.LinkStatus, data)
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName:        netdev.name,
			ObservedTimestamp: time.Now().UTC(),
			TracerData:        data,
		}); err != nil {
			log.Warnf("failed to save tracing data: %v", err)
		}
	}
}

func (netdev *netdevTracing) setInfo(ifname string, info *netdevInfo) {
	netdev.mu.Lock()
	netdev.netdevInfoStore[ifname] = info
	netdev.mu.Unlock()
}

// trackNewDevice starts tracking an interface first seen after startup so its
// later flag changes are reported like devices present at startup. Devices
// outside the configured device list, or without driver info, stay untracked,
// matching checkAndInitLinkStatus. The observed flags become the baseline and
// do not count as events.
func (netdev *netdevTracing) trackNewDevice(ifname string, flags uint32) {
	if !netdev.deviceMatcher.Match(ifname) {
		return
	}

	var drvInfo ethtool.DrvInfo
	if netdev.eth != nil {
		info, err := netdev.eth.DriverInfo(ifname)
		if err != nil {
			return
		}
		drvInfo = info
	}

	netdev.setInfo(ifname, &netdevInfo{
		flags:           flags,
		driver:          drvInfo.Driver,
		driverVersion:   drvInfo.Version,
		firmwareVersion: drvInfo.FwVersion,
	})
}

// removeDevice forgets a deleted interface so its metrics stop being exported
// and a reused interface name does not inherit its counters.
func (netdev *netdevTracing) removeDevice(ifname string) {
	netdev.mu.Lock()
	defer netdev.mu.Unlock()

	delete(netdev.netdevInfoStore, ifname)
	for _, counts := range netdev.linkStatusEventCounts {
		delete(counts, ifname)
	}
}

func (netdev *netdevTracing) loadAndSwapFlags(ifname string, newFlags uint32) (oldFlags uint32, driverInfo netdevInfo, ok bool) {
	netdev.mu.Lock()
	defer netdev.mu.Unlock()

	stored, ok := netdev.netdevInfoStore[ifname]
	if !ok {
		// new interface
		return 0, netdevInfo{}, false
	}

	oldFlags = stored.flags
	stored.flags = newFlags
	return oldFlags, *stored, true
}

func (netdev *netdevTracing) handleEvent(ev *netlink.LinkUpdate) {
	ifname := ev.Link.Attrs().Name

	if ev.Header.Type == unix.RTM_DELLINK {
		netdev.removeDevice(ifname)
		return
	}

	currFlags := ev.Attrs().RawFlags

	oldFlags, driverInfo, ok := netdev.loadAndSwapFlags(ifname, currFlags)
	if !ok {
		// interface first seen after startup
		netdev.trackNewDevice(ifname, currFlags)
		return
	}
	change := currFlags ^ oldFlags

	data := &netdevEventData{
		linkFlags:       currFlags,
		flagsChange:     change,
		Ifname:          ifname,
		Index:           ev.Link.Attrs().Index,
		Mac:             ev.Link.Attrs().HardwareAddr.String(),
		IsAtStart:       false,
		Driver:          driverInfo.driver,
		DriverVersion:   driverInfo.driverVersion,
		FirmwareVersion: driverInfo.firmwareVersion,
	}
	netdev.updateAndSaveEvent(data)
}

func (netdev *netdevTracing) close() {
	// netlink.LinkSubscribe closes linkUpdateCh inner
	close(netdev.linkDoneCh)
}
