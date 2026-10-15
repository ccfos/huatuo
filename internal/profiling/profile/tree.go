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

package profile

import (
	"fmt"
	"strings"
	"time"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

// TreeItem is the item in the tree.
type TreeItem struct {
	Stack [][]byte `json:"stack,omitempty"`
	Value uint64   `json:"value,omitempty"`
}

// BuildTreeItem joins optional prefixes with outermost-first user and kernel
// frames in the order expected by ParseTree.
func BuildTreeItem(prefixes, userFrames, kernelFrames []string, value uint64) *TreeItem {
	stack := make([][]byte, 0, len(prefixes)+len(userFrames)+len(kernelFrames))

	for _, frame := range prefixes {
		stack = append(stack, []byte(frame))
	}
	for _, frame := range userFrames {
		stack = append(stack, []byte(frame))
	}
	for _, frame := range kernelFrames {
		stack = append(stack, []byte(frame))
	}

	return &TreeItem{Stack: stack, Value: value}
}

// ParseTree parses the tree data and returns the profile data.
//
// The tree example:
//
// The tree slice example:
//
//	 Raw:
//			sleep;[unknown];_int_free 2385865
//			sleep;_dl_sysdep_start;dl_main;_dl_vdso_vsym 2386677
//			sleep;__mmap;entry_SYSCALL_64_after_hwframe;do_syscall_64;ksys_mmap_pgoff;do_mmap_pgoff 2386677
//
//	Slice:
//		[
//			{["sleep","[unknown]","_int_free"], 2385865},
//			{["sleep","_dl_sysdep_start","dl_main","_dl_vdso_vsym"], 2386677},
//			{["sleep","__mmap","entry_SYSCALL_64_after_hwframe","do_syscall_64","ksys_mmap_pgoff","do_mmap_pgoff"], 2386677},
//		]
//
// The profileType is as follows:
//
//   - CPU sample: process_cpu:cpu:nanoseconds:cpu:nanoseconds
//   - Memory alloc_space: memory:alloc_space:bytes:space:bytes
//   - Memory alloc_objects: memory:alloc_objects:count:space:bytes
func ParseTree(startTime time.Time, profileType string, data []*TreeItem, opt *ParseOption) (*Data, error) {
	profileTypes := strings.Split(profileType, ":")
	if len(profileTypes) != 5 {
		return nil, fmt.Errorf("invalid profile type: %q", profileType)
	}

	tree := ptree.New()

	for _, item := range data {
		tree.InsertStack(item.Stack, item.Value)
	}

	mdata := &ptree.PprofMetadata{
		Type:       profileTypes[1],
		Unit:       profileTypes[2],
		StartTime:  startTime,
		PeriodType: profileTypes[3],
		PeriodUnit: profileTypes[4],
	}

	scale := uint64(1)
	// If the sample rate is set (cpu), scale the tree.
	if opt != nil && opt.SampleRate > 0 {
		scale = uint64(time.Second.Nanoseconds() / opt.SampleRate)
	}
	// otherwise, keep counts unchanged (mem)
	tree.Scale(scale)
	mdata.Period = int64(scale)

	return &Data{
		ProfileType: profileType,
		Profile:     buildTreeProfile(tree, mdata),
	}, nil
}
