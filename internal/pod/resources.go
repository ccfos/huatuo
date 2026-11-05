// Copyright 2025, 2026 The HuaTuo Authors
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
	"fmt"
	"reflect"
	"sync"
)

var lifeResourcesTpl sync.Map

// RegisterContainerLifeResources automatically registers container life resources.
//
// When containers are created, they will be added automatically.
// When containers are deleted, they will be removed automatically.
//
// Example:
//
//	data := map[string]int{"acct": 0, "usage": 0}
//	RegisterContainerLifeResources("cpu", reflect.TypeOf(data))
func RegisterContainerLifeResources(key string, anythingType reflect.Type) error {
	if anythingType.Kind() != reflect.Pointer && anythingType.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("invalid anythingType: %v, only support pointer of struct", anythingType)
	}

	if _, loaded := lifeResourcesTpl.LoadOrStore(key, anythingType); loaded {
		return fmt.Errorf("key %s already exists in lifeResourcesTpl", key)
	}

	return nil
}

// newLifeResources allocates state only when the Store creates an instance.
func newLifeResources() map[string]any {
	resources := make(map[string]any)
	lifeResourcesTpl.Range(func(key, anythingType any) bool {
		typ := anythingType.(reflect.Type)
		resources[key.(string)] = reflect.New(typ.Elem()).Interface()
		return true
	})
	return resources
}
