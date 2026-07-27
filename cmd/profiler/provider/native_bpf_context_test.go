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

package provider

import "testing"

func TestValidateStackID(t *testing.T) {
	tests := []struct {
		name          string
		kernelStackID int32
		userStackID   int32
		want          bool
	}{
		{name: "no stack", kernelStackID: -1, userStackID: -1, want: false},
		{name: "kernel stack zero", kernelStackID: 0, userStackID: -1, want: true},
		{name: "user stack zero", kernelStackID: -1, userStackID: 0, want: true},
		{name: "positive stack IDs", kernelStackID: 1, userStackID: 2, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateStackID(tt.kernelStackID, tt.userStackID); got != tt.want {
				t.Fatalf("validateStackID() = %t, want %t", got, tt.want)
			}
		})
	}
}
