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
	"net"

	"github.com/mdlayher/netlink"
	"github.com/mdlayher/netlink/nlenc"
	"golang.org/x/sys/unix"
)

const (
	tcMessageLen              = 20
	netlinkAttributeHeaderLen = 4
	netlinkAttributeTypeMask  = 0x3fff
	rootParentHandle          = ^uint32(0)
)

// Nested TCA_STATS2 values defined by Linux gen_stats.h.
const (
	tcaStatsBasic         = 1
	tcaStatsQueue         = 3
	tcaStatsBasicHardware = 7
	tcaStatsPacket64      = 8
)

// Stats contains the measurements for one queuing discipline.
type Stats struct {
	Netdev       string
	Kind         string
	Bytes        uint64
	Packets      uint64
	Parent       uint32
	Drops        uint32
	Requeues     uint32
	Overlimits   uint32
	QueueLength  uint32
	BacklogBytes uint32
}

// IsRoot reports whether the statistics belong to a root qdisc.
func (s *Stats) IsRoot() bool {
	return s.Parent == rootParentHandle
}

type counters struct {
	bytes        uint64
	packets      uint64
	drops        uint32
	requeues     uint32
	overlimits   uint32
	queueLength  uint32
	backlogBytes uint32
}

// Read returns the queuing disciplines configured on the current network namespace.
func Read() ([]Stats, error) {
	conn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("dial qdisc netlink socket: %w", err)
	}
	defer conn.Close()

	if err := conn.SetOption(netlink.GetStrictCheck, true); err != nil {
		// Kernels before 4.20 do not implement strict netlink checks.
		if !errors.Is(err, unix.ENOPROTOOPT) {
			return nil, fmt.Errorf("enable qdisc netlink strict checks: %w", err)
		}
	}

	messages, err := conn.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  unix.RTM_GETQDISC,
			Flags: netlink.Request | netlink.Dump,
		},
		Data: make([]byte, tcMessageLen),
	})
	if err != nil {
		return nil, fmt.Errorf("dump qdisc statistics: %w", err)
	}

	netdevNames, err := readNetdevNames()
	if err != nil {
		return nil, err
	}

	qdiscStats := make([]Stats, 0, len(messages))
	for i := range messages {
		stat, err := decodeMessage(messages[i].Data, netdevNames)
		if err != nil {
			return nil, err
		}
		qdiscStats = append(qdiscStats, stat)
	}

	return qdiscStats, nil
}

func readNetdevNames() (map[int]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}

	names := make(map[int]string, len(interfaces))
	for i := range interfaces {
		names[interfaces[i].Index] = interfaces[i].Name
	}

	return names, nil
}

func decodeMessage(data []byte, netdevNames map[int]string) (Stats, error) {
	var stat Stats
	if len(data) < tcMessageLen {
		return stat, fmt.Errorf(
			"qdisc message length is %d bytes, want at least %d",
			len(data),
			tcMessageLen,
		)
	}

	ifindex := int(nlenc.Uint32(data[4:8]))
	stat.Netdev = netdevNames[ifindex]
	stat.Parent = nlenc.Uint32(data[12:16])

	var selected counters
	var hasStats2 bool
	attributes := data[tcMessageLen:]
	for len(attributes) > 0 {
		attributeType, payload, remaining, err := nextAttribute(attributes)
		if err != nil {
			return stat, fmt.Errorf("decode qdisc attributes: %w", err)
		}
		attributes = remaining

		switch attributeType {
		case unix.TCA_KIND:
			stat.Kind = nlenc.String(payload)
		case unix.TCA_STATS:
			legacy, err := decodeLegacyStats(payload)
			if err != nil {
				return stat, err
			}
			if !hasStats2 {
				selected = legacy
			}
		case unix.TCA_STATS2:
			selected, err = decodeStats2(payload)
			if err != nil {
				return stat, err
			}
			hasStats2 = true
		}
	}

	stat.Bytes = selected.bytes
	stat.Packets = selected.packets
	stat.Drops = selected.drops
	stat.Requeues = selected.requeues
	stat.Overlimits = selected.overlimits
	stat.QueueLength = selected.queueLength
	stat.BacklogBytes = selected.backlogBytes

	return stat, nil
}

