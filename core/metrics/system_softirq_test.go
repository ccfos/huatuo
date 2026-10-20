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
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
)

type softirqTestBPF struct {
	bpf.BPF
	items []bpf.MapItem
}

func (b *softirqTestBPF) Close() error                                { return nil }
func (b *softirqTestBPF) DumpMapByName(string) ([]bpf.MapItem, error) { return b.items, nil }

func TestSoftirqRefreshesOnlineCPUs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online")
	collector := &softirqLatency{cpuPossible: 8, onlineCPUPath: path}
	values := make([]softirqLatencyData, 8)
	for i := range values {
		values[i].LatencyCounts = [4]uint64{uint64(i + 1), 2, 3, 4}
	}
	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.LittleEndian, values); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 4)
	binary.LittleEndian.PutUint32(key, softirqNetRx)
	object := &softirqTestBPF{items: []bpf.MapItem{{Key: key, Value: encoded.Bytes()}}}
	if err := collector.bpf.Publish(object); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := collector.bpf.UnPublish(); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		online string
		cpus   []int
	}{
		{"0-1\n", []int{0, 1}},
		{"0-1,4-7\n", []int{0, 1, 4, 5, 6, 7}},
		{"0,7\n", []int{0, 7}},
	} {
		if err := os.WriteFile(path, []byte(tc.online), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := collector.Update()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != len(tc.cpus)*4 {
			t.Fatalf("online %q: metrics = %d, want %d", tc.online, len(data), len(tc.cpus)*4)
		}
		for i, cpu := range tc.cpus {
			for zone := range 4 {
				sample := data[i*4+zone]
				labels := sample.Labels()
				if labels["cpuid"] != strconv.Itoa(cpu) || labels["zone"] != strconv.Itoa(zone) || labels["type"] != "NET_RX" {
					t.Fatalf("online %q: labels = %v", tc.online, labels)
				}
				if sample.Value != float64(values[cpu].LatencyCounts[zone]) {
					t.Fatalf("CPU %d zone %d value = %v", cpu, zone, sample.Value)
				}
			}
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Update(); err == nil {
		t.Fatal("missing online topology did not fail scrape")
	}
}
