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

//go:build !didi

package pod

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseContainerTypeSidecarName(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet"}},
		},
	}

	tests := []struct {
		name          string
		containerName string
		want          ContainerType
	}{
		{
			name:          "exact sidecar name",
			containerName: "istio-proxy",
			want:          ContainerTypeSidecar,
		},
		{
			name:          "sidecar name suffix",
			containerName: "proxy",
			want:          ContainerTypeNormal,
		},
		{
			name:          "sidecar name prefix",
			containerName: "istio",
			want:          ContainerTypeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containerType, err := parseContainerType(
				&corev1.Container{Name: tt.containerName}, pod,
			)
			require.NoError(t, err)
			require.Equal(t, tt.want, containerType)
		})
	}
}
