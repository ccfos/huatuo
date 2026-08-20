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
	"strings"
	"testing"
)

func TestValidateTCPRetransmitCorrelation(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		blacklist []string
		wantError string
	}{
		{name: "disabled", blacklist: []string{"tcp_retransmit"}},
		{name: "standalone dropwatch disabled", enabled: true, blacklist: []string{"dropwatch"}},
		{
			name:      "tcp retransmit disabled",
			enabled:   true,
			blacklist: []string{"tcp_retransmit"},
			wantError: "remove tcp_retransmit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTCPRetransmitCorrelation(tt.enabled, tt.blacklist)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateTCPRetransmitCorrelation() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantError)
			}
		})
	}
}
