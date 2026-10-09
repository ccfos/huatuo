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

package driver

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
)

func TestNormalizeFilterValue(t *testing.T) {
	instant := time.Date(2026, 10, 8, 8, 0, 0, 123456789, time.FixedZone("east", 8*60*60))
	const formatted = "2026-10-08T00:00:00.123456789Z"
	tests := []struct {
		name  string
		op    Op
		value any
		want  any
	}{
		{"empty string", OpEq, "", ""},
		{"false", OpNe, false, false},
		{"zero", OpEq, 0, 0},
		{"large integer", OpGt, int64(9007199254740993), int64(9007199254740993)},
		{"unsigned integer", OpLte, uint64(math.MaxUint64), uint64(math.MaxUint64)},
		{"float", OpLt, 1.5, 1.5},
		{"time equal", OpEq, instant, formatted},
		{"timestamp range", OpGte, timeutil.Timestamp{Time: instant}, formatted},
		{"time array", OpIn, [1]time.Time{instant}, []any{formatted}},
		{"timestamp slice", OpIn, []timeutil.Timestamp{{Time: instant}}, []any{formatted}},
		{"exists", OpExists, nil, nil},
		{"not exists", OpNotExists, nil, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeFilterValue(Filter{Field: "value", Op: test.op, Value: test.value})
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("NormalizeFilterValue() = (%#v, %v), want (%#v, nil)", got, err, test.want)
			}
		})
	}
}

func TestNormalizeFilterValueRejectsInvalidOperands(t *testing.T) {
	tests := []struct {
		name  string
		op    Op
		value any
		want  error
	}{
		{"unknown operator", "unknown", "a", ErrUnsupportedOp},
		{"equal null", OpEq, nil, ErrInvalidQuery},
		{"not equal null", OpNe, nil, ErrInvalidQuery},
		{"typed nil", OpEq, (*string)(nil), ErrInvalidQuery},
		{"map", OpEq, map[string]int{"a": 1}, ErrInvalidQuery},
		{"slice", OpNe, []int{1}, ErrInvalidQuery},
		{"bool range", OpGt, true, ErrInvalidQuery},
		{"nan", OpEq, math.NaN(), ErrInvalidQuery},
		{"infinity", OpLt, math.Inf(1), ErrInvalidQuery},
		{"float32 infinity", OpGte, float32(math.Inf(-1)), ErrInvalidQuery},
		{"exists value", OpExists, "", ErrInvalidQuery},
		{"not exists value", OpNotExists, false, ErrInvalidQuery},
		{"in scalar", OpIn, "a", ErrInRequiresSlice},
		{"in empty", OpIn, []string{}, ErrInRequiresNonEmpty},
		{"in nil", OpIn, []string(nil), ErrInRequiresNonEmpty},
		{"in null element", OpIn, []any{"a", nil}, ErrInvalidQuery},
		{"in nested slice", OpIn, [][]int{{1}}, ErrInvalidQuery},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeFilterValue(Filter{Field: "value", Op: test.op, Value: test.value})
			if !errors.Is(err, test.want) {
				t.Fatalf("NormalizeFilterValue() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeFilterValuePreservesInput(t *testing.T) {
	instant := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	values := []any{instant}
	normalized, err := NormalizeFilterValue(Filter{Field: "time", Op: OpIn, Value: values})
	if err != nil {
		t.Fatal(err)
	}
	normalized.([]any)[0] = "changed"
	if values[0] != instant {
		t.Fatalf("input value was modified: %v", values)
	}
}
