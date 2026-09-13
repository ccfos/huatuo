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

package events

import (
	"testing"

	"huatuo-bamai/internal/linkstatus"
	"huatuo-bamai/internal/matcher"

	"github.com/safchain/ethtool"
	"github.com/stretchr/testify/assert"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type stubDriverInfoSource struct {
	info ethtool.DrvInfo
	err  error
}

func (s *stubDriverInfoSource) DriverInfo(string) (ethtool.DrvInfo, error) {
	return s.info, s.err
}

func newTestNetdevTracing(t *testing.T) *netdevTracing {
	t.Helper()

	attr, err := newNetdevTracing()
	assert.NoError(t, err)

	nd := attr.TracingData.(*netdevTracing)
	nd.eth = &stubDriverInfoSource{
		info: ethtool.DrvInfo{Driver: "veth", Version: "1.0", FwVersion: "N/A"},
	}
	return nd
}

func netdevLinkUpdate(linkType uint16, ifname string, flags uint32) *netlink.LinkUpdate {
	return &netlink.LinkUpdate{
		Header: unix.NlMsghdr{Type: linkType},
		Link: &netlink.Device{LinkAttrs: netlink.LinkAttrs{
			Name:     ifname,
			RawFlags: flags,
		}},
	}
}

func TestHandleEventTracksInterfacesAddedAfterStart(t *testing.T) {
	nd := newTestNetdevTracing(t)
	deviceMatcher, err := matcher.NewListMatcher([]string{"veth.*"})
	assert.NoError(t, err)
	nd.deviceMatcher = deviceMatcher

	// eth0 was present at startup and is the only tracked device so far.
	up := uint32(unix.IFF_UP | unix.IFF_RUNNING | unix.IFF_LOWER_UP)
	nd.setInfo("eth0", &netdevInfo{flags: up, driver: "mlx5_core"})

	// A veth is created after startup, then loses its carrier.
	carrierUp := up
	carrierDown := up &^ unix.IFF_LOWER_UP
	nd.handleEvent(netdevLinkUpdate(unix.RTM_NEWLINK, "veth0", carrierUp))
	nd.handleEvent(netdevLinkUpdate(unix.RTM_NEWLINK, "veth0", carrierDown))

	info, ok := nd.netdevInfoStore["veth0"]
	if !ok {
		t.Fatal("device added after start must be tracked")
	}
	assert.Equal(t, "veth", info.driver)
	assert.Equal(t, carrierDown, info.flags)

	// The first observation is the baseline, the later change is an event.
	assert.Equal(t, 0, nd.linkStatusEventCounts[linkstatus.CarrierUp]["veth0"])
	assert.Equal(t, 1, nd.linkStatusEventCounts[linkstatus.CarrierDown]["veth0"])
}

func TestHandleEventIgnoresUnlistedNewInterfaces(t *testing.T) {
	nd := newTestNetdevTracing(t)
	deviceMatcher, err := matcher.NewListMatcher([]string{"eth.*"})
	assert.NoError(t, err)
	nd.deviceMatcher = deviceMatcher

	nd.handleEvent(netdevLinkUpdate(unix.RTM_NEWLINK, "veth0", unix.IFF_UP))

	_, ok := nd.netdevInfoStore["veth0"]
	assert.False(t, ok, "device outside the configured list must stay untracked")
}

func TestHandleEventSkipsNewInterfacesWithoutDriverInfo(t *testing.T) {
	nd := newTestNetdevTracing(t)
	deviceMatcher, err := matcher.NewListMatcher([]string{"veth.*"})
	assert.NoError(t, err)
	nd.deviceMatcher = deviceMatcher
	nd.eth = &stubDriverInfoSource{err: unix.EOPNOTSUPP}

	nd.handleEvent(netdevLinkUpdate(unix.RTM_NEWLINK, "veth0", unix.IFF_UP))

	_, ok := nd.netdevInfoStore["veth0"]
	assert.False(t, ok, "device without driver info must stay untracked")
}

func TestHandleEventForgetsRemovedInterfaces(t *testing.T) {
	nd := newTestNetdevTracing(t)

	up := uint32(unix.IFF_UP | unix.IFF_LOWER_UP)
	nd.setInfo("eth1", &netdevInfo{flags: up, driver: "e1000"})
	nd.linkStatusEventCounts[linkstatus.CarrierUp]["eth1"] = 3

	nd.handleEvent(netdevLinkUpdate(unix.RTM_DELLINK, "eth1", 0))

	_, ok := nd.netdevInfoStore["eth1"]
	assert.False(t, ok, "removed device must be forgotten")
	assert.Empty(t, nd.linkStatusEventCounts[linkstatus.CarrierUp]["eth1"])

	metrics, err := nd.Update()
	assert.NoError(t, err)
	assert.Empty(t, metrics, "removed device must stop exporting metrics")
}
