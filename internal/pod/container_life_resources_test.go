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

package pod

import (
	"reflect"
	"testing"
)

type lifeResourceStruct struct {
	Acct  int
	Usage int
}

func TestRegisterContainerLifeResourcesAcceptsPointerOfStruct(t *testing.T) {
	if err := RegisterContainerLifeResources("cpu-ok", reflect.TypeOf(&lifeResourceStruct{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterContainerLifeResourcesRejectsNonPointer(t *testing.T) {
	err := RegisterContainerLifeResources("cpu-value", reflect.TypeOf(lifeResourceStruct{}))
	if err == nil {
		t.Fatal("expected an error for a non-pointer type")
	}
}

func TestRegisterContainerLifeResourcesRejectsNilType(t *testing.T) {
	err := RegisterContainerLifeResources("cpu-nil", nil)
	if err == nil {
		t.Fatal("expected an error for a nil type")
	}
}

func TestRegisterContainerLifeResourcesRejectsPointerToNonStruct(t *testing.T) {
	err := RegisterContainerLifeResources("cpu-int", reflect.TypeOf(new(int)))
	if err == nil {
		t.Fatal("expected an error for a pointer to a non-struct type")
	}
}

func TestRegisterContainerLifeResourcesRejectsDuplicateKey(t *testing.T) {
	if err := RegisterContainerLifeResources("cpu-dup", reflect.TypeOf(&lifeResourceStruct{})); err != nil {
		t.Fatalf("first registration should succeed: %v", err)
	}
	if err := RegisterContainerLifeResources("cpu-dup", reflect.TypeOf(&lifeResourceStruct{})); err == nil {
		t.Fatal("expected an error for a duplicate key")
	}
}
