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

package speedscope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestProcessScopedThreadProfiles(t *testing.T) {
	for _, threadID := range []string{"", "1", "main", "default"} {
		t.Run(fmt.Sprintf("thread=%q", threadID), func(t *testing.T) {
			f := New(100)
			for _, sample := range []output.Sample{
				{PID: 101, ThreadID: threadID, ThreadName: "worker", Frames: []string{"process101"}, Count: 2},
				{PID: 202, ThreadID: threadID, ThreadName: "worker", Frames: []string{"process202"}, Count: 3},
				{PID: 101, ThreadID: threadID, ThreadName: "worker", Frames: []string{"process101"}, Count: 1},
			} {
				if err := f.Add(&sample); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			if err := f.Write(&buf); err != nil {
				t.Fatal(err)
			}
			var got speedscopeFile
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Profiles) != 2 {
				t.Fatalf("profiles = %d, want 2: %s", len(got.Profiles), buf.String())
			}
			if got.Profiles[0].Name == got.Profiles[1].Name {
				t.Fatalf("indistinguishable profile names: %q", got.Profiles[0].Name)
			}
			for i, profile := range got.Profiles {
				if len(profile.Samples) != 3 || profile.EndValue != 0.03 {
					t.Errorf("profile %d: samples=%d, end=%v", i, len(profile.Samples), profile.EndValue)
				}
				expected := fmt.Sprintf("process%d", []int{101, 202}[i])
				for _, stack := range profile.Samples {
					if len(stack) != 1 || got.Shared.Frames[stack[0]].Name != expected {
						t.Errorf("profile %d contains another process's frames: %v", i, stack)
					}
				}
			}
			f.Reset()
			if !f.IsEmpty() {
				t.Fatal("Reset retained profiles")
			}
		})
	}
}

func TestUnknownProcessAndThreadProfiles(t *testing.T) {
	f := New(100)
	for _, s := range []output.Sample{
		{Frames: []string{"unknown"}},
		{ThreadID: "main", Frames: []string{"named"}},
		{PID: 101, Frames: []string{"known"}},
	} {
		if err := f.Add(&s); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var got speedscopeFile
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 3 {
		t.Fatalf("profiles=%d, want 3", len(got.Profiles))
	}
	if got.Profiles[0].Name != "main thread" || got.Profiles[1].Name != "main" {
		t.Fatalf("unknown-PID names changed: %+v", got.Profiles)
	}
}

func BenchmarkProcessGrouping(b *testing.B) {
	f := New(100)
	s := output.Sample{PID: 101, ThreadID: "1", Frames: []string{"root", "leaf"}, Count: 1}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if i%1024 == 0 {
			f.Reset()
		}
		if err := f.Add(&s); err != nil {
			b.Fatal(err)
		}
	}
}
