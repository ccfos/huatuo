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

// Package qdisc reads Linux queuing discipline statistics.
package qdisc

import (
	"errors"
	"fmt"
	"math"
	"net"
	"syscall"

	"github.com/mdlayher/netlink"
	"github.com/mdlayher/netlink/nlenc"
)

const (
	netlinkRoute = 0
	rtmGetQdisc  = 38
)

// Linux rtnetlink TCA_* values used by RTM_GETQDISC messages.
const (
	tcaUnspec = iota
	tcaKind
	tcaOptions
	tcaStats
	tcaXStats
	tcaRate
	tcaFCnt
	tcaStats2
	tcaStab
	// __TCA_MAX
)

// Nested TCA_STATS2 values defined by Linux gen_stats.h.
const (
	tcaStatsUnspec = iota
	tcaStatsBasic
	tcaStatsRateEst
	tcaStatsQueue
	tcaStatsApp
	tcaStatsRateEst64
	tcaStatsPad
	tcaStatsBasicHardware
	tcaStatsPacket64
	// __TCA_STATS_MAX
)

// Info contains the statistics exported for one queuing discipline.
type Info struct {
	IfaceName  string
	Parent     uint32
	Kind       string
	Bytes      uint64
	Packets    uint64
	Drops      uint32
	Requeues   uint32
	Overlimits uint32
	Qlen       uint32
	Backlog    uint32
}

type stats struct {
	bytes      uint64
	packets    uint64
	drops      uint32
	requeues   uint32
	overlimits uint32
	qlen       uint32
	backlog    uint32
}

// Get returns the queuing disciplines configured on the current network namespace.
func Get() ([]Info, error) {
	conn, err := netlink.Dial(netlinkRoute, nil)
	if err != nil {
		return nil, fmt.Errorf("dial qdisc netlink socket: %w", err)
	}
	defer conn.Close()

	if err := conn.SetOption(netlink.GetStrictCheck, true); err != nil {
		// silently accept ENOPROTOOPT errors when kernel is not > 4.20
		if !errors.Is(err, syscall.ENOPROTOOPT) {
			return nil, fmt.Errorf("enable qdisc netlink strict checks: %w", err)
		}
	}

	messages, err := conn.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  rtmGetQdisc,
			Flags: netlink.Request | netlink.Dump,
		},
		Data: make([]byte, 20),
	})
	if err != nil {
		return nil, fmt.Errorf("dump qdiscs: %w", err)
	}

	ifaceNames, err := interfaceNames()
	if err != nil {
		return nil, err
	}

	infos := make([]Info, 0, len(messages))
	for _, message := range messages {
		info, err := parseMessage(message, ifaceNames)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}

	return infos, nil
}

func interfaceNames() (map[int]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}

	names := make(map[int]string, len(interfaces))
	for _, iface := range interfaces {
		names[iface.Index] = iface.Name
	}

	return names, nil
}

func parseMessage(message netlink.Message, ifaceNames map[int]string) (Info, error) {
	var info Info

	/*
	   struct tcmsg {
	       unsigned char   tcm_family;
	       unsigned char   tcm__pad1;
	       unsigned short  tcm__pad2;
	       int     tcm_ifindex;
	       __u32       tcm_handle;
	       __u32       tcm_parent;
	       __u32       tcm_info;
	   };
	*/

	if len(message.Data) < 20 {
		return info, fmt.Errorf("qdisc message is short: got %d bytes", len(message.Data))
	}

	ifaceIndex := int(nlenc.Uint32(message.Data[4:8]))
	info.Parent = nlenc.Uint32(message.Data[12:16])
	if info.Parent == math.MaxUint32 {
		info.Parent = 0
	}

	attributes, err := netlink.UnmarshalAttributes(message.Data[20:])
	if err != nil {
		return info, fmt.Errorf("unmarshal qdisc attributes: %w", err)
	}

	var legacyStats *stats
	var stats2 *stats
	for _, attribute := range attributes {
		switch attribute.Type {
		case tcaKind:
			info.Kind = nlenc.String(attribute.Data)
		case tcaStats:
			parsed, err := parseLegacyStats(attribute.Data)
			if err != nil {
				return info, err
			}
			legacyStats = &parsed
		case tcaStats2:
			parsed, err := parseStats2(attribute.Data)
			if err != nil {
				return info, err
			}
			stats2 = &parsed
		}
	}

	if stats2 != nil {
		applyStats(&info, *stats2)
	} else if legacyStats != nil {
		applyStats(&info, *legacyStats)
	}
	info.IfaceName = ifaceNames[ifaceIndex]

	return info, nil
}

func parseStats2(data []byte) (stats, error) {
	attributes, err := netlink.UnmarshalAttributes(data)
	if err != nil {
		return stats{}, fmt.Errorf("unmarshal qdisc statistics: %w", err)
	}

	var result stats
	var basicPackets uint32
	var packets64 uint64
	var hasPackets64 bool
	var previousType uint16
	for _, attribute := range attributes {
		switch attribute.Type {
		case tcaStatsBasic:
			if len(attribute.Data) < 12 {
				return stats{}, fmt.Errorf("qdisc basic statistics are short: got %d bytes", len(attribute.Data))
			}
			result.bytes = nlenc.Uint64(attribute.Data[0:8])
			basicPackets = nlenc.Uint32(attribute.Data[8:12])
		case tcaStatsQueue:
			if len(attribute.Data) < 20 {
				return stats{}, fmt.Errorf("qdisc queue statistics are short: got %d bytes", len(attribute.Data))
			}
			result.qlen = nlenc.Uint32(attribute.Data[0:4])
			result.backlog = nlenc.Uint32(attribute.Data[4:8])
			result.drops = nlenc.Uint32(attribute.Data[8:12])
			result.requeues = nlenc.Uint32(attribute.Data[12:16])
			result.overlimits = nlenc.Uint32(attribute.Data[16:20])
		case tcaStatsPacket64:
			if len(attribute.Data) < 8 {
				return stats{}, fmt.Errorf("qdisc 64-bit packet statistics are short: got %d bytes", len(attribute.Data))
			}
			if previousType == tcaStatsBasic {
				packets64 = nlenc.Uint64(attribute.Data[0:8])
				hasPackets64 = true
			}
		}
		previousType = attribute.Type
	}

	result.packets = uint64(basicPackets)
	if hasPackets64 {
		result.packets = packets64
	}

	return result, nil
}

func parseLegacyStats(data []byte) (stats, error) {
	if len(data) < 36 {
		return stats{}, fmt.Errorf("legacy qdisc statistics are short: got %d bytes", len(data))
	}

	return stats{
		bytes:      nlenc.Uint64(data[0:8]),
		packets:    uint64(nlenc.Uint32(data[8:12])),
		drops:      nlenc.Uint32(data[12:16]),
		overlimits: nlenc.Uint32(data[16:20]),
		qlen:       nlenc.Uint32(data[28:32]),
		backlog:    nlenc.Uint32(data[32:36]),
	}, nil
}

func applyStats(info *Info, stats stats) {
	info.Bytes = stats.bytes
	info.Packets = stats.packets
	info.Drops = stats.drops
	info.Requeues = stats.requeues
	info.Overlimits = stats.overlimits
	info.Qlen = stats.qlen
	info.Backlog = stats.backlog
}
