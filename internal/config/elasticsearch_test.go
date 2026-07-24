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

package config

import (
	"strings"
	"testing"
)

func TestElasticsearchConfigValidate(t *testing.T) {
	tests := []struct {
		name        string
		config      ElasticsearchConfig
		wantEnabled bool
		wantError   string
	}{
		{
			name: "disabled",
		},
		{
			name: "enabled",
			config: ElasticsearchConfig{
				Address:  "http://127.0.0.1:9200",
				Username: "elastic",
				Password: "secret",
			},
			wantEnabled: true,
		},
		{
			name: "multiple addresses",
			config: ElasticsearchConfig{
				Address:  "http://es-a:9200, https://es-b:9200",
				Username: "elastic",
				Password: "secret",
			},
			wantEnabled: true,
		},
		{
			name:      "partial connection",
			config:    ElasticsearchConfig{Address: "http://127.0.0.1:9200"},
			wantError: "address, username, and password must be configured together",
		},
		{
			name: "invalid address",
			config: ElasticsearchConfig{
				Address:  "127.0.0.1:9200",
				Username: "elastic",
				Password: "secret",
			},
			wantEnabled: true,
			wantError:   `invalid address "127.0.0.1:9200"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want contain %q", err, tt.wantError)
			}
			if got := tt.config.Enabled(); got != tt.wantEnabled {
				t.Errorf("Enabled() = %t, want %t", got, tt.wantEnabled)
			}
		})
	}
}
