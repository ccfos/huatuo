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

package provider

import "testing"

func TestProfilerEventBaseHasStackAcceptsStackIDZero(t *testing.T) {
	tests := []struct {
		name string
		base *ProfilerEventBase
		want bool
	}{
		{name: "nil", base: nil, want: false},
		{name: "no stack", base: &ProfilerEventBase{Kernstack: -1, Userstack: -1}, want: false},
		{name: "kernel stack zero", base: &ProfilerEventBase{Kernstack: 0, Userstack: -1}, want: true},
		{name: "user stack zero", base: &ProfilerEventBase{Kernstack: -1, Userstack: 0}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.base.hasStack(); got != tt.want {
				t.Fatalf("hasStack() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestUserStackCacheKeyIncludesPID(t *testing.T) {
	cache := map[userStackCacheKey]string{
		{id: 7, pid: 100}: "first process",
		{id: 7, pid: 200}: "second process",
	}
	if len(cache) != 2 {
		t.Fatalf("cache entries = %d, want stack ID reused independently by two processes", len(cache))
	}
}
