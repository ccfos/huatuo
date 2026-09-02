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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
)

const (
	sockstatFixture = "sockets: used 7\nTCP: inuse 1 orphan 0 tw 0 alloc 1 mem 1\n"
	netdevFixture   = "Inter-|   Receive                            |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"  eth0: 1000    1    0    0    0     0          0         0     2000       2    0    0    0     0       0          0\n"
)

// Redirect procfs so host and container failure boundaries are deterministic.
func withFixtureProcfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return root
}

func writeProcNetFile(t *testing.T, root string, pid int, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "proc", strconv.Itoa(pid), "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error=%v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", path, err)
	}
}

func findSeries(data []*metric.Data, name string, value float64) bool {
	for _, d := range data {
		if d.Name() == name && d.Value == value {
			return true
		}
	}
	return false
}

// A container with a broken /proc/<pid> file must be skipped, not abort the
// whole scrape: the healthy container's metrics must still be exported.
func TestSockstatCollectorSkipsBrokenContainer(t *testing.T) {
	root := withFixtureProcfs(t)
	writeProcNetFile(t, root, 1, "sockstat", sockstatFixture)
	writeProcNetFile(t, root, 1111, "sockstat", sockstatFixture)
	writeProcNetFile(t, root, 2222, "sockstat", "garbage\n")

	containers := map[string]*pod.Container{
		"a": {Name: "healthy", InitPid: 1111, Labels: map[string]any{"HostNamespace": "default"}},
		"b": {Name: "broken", InitPid: 2222, Labels: map[string]any{"HostNamespace": "default"}},
	}

	collector := &sockstatCollector{}
	data, err := collector.collect(containers)
	if err != nil {
		t.Fatalf("collect() error=%v, want broken container skipped", err)
	}
	if !findSeries(data, "container_sockets_used", 7) {
		t.Errorf("healthy container's sockets_used=7 missing from %d series", len(data))
	}
	if !findSeries(data, "sockets_used", 7) {
		t.Errorf("host sockets_used=7 missing from %d series", len(data))
	}
}

func TestSockstatCollectorReturnsHostFailure(t *testing.T) {
	root := withFixtureProcfs(t)
	writeProcNetFile(t, root, 1, "sockstat", "garbage\n")
	writeProcNetFile(t, root, 1111, "sockstat", sockstatFixture)

	collector := &sockstatCollector{}
	data, err := collector.collect(map[string]*pod.Container{
		"a": {Name: "healthy", InitPid: 1111, Labels: map[string]any{"HostNamespace": "default"}},
	})
	if err == nil || !strings.Contains(err.Error(), "host sockstat") {
		t.Fatalf("collect() error=%v, want host sockstat error", err)
	}
	if !findSeries(data, "container_sockets_used", 7) {
		t.Errorf("partial container sockets_used=7 missing from %d series", len(data))
	}
}

// netdev_stats must keep the healthy container's series when another
// container's /proc/<pid>/net/dev is unreadable.
func TestNetdevCollectorSkipsBrokenContainer(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.NetdevStats.DeviceIncluded = "eth0"
	Set(testConfig)

	root := withFixtureProcfs(t)
	writeProcNetFile(t, root, 1, "dev", netdevFixture)
	writeProcNetFile(t, root, 1111, "dev", netdevFixture)

	containers := map[string]*pod.Container{
		"a": {Name: "healthy", InitPid: 1111, Labels: map[string]any{"HostNamespace": "default"}},
		"b": {Name: "broken", InitPid: 2222, Labels: map[string]any{"HostNamespace": "default"}},
	}

	collector := &netdevCollector{}
	if _, err := collector.getStats(containers["b"], nil, false); err == nil {
		t.Fatal("getStats() error=nil for broken container PID 2222")
	}
	data, err := collector.collect(containers)
	if err != nil {
		t.Fatalf("collect() error=%v, want broken container skipped", err)
	}
	if !findSeries(data, "container_receive_bytes_total", 1000) {
		t.Errorf("healthy container's receive_bytes_total=1000 missing from %d series", len(data))
	}
	if !findSeries(data, "receive_bytes_total", 1000) {
		t.Errorf("host receive_bytes_total=1000 missing from %d series", len(data))
	}
}

func TestNetdevCollectorReturnsInvalidFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.NetdevStats.DeviceIncluded = "["
	Set(testConfig)

	collector := &netdevCollector{}
	data, err := collector.collect(nil)
	if err == nil || !strings.Contains(err.Error(), "netdev device filter") {
		t.Fatalf("collect() error=%v, want netdev device filter error", err)
	}
	if data != nil {
		t.Fatalf("collect() data=%v, want nil for invalid filter", data)
	}
}

func TestNetdevCollectorReturnsHostFailure(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.NetdevStats.DeviceIncluded = "eth0"
	Set(testConfig)

	root := withFixtureProcfs(t)
	writeProcNetFile(t, root, 1111, "dev", netdevFixture)

	collector := &netdevCollector{}
	data, err := collector.collect(map[string]*pod.Container{
		"a": {Name: "healthy", InitPid: 1111, Labels: map[string]any{"HostNamespace": "default"}},
	})
	if err == nil || !strings.Contains(err.Error(), "host netdev") {
		t.Fatalf("collect() error=%v, want host netdev error", err)
	}
	if !findSeries(data, "container_receive_bytes_total", 1000) {
		t.Errorf("partial container receive_bytes_total=1000 missing from %d series", len(data))
	}
}

type fakeMemEventsCgroup struct {
	cgroups.Cgroup
	byPath map[string]map[string]uint64
}

func (c *fakeMemEventsCgroup) MemoryEventRaw(path string) (map[string]uint64, error) {
	raw, ok := c.byPath[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}

// memory_events must keep the healthy container's events when another
// container's cgroup has disappeared.
func TestMemoryEventsSkipsBrokenContainer(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	Set(&Config{})

	containers := map[string]*pod.Container{
		"a": {Name: "healthy", CgroupPath: "cgrp-a", Labels: map[string]any{"HostNamespace": "default"}},
		"b": {Name: "broken", CgroupPath: "cgrp-b", Labels: map[string]any{"HostNamespace": "default"}},
	}

	fake := &fakeMemEventsCgroup{byPath: map[string]map[string]uint64{
		"cgrp-a": {"oom_kill": 2},
	}}
	collector := &memEventsCollector{cgroup: fake}

	data, err := collector.collect(containers)
	if err != nil {
		t.Fatalf("collect() error=%v, want broken container skipped", err)
	}
	if !findSeries(data, "container_oom_kill", 2) {
		t.Errorf("healthy container's oom_kill=2 missing from %d series", len(data))
	}
}

func TestMemoryEventsReturnsInvalidFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.MemoryEvents.Included = "["
	Set(testConfig)

	collector := &memEventsCollector{}
	data, err := collector.collect(nil)
	if err == nil || !strings.Contains(err.Error(), "memory events filter") {
		t.Fatalf("collect() error=%v, want memory events filter error", err)
	}
	if data != nil {
		t.Fatalf("collect() data=%v, want nil for invalid filter", data)
	}
}
