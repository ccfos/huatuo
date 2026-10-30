// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

// setCgroupRootfs points the cgroup rootfs at a temp dir containing an empty
// memory subsystem directory, and restores the original on cleanup.
func setCgroupRootfs(t *testing.T) string {
	t.Helper()

	orig := paths.RootfsDefaultPath
	t.Cleanup(func() { paths.RootfsDefaultPath = orig })

	paths.RootfsDefaultPath = t.TempDir()

	memDir := filepath.Join(paths.RootfsDefaultPath, subsystem.SubsystemMemory)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("create memory cgroup dir: %v", err)
	}

	return memDir
}

func TestNewMemOthersCollectorNotSupported(t *testing.T) {
	setCgroupRootfs(t)

	// No Didi memcg extension files: the collector must opt out with
	// ErrNotSupported so the framework marks it inactive.
	if _, err := newMemOthersCollector(); !errors.Is(err, types.ErrNotSupported) {
		t.Fatalf("newMemOthersCollector() error = %v, want ErrNotSupported", err)
	}
}

func TestNewMemOthersCollectorSupported(t *testing.T) {
	memDir := setCgroupRootfs(t)

	stat := filepath.Join(memDir, "memory.directstall_stat")
	if err := os.WriteFile(stat, []byte("directstall_time 0\n"), 0o600); err != nil {
		t.Fatalf("create %s: %v", stat, err)
	}

	attr, err := newMemOthersCollector()
	if err != nil {
		t.Fatalf("newMemOthersCollector() error = %v, want nil", err)
	}
	if attr == nil || attr.Flag&tracing.FlagMetric == 0 {
		t.Fatalf("newMemOthersCollector() attr = %+v, want metric collector", attr)
	}
}

// Decode mixed snapshots without letting a bad row discard valid siblings.
func TestMemoryPerCPUSnapshots(t *testing.T) {
	words := func(values ...uint64) []byte {
		out := make([]byte, 8*len(values))
		for i, value := range values {
			binary.LittleEndian.PutUint64(out[8*i:], value)
		}
		return out
	}
	container := &pod.Container{Name: "workload", Labels: map[string]any{"HostNamespace": "test"}}
	for _, kind := range []string{"stall", "reclaim"} {
		t.Run(kind, func(t *testing.T) {
			decode := memcgReclaimMetrics
			host, sibling := words(2, 3), words(1, 2)
			expected := map[string]float64{"directstall": 5, "container_directstall": 3}
			if kind == "stall" {
				decode = containerMemoryStalls
				host, sibling = words(1000000, 2000000, 3000000, 4000000), words(1000000, 2000000, 0, 1000000)
				expected = map[string]float64{"compaction_stall": 4, "allocpages_stall": 6, "container_compaction_stall": 1, "container_allocpages_stall": 3}
			}
			valid := []bpf.MapItem{{Key: words(0), Value: host}, {Key: words(7), Value: sibling}, {Key: words(9), Value: sibling}}
			containers := map[uint64]*pod.Container{7: container}
			assert := func(items []bpf.MapItem, metadata map[uint64]*pod.Container, want map[string]float64, wantErr bool) {
				t.Helper()
				data, err := decode(items, metadata)
				if (err != nil) != wantErr {
					t.Fatalf("error=%v, want error=%v", err, wantErr)
				}
				got := map[string]float64{}
				for _, d := range data {
					if d.Type() != metric.MetricTypeGauge {
						t.Fatalf("type=%v", d.Type())
					}
					if _, duplicate := got[d.Name()]; duplicate {
						t.Fatalf("duplicate %s", d.Name())
					}
					got[d.Name()] = d.Value
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("metrics=%v, want %v", got, want)
				}
			}
			assert(valid, containers, expected, false)
			for _, bad := range []bpf.MapItem{
				{Key: []byte{1}, Value: host},
				{Key: words(8), Value: nil},
				{Key: words(8), Value: []byte{1}},
			} {
				// Stall intentionally ignores unknown CSS before decoding its value.
				metadata := map[uint64]*pod.Container{7: container, 8: container}
				assert(append(append([]bpf.MapItem{}, valid...), bad), metadata, expected, true)
			}
			hostOnly := map[string]float64{}
			for name, value := range expected {
				if !strings.HasPrefix(name, "container_") {
					hostOnly[name] = value
				}
			}
			assert(valid, nil, hostOnly, false)
			data, err := decode(valid[1:], containers)
			if err == nil || !strings.Contains(err.Error(), "reserved host") || len(data) == 0 {
				t.Fatalf("missing host: data=%v error=%v", data, err)
			}
			_, err = decode([]bpf.MapItem{{Key: words(0), Value: nil}}, nil)
			if err == nil || !strings.Contains(err.Error(), "reserved host") {
				t.Fatalf("invalid host error=%v", err)
			}
		})
	}
}
