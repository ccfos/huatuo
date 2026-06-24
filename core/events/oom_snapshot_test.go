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

package events

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
)

type fakeOOMCgroupReader struct {
	usage  map[string]*stats.MemoryUsage
	stat   map[string]map[string]uint64
	events map[string]map[string]uint64
}

func (f *fakeOOMCgroupReader) MemoryUsage(path string) (*stats.MemoryUsage, error) {
	return f.usage[path], nil
}

func (f *fakeOOMCgroupReader) MemoryStatRaw(path string) (map[string]uint64, error) {
	return f.stat[path], nil
}

func (f *fakeOOMCgroupReader) MemoryEventRaw(path string) (map[string]uint64, error) {
	return f.events[path], nil
}

func TestReadOOMProcessMemory(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status")
	writeFile(t, statusPath, `Name:	postgres
Pid:	4321
VmRSS:	4096 kB
RssAnon:	2048 kB
RssFile:	1024 kB
RssShmem:	512 kB
VmSwap:	128 kB
`)

	got, err := readOOMProcessMemory(statusPath, 1)
	if err != nil {
		t.Fatalf("readOOMProcessMemory() returned error: %v", err)
	}

	if got.PID != 4321 {
		t.Errorf("PID = %d, want 4321", got.PID)
	}
	if got.ProcessName != "postgres" {
		t.Errorf("ProcessName = %q, want postgres", got.ProcessName)
	}
	if got.RssAnon != 2048*1024 {
		t.Errorf("RssAnon = %d, want %d", got.RssAnon, 2048*1024)
	}
	wantTotal := uint64(2048+1024+512+128) * 1024
	if got.Total != wantTotal {
		t.Errorf("Total = %d, want %d", got.Total, wantTotal)
	}
}

func TestTopOOMMemoryProcessesSortsAndLimits(t *testing.T) {
	procRoot := filepath.Join(t.TempDir(), "proc")
	writeProcStatus(t, procRoot, 100, "small", 512, 128, 0, 0)
	writeProcStatus(t, procRoot, 200, "large", 4096, 2048, 512, 0)
	writeProcStatus(t, procRoot, 300, "swap-heavy", 1024, 256, 0, 8192)
	writeFile(t, filepath.Join(procRoot, "not-a-pid", "status"), "")

	got, err := topOOMMemoryProcesses(procRoot, 2)
	if err != nil {
		t.Fatalf("topOOMMemoryProcesses() returned error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("len(top processes) = %d, want 2", len(got))
	}
	if got[0].PID != 300 {
		t.Errorf("first PID = %d, want 300", got[0].PID)
	}
	if got[1].PID != 200 {
		t.Errorf("second PID = %d, want 200", got[1].PID)
	}
}

func TestNewOOMTracingDataIncludesMemorySnapshot(t *testing.T) {
	root := t.TempDir()
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix("") })

	procRoot := filepath.Join(root, "proc")
	writeFile(t, filepath.Join(procRoot, "meminfo"), `MemTotal:       100000 kB
MemAvailable:   64000 kB
Cached:         20000 kB
Slab:            4096 kB
`)
	writeProcStatus(t, procRoot, 101, "trigger", 2048, 1024, 0, 0)
	writeProcStatus(t, procRoot, 202, "victim", 4096, 2048, 0, 512)

	containers := map[string]*pod.Container{
		"trigger-container": {
			ID:         "trigger-container",
			Hostname:   "trigger-host",
			CgroupPath: "kubepods.slice/trigger",
			CgroupCss: map[string]uint64{
				pod.SubSysMemory: 0x100,
			},
		},
		"victim-container": {
			ID:         "victim-container",
			Hostname:   "victim-host",
			CgroupPath: "kubepods.slice/victim",
			CgroupCss: map[string]uint64{
				pod.SubSysMemory: 0x200,
			},
		},
	}

	cgroup := &fakeOOMCgroupReader{
		usage: map[string]*stats.MemoryUsage{
			"kubepods.slice/trigger": {Usage: 128, MaxLimited: 1024},
			"kubepods.slice/victim":  {Usage: 512, MaxLimited: 2048},
		},
		stat: map[string]map[string]uint64{
			"kubepods.slice/trigger": {"anon": 64, "file": 32},
			"kubepods.slice/victim":  {"anon": 256, "file": 128},
		},
		events: map[string]map[string]uint64{
			"kubepods.slice/trigger": {"oom": 1},
			"kubepods.slice/victim":  {"oom_kill": 1},
		},
	}

	data := perfEventData{
		TriggerProcessName: taskComm("trigger"),
		VictimProcessName:  taskComm("victim"),
		TriggerPid:         101,
		VictimPid:          202,
		TriggerMemcgCSS:    0x100,
		VictimMemcgCSS:     0x200,
		MemLimitPages:      10,
		MemUsagePages:      7,
	}

	got := newOOMTracingData(data, containers, 4096, cgroup)
	if got.TriggerContainerHostname != "trigger-host" {
		t.Errorf("TriggerContainerHostname = %q, want trigger-host", got.TriggerContainerHostname)
	}
	if got.VictimContainerHostname != "victim-host" {
		t.Errorf("VictimContainerHostname = %q, want victim-host", got.VictimContainerHostname)
	}
	if got.CgroupMemoryLimit != 10*4096 {
		t.Errorf("CgroupMemoryLimit = %d, want %d", got.CgroupMemoryLimit, 10*4096)
	}
	if got.MemorySnapshot == nil {
		t.Fatal("MemorySnapshot = nil")
	}
	if got.MemorySnapshot.HostMemInfo["MemAvailable"] != 64000*1024 {
		t.Errorf("MemAvailable = %d, want %d", got.MemorySnapshot.HostMemInfo["MemAvailable"], 64000*1024)
	}
	if len(got.MemorySnapshot.TopProcesses) != 2 {
		t.Fatalf("len(TopProcesses) = %d, want 2", len(got.MemorySnapshot.TopProcesses))
	}
	if got.MemorySnapshot.TopProcesses[0].PID != 202 {
		t.Errorf("top process PID = %d, want 202", got.MemorySnapshot.TopProcesses[0].PID)
	}
	if got.MemorySnapshot.TriggerCgroup.Current != 128 {
		t.Errorf("trigger current = %d, want 128", got.MemorySnapshot.TriggerCgroup.Current)
	}
	if got.MemorySnapshot.VictimCgroup.Stat["anon"] != 256 {
		t.Errorf("victim anon = %d, want 256", got.MemorySnapshot.VictimCgroup.Stat["anon"])
	}
	if got.MemorySnapshot.VictimCgroup.Events["oom_kill"] != 1 {
		t.Errorf("victim oom_kill = %d, want 1", got.MemorySnapshot.VictimCgroup.Events["oom_kill"])
	}
}

func taskComm(name string) [bpf.TaskCommLen]byte {
	var comm [bpf.TaskCommLen]byte
	copy(comm[:], name)
	return comm
}

func writeProcStatus(t *testing.T, procRoot string, pid int, name string, rssAnonKiB, rssFileKiB, rssShmemKiB, swapKiB uint64) {
	t.Helper()

	status := filepath.Join(procRoot, strconv.Itoa(pid), "status")
	writeFile(t, status, fmt.Sprintf(`Name:	%s
Pid:	%d
VmRSS:	%d kB
RssAnon:	%d kB
RssFile:	%d kB
RssShmem:	%d kB
VmSwap:	%d kB
`, name, pid, rssAnonKiB+rssFileKiB+rssShmemKiB, rssAnonKiB, rssFileKiB, rssShmemKiB, swapKiB))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) returned error: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", path, err)
	}
}
