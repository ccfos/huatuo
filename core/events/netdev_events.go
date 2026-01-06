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

package events

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type linkStatusType uint8

const (
	linkStatusUnknown linkStatusType = iota
	linkStatusAdminUp
	linkStatusAdminDown
	linkStatusCarrierUp
	linkStatusCarrierDown
	maxLinkStatus
)

func (l linkStatusType) String() string {
	return [...]string{"linkstatus_unknown", "linkstatus_adminup", "linkstatus_admindown", "linkstatus_carrierup", "linkstatus_carrierdown"}[l]
}

func flags2status(flags, change uint32) []linkStatusType {
	var status []linkStatusType

	if change&unix.IFF_UP != 0 {
		if flags&unix.IFF_UP != 0 {
			status = append(status, linkStatusAdminUp)
		} else {
			status = append(status, linkStatusAdminDown)
		}
	}

	if change&unix.IFF_LOWER_UP != 0 {
		if flags&unix.IFF_LOWER_UP != 0 {
			status = append(status, linkStatusCarrierUp)
		} else {
			status = append(status, linkStatusCarrierDown)
		}
	}

	return status
}

type netdevInfo struct {
	flags           uint32
	driver          string
	driverVersion   string
	firmwareVersion string
}

type netdevTracing struct {
	name                  string
	linkUpdateCh          chan netlink.LinkUpdate
	linkDoneCh            chan struct{}
	mu                    sync.Mutex
	netdevInfoStore       map[string]*netdevInfo            // [ifname]ifinfomsg::netdevInfo
	linkStatusEventCounts map[linkStatusType]map[string]int // [netdevEventType][ifname]count
}

type netdevEventData struct {
	linkFlags       uint32
	flagsChange     uint32
	Ifname          string `json:"ifname"`
	Index           int    `json:"index"`
	LinkStatus      string `json:"linkstatus"`
	Mac             string `json:"mac"`
	AtStart         bool   `json:"start"` // true: be scanned at start, false: event trigger
	Driver          string `json:"driver"`
	DriverVersion   string `json:"driver_version"`
	FirmwareVersion string `json:"firmware_version"`
}

func init() {
	tracing.RegisterEventTracing("netdev_events", newNetdevTracing)
}

func newNetdevTracing() (*tracing.EventTracingAttr, error) {
	initMap := make(map[linkStatusType]map[string]int)
	for i := linkStatusUnknown; i < maxLinkStatus; i++ {
		initMap[i] = make(map[string]int)
	}

	return &tracing.EventTracingAttr{
		TracingData: &netdevTracing{
			linkUpdateCh:          make(chan netlink.LinkUpdate),
			linkDoneCh:            make(chan struct{}),
			netdevInfoStore:       make(map[string]*netdevInfo),
			linkStatusEventCounts: initMap,
			name:                  "netdev_events",
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (netdev *netdevTracing) Start(ctx context.Context) (err error) {
	if err := netdev.checkAndInitLinkStatus(); err != nil {
		return err
	}

	if err := netlink.LinkSubscribe(netdev.linkUpdateCh, netdev.linkDoneCh); err != nil {
		return err
	}
	defer netdev.close()

	for {
		update, ok := <-netdev.linkUpdateCh
		if !ok {
			return nil
		}
		switch update.Header.Type {
		case unix.NLMSG_ERROR:
			return fmt.Errorf("NLMSG_ERROR")
		case unix.RTM_NEWLINK:
			ifname := update.Link.Attrs().Name
			if _, ok := netdev.netdevInfoStore[ifname]; !ok {
				// new interface
				continue
			}
			netdev.handleEvent(&update)
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
			metrics = append(metrics, metric.NewCounterData(
				typ.String()+"_total", float64(count), typ.String(),
				map[string]string{
					"device":           ifname,
					"driver":           netdev.netdevInfoStore[ifname].driver,
					"driver_version":   netdev.netdevInfoStore[ifname].driverVersion,
					"firmware_version": netdev.netdevInfoStore[ifname].firmwareVersion,
				}))
		}
	}

	return metrics, nil
}

func (netdev *netdevTracing) checkAndInitLinkStatus() error {
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return err
	}
	defer eth.Close()

	for _, link := range links {
		ifname := link.Attrs().Name
		if !slices.Contains(conf.Get().EventTracing.Netdev.DeviceList,
			ifname) {
			continue
		}

		drvInfo, err := eth.DriverInfo(ifname)
		if err != nil {
			continue
		}

		flags := link.Attrs().RawFlags
		netdev.netdevInfoStore[ifname] = &netdevInfo{
			flags:           flags,
			driver:          drvInfo.Driver,
			driverVersion:   drvInfo.Version,
			firmwareVersion: drvInfo.FwVersion,
		}

		data := &netdevEventData{
			linkFlags:       flags,
			Ifname:          ifname,
			Index:           link.Attrs().Index,
			Mac:             link.Attrs().HardwareAddr.String(),
			AtStart:         true,
			Driver:          drvInfo.Driver,
			DriverVersion:   drvInfo.Version,
			FirmwareVersion: drvInfo.FwVersion,
		}
		netdev.updateAndSaveEvent(data)
	}

	return nil
}

func (netdev *netdevTracing) updateAndSaveEvent(data *netdevEventData) {
	for _, status := range flags2status(data.linkFlags, data.flagsChange) {
		netdev.mu.Lock()
		netdev.linkStatusEventCounts[status][data.Ifname]++
		netdev.mu.Unlock()

		if data.LinkStatus == "" {
			data.LinkStatus = status.String()
		} else {
			data.LinkStatus = data.LinkStatus + ", " + status.String()
		}
	}

	if !data.AtStart && data.LinkStatus != "" {
		log.Infof("%s %+v", data.LinkStatus, data)
		storage.Save(netdev.name, "", time.Now(), data)
	}
}

func (netdev *netdevTracing) handleEvent(ev *netlink.LinkUpdate) {
	ifname := ev.Link.Attrs().Name

	currFlags := ev.Attrs().RawFlags
	lastFlags := netdev.netdevInfoStore[ifname].flags
	change := currFlags ^ lastFlags

	netdev.netdevInfoStore[ifname].flags = currFlags

	data := &netdevEventData{
		linkFlags:       currFlags,
		flagsChange:     change,
		Ifname:          ifname,
		Index:           ev.Link.Attrs().Index,
		Mac:             ev.Link.Attrs().HardwareAddr.String(),
		AtStart:         false,
		Driver:          netdev.netdevInfoStore[ifname].driver,
		DriverVersion:   netdev.netdevInfoStore[ifname].driverVersion,
		FirmwareVersion: netdev.netdevInfoStore[ifname].firmwareVersion,
	}
	netdev.updateAndSaveEvent(data)
}

func (netdev *netdevTracing) close() {
	close(netdev.linkDoneCh)
	close(netdev.linkUpdateCh)
}
