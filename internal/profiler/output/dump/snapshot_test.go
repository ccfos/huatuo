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

package dump

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestAddSnapshotsReusedFrames(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		name := "text"
		if asJSON {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			f := New(Options{JSON: asJSON})
			sample := output.Sample{ThreadID: "7", Count: 1, Frames: []string{"main", "first"}}
			if err := f.Add(&sample); err != nil {
				t.Fatal(err)
			}
			sample.Frames[1] = "second"
			if err := f.Add(&sample); err != nil {
				t.Fatal(err)
			}
			sample.Frames[0] = "reused"
			sample.Frames[1] = "buffer"

			var buf bytes.Buffer
			if err := f.Write(&buf); err != nil {
				t.Fatal(err)
			}
			if asJSON {
				var entries []dumpEntry
				if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
					t.Fatal(err)
				}
				if len(entries) != 2 {
					t.Fatalf("got %d entries, want 2", len(entries))
				}
				for i, want := range [][]string{{"main", "first"}, {"main", "second"}} {
					if !reflect.DeepEqual(entries[i].Frames, want) {
						t.Errorf("entry %d frames = %v, want %v", i, entries[i].Frames, want)
					}
				}
			} else {
				const want = "Thread 7\n    main\n    first\n\nThread 7\n    main\n    second\n\n"
				if buf.String() != want {
					t.Errorf("dump = %q, want %q", buf.String(), want)
				}
			}
		})
	}
}
