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

//go:build integration && !didi

package pod

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
)

const (
	cgroupSubsysIDEventMap     = "cgroup_subsys_id_events"
	cgroupSubsysIDProgram      = "cgroup_subsys_id_prog"
	cgroupSubsysIDEventSize    = 32
	cgroupSubsysIDEventTimeout = 10 * time.Second
)

type cgroupSubsysIDEvent struct {
	Cgroup         uint64
	SubsystemID    int32
	SubsystemCount uint32
	SubsystemName  [16]uint8
}

var (
	_ [cgroupSubsysIDEventSize]byte = [unsafe.Sizeof(cgroupSubsysIDEvent{})]byte{}
	_ [0]byte                       = [unsafe.Offsetof(cgroupSubsysIDEvent{}.Cgroup)]byte{}
	_ [8]byte                       = [unsafe.Offsetof(cgroupSubsysIDEvent{}.SubsystemID)]byte{}
	_ [12]byte                      = [unsafe.Offsetof(cgroupSubsysIDEvent{}.SubsystemCount)]byte{}
	_ [16]byte                      = [unsafe.Offsetof(cgroupSubsysIDEvent{}.SubsystemName)]byte{}
)

func TestCgroupSubsysIDIntegration(t *testing.T) {
	oraclePath := integrationTestEnv(t, "HUATUO_CGROUP_SUBSYS_ID_OBJECT")
	notifyPath := integrationTestEnv(t, "HUATUO_CGROUP_SUBSYS_ID_NOTIFY_PATH")
	symbol := integrationTestEnv(t, "HUATUO_CGROUP_SUBSYS_ID_SYMBOL")

	if err := bpf.Init(nil); err != nil {
		t.Fatalf("bpf.Init() error = %v", err)
	}
	t.Cleanup(bpf.Shutdown)

	previousBPFDir := bpf.DefaultObjDir
	previousIDs := cgroupCssID2SubSysNameMap
	t.Cleanup(func() {
		bpf.DefaultObjDir = previousBPFDir
		cgroupCssID2SubSysNameMap = previousIDs
	})

	if err := cgroupInitSubSysIDs(); err != nil {
		t.Fatalf("cgroupInitSubSysIDs() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), cgroupSubsysIDEventTimeout)
	t.Cleanup(cancel)

	bpf.DefaultObjDir = filepath.Dir(oraclePath)
	oracle, err := bpf.LoadBPF(filepath.Base(oraclePath), nil)
	if err != nil {
		t.Fatalf("load BPF object %q: %v", oraclePath, err)
	}
	t.Cleanup(func() {
		if err := oracle.Close(); err != nil {
			t.Errorf("close BPF object %q: %v", oraclePath, err)
		}
	})

	reader, err := oracle.EventPipeByName(ctx, cgroupSubsysIDEventMap, 8192)
	if err != nil {
		t.Fatalf("open BPF event map %q: %v", cgroupSubsysIDEventMap, err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close BPF event map %q: %v", cgroupSubsysIDEventMap, err)
		}
	})

	if err := oracle.AttachWithOptions([]bpf.AttachOption{{
		ProgramName: cgroupSubsysIDProgram,
		Symbol:      symbol,
	}}); err != nil {
		t.Fatalf("attach BPF program %q to %q: %v", cgroupSubsysIDProgram, symbol, err)
	}

	if _, err := os.ReadFile(notifyPath); err != nil {
		t.Fatalf("read cgroup notification file %q: %v", notifyPath, err)
	}

	kernelIDs := readKernelCgroupSubsysIDs(t, reader)
	assertCgroupSubsysIDs(t, kernelIDs)
}

func integrationTestEnv(t *testing.T, name string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Skipf("%s is not set; run through integration/test_cgroup_subsys_id.sh", name)
	}
	return value
}

func readKernelCgroupSubsysIDs(
	t *testing.T,
	reader bpf.PerfEventReader,
) map[int]string {
	t.Helper()

	var cgroup uint64
	var want uint32
	var ids map[int]string
	for {
		var event cgroupSubsysIDEvent
		if err := reader.ReadInto(&event); err != nil {
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				continue
			}
			t.Fatalf("read subsystem identity event: %v", err)
		}
		if event.SubsystemCount == 0 {
			t.Fatalf("cgroup %#x reported no subsystems", event.Cgroup)
		}
		if ids == nil {
			cgroup = event.Cgroup
			want = event.SubsystemCount
			ids = make(map[int]string, want)
		}
		if event.Cgroup != cgroup {
			continue
		}
		if event.SubsystemCount != want {
			t.Fatalf(
				"cgroup %#x subsystem count changed from %d to %d",
				cgroup,
				want,
				event.SubsystemCount,
			)
		}

		id := int(event.SubsystemID)
		ids[id] = normalizeKernelCgroupSubsysName(cString(event.SubsystemName[:]))
		if uint32(len(ids)) == want {
			return ids
		}
	}
}

func assertCgroupSubsysIDs(
	t *testing.T,
	kernelIDs map[int]string,
) {
	t.Helper()

	for id, kernelName := range kernelIDs {
		btfName, ok := cgroupCssID2SubSysNameMap[id]
		if !ok {
			t.Errorf("BTF mapping has no subsystem ID %d (%q)", id, kernelName)
			continue
		}
		if btfName != kernelName {
			t.Errorf(
				"subsystem ID %d BTF name = %q, kernel name = %q",
				id,
				btfName,
				kernelName,
			)
		}
	}
}

func normalizeKernelCgroupSubsysName(name string) string {
	if name == "io" {
		return subsystem.SubsystemBlkIO
	}
	return name
}

func cString(value []byte) string {
	if index := bytes.IndexByte(value, 0); index >= 0 {
		return string(value[:index])
	}
	return string(value)
}
