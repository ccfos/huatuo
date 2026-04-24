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

package autotracing

import (
	"testing"

	"huatuo-bamai/internal/pod"
)

func TestIgnoreContainer_Exclude(t *testing.T) {
	tests := []struct {
		name      string
		container *pod.Container
		filter    filter
		want      bool
	}{
		{
			name: "exclude by namespace regex",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "kube-system"},
			},
			filter: filter{
				Exclude: []filterRule{
					{Field: "container_host_namespace", Pattern: "^kube-", MatchType: "regex"},
				},
			},
			want: true,
		},
		{
			name: "exclude by hostname exact",
			container: &pod.Container{
				Hostname: "legacy-host-001",
				Labels:   map[string]any{"HostNamespace": "default"},
			},
			filter: filter{
				Exclude: []filterRule{
					{Field: "container_hostname", Pattern: "legacy-host-001", MatchType: "exact"},
				},
			},
			want: true,
		},
		{
			name: "exclude by qos exact",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "default"},
				Qos:      pod.ContainerQos(3),
			},
			filter: filter{
				Exclude: []filterRule{
					{Field: "container_qos", Pattern: "besteffort", MatchType: "exact"},
				},
			},
			want: true,
		},
		{
			name: "no exclude rule",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "default"},
			},
			filter: filter{
				Exclude: []filterRule{
					{Field: "container_host_namespace", Pattern: "^kube-", MatchType: "regex"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ignoreContainer(tt.container, tt.filter)
			if got != tt.want {
				t.Errorf("ignoreContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIgnoreContainer_Include(t *testing.T) {
	tests := []struct {
		name      string
		container *pod.Container
		filter    filter
		want      bool
	}{
		{
			name: "include by namespace regex",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "application-prod"},
			},
			filter: filter{
				Include: []filterRule{
					{Field: "container_host_namespace", Pattern: "^application-", MatchType: "regex"},
				},
			},
			want: false,
		},
		{
			name: "include but not match",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "kube-system"},
			},
			filter: filter{
				Include: []filterRule{
					{Field: "container_host_namespace", Pattern: "^application-", MatchType: "regex"},
				},
			},
			want: true,
		},
		{
			name: "include by qos exact",
			container: &pod.Container{
				Hostname: "test-host",
				Labels:   map[string]any{"HostNamespace": "default"},
				Qos:      pod.ContainerQos(1),
			},
			filter: filter{
				Include: []filterRule{
					{Field: "container_qos", Pattern: "guaranteed", MatchType: "exact"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ignoreContainer(tt.container, tt.filter)
			if got != tt.want {
				t.Errorf("ignoreContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIgnoreContainer_EmptyFilter(t *testing.T) {
	container := &pod.Container{
		Hostname: "test-host",
		Labels:   map[string]any{"HostNamespace": "default"},
	}

	filter := filter{}

	got := ignoreContainer(container, filter)
	if got != false {
		t.Errorf("ignoreContainer() = %v, want false", got)
	}
}
