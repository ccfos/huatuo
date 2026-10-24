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

package bpf

import "testing"

func TestMockObjectSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option *Option
		object string
		want   bool
	}{
		{name: "production", object: "memory_oom_kill.o"},
		{name: "disabled", option: &Option{}, object: "memory_oom_kill.o"},
		{name: "all mock objects", option: &Option{Mock: true}, object: "memory_oom_kill.o", want: true},
		{name: "selected object", option: &Option{Mock: true, MockObjects: []string{"memory_oom_kill.o"}}, object: "memory_oom_kill.o", want: true},
		{name: "real CSS sync", option: &Option{Mock: true, MockObjects: []string{"memory_oom_kill.o"}}, object: "cgroup_css_sync.o"},
		{name: "real CSS lifecycle", option: &Option{Mock: true, MockObjects: []string{"memory_oom_kill.o"}}, object: "cgroup_css_events.o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.option.mockObject(tc.object); got != tc.want {
				t.Fatalf("mockObject = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitRejectsInvalidMockSelection(t *testing.T) {
	for _, option := range []*Option{
		{MockObjects: []string{"memory_oom_kill.o"}},
		{Mock: true, MockObjects: []string{"../memory_oom_kill.o"}},
		{Mock: true, MockObjects: []string{""}},
	} {
		if err := Init(option); err == nil {
			t.Fatalf("invalid mock selection accepted: %+v", option)
		}
	}
}
