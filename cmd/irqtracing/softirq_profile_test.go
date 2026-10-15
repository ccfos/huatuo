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
	"time"

	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestSoftIRQWindowUsesCommonProfileAndQuality(t *testing.T) {
	start := time.Unix(123, 456)
	data, err := profile.ParseTree(start, profile.ProfileTypeIRQTracingSample,
		[]*profile.TreeItem{profile.BuildTreeItem([]string{"source"}, []string{"raise"}, nil, 7)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &irqAggregator{containerID: "irq-container"}
	a.Aggregate(&softIRQBatch{profile: data, dropped: 3, targetCPU: 2})
	window, err := a.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if window.ContainerID != "irq-container" {
		t.Fatalf("container ID = %q", window.ContainerID)
	}
	if window.ProfileType != profile.ProfileTypeIRQTracingSample || window.Profile.TimeNanos != 0 || window.Profile.DurationNanos != 0 {
		t.Fatalf("window = %+v", window)
	}
	if window.DroppedSamples[types.DropReasonCollection] != 3 || window.Profile.Sample[0].Value[0] != 7 {
		t.Fatalf("window = %+v", window)
	}
	label := window.Profile.Sample[0].Label[0]
	if window.Profile.StringTable[label.Key] != "cpu" || window.Profile.StringTable[label.Str] != "2" {
		t.Fatalf("CPU label = %+v", label)
	}
	a.Reset()
	if _, err := a.Snapshot(t.Context()); err == nil {
		t.Fatal("missing batch reported success")
	}
}
