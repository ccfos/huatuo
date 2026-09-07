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

func TestContainerMemoryStallsConversion(t *testing.T) {
	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, 42)
	value := make([]byte, 16)
	binary.LittleEndian.PutUint64(value, 2000000)
	binary.LittleEndian.PutUint64(value[8:], 3000000)
	// A bad item must report an error without discarding another valid target.
	items := []bpf.MapItem{{Key: []byte{1}, Value: value}, {Key: key, Value: value}, {Key: make([]byte, 8), Value: value}}
	data, err := containerMemoryStalls(items, map[uint64]*pod.Container{42: {Name: "stall-fixture", Labels: map[string]any{"HostNamespace": "default"}}})
	if err == nil || len(data) != 2 {
		t.Fatalf("data=%v error=%v", data, err)
	}
	for i, name := range []string{"container_compaction_stall", "container_allocpages_stall"} {
		if data[i].Name() != name || data[i].Value != float64(i+2) || data[i].Type() != metric.MetricTypeGauge || data[i].Labels()[metric.LabelContainerName] != "stall-fixture" {
			t.Fatalf("invalid container stall: name=%s value=%v labels=%v", data[i].Name(), data[i].Value, data[i].Labels())
		}
	}
	data, err = containerMemoryStalls(items[1:], nil)
	if err != nil || len(data) != 0 {
		t.Fatalf("unknown targets exported: data=%v err=%v", data, err)
	}
}

type memoryStallBPFStub struct {
	bpf.BPF
	containerErr error
}

func (s *memoryStallBPFStub) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name == "mm_container_free_compact_map" {
		return nil, s.containerErr
	}
	value := make([]byte, 16)
	binary.LittleEndian.PutUint64(value, 10000000)
	binary.LittleEndian.PutUint64(value[8:], 20000000)
	return []bpf.MapItem{{Value: value}}, nil
}

func TestMemoryStallsPreserveHostOnContainerFailure(t *testing.T) {
	failure := errors.New("container unavailable")
	for _, stage := range []string{"discovery", "map"} {
		t.Run(stage, func(t *testing.T) {
			obj := &memoryStallObject{BPF: &memoryStallBPFStub{containerErr: failure}, containers: true}
			data, err := (&reclaimCompact{}).update(obj, func() (map[string]*pod.Container, error) {
				if stage == "discovery" {
					return nil, failure
				}
				return map[string]*pod.Container{"fixture": {Name: "fixture"}}, nil
			})
			if !errors.Is(err, failure) || len(data) != 2 || data[0].Value != 10 || data[1].Value != 20 {
				t.Fatalf("host output lost on %s failure: data=%v err=%v", stage, data, err)
			}
		})
	}
}
