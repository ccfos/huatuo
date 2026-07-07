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

package pod

import (
	"reflect"
	"testing"
)

func TestLabelHostNamespace_MissingLabel(t *testing.T) {
	c := &Container{
		ID:     "abc123",
		Labels: map[string]any{},
	}
	got := c.LabelHostNamespace()
	if got != "" {
		t.Errorf("LabelHostNamespace() = %q, want empty string when label is missing", got)
	}
}

func TestLabelHostNamespace_NilLabels(t *testing.T) {
	c := &Container{
		ID:     "abc123",
		Labels: nil,
	}
	got := c.LabelHostNamespace()
	if got != "" {
		t.Errorf("LabelHostNamespace() = %q, want empty string when Labels is nil", got)
	}
}

func TestLabelHostNamespace_NilReceiver(t *testing.T) {
	var c *Container
	got := c.LabelHostNamespace()
	if got != "" {
		t.Errorf("LabelHostNamespace() = %q, want empty string when receiver is nil", got)
	}
}

func TestLabelHostNamespace_WrongType(t *testing.T) {
	c := &Container{
		ID: "abc123",
		Labels: map[string]any{
			labelHostNamespace: 12345, // int, not string
		},
	}
	got := c.LabelHostNamespace()
	if got != "" {
		t.Errorf("LabelHostNamespace() = %q, want empty string when label is wrong type", got)
	}
}

func TestLabelHostNamespace_ValidLabel(t *testing.T) {
	c := &Container{
		ID: "abc123",
		Labels: map[string]any{
			labelHostNamespace: "default",
		},
	}
	got := c.LabelHostNamespace()
	if got != "default" {
		t.Errorf("LabelHostNamespace() = %q, want %q", got, "default")
	}
}

func TestRegisterContainerLifeResources_NilType(t *testing.T) {
	err := RegisterContainerLifeResources("test-nil", nil)
	if err == nil {
		t.Error("RegisterContainerLifeResources with nil type should return error")
	}
}

func TestRegisterContainerLifeResources_NonPointerType(t *testing.T) {
	rt := reflect.TypeOf(42) // int, not a pointer
	err := RegisterContainerLifeResources("test-nonptr", rt)
	if err == nil {
		t.Error("RegisterContainerLifeResources with non-pointer type should return error")
	}
}

func TestRegisterContainerLifeResources_PointerToNonStruct(t *testing.T) {
	rt := reflect.TypeOf(new(int)) // *int, pointer to non-struct
	err := RegisterContainerLifeResources("test-ptrnonstruct", rt)
	if err == nil {
		t.Error("RegisterContainerLifeResources with pointer to non-struct should return error")
	}
}
