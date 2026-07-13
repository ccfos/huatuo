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

package raw

import (
	"bytes"
	"testing"

	"huatuo-bamai/internal/profiler/output"
)

func TestFormatterAdd_RemovesBalancedStack(t *testing.T) {
	formatter := New()
	frames := []string{"root", "allocate"}

	if err := formatter.Add(&output.Sample{Frames: frames, Count: 4096}); err != nil {
		t.Fatalf("add allocation sample: %v", err)
	}
	if err := formatter.Add(&output.Sample{Frames: frames, Count: -4096}); err != nil {
		t.Fatalf("add free sample: %v", err)
	}

	if !formatter.IsEmpty() {
		t.Fatalf("balanced stack retained in formatter: %v", formatter.Counts())
	}

	var got bytes.Buffer
	if err := formatter.Write(&got); err != nil {
		t.Fatalf("write formatter: %v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("balanced stack output = %q, want empty", got.String())
	}
}
