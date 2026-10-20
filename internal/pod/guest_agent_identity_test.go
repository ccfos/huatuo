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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGuestAgentIdentity(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "payments"},
		Spec:       corev1.PodSpec{NodeName: "worker-96"},
	}
	literal := []corev1.EnvVar{
		{Name: "BAMAI_POD_NAME", Value: "orders"},
		{Name: "BAMAI_POD_NAMESPACE", Value: "payments"},
		{Name: "BAMAI_NODE_NAME", Value: "worker-96"},
	}
	downward := []corev1.EnvVar{
		{
			Name:      "BAMAI_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
		},
		{
			Name:      "BAMAI_POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name:      "BAMAI_NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "spec.nodeName"}},
		},
	}
	for _, test := range []struct {
		name string
		env  []corev1.EnvVar
		want bool
	}{
		{"literal identity", literal, true},
		{"downward API identity", downward, true},
		{"business named agent", nil, false},
		{"partial identity", literal[:2], false},
		{"duplicate identity", append(append([]corev1.EnvVar(nil), literal...), literal[0]), false},
		{"wrong pod", []corev1.EnvVar{{Name: "BAMAI_POD_NAME", Value: "other"}, literal[1], literal[2]}, false},
		{
			"wrong field",
			[]corev1.EnvVar{
				{
					Name:      "BAMAI_POD_NAME",
					ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}},
				},
				literal[1],
				literal[2],
			},
			false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := &corev1.Container{Name: "agent", Env: test.env}
			if got := hasGuestAgentIdentity(container, pod); got != test.want {
				t.Errorf("Guest identity=%t, want %t", got, test.want)
			}
		})
	}
}

func TestKataRepresentativePrefersBusinessOverGuestAgent(t *testing.T) {
	agent := &Container{
		ID:          "agent-first",
		Type:        ContainerTypeSidecar,
		RuntimeType: "io.containerd.kata-qemu.v2",
		SandboxID:   "orders-vm",
		SyncedAt:    time.Unix(1, 0),
		CgroupCss:   map[string]uint64{"cpu": 10},
	}
	business := *agent
	business.ID = "checkout"
	business.Type = ContainerTypeNormal
	business.SyncedAt = time.Unix(2, 0)
	members := map[string]*Container{agent.ID: agent, business.ID: &business}
	for range 100 {
		if got := BuildCssContainers(members, "cpu")[10]; got != &business {
			t.Errorf("representative=%v, want checkout", got)
			break
		}
		if got := BuildCssContainersID(members, "cpu")[10]; got != business.ID {
			t.Errorf("representative ID=%q, want checkout", got)
			break
		}
	}
}
