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

package testutils

import (
	"fmt"
	"reflect"
	"testing"
)

// PopulateCloneSource initializes every cloneable field so alias checks also
// cover reference fields added after the test was written.
func PopulateCloneSource(tb testing.TB, target any) {
	tb.Helper()

	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		tb.Fatal("clone source must be a non-nil pointer")
	}
	populateCloneValue(tb, value, value.Type().String())
}

// AssertDeepClone verifies value equality without shared mutable references.
func AssertDeepClone(tb testing.TB, source, clone any) {
	tb.Helper()

	if !reflect.DeepEqual(source, clone) {
		tb.Fatalf("clone mismatch:\noriginal: %#v\ncopy:     %#v", source, clone)
	}
	assertNoSharedReferences(
		tb,
		reflect.ValueOf(source),
		reflect.ValueOf(clone),
		reflect.TypeOf(source).String(),
	)
}

func populateCloneValue(tb testing.TB, value reflect.Value, path string) {
	tb.Helper()

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		populateCloneValue(tb, value.Elem(), path)
	case reflect.Interface:
		tb.Fatalf("%s: interface fields require an explicit clone test value", path)
	case reflect.Struct:
		for i := range value.NumField() {
			fieldType := value.Type().Field(i)
			fieldPath := path + "." + fieldType.Name
			if fieldType.PkgPath != "" {
				if isMutableReference(value.Field(i).Kind()) {
					tb.Fatalf("%s: unexported mutable field cannot be verified", fieldPath)
				}
				continue
			}
			populateCloneValue(tb, value.Field(i), fieldPath)
		}
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		populateCloneValue(tb, value.Index(0), path+"[0]")
	case reflect.Map:
		value.Set(reflect.MakeMapWithSize(value.Type(), 1))
		key := reflect.New(value.Type().Key()).Elem()
		populateCloneValue(tb, key, path+".key")
		item := reflect.New(value.Type().Elem()).Elem()
		populateCloneValue(tb, item, path+".value")
		value.SetMapIndex(key, item)
	case reflect.Array:
		for i := range value.Len() {
			populateCloneValue(tb, value.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.String:
		value.SetString("clone-test")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(1)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(1)
	case reflect.Complex64, reflect.Complex128:
		value.SetComplex(1)
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		tb.Fatalf("%s: %s fields are not supported in immutable config", path, value.Kind())
	}
}

func assertNoSharedReferences(tb testing.TB, source, clone reflect.Value, path string) {
	tb.Helper()

	if !source.IsValid() || !clone.IsValid() {
		return
	}
	switch source.Kind() {
	case reflect.Pointer:
		if source.IsNil() {
			return
		}
		if source.Pointer() == clone.Pointer() {
			tb.Fatalf("%s: clone shares pointer with source", path)
		}
		assertNoSharedReferences(tb, source.Elem(), clone.Elem(), path)
	case reflect.Interface:
		if source.IsNil() {
			return
		}
		assertNoSharedReferences(tb, source.Elem(), clone.Elem(), path)
	case reflect.Struct:
		for i := range source.NumField() {
			fieldType := source.Type().Field(i)
			if fieldType.PkgPath != "" {
				continue
			}
			assertNoSharedReferences(
				tb,
				source.Field(i),
				clone.Field(i),
				path+"."+fieldType.Name,
			)
		}
	case reflect.Slice:
		if source.IsNil() {
			return
		}
		if source.Pointer() == clone.Pointer() {
			tb.Fatalf("%s: clone shares slice storage with source", path)
		}
		for i := range source.Len() {
			assertNoSharedReferences(
				tb,
				source.Index(i),
				clone.Index(i),
				fmt.Sprintf("%s[%d]", path, i),
			)
		}
	case reflect.Map:
		if source.IsNil() {
			return
		}
		if source.Pointer() == clone.Pointer() {
			tb.Fatalf("%s: clone shares map storage with source", path)
		}
		for _, key := range source.MapKeys() {
			assertNoSharedReferences(
				tb,
				source.MapIndex(key),
				clone.MapIndex(key),
				fmt.Sprintf("%s[%v]", path, key.Interface()),
			)
		}
	case reflect.Array:
		for i := range source.Len() {
			assertNoSharedReferences(
				tb,
				source.Index(i),
				clone.Index(i),
				fmt.Sprintf("%s[%d]", path, i),
			)
		}
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		tb.Fatalf("%s: %s fields are not supported in immutable config", path, source.Kind())
	}
}

func isMutableReference(kind reflect.Kind) bool {
	switch kind {
	case reflect.Pointer,
		reflect.Interface,
		reflect.Slice,
		reflect.Map,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return true
	default:
		return false
	}
}
