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

func TestContainerString(t *testing.T) {
	tests := []struct {
		name string
		c    *Container
		want string
	}{
		{
			name: "nil receiver",
			c:    nil,
			want: "<nil>",
		},
		{
			name: "full container",
			c: &Container{
				ID:        "0123456789ab",
				Hostname:  "node-1",
				Name:      "web",
				Type:      ContainerTypeNormal,
				Qos:       containerQosBurstable,
				IPAddress: "10.0.0.1",
			},
			want: "0123456789ab:node-1/web/normal:burstable/10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
