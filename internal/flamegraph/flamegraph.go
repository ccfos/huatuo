// Copyright 2025 The HuaTuo Authors
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
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Level is a depth array of flame graph data
type Level struct {
	Values []int64
}

// Flamebearer is pyroscope flame graph data
type Flamebearer struct {
	Names   []string
	Levels  []*Level
	Total   int64
	MaxSelf int64
}

// StartOffest is offset of the bar relative to previous sibling
const StartOffest = 0

// ValueOffest is value or width of the bar
const ValueOffest = 1

// SelfOffest is self value of the bar
const SelfOffest = 2

// NameOffest is index into the names array
const NameOffest = 3

// ItemOffest Next bar. Each bar of the profile is represented by 4 number in a flat array.
const ItemOffest = 4

// ProfileTree grafana tree struct
type ProfileTree struct {
	Start int64
	Value int64
	Self  int64
	Level int
	Name  string
	Nodes []*ProfileTree
}

// LevelsToTree converts flamebearer format into a tree. This is needed to then convert it into nested set format
func LevelsToTree(levels []*Level, names []string) *ProfileTree {
	if len(levels) == 0 || levels[0] == nil || len(levels[0].Values) < ItemOffest {
		return nil
	}
	rootName, ok := flamebearerName(names, levels[0].Values[NameOffest])
	if !ok {
		return nil
	}

	tree := &ProfileTree{
		Start: 0,
		Value: levels[0].Values[ValueOffest],
		Self:  levels[0].Values[SelfOffest],
		Level: 0,
		Name:  rootName,
	}

	parentsStack := []*ProfileTree{tree}
	currentLevel := 1

	// Cycle through each level
	for {
		if currentLevel >= len(levels) {
			break
		}

		// If we still have levels to go, this should not happen. Something is probably wrong with the flamebearer data.
		if len(parentsStack) == 0 {
			break
		}

		if levels[currentLevel] == nil {
			break
		}

		var nextParentsStack []*ProfileTree
		currentParent := parentsStack[:1][0]
		parentsStack = parentsStack[1:]
		itemIndex := 0
		// cumulative offset as items in flamebearer format have just relative to prev item
		offset := int64(0)

		// Cycle through bar in a level
		for {
			if itemIndex+ItemOffest > len(levels[currentLevel].Values) {
				break
			}

			itemStart := levels[currentLevel].Values[itemIndex+StartOffest] + offset
			itemValue := levels[currentLevel].Values[itemIndex+ValueOffest]
			selfValue := levels[currentLevel].Values[itemIndex+SelfOffest]
			itemEnd := itemStart + itemValue
			parentEnd := currentParent.Start + currentParent.Value

			if itemStart >= currentParent.Start && itemEnd <= parentEnd {
				name, ok := flamebearerName(names, levels[currentLevel].Values[itemIndex+NameOffest])
				if !ok {
					break
				}
				// We have an item that is in the bounds of current parent item, so it should be its child
				treeItem := &ProfileTree{
					Start: itemStart,
					Value: itemValue,
					Self:  selfValue,
					Level: currentLevel,
					Name:  name,
				}
				// Add to parent
				currentParent.Nodes = append(currentParent.Nodes, treeItem)
				// Add this item as parent for the next level
				nextParentsStack = append(nextParentsStack, treeItem)
				itemIndex += ItemOffest

				// Update offset for next item. This is changing relative offset to absolute one.
				offset = itemEnd
			} else {
				// We went out of parents bounds so lets move to next parent. We will evaluate the same item again, but
				// we will check if it is a child of the next parent item in line.
				if len(parentsStack) == 0 {
					break
				}
				currentParent = parentsStack[:1][0]
				parentsStack = parentsStack[1:]
				continue
			}
		}
		parentsStack = nextParentsStack
		currentLevel++
	}

	return tree
}

func flamebearerName(names []string, index int64) (string, bool) {
	if index < 0 || index >= int64(len(names)) {
		return "", false
	}

	return names[index], true
}

// TreeToNestedSetDataFrame walks the tree depth first and adds items into the dataframe. This is a nested set format
func TreeToNestedSetDataFrame(tree *ProfileTree, unit string) (*data.Frame, *EnumField) {
	frame := data.NewFrame("response")
	frame.Meta = &data.FrameMeta{PreferredVisualization: "flamegraph"}

	levelField := data.NewField("level", nil, []int64{})
	valueField := data.NewField("value", nil, []int64{})
	selfField := data.NewField("self", nil, []int64{})

	// profileTypeID should encode the type of the profile with unit being the 3rd part
	valueField.Config = &data.FieldConfig{Unit: unit}
	selfField.Config = &data.FieldConfig{Unit: unit}
	frame.Fields = data.Fields{levelField, valueField, selfField}

	labelField := NewEnumField("label", nil)

	// Tree can be nil if profile was empty, we can still send empty frame in that case
	if tree != nil {
		walkTree(tree, func(tree *ProfileTree) {
			levelField.Append(int64(tree.Level))
			valueField.Append(tree.Value)
			selfField.Append(tree.Self)
			labelField.Append(tree.Name)
		})
	}
	frame.Fields = append(frame.Fields, labelField.GetField())
	return frame, labelField
}

// EnumField label struct
type EnumField struct {
	field     *data.Field
	valuesMap map[string]data.EnumItemIndex
	counter   data.EnumItemIndex
}

// NewEnumField add a new label field
func NewEnumField(name string, labels data.Labels) *EnumField {
	return &EnumField{
		field:     data.NewField(name, labels, []data.EnumItemIndex{}),
		valuesMap: make(map[string]data.EnumItemIndex),
	}
}

// GetValuesMap get label.valuesMap
func (e *EnumField) GetValuesMap() map[string]data.EnumItemIndex {
	return e.valuesMap
}

// Append data
func (e *EnumField) Append(value string) {
	if valueIndex, ok := e.valuesMap[value]; ok {
		e.field.Append(valueIndex)
	} else {
		e.valuesMap[value] = e.counter
		e.field.Append(e.counter)
		e.counter++
	}
}

// GetField get fields
func (e *EnumField) GetField() *data.Field {
	s := make([]string, len(e.valuesMap))
	for k, v := range e.valuesMap {
		s[v] = k
	}

	e.field.SetConfig(&data.FieldConfig{
		TypeConfig: &data.FieldTypeConfig{
			Enum: &data.EnumFieldConfig{
				Text: s,
			},
		},
	})

	return e.field
}

func walkTree(tree *ProfileTree, fn func(tree *ProfileTree)) {
	fn(tree)
	stack := tree.Nodes

	for {
		if len(stack) == 0 {
			break
		}

		fn(stack[0])
		if stack[0].Nodes != nil {
			stack = append(stack[0].Nodes, stack[1:]...)
		} else {
			stack = stack[1:]
		}
	}
}
