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

import "testing"

func TestContainerLabelHostNamespace(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]any
		want   string
	}{
		{
			name:   "nil labels",
			labels: nil,
			want:   "",
		},
		{
			name:   "missing key",
			labels: map[string]any{"other": "ns"},
			want:   "",
		},
		{
			name:   "non-string value",
			labels: map[string]any{labelHostNamespace: 42},
			want:   "",
		},
		{
			name:   "present value",
			labels: map[string]any{labelHostNamespace: "ns-1"},
			want:   "ns-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Container{Labels: tt.labels}
			if got := c.LabelHostNamespace(); got != tt.want {
				t.Fatalf("LabelHostNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}
