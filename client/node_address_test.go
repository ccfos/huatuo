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

package client

import (
	"errors"
	"testing"
)

func TestNodeAddressServerURL(t *testing.T) {
	tests := []struct {
		name    string
		address NodeAddress
		want    string
	}{
		{
			name:    "default HTTP",
			address: NodeAddress{HostPort: "node-1:19704"},
			want:    "http://node-1:19704",
		},
		{
			name:    "HTTPS",
			address: NodeAddress{HostPort: "node-1:443", Scheme: "https"},
			want:    "https://node-1:443",
		},
		{
			name:    "IPv4",
			address: NodeAddress{HostPort: "127.0.0.1:19704"},
			want:    "http://127.0.0.1:19704",
		},
		{
			name:    "IPv6",
			address: NodeAddress{HostPort: "[2001:db8::1]:19704"},
			want:    "http://[2001:db8::1]:19704",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.address.serverURL()
			if err != nil {
				t.Fatalf("serverURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("serverURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNodeAddressRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		address NodeAddress
	}{
		{name: "missing host port"},
		{name: "missing port", address: NodeAddress{HostPort: "node-1"}},
		{name: "missing host", address: NodeAddress{HostPort: ":19704"}},
		{name: "nonnumeric port", address: NodeAddress{HostPort: "node-1:http"}},
		{name: "signed port", address: NodeAddress{HostPort: "node-1:+19704"}},
		{name: "zero port", address: NodeAddress{HostPort: "node-1:0"}},
		{name: "out of range port", address: NodeAddress{HostPort: "node-1:65536"}},
		{name: "surrounding whitespace", address: NodeAddress{HostPort: " node-1:19704"}},
		{name: "host whitespace", address: NodeAddress{HostPort: "node 1:19704"}},
		{name: "host path", address: NodeAddress{HostPort: "node/1:19704"}},
		{name: "invalid IPv6", address: NodeAddress{HostPort: "[not:IPv6]:19704"}},
		{name: "URL in host port", address: NodeAddress{HostPort: "http://node-1:19704"}},
		{
			name:    "unsupported scheme",
			address: NodeAddress{HostPort: "node-1:19704", Scheme: "ftp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.address.serverURL()
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("serverURL() error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeInvalidArgument || nodeErr.StatusCode != 0 {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}
