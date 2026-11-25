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

package pod

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/pkg/observation"
)

func parseContainerLabels(typ ContainerType, pod *corev1.Pod) (map[string]any, error) {
	var err error
	labels := make(map[string]any)

	labels[observation.FieldContainerNamespace], err = parseContainerNamespace(typ, pod)
	if err != nil {
		return nil, err
	}

	return labels, nil
}