func decodeStats2(attributes []byte) (counters, error) {
	var decoded counters
	var basicPackets uint32
	var packets64 uint64
	var hasPackets64 bool
	var previousType uint16
	for len(attributes) > 0 {
		attributeType, payload, remaining, err := nextAttribute(attributes)
		if err != nil {
			return counters{}, fmt.Errorf("decode qdisc statistics: %w", err)
		}
		attributes = remaining

		switch attributeType {
		case tcaStatsBasic:
			if len(payload) < 12 {
				return counters{}, fmt.Errorf(
					"qdisc basic statistics length is %d bytes, want at least 12",
					len(payload),
				)
			}
			decoded.bytes = nlenc.Uint64(payload[0:8])
			basicPackets = nlenc.Uint32(payload[8:12])
		case tcaStatsQueue:
			if len(payload) < 20 {
				return counters{}, fmt.Errorf(
					"qdisc queue statistics length is %d bytes, want at least 20",
					len(payload),
				)
			}
			decoded.queueLength = nlenc.Uint32(payload[0:4])
			decoded.backlogBytes = nlenc.Uint32(payload[4:8])
			decoded.drops = nlenc.Uint32(payload[8:12])
			decoded.requeues = nlenc.Uint32(payload[12:16])
			decoded.overlimits = nlenc.Uint32(payload[16:20])
		case tcaStatsPacket64:
			if len(payload) < 8 {
				return counters{}, fmt.Errorf(
					"qdisc 64-bit packet statistics length is %d bytes, want at least 8",
					len(payload),
				)
			}
			if previousType == tcaStatsBasic {
				packets64 = nlenc.Uint64(payload[0:8])
				hasPackets64 = true
			}
		}
		previousType = attributeType
	}

	decoded.packets = uint64(basicPackets)
	if hasPackets64 {
		decoded.packets = packets64
	}

	return decoded, nil
}

func decodeLegacyStats(data []byte) (counters, error) {
	if len(data) < 36 {
		return counters{}, fmt.Errorf(
			"legacy qdisc statistics length is %d bytes, want at least 36",
			len(data),
		)
	}

	return counters{
		bytes:        nlenc.Uint64(data[0:8]),
		packets:      uint64(nlenc.Uint32(data[8:12])),
		drops:        nlenc.Uint32(data[12:16]),
		overlimits:   nlenc.Uint32(data[16:20]),
		queueLength:  nlenc.Uint32(data[28:32]),
		backlogBytes: nlenc.Uint32(data[32:36]),
	}, nil
}

func nextAttribute(data []byte) (uint16, []byte, []byte, error) {
	if len(data) < netlinkAttributeHeaderLen {
		return 0, nil, nil, fmt.Errorf(
			"netlink attribute has %d bytes, want at least %d",
			len(data),
			netlinkAttributeHeaderLen,
		)
	}

	attributeLen := int(nlenc.Uint16(data[0:2]))
	if attributeLen < netlinkAttributeHeaderLen {
		return 0, nil, nil, fmt.Errorf(
			"netlink attribute length is %d bytes, want at least %d",
			attributeLen,
			netlinkAttributeHeaderLen,
		)
	}
	if attributeLen > len(data) {
		return 0, nil, nil, fmt.Errorf(
			"netlink attribute length is %d bytes, only %d bytes remain",
			attributeLen,
			len(data),
		)
	}

	nextOffset := (attributeLen + 3) &^ 3
	if nextOffset > len(data) {
		nextOffset = len(data)
	}

	attributeType := nlenc.Uint16(data[2:4]) & netlinkAttributeTypeMask
	return attributeType, data[netlinkAttributeHeaderLen:attributeLen], data[nextOffset:], nil
}
