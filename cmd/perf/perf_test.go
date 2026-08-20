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

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/pod"
)

func TestCgroupTarget(t *testing.T) {
	t.Run("v2 leaf ID", func(t *testing.T) {
		css, cgroupID, err := cgroupTarget(&pod.Container{ID: "v2", CgroupID: 101})
		if err != nil || css != 0 || cgroupID != 101 {
			t.Fatalf("cgroupTarget() = (%d, %d, %v), want (0, 101, nil)", css, cgroupID, err)
		}
	})

	t.Run("no key", func(t *testing.T) {
		if _, _, err := cgroupTarget(&pod.Container{ID: "none"}); err == nil {
			t.Fatal("cgroupTarget() error = nil, want non-nil")
		}
	})

	t.Run("CSS only", func(t *testing.T) {
		_, _, err := cgroupTarget(&pod.Container{
			ID:        "css",
			CgroupCss: map[string]uint64{"cpu": 101},
		})
		wantErr := cgroups.CgroupMode() == cgroups.Unified
		if (err != nil) != wantErr {
			t.Fatalf("cgroupTarget() error = %v, want error=%t", err, wantErr)
		}
	})
}
