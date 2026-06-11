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
	"encoding/json"
	"sort"
	"strings"
)

type flameTreeNode struct {
	name     string
	self     int64
	total    int64
	children map[string]*flameTreeNode
}

func MapToFlameData(data map[string]int64) ([]byte, error) {
	if len(data) == 0 {
		return []byte("[]"), nil
	}

	root := &flameTreeNode{name: "all", children: make(map[string]*flameTreeNode)}

	for stack, count := range data {
		frames := strings.Split(stack, ";")
		node := root
		for _, frame := range frames {
			frame = strings.TrimSpace(frame)
			if frame == "" {
				continue
			}
			if _, ok := node.children[frame]; !ok {
				node.children[frame] = &flameTreeNode{name: frame, children: make(map[string]*flameTreeNode)}
			}
			node = node.children[frame]
		}
		node.self += count
	}

	calculateTotal(root)

	var result []FrameData
	walkFlameTree(root, 0, &result)

	return json.Marshal(result)
}

func calculateTotal(node *flameTreeNode) int64 {
	total := node.self
	for _, child := range node.children {
		total += calculateTotal(child)
	}
	node.total = total
	return total
}

func walkFlameTree(node *flameTreeNode, level int64, result *[]FrameData) {
	*result = append(*result, FrameData{
		Level: level,
		Value: node.total,
		Self:  node.self,
		Label: node.name,
	})

	children := make([]*flameTreeNode, 0, len(node.children))
	for _, child := range node.children {
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].total != children[j].total {
			return children[i].total > children[j].total
		}
		return children[i].name < children[j].name
	})

	for _, child := range children {
		walkFlameTree(child, level+1, result)
	}
}
