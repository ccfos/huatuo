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

package dropwatch

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
)

const (
	netdevModeDisabled uint32 = iota
	netdevModeAllow
	netdevModeDeny
)

const netdevFilterModeMap = "skb_filter_dev_map"

type netdevOptions struct {
	mode      uint32
	ifindexes []uint32
}

func applyNetdevOptions(b bpf.BPF, options netdevOptions) error {
	if options.mode == netdevModeDisabled {
		return nil
	}
	mapID := b.MapIDByName(netdevFilterModeMap)
	if mapID == 0 {
		return fmt.Errorf("bpf map %q not found", netdevFilterModeMap)
	}

	items := make([]bpf.MapItem, 0, len(options.ifindexes))
	for _, idx := range options.ifindexes {
		key := make([]byte, 4)
		binary.NativeEndian.PutUint32(key, idx)
		items = append(items, bpf.MapItem{Key: key, Value: []byte{1}})
	}
	return b.WriteMapItems(mapID, items)
}

func resolveNetdevOptions(included, excluded []string) (netdevOptions, error) {
	var (
		list []string
		mode uint32
	)
	switch {
	case len(included) != 0:
		list, mode = included, netdevModeAllow
	case len(excluded) != 0:
		list, mode = excluded, netdevModeDeny
	default:
		return netdevOptions{mode: netdevModeDisabled}, nil
	}

	var ifindexes []uint32
	for _, name := range list {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return netdevOptions{}, fmt.Errorf("device %q: %w", name, err)
		}
		ifindexes = append(ifindexes, uint32(iface.Index))
	}
	if len(ifindexes) == 0 {
		return netdevOptions{}, errors.New("no valid interfaces specified")
	}
	return netdevOptions{mode: mode, ifindexes: ifindexes}, nil
}
