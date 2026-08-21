// Copyright 2025, 2026 The HuaTuo Authors
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

//go:build !didi

package pod

import corev1 "k8s.io/api/core/v1"

var sidecarModules = map[string]struct{}{
	"istio-proxy": {},
}

func parseContainerType(container *corev1.Container, pod *corev1.Pod) (ContainerType, error) {
	// List of objects depended by this object. If ALL objects in the list have
	// been deleted, this object will be garbage collected. If this object is managed by a controller,
	// then an entry in this list will point to this controller, with the controller field set to true.
	// There cannot be more than one managing controller.
	//
	// Pod is deleted or static (kubelet manifests or pod.yaml)
	if len(pod.OwnerReferences) == 0 {
		if pod.Status.Phase == corev1.PodRunning {
			return ContainerTypeNormal, nil
		}
		return ContainerTypeUnknown, nil
	}

	if pod.OwnerReferences[0].Kind == "DaemonSet" {
		return ContainerTypeDaemonSet, nil
	}

	if _, ok := sidecarModules[container.Name]; ok {
		return ContainerTypeSidecar, nil
	}

	// kind:
	// Deployment
	// ReplicaSet
	// StatefulSet
	// CloneSet
	return ContainerTypeNormal, nil
}
