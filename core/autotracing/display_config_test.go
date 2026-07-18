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

import "testing"

func TestDisplayConfigResolveBackend(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		want    DisplayBackend
		wantErr bool
	}{
		{name: "omitted defaults to Pyroscope", want: DisplayBackendPyroscope},
		{name: "Pyroscope", backend: "pyroscope", want: DisplayBackendPyroscope},
		{name: "API server", backend: "apiserver", want: DisplayBackendAPIServer},
		{name: "normalizes whitespace and case", backend: "  APIserver ", want: DisplayBackendAPIServer},
		{name: "rejects unknown backend", backend: "elasticsearch", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (DisplayConfig{Backend: tt.backend}).ResolveBackend()
			if tt.wantErr {
				if err == nil {
					t.Fatal("ResolveBackend returned nil error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveBackend returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveBackend = %q, want %q", got, tt.want)
			}
		})
	}
}
