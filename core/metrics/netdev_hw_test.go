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

package collector

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/bpf"

	"github.com/safchain/ethtool"
)

func newTestNetdevHw(sysNetPath string, ifaces map[string]string) *netdevHw {
	ifaceList := make(map[string]*ethtool.DrvInfo, len(ifaces))
	for iface, driver := range ifaces {
		ifaceList[iface] = &ethtool.DrvInfo{Driver: driver}
	}
	swCounter := make(map[string]uint64, len(ifaces))
	for iface := range ifaces {
		swCounter[iface] = 0
	}
	return &netdevHw{
		ifaceList:             ifaceList,
		ifaceSwDroppedCounter: swCounter,
		sysNetPath:            sysNetPath,
		interfaceByIndex: func(int) (*net.Interface, error) {
			return nil, errors.New("not wired")
		},
	}
}

func writeIfaceStat(t *testing.T, sysNetPath, iface, stat, value string) {
	t.Helper()
	dir := filepath.Join(sysNetPath, iface, "statistics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error=%v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, stat), []byte(value), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", stat, err)
	}
}

func TestNetdevHwCollectDataSkipsFailedStatRead(t *testing.T) {
	sysNetPath := t.TempDir()
	// eth0 is complete, eth1 misses rx_missed_errors.
	writeIfaceStat(t, sysNetPath, "eth0", "rx_dropped", "7")
	writeIfaceStat(t, sysNetPath, "eth0", "rx_missed_errors", "0")
	writeIfaceStat(t, sysNetPath, "eth1", "rx_dropped", "9")

	netdev := newTestNetdevHw(sysNetPath, map[string]string{
		"eth0": "mlx5_core",
		"eth1": "mlx5_core",
	})

	data := netdev.collectData()
	if len(data) != 1 {
		t.Fatalf("collectData() emitted %d samples, want 1 (eth1 must be skipped)", len(data))
	}
	if got := data[0].Value; got != 7 {
		t.Errorf("eth0 rx_dropped_total=%v, want 7", got)
	}
}

func TestNetdevHwCollectDataSubtractsSoftwareDrops(t *testing.T) {
	sysNetPath := t.TempDir()
	writeIfaceStat(t, sysNetPath, "eth0", "rx_dropped", "10")
	writeIfaceStat(t, sysNetPath, "eth0", "rx_missed_errors", "0")

	netdev := newTestNetdevHw(sysNetPath, map[string]string{"eth0": "mlx5_core"})
	netdev.ifaceSwDroppedCounter["eth0"] = 4

	data := netdev.collectData()
	if len(data) != 1 {
		t.Fatalf("collectData() emitted %d samples, want 1", len(data))
	}
	if got := data[0].Value; got != 6 {
		t.Errorf("rx_dropped_total=%v, want 6 (10 - 4 software drops)", got)
	}
}

func TestNetdevHwApplySwDroppedItemsToleratesStaleIfindex(t *testing.T) {
	encodeItem := func(ifidx uint32, counter uint64) bpf.MapItem {
		key := make([]byte, 4)
		value := make([]byte, 8)
		binary.LittleEndian.PutUint32(key, ifidx)
		binary.LittleEndian.PutUint64(value, counter)
		return bpf.MapItem{Key: key, Value: value}
	}

	sysNetPath := t.TempDir()
	netdev := newTestNetdevHw(sysNetPath, map[string]string{"eth0": "mlx5_core"})
	lookups := 0
	netdev.interfaceByIndex = func(ifindex int) (*net.Interface, error) {
		lookups++
		if ifindex == 101 {
			// Stale key: the interface no longer exists.
			return nil, errors.New("route ip+net: no such network interface")
		}
		return &net.Interface{Name: "eth0"}, nil
	}

	items := []bpf.MapItem{
		encodeItem(101, 111), // stale, must be skipped
		encodeItem(102, 222), // eth0, must be applied
	}
	if err := netdev.applySwDroppedItems(items); err != nil {
		t.Fatalf("applySwDroppedItems() error=%v, want stale key tolerated", err)
	}
	if lookups != 2 {
		t.Errorf("interface lookups=%d, want 2 (loop must continue past stale keys)", lookups)
	}
	if got := netdev.ifaceSwDroppedCounter["eth0"]; got != 222 {
		t.Errorf("ifaceSwDroppedCounter[eth0]=%d, want 222", got)
	}
}
