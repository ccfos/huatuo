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

package pcapfilter

import (
	"errors"
	"testing"
)

func TestValidateL3Compatible(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr error
	}{
		{name: "TCP", expr: "tcp and port 443"},
		{name: "IPv4 ethertype", expr: "ether proto ip and tcp"},
		{name: "IPv6 ethertype", expr: "ether proto ip6 and tcp"},
		{
			name:    "Ethernet host",
			expr:    "ether host 02:00:00:00:00:01",
			wantErr: ErrL3IncompatibleFilter,
		},
		{
			name:    "Ethernet source host",
			expr:    "ether src host 02:00:00:00:00:01",
			wantErr: ErrL3IncompatibleFilter,
		},
		{name: "empty", wantErr: ErrEmptyFilter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateL3Compatible(tt.expr)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateL3Compatible() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
