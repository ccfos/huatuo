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

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"

	"huatuo-bamai/internal/bpf"
)

const (
	netdevFilterModeOff uint32 = iota
	netdevFilterModeWhitelist
	netdevFilterModeBlacklist
)

const netdevFilterModeMap = "skb_filter_dev_map"

func applyDeviceFilter(b bpf.BPF, mode uint32, ifindexes []uint32) error {
	if mode == netdevFilterModeOff {
		return nil
	}
	mapID := b.MapIDByName(netdevFilterModeMap)
	if mapID == 0 {
		return fmt.Errorf("bpf map %q not found", netdevFilterModeMap)
	}

	items := make([]bpf.MapItem, 0, len(ifindexes))
	for _, idx := range ifindexes {
		key := make([]byte, 4)
		binary.NativeEndian.PutUint32(key, idx)
		items = append(items, bpf.MapItem{Key: key, Value: []byte{1}})
	}
	return b.WriteMapItems(mapID, items)
}

func parseNetdevFilterFlags(device, excluded string) (uint32, []uint32, error) {
	var (
		list string
		mode uint32
	)
	switch {
	case device != "":
		list, mode = device, netdevFilterModeWhitelist
	case excluded != "":
		list, mode = excluded, netdevFilterModeBlacklist
	default:
		return netdevFilterModeOff, nil, nil
	}

	var ifindexes []uint32
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return 0, nil, fmt.Errorf("device %q: %w", name, err)
		}
		ifindexes = append(ifindexes, uint32(iface.Index))
	}
	if len(ifindexes) == 0 {
		return 0, nil, errors.New("no valid interfaces specified")
	}
	return mode, ifindexes, nil
}
