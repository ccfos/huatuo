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

package flamegraph

import (
	"slices"
	"sync"
	"testing"
)

func TestWalkTreePreservesChildBackingArray(t *testing.T) {
	grandchild := &ProfileTree{Name: "grandchild"}
	reserved := &ProfileTree{Name: "reserved"}
	children := []*ProfileTree{grandchild, reserved}
	first := &ProfileTree{Name: "first", Nodes: children[:1]}
	second := &ProfileTree{Name: "second"}
	root := &ProfileTree{Name: "root", Nodes: []*ProfileTree{first, second}}
	want := []string{"root", "first", "grandchild", "second"}

	for range 2 {
		var visited []string
		walkTree(root, func(node *ProfileTree) {
			visited = append(visited, node.Name)
		})
		if !slices.Equal(visited, want) {
			t.Fatalf("walk order = %v, want %v", visited, want)
		}
		if children[1] != reserved {
			t.Fatal("walk overwrote the caller's child backing array")
		}
	}
}

func TestWalkTreeConcurrentReaders(t *testing.T) {
	children := make([]*ProfileTree, 1, 2)
	children[0] = &ProfileTree{Name: "grandchild"}
	root := &ProfileTree{Nodes: []*ProfileTree{
		{Name: "first", Nodes: children},
		{Name: "second"},
	}}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				walkTree(root, func(*ProfileTree) {})
			}
		}()
	}
	readers.Wait()
}
