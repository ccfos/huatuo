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
	"path/filepath"

	"github.com/ccfos/huatuo/internal/cgroups/stats"
	v1 "github.com/ccfos/huatuo/internal/cgroups/v1"
	v2 "github.com/ccfos/huatuo/internal/cgroups/v2"
	"github.com/ccfos/huatuo/internal/pod"
)

func isCPUWorkload(workload *pod.Pod) bool {
	switch workload.Type {
	case pod.TypeCubeSandbox, pod.TypeOpenSandbox:
		return true
	case pod.TypeContainer:
		info := workload.Container
		_, namespaceOK := workload.Labels[pod.LabelHostNamespace].(string)
		return info != nil && namespaceOK && info.Type&(pod.ContainerTypeNormal|pod.ContainerTypeSidecar) != 0 &&
			info.Qos >= pod.ContainerQosLevelMin && (!pod.IsKataRuntime(info.RuntimeType) || info.SandboxID != "")
	default:
		return false
	}
}

func workloadCPUUsage(workload *pod.Pod) (*stats.CpuUsage, error) {
	directory, err := workload.Cgroups.Directory("cpuacct")
	if err != nil {
		return nil, err
	}
	if _, legacy := workload.Cgroups.Controllers["cpuacct"]; legacy {
		return v1.ReadCPUUsage(directory)
	}
	return v2.ReadCPUUsage(directory)
}

func workloadCPUStat(workload *pod.Pod) (map[string]uint64, error) {
	directory, err := workload.Cgroups.Directory("cpu")
	if err != nil {
		return nil, err
	}
	if _, legacy := workload.Cgroups.Controllers["cpu"]; legacy {
		return v1.ReadCPUStat(directory)
	}
	return v2.ReadCPUStat(directory)
}

func workloadCPUQuota(workload *pod.Pod) (*stats.CpuQuota, error) {
	directory, err := workload.Cgroups.Directory("cpu")
	if err != nil {
		return nil, err
	}
	var cpusetFile string
	// A missing cpuset binding must not fall back to a host or CPU directory.
	if cpuset, err := workload.Cgroups.Directory("cpuset"); err == nil {
		file := "cpuset.cpus.effective"
		if _, legacy := workload.Cgroups.Controllers["cpuset"]; legacy {
			file = "cpuset.cpus"
		}
		cpusetFile = filepath.Join(cpuset, file)
	}
	if _, legacy := workload.Cgroups.Controllers["cpu"]; legacy {
		return v1.ReadCPUQuota(directory, cpusetFile)
	}
	return v2.ReadCPUQuota(directory, cpusetFile)
}
