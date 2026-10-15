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
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestCgroupCPUInodeIntegration(t *testing.T) {
	previousDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = integrationTestEnv(t, "HUATUO_BPF_OBJECT_DIR")
	t.Cleanup(func() { bpf.DefaultObjDir = previousDir })
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	if err := containerCgroupCssInit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(containerCgroupCssRelease)
	paths, err := cgroups.PathsForPID(1)
	if err != nil {
		t.Fatal(err)
	}
	path := paths.Controllers[subsystem.SubsystemCPU]
	if cgroups.CgroupMode() == cgroups.Unified {
		path = paths.Unified
	}
	root := cgroups.RootFsFilePath(subsystem.SubsystemCPU)
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
	}
	// The one-shot reader joins before we verify the actual inode/CSS mapping.
	if err := triggerContainerCSSSync(root + path); err != nil {
		t.Fatal(err)
	}
	css, err := parseCgroupCPUCSS(path)
	if err != nil || css[subsystem.SubsystemCPU] == 0 {
		t.Fatalf("CPU CSS=%v err=%v", css, err)
	}
}
