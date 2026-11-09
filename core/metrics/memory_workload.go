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

import "github.com/ccfos/huatuo/internal/pod"

func isMemoryWorkload(workload *pod.Pod) bool {
	switch workload.Type {
	case pod.TypeCubeSandbox, pod.TypeOpenSandbox:
		return true
	case pod.TypeContainer:
		info := workload.Container
		_, namespaceOK := workload.Labels[pod.LabelHostNamespace].(string)
		return info != nil && namespaceOK && info.Type&pod.ContainerTypeNormal != 0 &&
			info.Qos >= pod.ContainerQosLevelMin && (!pod.IsKataRuntime(info.RuntimeType) || info.SandboxID != "")
	default:
		return false
	}
}
