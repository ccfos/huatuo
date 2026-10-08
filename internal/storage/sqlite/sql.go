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

	"github.com/ccfos/huatuo/internal/storage/driver"
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

	sb.WriteString(" LIMIT ?")
	args = append(args, q.Limit)
	if q.Offset > 0 {
		sb.WriteString(" OFFSET ?")
		args = append(args, q.Offset)
	}
	return sb.String(), args, nil
}

func buildCountSQL(collection string, q driver.Query) (string, []any, error) {
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

func buildDeleteSQL(collection string, q driver.DeleteQuery) (string, []any, error) {
	if len(q.Filters) == 0 {
		return "", nil, fmt.Errorf(
			"%w: query deletion requires at least one filter",
			driver.ErrInvalidQuery,
		)
	}
	if q.Limit < 0 {
		return "", nil, fmt.Errorf(
			"%w: delete limit must be non-negative",
			driver.ErrInvalidQuery,
		)
	}
	whereSQL, args, err := buildWhereSQL(q.Filters)
	if err != nil {
		return "", nil, err
	}
	var selection strings.Builder
	selection.WriteString("SELECT id FROM ")
	selection.WriteString(quoteIdentifier(collection))
	selection.WriteString(" WHERE ")
	selection.WriteString(whereSQL)
	if q.Limit > 0 {
		selection.WriteString(" LIMIT ?")
		args = append(args, q.Limit)
	}

	statement := fmt.Sprintf(
		"DELETE FROM %s WHERE id IN (%s)",
		quoteIdentifier(collection),
		selection.String(),
	)
	return statement, args, nil
}

func buildWhereSQL(filters []driver.Filter) (string, []any, error) {
	clauses := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))

	for _, filter := range filters {
		if err := validateIdentifier(filter.Field); err != nil {
			return "", nil, err
		}

		value, err := driver.NormalizeFilterValue(filter)
		if err != nil {
			return "", nil, err
		}

		fieldExpr := jsonExtractExpr(filter.Field)
		if opStr, ok := binaryOpSQL[filter.Op]; ok {
			clauses = append(clauses, fieldExpr+" "+opStr+" ?")
			args = append(args, value)
		} else if filter.Op == driver.OpIn {
			inValues := value.([]any)
			placeholders := make([]string, len(inValues))
			for i, value := range inValues {
				placeholders[i] = "?"
				args = append(args, value)
			}
			clauses = append(clauses, fmt.Sprintf("%s IN (%s)", fieldExpr, strings.Join(placeholders, ", ")))
		} else if filter.Op == driver.OpExists {
			clauses = append(clauses, fieldExpr+" IS NOT NULL")
		} else if filter.Op == driver.OpNotExists {
			clauses = append(clauses, fieldExpr+" IS NULL")
		} else if filter.Op == driver.OpPrefix {
			clauses = append(clauses, fieldExpr+" LIKE ? ESCAPE '\\'")
			args = append(args, likeEscape(value.(string))+"%")
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
	if field == "id" {
		return quoteIdentifier(field)
	}
	return fmt.Sprintf("json_extract(fields, '%s')", jsonPath(field))
}

func jsonPath(field string) string {
	return "$." + field
}

// likeEscape escapes LIKE wildcards so a prefix matches literal characters only.
func likeEscape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
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
