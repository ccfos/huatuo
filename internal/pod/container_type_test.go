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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func deploymentPod(containerName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet"},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestParseContainerTypeExactSidecarMatch(t *testing.T) {
	pod := deploymentPod("istio-proxy")
	container := &corev1.Container{Name: "istio-proxy"}

	got, err := parseContainerType(container, pod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ContainerTypeSidecar {
		t.Fatalf("expected ContainerTypeSidecar, got %v", got)
	}
}

func TestParseContainerTypePartialNamesRemainNormal(t *testing.T) {
	cases := []string{"proxy", "istio", "istio-pro", "istio-proxy-envoy", "p"}
	for _, name := range cases {
		pod := deploymentPod(name)
		container := &corev1.Container{Name: name}

		got, err := parseContainerType(container, pod)
		if err != nil {
			t.Fatalf("container %q: unexpected error: %v", name, err)
		}
		if got != ContainerTypeNormal {
			t.Errorf("container %q: expected ContainerTypeNormal, got %v", name, got)
		}
	}
}

func TestParseContainerTypeDaemonSet(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	container := &corev1.Container{Name: "istio-proxy"}

	got, err := parseContainerType(container, pod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ContainerTypeDaemonSet {
		t.Fatalf("expected ContainerTypeDaemonSet, got %v", got)
	}
}

func TestParseContainerTypeNoOwnerRunningIsNormal(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	container := &corev1.Container{Name: "anything"}

	got, err := parseContainerType(container, pod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ContainerTypeNormal {
		t.Fatalf("expected ContainerTypeNormal, got %v", got)
	}
}
