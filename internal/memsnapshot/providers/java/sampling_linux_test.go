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
	"testing"
)

func TestJavaSamplingBounds(t *testing.T) {
	regions := []region{{bottom: 4096, top: 4096 + 1<<20}}
	windows := planWindows(regions, 7, 8192, 4096)
	var sampled uint64
	for _, w := range windows {
		if w.start < regions[0].bottom || w.start+w.size > regions[0].top {
			t.Fatal("sample outside region")
		}
		sampled += w.size
	}
	if sampled == 0 || sampled > 8192 {
		t.Fatalf("sample budget = %d", sampled)
	}
}
