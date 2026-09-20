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

package java

import (
	"errors"
	"testing"
)

func TestJavaRegionGrouping(t *testing.T) {
	metadata := &vmMeta{constants: map[string]int64{
		"HeapRegionType::StartsHumongousTag":    12,
		"HeapRegionType::ContinuesHumongousTag": 13,
		"Klass::_lh_header_size_shift":          16,
		"Klass::_lh_header_size_mask":           255,
		"Klass::_lh_log2_element_size_mask":     255,
	}}
	regions := []region{
		{bottom: 4096, top: 8192, capacity: 4096, tag: 12, hasTag: true},
		{bottom: 8192, top: 9000, capacity: 4096, tag: 13, hasTag: true},
	}
	heap, err := groupRegions(regions, metadata)
	if err != nil || len(heap.humongous) != 1 || len(heap.humongous[0].regions) != 2 {
		t.Fatalf("humongous grouping: %+v, %v", heap, err)
	}
	if _, err := groupRegions(regions[1:], metadata); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("orphan continuation accepted: %v", err)
	}
}
