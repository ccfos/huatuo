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

package procutil

import (
	"reflect"
	"testing"
)

func TestGroupProcessesByRootTraversesAllAncestors(t *testing.T) {
	processes := []procInfo{
		{pid: 100, ppid: 1},
		{pid: 101, ppid: 100},
		{pid: 102, ppid: 101},
		{pid: 200, ppid: 1},
	}
	got, err := groupProcessesByRoot(processes)
	if err != nil {
		t.Fatalf("groupProcessesByRoot() error = %v", err)
	}
	want := map[int][]int{
		100: {100, 101, 102},
		200: {200},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groupProcessesByRoot() = %v, want %v", got, want)
	}
}

func TestGroupProcessesByRootRejectsParentCycle(t *testing.T) {
	processes := []procInfo{
		{pid: 100, ppid: 101},
		{pid: 101, ppid: 100},
	}

	if _, err := groupProcessesByRoot(processes); err == nil {
		t.Fatal("groupProcessesByRoot() error = nil, want parent cycle error")
	}
}
