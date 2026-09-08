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
	containerErr   error
	containerItems []bpf.MapItem
}

func (s *memoryStallBPFStub) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name == "mm_container_free_compact_map" {
		return s.containerItems, s.containerErr
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
			}, paths.KernfsID)
			if !errors.Is(err, failure) || len(data) != 2 || data[0].Value != 10 || data[1].Value != 20 {
				t.Fatalf("host output lost on %s failure: data=%v err=%v", stage, data, err)
			}
		})
	}
}

func TestMemoryStallsPreserveSiblingOnResolverFailure(t *testing.T) {
	for _, failure := range []error{os.ErrNotExist, os.ErrPermission} {
		t.Run(failure.Error(), func(t *testing.T) {
			item := func(id uint64) bpf.MapItem {
				key, value := make([]byte, 8), make([]byte, 16)
				binary.LittleEndian.PutUint64(key, id)
				binary.LittleEndian.PutUint64(value, 2000000)
				binary.LittleEndian.PutUint64(value[8:], 3000000)
				return bpf.MapItem{Key: key, Value: value}
			}
			obj := &memoryStallObject{BPF: &memoryStallBPFStub{containerItems: []bpf.MapItem{item(42), item(43)}}, containers: true}
			calls := make(map[string]int)
			data, err := (&reclaimCompact{}).update(obj, func() (map[string]*pod.Container, error) {
				return map[string]*pod.Container{
					"valid":   {Name: "valid", CgroupPath: "/valid", Labels: map[string]any{"HostNamespace": "default"}},
					"invalid": {Name: "invalid", CgroupPath: "/invalid", Labels: map[string]any{"HostNamespace": "default"}},
				}, nil
			}, func(path string) (uint64, error) {
				target := filepath.Base(path)
				calls[target]++
				if target == "invalid" {
					return 0, failure
				}
				if target != "valid" {
					t.Fatalf("unexpected resolver path %q", path)
				}
				return 42, nil
			})
			if errors.Is(failure, os.ErrNotExist) {
				if err != nil {
					t.Fatalf("disappearance returned error: %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("resolver error=%v, want %v", err, failure)
			}
			if len(calls) != 2 || calls["valid"] != 1 || calls["invalid"] != 1 {
				t.Fatalf("resolver calls=%v", calls)
			}
			if len(data) != 4 {
				t.Fatalf("host/sibling output lost or invalid target exported: data=%v", data)
			}
			for i, name := range []string{"compaction_stall", "allocpages_stall", "container_compaction_stall", "container_allocpages_stall"} {
				want := []float64{10, 20, 2, 3}[i]
				if data[i].Name() != name || data[i].Value != want {
					t.Fatalf("metric %d: name=%s value=%v", i, data[i].Name(), data[i].Value)
				}
				if i >= 2 && data[i].Labels()[metric.LabelContainerName] != "valid" {
					t.Fatalf("invalid sibling labels: %v", data[i].Labels())
				}
			}
		})
	}
}

type memoryStallAttachStub struct {
	bpf.BPF
	attachErr, closeErr error
	closed              int
}

func (s *memoryStallAttachStub) AttachWithOptions([]bpf.AttachOption) error { return s.attachErr }
func (s *memoryStallAttachStub) Close() error                               { s.closed++; return s.closeErr }
func TestMemoryStallsHostFallback(t *testing.T) {
	for _, stage := range []string{"load", "attach", "host", "close"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("container unavailable")
			hostFailure := errors.New("host unavailable")
			closeFailure := errors.New("close failed")
			first, last := &memoryStallAttachStub{}, &memoryStallAttachStub{}
			calls := []bool{}
			obj, err := loadMemoryStalls("fixture", func(_ string, containers bool) (bpf.BPF, error) {
				calls = append(calls, containers)
				if !containers {
					if stage == "host" {
						return nil, hostFailure
					}
					return last, nil
				}
				if stage == "load" || stage == "host" {
					return nil, failure
				}
				first.attachErr = failure
				if stage == "close" {
					first.closeErr = closeFailure
				}
				return first, nil
			})
			if stage == "host" || stage == "close" {
				if obj != nil || !errors.Is(err, failure) {
					t.Fatalf("obj=%v err=%v", obj, err)
				}
				expected := hostFailure
				if stage == "close" {
					expected = closeFailure
				}
				if !errors.Is(err, expected) {
					t.Fatalf("missing error %v: %v", expected, err)
				}
			} else if err != nil || obj == nil || obj.containers || obj.BPF != last {
				t.Fatalf("fallback obj=%v err=%v", obj, err)
			}
			want := 2
			if stage == "close" {
				want = 1
			}
			if len(calls) != want || !calls[0] || (want == 2 && calls[1]) {
				t.Fatalf("load modes=%v", calls)
			}
			if stage == "attach" || stage == "close" {
				if first.closed != 1 {
					t.Fatal("failed object not closed")
				}
			}
		})
	}
}
