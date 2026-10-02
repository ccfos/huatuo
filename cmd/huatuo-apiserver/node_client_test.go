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

package main

import (
	"testing"

	"github.com/ccfos/huatuo/client"
)

func TestNodeOperationClientAddress(t *testing.T) {
	tests := []struct {
		name string
		host string
		want client.NodeAddress
	}{
		{
			name: "hostname",
			host: "node-1",
			want: client.NodeAddress{HostPort: "node-1:19704"},
		},
		{
			name: "IPv6",
			host: "2001:db8::1",
			want: client.NodeAddress{HostPort: "[2001:db8::1]:19704"},
		},
	}
	nodeClient := newNodeOperationClient(nil, 19704)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeClient.address(tt.host); got != tt.want {
				t.Fatalf("address() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
