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

package pod

import (
	"fmt"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

// TestContainerBindingResourceReadiness verifies that CSS and netns availability
// do not gate identity or file metrics, and binding cannot mutate a cached view.
func TestContainerBindingResourceReadiness(t *testing.T) {
	originalRoot := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })
	for _, runtime := range []string{"io.containerd.runc.v2", "io.containerd.kata.v2"} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ready=%t", runtime, ready), func(t *testing.T) {
				root := t.TempDir()
				procfs.RootPrefix(root)
				procRoot := filepath.Join(root, "proc")
				cgroupRoot := filepath.Join(root, "cgroup")
				guestDirectory(t, cgroupRoot, "/checkout")
				guestFile(t, procRoot, "self/mountinfo", fmt.Sprintf("40 1 0:40 / %s rw - cgroup2 none rw\n", cgroupRoot))
				guestProc(t, &guestScanner{options: GuestScannerOptions{ProcRoot: procRoot}}, 4321, 1, 8500, "0::/checkout\n")
				if ready {
					guestFile(t, procRoot, "4321/ns/net", "")
					var stat syscall.Stat_t
					if err := syscall.Stat(filepath.Join(cgroupRoot, "checkout"), &stat); err != nil {
						t.Error(err)
						return
					}
					for name, css := range map[string]uint64{"cpu": 8500, "memory": 8600} {
						cgroupCssMetaDataMap.Store(css, &containerCssMetaData{CSS: css, Inode: stat.Ino, SubSys: name})
						t.Cleanup(func() { cgroupCssMetaDataMap.Delete(css) })
					}
				}
				original := &Container{
					ID: "checkout", InitPid: 4321, RuntimeType: runtime, SandboxID: "checkout-vm",
					CgroupPath: "/legacy-checkout", IPAddress: "172.16.0.2",
					CgroupCss: map[string]uint64{"cpu": 1}, NetNamespaceInum: 2, NetNamespaceCookie: 3,
				}
				record, err := containerMemoryBinding(original)
				if err != nil {
					t.Error(err)
					return
				}
				if original.CgroupCss["cpu"] != 1 || original.NetNamespaceInum != 2 || original.NetNamespaceCookie != 3 {
					t.Error("binding changed the previously published view")
				}
				if record.pod.Process == nil || record.pod.Process.PID != 4321 || record.startTime != 8500 {
					t.Error("resource readiness lost process identity")
				}
				if path, ok := record.pod.Cgroups.Path("memory"); !ok || path != "/checkout" {
					t.Errorf("memory path=%q available=%t", path, ok)
				}
				view := record.container
				if view.CgroupPath != original.CgroupPath || view.IPAddress != original.IPAddress || view.CgroupCss == nil {
					t.Errorf("legacy path, IP or CSS shape changed: %+v", view)
				}
				if (view.CgroupCss["cpu"] == 8500) != ready {
					t.Errorf("CPU CSS readiness=%v, want %t", view.CgroupCss, ready)
				}
				kata := runtime == "io.containerd.kata.v2"
				if kata && (view.CgroupCss["memory"] != 0 || record.ref.MemoryCgroupPath != "") {
					t.Error("Kata published application memory targets")
				}
				if (view.NetNamespaceInum != 0) != (ready && !kata) || view.NetNamespaceCookie != 0 {
					t.Errorf("network readiness mismatch: %+v", view)
				}
				if kata && record.pod.Network != nil && record.pod.Network.PID != 0 {
					t.Error("Kata VM became a Guest network target")
				}
			})
		}
	}
}
