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

package bpf

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
)

func TestProgRuntimeIntegration(t *testing.T) {
	if os.Getenv("BPF_PROG_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set BPF_PROG_RUNTIME_INTEGRATION=1 and run as root")
	}
	if err := Init(&Option{}); err != nil {
		t.Fatalf("initialize BPF manager: %v", err)
	}

	targetFunc := &btf.Func{
		Name: "profile_test_target",
		Type: &btf.FuncProto{
			Return: &btf.Int{Name: "int", Size: 4, Encoding: btf.Signed},
			Params: []btf.FuncParam{
				{
					Name: "ctx",
					Type: &btf.Pointer{Target: &btf.Struct{Name: "xdp_md"}},
				},
			},
		},
		Linkage: btf.GlobalFunc,
	}
	target, err := ebpf.NewProgram(&ebpf.ProgramSpec{
		Name: "profile_test_ta",
		Type: ebpf.XDP,
		Instructions: asm.Instructions{
			btf.WithFuncMetadata(asm.Mov.Imm(asm.R0, 2), targetFunc),
			asm.Return(),
		},
		License: "GPL",
	})
	if err != nil {
		t.Fatalf("load target program: %v", err)
	}
	defer target.Close()

	info, err := target.Info()
	if err != nil {
		t.Fatalf("get target info: %v", err)
	}
	programID, ok := info.ID()
	if !ok {
		t.Fatal("target program has no ID")
	}

	profile, err := newBPFProgRuntimeByID(uint32(programID), filepath.Join("..", "..", "bpf", progRuntimeObjectName))
	if err != nil {
		t.Fatalf("attach profiler: %v", err)
	}

	const runs = 5
	for i := 0; i < runs; i++ {
		if _, _, err := target.Test(make([]byte, 64)); err != nil {
			_ = profile.Close()
			t.Fatalf("run target program: %v", err)
		}
	}

	stats, err := profile.Read()
	if err != nil {
		_ = profile.Close()
		t.Fatalf("read profiler: %v", err)
	}
	if stats.RunCount != runs {
		_ = profile.Close()
		t.Fatalf("RunCount = %d, want %d", stats.RunCount, runs)
	}
	if stats.RunTimeNS == 0 {
		_ = profile.Close()
		t.Fatal("RunTimeNS = 0, want greater than zero")
	}

	if err := profile.Close(); err != nil {
		t.Fatalf("close profiler: %v", err)
	}
	if _, err := profile.Read(); !errors.Is(err, errProgRuntimeClosed) {
		t.Fatalf("Read() after Close error = %v, want errProgRuntimeClosed", err)
	}
	if err := profile.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestManagedProgRuntimeReloadIntegration(t *testing.T) {
	if os.Getenv("BPF_PROG_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set BPF_PROG_RUNTIME_INTEGRATION=1 and run as root")
	}
	if err := Init(&Option{}); err != nil {
		t.Fatalf("initialize BPF manager: %v", err)
	}

	originalObjectDir := DefaultObjDir
	DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() {
		DefaultObjDir = originalObjectDir
		// A failed attach leaves an active placeholder; drop it so the
		// shared manager can be reconfigured by later tests.
		progRuntime.Lock()
		progRuntime.active = map[*bpfProgRuntimeInstance]struct{}{}
		progRuntime.Unlock()
		if err := progRuntime.configure(false, false, nil); err != nil {
			t.Errorf("reset BPF program runtime profiler: %v", err)
		}
	})
	if err := progRuntime.configure(true, true, nil); err != nil {
		t.Fatalf("configure BPF program runtime profiler: %v", err)
	}

	first := loadManagedProgRuntimeTarget(t, "profile_reload_first")
	runManagedProgRuntimeTarget(t, first, 5)
	assertManagedProgRuntimeMetric(t, 5, true)
	if err := first.Close(); err != nil {
		t.Fatalf("close first target: %v", err)
	}
	assertManagedProgRuntimeMetric(t, 5, false)

	second := loadManagedProgRuntimeTarget(t, "profile_reload_second")
	runManagedProgRuntimeTarget(t, second, 3)
	assertManagedProgRuntimeMetric(t, 8, true)

	third := loadManagedProgRuntimeTarget(t, "profile_reload_third")
	runManagedProgRuntimeTarget(t, third, 2)
	assertManagedProgRuntimeMetric(t, 10, true)
	if err := second.Close(); err != nil {
		t.Fatalf("close second target: %v", err)
	}
	assertManagedProgRuntimeMetric(t, 10, true)

	runManagedProgRuntimeTarget(t, third, 1)
	assertManagedProgRuntimeMetric(t, 11, true)
	if err := third.Close(); err != nil {
		t.Fatalf("close third target: %v", err)
	}
	assertManagedProgRuntimeMetric(t, 11, false)

	if err := progRuntime.configure(true, true, nil); err != nil {
		t.Fatalf("reconfigure BPF program runtime profiler: %v", err)
	}
	DefaultObjDir = t.TempDir()
	failedProfile := loadManagedProgRuntimeTarget(t, "profile_attach_failure")
	if err := failedProfile.Close(); err != nil {
		t.Fatalf("close target after profiler attach failure: %v", err)
	}
	metrics := ReadBPFProgRuntimeData()
	if len(metrics) != 1 || metrics[0].Up || metrics[0].AttachFailures != 1 {
		t.Fatalf("failed attach metric = %+v, want up=false and attach_failures=1", metrics)
	}
}

func loadManagedProgRuntimeTarget(t *testing.T, name string) BPF {
	t.Helper()
	targetFunc := &btf.Func{
		Name: "profile_reload_target",
		Type: &btf.FuncProto{
			Return: &btf.Int{Name: "int", Size: 4, Encoding: btf.Signed},
			Params: []btf.FuncParam{{
				Name: "ctx",
				Type: &btf.Pointer{Target: &btf.Struct{Name: "xdp_md"}},
			}},
		},
		Linkage: btf.GlobalFunc,
	}
	spec := &ebpf.CollectionSpec{Programs: map[string]*ebpf.ProgramSpec{
		"profile_reload": {
			Name: "profile_reload",
			Type: ebpf.XDP,
			Instructions: asm.Instructions{
				btf.WithFuncMetadata(asm.Mov.Imm(asm.R0, 2), targetFunc),
				asm.Return(),
			},
			License: "GPL",
		},
	}}
	target, err := loadBPFFromCollectionSpec(name, spec, nil)
	if err != nil {
		t.Fatalf("load managed target %q: %v", name, err)
	}
	return target
}

func runManagedProgRuntimeTarget(t *testing.T, target BPF, runs int) {
	t.Helper()
	managed := target.(*defaultBPF)
	id := managed.ProgramIDByName("profile_reload")
	program := managed.programsByID[id].handle
	for i := 0; i < runs; i++ {
		if _, _, err := program.Test(make([]byte, 64)); err != nil {
			t.Fatalf("run managed target: %v", err)
		}
	}
}

func assertManagedProgRuntimeMetric(t *testing.T, runCount uint64, up bool) {
	t.Helper()
	metrics := ReadBPFProgRuntimeData()
	if len(metrics) != 1 {
		t.Fatalf("ReadBPFProgRuntimeData() returned %d metrics, want 1: %+v", len(metrics), metrics)
	}
	metric := metrics[0]
	if metric.ProgramName != "profile_reload" || metric.RunCount != runCount || metric.RunTimeNS == 0 || metric.Up != up {
		t.Fatalf("profile metric = %+v, want name=profile_reload run_count=%d runtime>0 up=%v", metric, runCount, up)
	}
}
