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

func TestValidateMemBurstConfig(t *testing.T) {
	cases := []struct {
		name                string
		historyWindowLength int
		sampleInterval      int
		topNProcesses       int
		wantErr             bool
	}{
		{
			name:                "valid",
			historyWindowLength: 60,
			sampleInterval:      10,
			topNProcesses:       10,
		},
		{
			name:                "zero history",
			historyWindowLength: 0,
			sampleInterval:      10,
			topNProcesses:       10,
			wantErr:             true,
		},
		{
			name:                "zero interval",
			historyWindowLength: 60,
			sampleInterval:      0,
			topNProcesses:       10,
			wantErr:             true,
		},
		{
			name:                "zero top processes",
			historyWindowLength: 60,
			sampleInterval:      10,
			topNProcesses:       0,
			wantErr:             true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMemBurstConfig(tc.historyWindowLength, tc.sampleInterval, tc.topNProcesses)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateMemBurstConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
