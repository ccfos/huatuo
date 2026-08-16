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

package sqlite

import (
	"fmt"
	"regexp"
	"strings"

	"huatuo-bamai/internal/storage/driver"
)

var safeIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// binaryOpSQL maps comparison operators to their SQL string equivalents.
var binaryOpSQL = map[driver.Op]string{
	driver.OpEq:  "=",
	driver.OpNe:  "!=",
	driver.OpGt:  ">",
	driver.OpGte: ">=",
	driver.OpLt:  "<",
	driver.OpLte: "<=",
}

func buildSelectSQL(collection string, q driver.Query) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}
	if len(q.SearchAfter) > 0 {
		return "", nil, fmt.Errorf("%w: sqlite does not support search_after", driver.ErrUnsupportedOp)
	}

	baseSQL := fmt.Sprintf(`SELECT id, data, fields FROM %s`, quoteIdentifier(collection))
	whereSQL, args, err := buildWhereSQL(q.Filters)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	sb.WriteString(baseSQL)
	if whereSQL != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(whereSQL)
	}

	orderSQL, err := buildOrderSQL(q.Sorts)
	if err != nil {
		return "", nil, err
	}
	if orderSQL != "" {
		sb.WriteString(" ORDER BY ")
		sb.WriteString(orderSQL)
	}

	if q.Limit > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, q.Limit)
	}
	if q.Offset > 0 {
		if q.Limit == 0 {
			sb.WriteString(" LIMIT -1")
		}
		sb.WriteString(" OFFSET ?")
		args = append(args, q.Offset)
	}
	return sb.String(), args, nil
}

func buildCountSQL(collection string, q driver.Query) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}

	baseSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoteIdentifier(collection))
	whereSQL, args, err := buildWhereSQL(q.Filters)
	if err != nil {
		return "", nil, err
	}
	if whereSQL == "" {
		return baseSQL, args, nil
	}
	return baseSQL + " WHERE " + whereSQL, args, nil
}

func buildValuesSQL(collection, field string, q driver.Query, size int) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}
	if size < 0 {
		return "", nil, driver.ErrNegativeSize
	}

	termExpr := jsonExtractExpr(field)
	baseSQL := fmt.Sprintf(`SELECT DISTINCT %s AS term FROM %s`, termExpr, quoteIdentifier(collection))
	whereSQL, args, err := buildWhereSQL(q.Filters)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	sb.WriteString(baseSQL)
	if whereSQL != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(whereSQL)
		sb.WriteString(" AND ")
	} else {
		sb.WriteString(" WHERE ")
	}
	sb.WriteString(termExpr)
	sb.WriteString(" IS NOT NULL ORDER BY term ASC")
	if size > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, size)
	}
	return sb.String(), args, nil
}

func buildWhereSQL(filters []driver.Filter) (string, []any, error) {
	clauses := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))

	for _, filter := range filters {
		if err := validateIdentifier(filter.Field); err != nil {
			return "", nil, err
		}

		fieldExpr := jsonExtractExpr(filter.Field)
		if opStr, ok := binaryOpSQL[filter.Op]; ok {
			clauses = append(clauses, fieldExpr+" "+opStr+" ?")
			args = append(args, driver.NormalizeValue(filter.Value))
		} else if filter.Op == driver.OpIn {
			inValues, err := driver.FlattenInValues(filter.Value)
			if err != nil {
				return "", nil, err
			}
			placeholders := make([]string, len(inValues))
			for i, value := range inValues {
				placeholders[i] = "?"
				args = append(args, value)
			}
			clauses = append(clauses, fmt.Sprintf("%s IN (%s)", fieldExpr, strings.Join(placeholders, ", ")))
		} else {
			return "", nil, driver.ErrUnsupportedOp
		}
	}
	return strings.Join(clauses, " AND "), args, nil
}

func buildOrderSQL(sorts []driver.Sort) (string, error) {
	orderParts := make([]string, 0, len(sorts))
	for _, s := range sorts {
		if err := validateIdentifier(s.Field); err != nil {
			return "", err
		}
		direction := "ASC"
		if s.Desc {
			direction = "DESC"
		}
		orderParts = append(orderParts, fmt.Sprintf("%s %s", jsonExtractExpr(s.Field), direction))
	}
	return strings.Join(orderParts, ", "), nil
}

func jsonExtractExpr(field string) string {
	return fmt.Sprintf("json_extract(fields, '%s')", jsonPath(field))
}

func jsonPath(field string) string {
	return "$." + field
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func validateIdentifier(name string) error {
	if !safeIdentifierPattern.MatchString(name) {
		return driver.ErrInvalidField
	}
	return nil
}
