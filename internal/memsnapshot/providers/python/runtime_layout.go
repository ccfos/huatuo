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

package python

import "fmt"

type version struct {
	major      int
	minor      int
	micro      int
	microKnown bool
}

type interpreterLayout uint8

const (
	layoutUnknown interpreterLayout = iota
	layoutRuntimeGC
	layoutProbedList
	layoutFixed
	layoutDebugOffsets
)

type runtimeLayout struct {
	interpreterMode     interpreterLayout
	runtimeHeadOffset   uint64
	interpreterGCOffset uint64
	debugInterpreterGC  uint64
	debugObjectType     uint64
	debugTypeName       uint64
	debugTypeFlags      uint64
	objectTypeOffset    uint64
	objectSizeOffset    uint64
	typeNameOffset      uint64
	typeFlagsOffset     uint64
	unicodeDataOffset   uint64
}

var runtimeLayouts = map[int]runtimeLayout{
	8: {
		interpreterMode: layoutRuntimeGC, unicodeDataOffset: 48,
	},
	9: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	10: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	11: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	12: {
		interpreterMode: layoutFixed, runtimeHeadOffset: 40,
		interpreterGCOffset: 112, unicodeDataOffset: 40,
	},
	13: {
		interpreterMode:    layoutDebugOffsets,
		debugInterpreterGC: 80, debugObjectType: 360,
		debugTypeName: 376, debugTypeFlags: 392, unicodeDataOffset: 40,
	},
	14: {
		interpreterMode:    layoutDebugOffsets,
		debugInterpreterGC: 88, debugObjectType: 408,
		debugTypeName: 424, debugTypeFlags: 440, unicodeDataOffset: 40,
	},
}

func (v version) String() string {
	if !v.microKnown {
		return fmt.Sprintf("%d.%d.x", v.major, v.minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.micro)
}

func layoutFor(version version) (runtimeLayout, error) {
	layout, ok := runtimeLayouts[version.minor]
	if version.major != 3 || !ok {
		return runtimeLayout{}, fmt.Errorf("%w: %s", errUnsupportedRuntime,
			version.String())
	}
	layout.objectTypeOffset = pyObjectTypeOffset
	layout.objectSizeOffset = pyObjectSizeOffset
	layout.typeNameOffset = pyTypeNameOffset
	layout.typeFlagsOffset = pyTypeFlagsOffset
	return layout, nil
}
