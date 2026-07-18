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

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"huatuo-bamai/internal/procfs"
)

func TestPSISamples(t *testing.T) {
	some := &procfs.PSILine{Avg10: 1.1, Avg60: 2.2, Avg300: 3.3, Total: 100}
	full := &procfs.PSILine{Avg10: 4.4, Avg60: 5.5, Avg300: 6.6, Total: 200}

	cases := []struct {
		name     string
		resource string
		stats    procfs.PSIStats
		want     []psiSample
	}{
		{
			name:     "cpu has some only",
			resource: "cpu",
			stats:    procfs.PSIStats{Some: some},
			want: []psiSample{
				{"psi_some_avg10", 1.1, "PSI some avg10 stall fraction (percent) for cpu", false},
				{"psi_some_avg60", 2.2, "PSI some avg60 stall fraction (percent) for cpu", false},
				{"psi_some_avg300", 3.3, "PSI some avg300 stall fraction (percent) for cpu", false},
				{"psi_some_total", 100, "PSI some total stall time (microseconds) for cpu", true},
			},
		},
		{
			name:     "memory has some and full",
			resource: "memory",
			stats:    procfs.PSIStats{Some: some, Full: full},
			want: []psiSample{
				{"psi_some_avg10", 1.1, "PSI some avg10 stall fraction (percent) for memory", false},
				{"psi_some_avg60", 2.2, "PSI some avg60 stall fraction (percent) for memory", false},
				{"psi_some_avg300", 3.3, "PSI some avg300 stall fraction (percent) for memory", false},
				{"psi_some_total", 100, "PSI some total stall time (microseconds) for memory", true},
				{"psi_full_avg10", 4.4, "PSI full avg10 stall fraction (percent) for memory", false},
				{"psi_full_avg60", 5.5, "PSI full avg60 stall fraction (percent) for memory", false},
				{"psi_full_avg300", 6.6, "PSI full avg300 stall fraction (percent) for memory", false},
				{"psi_full_total", 200, "PSI full total stall time (microseconds) for memory", true},
			},
		},
		{
			name:     "empty stats yields nothing",
			resource: "io",
			stats:    procfs.PSIStats{},
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := psiSamples(tc.resource, tc.stats)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(psiSample{})); diff != "" {
				t.Errorf("psiSamples(%q) (-want +got):\n%s", tc.resource, diff)
			}
		})
	}
}
