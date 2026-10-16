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
	"fmt"
	"math"
)

// NormalizeFilterValue validates the operator and its operands and formats time
// values consistently across backends. OpIn returns a newly allocated []any.
func NormalizeFilterValue(filter Filter) (any, error) {
	switch filter.Op {
	case OpExists, OpNotExists:
		if filter.Value != nil {
			return nil, fmt.Errorf("%w: field %q operator %s requires no value", ErrInvalidQuery, filter.Field, filter.Op)
		}

		return nil, nil
	case OpPrefix:
		value, ok := filter.Value.(string)
		if !ok {
			return nil, fmt.Errorf(
				"%w: field %q operator %s requires a string value",
				ErrInvalidQuery, filter.Field, filter.Op,
			)
		}

		return value, nil
	case OpIn:
		values, err := FlattenInValues(filter.Value)
		if err != nil {
			return nil, err
		}

		for i, value := range values {
			values[i], err = normalizeComparisonValue(value, false)
			if err != nil {
				return nil, fmt.Errorf("field %q operator %s element %d: %w", filter.Field, filter.Op, i, err)
			}
		}

		return values, nil
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte:
		ordered := filter.Op != OpEq && filter.Op != OpNe
		value, err := normalizeComparisonValue(filter.Value, ordered)
		if err != nil {
			return nil, fmt.Errorf("field %q operator %s: %w", filter.Field, filter.Op, err)
		}

		return value, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedOp, filter.Op)
	}
}

func normalizeComparisonValue(value any, ordered bool) (any, error) {
	value = NormalizeValue(value)
	switch v := value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return value, nil
	case bool:
		if ordered {
			return nil, fmt.Errorf("%w: range comparison does not accept booleans", ErrInvalidQuery)
		}

		return value, nil
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("%w: comparison value must be finite", ErrInvalidQuery)
		}

		return value, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("%w: comparison value must be finite", ErrInvalidQuery)
		}

		return value, nil
	}

	return nil, fmt.Errorf("%w: unsupported comparison value of type %T", ErrInvalidQuery, value)
}
