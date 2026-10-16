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
	"fmt"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

func TestEnumFieldPreservesLabelsAtCapacity(t *testing.T) {
	for _, count := range []int{1 << 16, (1 << 16) + 1, 70_000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			e := NewEnumField("label", data.Labels{"source": "test"})
			for i := range count {
				e.Append(fmt.Sprintf("symbol_%d", i))
			}
			e.Append("symbol_0")
			e.Append(fmt.Sprintf("symbol_%d", count-1))
			field := e.GetField()
			if count > 1<<16 {
				if e.GetValuesMap() != nil || field.Config != nil {
					t.Fatal("string fallback retained an enum dictionary or config")
				}
			} else if len(e.GetValuesMap()) != count {
				t.Fatalf("enum dictionary length = %d, want %d", len(e.GetValuesMap()), count)
			}
			if field.Len() != count+2 || field.Name != "label" || field.Labels["source"] != "test" {
				t.Fatalf("field metadata or row count changed: %s, %v, %d", field.Name, field.Labels, field.Len())
			}
			for i := range field.Len() {
				var got string
				switch value := field.At(i).(type) {
				case data.EnumItemIndex:
					got = field.Config.TypeConfig.Enum.Text[value]
				case string:
					got = value
				default:
					t.Fatalf("unexpected label type %T", value)
				}
				index := i
				if i == count {
					index = 0
				} else if i == count+1 {
					index = count - 1
				}
				if want := fmt.Sprintf("symbol_%d", index); got != want {
					t.Fatalf("row %d label = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestTreeToNestedSetDataFrameBeyondEnumCapacity(t *testing.T) {
	const children = 1 << 16
	tree := &ProfileTree{Name: "root", Value: children, Nodes: make([]*ProfileTree, children)}
	for i := range tree.Nodes {
		tree.Nodes[i] = &ProfileTree{Name: fmt.Sprintf("child_%d", i), Level: 1, Value: 1, Self: 1}
	}
	frame, _ := TreeToNestedSetDataFrame(tree, "bytes")
	for _, field := range frame.Fields {
		if field.Len() != children+1 {
			t.Fatalf("%s has %d rows, want %d", field.Name, field.Len(), children+1)
		}
	}
	labels := frame.Fields[3]
	for i := range labels.Len() {
		want := "root"
		if i > 0 {
			want = fmt.Sprintf("child_%d", i-1)
		}
		if got, ok := labels.At(i).(string); !ok || got != want {
			t.Fatalf("row %d label = %v, want %q", i, labels.At(i), want)
		}
	}
	if frame.Fields[1].Config.Unit != "bytes" || frame.Meta.PreferredVisualization != "flamegraph" {
		t.Fatal("frame visualization metadata changed")
	}
}
