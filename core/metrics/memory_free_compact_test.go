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
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"

	"github.com/cilium/ebpf"
)

func memoryStallItem(css, compact, reclaim uint64) bpf.MapItem {
	key, value := make([]byte, 8), make([]byte, 16)
	binary.LittleEndian.PutUint64(key, css)
	binary.LittleEndian.PutUint64(value, compact)
	binary.LittleEndian.PutUint64(value[8:], reclaim)
	return bpf.MapItem{Key: key, Value: value}
}

type memoryStallBPF struct {
	bpf.BPF
	hostOnly  bool
	attachErr error
	closed    bool
	opts      []bpf.AttachOption
}

func (b *memoryStallBPF) ProgramIDByName(string) uint32 {
	if b.hostOnly {
		return 0
	}
	return 1
}

func (b *memoryStallBPF) AttachWithOptions(opts []bpf.AttachOption) error {
	b.opts = opts
	return b.attachErr
}

func (b *memoryStallBPF) Close() error { b.closed = true; return nil }

func TestMemoryStallFallback(t *testing.T) {
	full := &memoryStallBPF{attachErr: errors.New("unsupported container feature")}
	host := &memoryStallBPF{hostOnly: true}
	obj, err := loadMemoryStalls("test", func(_ string, containers bool) (bpf.BPF, error) {
		if containers {
			return full, nil
		}
		return host, nil
	})
	if err != nil || obj != host || !full.closed || len(host.opts) != 4 {
		t.Fatalf("host fallback: object=%v err=%v", obj, err)
	}
	c := reclaimCompact{}
	data, err := c.update(obj, func() (map[string]*pod.Container, error) {
		t.Fatal("host fallback must skip container discovery")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d host metrics, want 2", len(data))
	}
	for _, m := range data {
		if (m.Name() != "compaction_stall" || m.Value != 2) &&
			(m.Name() != "allocpages_stall" || m.Value != 3) {
			t.Fatalf("unexpected host metric %s=%v", m.Name(), m.Value)
		}
	}
}

func (b *memoryStallBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name == "mm_free_compact_map" {
		return []bpf.MapItem{memoryStallItem(0, 2_000_000, 3_000_000)}, nil
	}
	return nil, nil
}

// HUATUO_MEMORY_STALL_BPF_OBJECT selects a freshly compiled object, avoiding
// accidental verification of stale generated artifacts.
func TestMemoryStallBPFLayout(t *testing.T) {
	object := os.Getenv("HUATUO_MEMORY_STALL_BPF_OBJECT")
	if object == "" {
		t.Skip("set HUATUO_MEMORY_STALL_BPF_OBJECT to a freshly built object")
	}
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name            string
		kind            ebpf.MapType
		key, value, max uint32
	}{
		{"mm_free_compact_map", ebpf.Array, 4, 16, 1},
		{"mm_container_free_compact_map", ebpf.LRUHash, 8, 16, 10240},
		{"mm_stall_start", ebpf.LRUHash, 16, 24, 10240},
	} {
		m := spec.Maps[tt.name]
		if m == nil || m.Type != tt.kind || m.KeySize != tt.key || m.ValueSize != tt.value || m.MaxEntries != tt.max {
			t.Fatalf("unexpected map %s: %+v", tt.name, m)
		}
	}
	if len(spec.Programs) != 5 {
		t.Fatalf("got %d programs, want 5", len(spec.Programs))
	}
	for _, enabled := range []uint32{0, 1} {
		if err := spec.Copy().RewriteConstants(map[string]any{"enable_container_stalls": enabled}); err != nil {
			t.Fatalf("rewrite container mode %d: %v", enabled, err)
		}
	}
	if os.Getenv("HUATUO_BPF_INTEGRATION") != "1" {
		return
	}
	// Exercise both production load modes, not just the default BPF object.
	previousDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Dir(object)
	t.Cleanup(func() { bpf.DefaultObjDir = previousDir })
	for _, mode := range []struct {
		name       string
		containers bool
	}{{"host", false}, {"containers", true}} {
		t.Run(mode.name, func(t *testing.T) {
			obj, err := loadMemoryStallObject(filepath.Base(object), mode.containers)
			if err != nil {
				t.Fatal(err)
			}
			defer obj.Close()
			if err := obj.AttachWithOptions(memoryStallAttachOptions(mode.containers)); err != nil {
				t.Fatal(err)
			}
			c := reclaimCompact{}
			data, err := c.update(obj, func() (map[string]*pod.Container, error) {
				if !mode.containers {
					t.Fatal("host fallback must not discover containers")
				}
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != 2 {
				t.Fatalf("got %d host metrics, want 2", len(data))
			}
		})
	}
}
