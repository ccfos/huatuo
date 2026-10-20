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

import corev1 "k8s.io/api/core/v1"

// hasGuestAgentIdentity recognizes the existing Guest deployment contract,
// without guessing a role from container or image names.
func hasGuestAgentIdentity(container *corev1.Container, pod *corev1.Pod) bool {
	for _, identity := range []struct{ name, field, value string }{
		{"BAMAI_POD_NAME", "metadata.name", pod.Name},
		{"BAMAI_POD_NAMESPACE", "metadata.namespace", pod.Namespace},
		{"BAMAI_NODE_NAME", "spec.nodeName", pod.Spec.NodeName},
	} {
		var matched *corev1.EnvVar
		for i := range container.Env {
			env := &container.Env[i]
			if env.Name != identity.name {
				continue
			}
			if matched != nil {
				return false
			}
			matched = env
		}
		if matched == nil || identity.value == "" {
			return false
		}
		if matched.ValueFrom == nil {
			if matched.Value != identity.value {
				return false
			}
		} else if matched.Value != "" ||
			matched.ValueFrom.FieldRef == nil ||
			matched.ValueFrom.FieldRef.FieldPath != identity.field ||
			(matched.ValueFrom.FieldRef.APIVersion != "" &&
				matched.ValueFrom.FieldRef.APIVersion != "v1") {
			return false
		}
	}
	return true
}
