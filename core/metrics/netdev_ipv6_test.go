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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

func setupIPv6Proc(t testing.TB, pid int, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "proc", fmt.Sprint(pid), "net", "dev_snmp6")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(old) })
	procfs.RootPrefix(root)
	return dir
}

func TestNetdevIPv6HostAndDeviceFilters(t *testing.T) {
	setupIPv6Proc(t, 1, map[string]string{"eth0": "ifIndex 2\nIp6InDiscards 7\nIcmp6InErrors 0\n", "lo": "ifIndex 1\nIp6InDiscards 3\n"})
	filter, err := matcher.NewValueMatcher("eth0", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (&netdevIPv6Collector{}).namespaceStats(nil, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 {
		t.Fatalf("metrics=%d want 2", len(data))
	}
	values := map[string]float64{}
	for _, d := range data {
		if d.Type() != metric.MetricTypeCounter || d.Labels()["device"] != "eth0" {
			t.Fatalf("unexpected metric %+v", d)
		}
		values[d.Name()] = d.Value
	}
	if values["Ip6InDiscards_total"] != 7 || values["Icmp6InErrors_total"] != 0 {
		t.Fatalf("values=%v", values)
	}
	filter, err = matcher.NewValueMatcher("eth0", "eth0")
	if err != nil {
		t.Fatal(err)
	}
	data, err = (&netdevIPv6Collector{}).namespaceStats(nil, filter)
	if err != nil || len(data) != 0 {
		t.Fatalf("excluded device: %v %v", data, err)
	}
}

func TestNetdevIPv6ContainerNamespace(t *testing.T) {
	setupIPv6Proc(t, 42, map[string]string{"eth0": "ifIndex 2\nIp6OutNoRoutes 9\n"})
	filter, err := matcher.NewValueMatcher("", "")
	if err != nil {
		t.Fatal(err)
	}
	container := &pod.Container{ID: "container-test", InitPid: 42, Hostname: "pod-test", Name: "app", Labels: map[string]any{"HostNamespace": "test-ns"}}
	data, err := (&netdevIPv6Collector{}).namespaceStats(container, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0].Name() != "container_Ip6OutNoRoutes_total" || data[0].Value != 9 {
		t.Fatalf("container metrics=%v", data)
	}
	labels := data[0].Labels()
	if labels[metric.LabelContainerHost] != "pod-test" || labels[metric.LabelContainerHostNamespace] != "test-ns" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestNetdevIPv6UnavailableAndMalformed(t *testing.T) {
	dir := setupIPv6Proc(t, 1, map[string]string{})
	if _, err := newNetdevIPv6Collector(); err != nil {
		t.Fatalf("empty supported directory: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := newNetdevIPv6Collector(); !errors.Is(err, types.ErrNotSupported) {
		t.Fatalf("unsupported=%v", err)
	}
	filter, err := matcher.NewValueMatcher("", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (&netdevIPv6Collector{}).namespaceStats(nil, filter)
	if err != nil || len(data) != 0 {
		t.Fatalf("IPv6 disabled: %v %v", data, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "eth0"), []byte("Ip6InDiscards invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&netdevIPv6Collector{}).namespaceStats(nil, filter); err == nil {
		t.Fatal("invalid counter was silently accepted")
	}
}

func BenchmarkNetdevIPv6Namespace(b *testing.B) {
	files := map[string]string{}
	for i := range 32 {
		files[fmt.Sprintf("eth%d", i)] = "ifIndex 2\nIp6InReceives 123\nIp6InDiscards 7\nIcmp6InErrors 0\n"
	}
	setupIPv6Proc(b, 1, files)
	filter, err := matcher.NewValueMatcher("", "")
	if err != nil {
		b.Fatal(err)
	}
	c := &netdevIPv6Collector{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.namespaceStats(nil, filter); err != nil {
			b.Fatal(err)
		}
	}
}
