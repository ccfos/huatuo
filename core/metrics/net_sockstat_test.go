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

package collector

import "testing"

func TestScaleSockstatValueUsesHostPageSize(t *testing.T) {
	const pages = 3

	tests := []struct {
		name     string
		pageSize int
		want     float64
	}{
		{name: "unscaled value", want: 3},
		{name: "four KiB pages", pageSize: 4 * 1024, want: 12 * 1024},
		{name: "sixty-four KiB pages", pageSize: 64 * 1024, want: 192 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scaleSockstatValue(pages, tt.pageSize); got != tt.want {
				t.Errorf("scaleSockstatValue() = %.0f, want %.0f", got, tt.want)
			}
		})
	}
}
