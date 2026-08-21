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

package doris

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

// Reserved column names. Indexed fields are prefixed so a field literally
// named "data" cannot collide with the document payload column.
const (
	colID     = "id"
	colData   = "data"
	colFields = "fields"

	fieldColumnPrefix = "f_"
)

// timeLayout is lexically sortable in UTC, so string comparison on a VARCHAR
// column yields chronological ordering. Every time value — stored or filtered
// — is funnelled through normalizeValue into this one layout; mixing layouts
// silently breaks range filters.
const timeLayout = "2006-01-02 15:04:05.000000"

// inputTimeLayouts are the layouts callers are known to send as filter values.
// driver.NormalizeValue emits the second one; the profile query service emits
// RFC3339 variants.
var inputTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.000 -0700",
	time.RFC3339,
	timeLayout,
}

var nonColumnChars = regexp.MustCompile(`[^a-z0-9_]+`)

var binaryOpSQL = map[driver.Op]string{
	driver.OpEq:  "=",
	driver.OpNe:  "!=",
	driver.OpGt:  ">",
	driver.OpGte: ">=",
	driver.OpLt:  "<",
	driver.OpLte: "<=",
}

// resolveColumn maps a query field name onto a physical column.
//
// The ".keyword" suffix is an Elasticsearch mapping artifact that the profile
// query service appends to every string filter; outside Elasticsearch it names
// no field, so it is stripped here rather than requiring every caller to know
// which backend it is talking to.
func resolveColumn(field string) (string, error) {
	name := strings.TrimSuffix(field, ".keyword")
	if name == "" {
		return "", driver.ErrInvalidField
	}

	sanitized := nonColumnChars.ReplaceAllString(strings.ToLower(name), "_")
	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		return "", driver.ErrInvalidField
	}

	return fieldColumnPrefix + sanitized, nil
}

// normalizeValue converts time values — however the caller spelled them — into
// the single stored layout so range comparisons stay chronological.
//
// DATETIME in Doris carries no zone, so values are rendered in the server's
// session zone. Writing UTC wall-clock instead would stay self-consistent for
// this backend's own filters while putting every row hours away from what the
// server's NOW() and any external tool consider the same instant.
func normalizeValue(value any, loc *time.Location) any {
	if loc == nil {
		loc = time.UTC
	}

	switch typed := value.(type) {
	case time.Time:
		return typed.In(loc).Format(timeLayout)
	case string:
		for _, layout := range inputTimeLayouts {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.In(loc).Format(timeLayout)
			}
		}
		return typed
	default:
		return value
	}
}

func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func qualified(database, table string) string {
	return quote(database) + "." + quote(table)
}

func validateIdentifier(name string) error {
	if name == "" || strings.ContainsAny(name, "`\x00") {
		return driver.ErrInvalidField
	}
	return nil
}

// buildCreateTableSQL promotes every declared index field to its own column.
//
// Doris has no expression index over a JSON blob, so the queryable fields
// become real columns; `fields` keeps the full map for round-tripping values
// that were never declared as indexes.
// Daily partitions suit every collection this backend stores: both are
// append-mostly and queried by time range. Pre-creating a few days keeps a
// clock skew or a burst of backfilled rows from missing a partition.
const (
	partitionTimeUnit    = "DAY"
	partitionsAhead      = 3
	defaultRetentionDays = 30

	// indexParser tokenizes on non-alphanumeric boundaries, which suits the
	// identifiers indexed here — hostnames, container IDs, tracer names.
	indexParser = "basic"
)

// TableOptions carries the physical layout choices for a Doris table.
type TableOptions struct {
	Buckets  int
	Replicas int

	// PartitionField names a declared KindTime field to range-partition on.
	// Empty disables partitioning. An unpartitioned table works but forfeits
	// partition pruning and time-based retention, so callers should set it.
	PartitionField string
	// RetentionDays is how long partitions are kept; older ones are dropped.
	RetentionDays int
}

func buildCreateTableSQL(database, table string, indexes []driver.Index, opts TableOptions) (string, error) {
	if err := validateIdentifier(database); err != nil {
		return "", err
	}
	if err := validateIdentifier(table); err != nil {
		return "", err
	}

	partitionColumn, err := partitionColumnOf(indexes, opts.PartitionField)
	if err != nil {
		return "", err
	}

	// Doris requires key columns first, and a range-partition column must be
	// part of the key. Putting the time column ahead of id also makes the
	// prefix index useful for the time-range filters every query carries.
	keys := []string{}
	columns := []string{}
	if partitionColumn != "" {
		keys = append(keys, quote(partitionColumn))
		columns = append(columns, quote(partitionColumn)+" DATETIME(6) NOT NULL")
	}
	keys = append(keys, quote(colID))
	columns = append(columns, quote(colID)+" VARCHAR(255) NOT NULL")

	seen := map[string]bool{partitionColumn: true, colID: true}
	textColumns := []string{}
	for _, idx := range indexes {
		column, err := resolveColumn(idx.Field)
		if err != nil {
			return "", err
		}
		if seen[column] {
			continue
		}
		seen[column] = true
		columns = append(columns, quote(column)+" "+columnType(idx.Kind)+" NULL")
		if idx.Kind != driver.KindTime {
			textColumns = append(textColumns, column)
		}
	}
	// fields is a small flat scalar map, so VARIANT keeps every field a mapper
	// emits queryable even when it was never declared as an index — a field
	// that is not declared then degrades to "queryable but unindexed" instead
	// of vanishing.
	//
	// data stays opaque: Record.Data is whatever Mapper.Encode produced, and
	// the backend must round-trip it untouched. VARIANT would both assume it
	// is a JSON object and columnarize a payload nothing ever queries into.
	columns = append(columns,
		quote(colFields)+" VARIANT NULL",
		quote(colData)+" STRING NULL",
	)

	// Inverted indexes go on the text columns queries filter by. The time
	// columns are covered by partition pruning and the key prefix, and the
	// payload columns are megabyte-scale documents nothing searches inside.
	//
	// A tokenized index answers MATCH, not "=", so equality predicates still
	// scan — acceptable because partition pruning bounds what they scan.
	for _, column := range textColumns {
		columns = append(columns, fmt.Sprintf(
			"INDEX %s (%s) USING INVERTED PROPERTIES(\"parser\" = \"%s\")",
			quote("idx_"+column), quote(column), indexParser))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "CREATE TABLE IF NOT EXISTS %s (\n  %s\n)\nUNIQUE KEY(%s)\n",
		qualified(database, table), strings.Join(columns, ",\n  "), strings.Join(keys, ", "))
	if partitionColumn != "" {
		fmt.Fprintf(&sb, "PARTITION BY RANGE(%s) ()\n", quote(partitionColumn))
	}
	fmt.Fprintf(&sb, "DISTRIBUTED BY HASH(%s) BUCKETS %d\n", quote(colID), opts.Buckets)

	properties := []string{fmt.Sprintf("\"replication_num\" = \"%d\"", opts.Replicas)}
	if partitionColumn != "" {
		retention := opts.RetentionDays
		if retention <= 0 {
			retention = defaultRetentionDays
		}
		properties = append(properties,
			"\"dynamic_partition.enable\" = \"true\"",
			fmt.Sprintf("\"dynamic_partition.time_unit\" = \"%s\"", partitionTimeUnit),
			// Doris counts retained history backwards from today.
			fmt.Sprintf("\"dynamic_partition.start\" = \"-%d\"", retention),
			fmt.Sprintf("\"dynamic_partition.end\" = \"%d\"", partitionsAhead),
			"\"dynamic_partition.prefix\" = \"p\"",
			fmt.Sprintf("\"dynamic_partition.buckets\" = \"%d\"", opts.Buckets),
			"\"dynamic_partition.create_history_partition\" = \"true\"",
		)
	}
	fmt.Fprintf(&sb, "PROPERTIES(\n  %s\n)", strings.Join(properties, ",\n  "))

	return sb.String(), nil
}

// partitionColumnOf resolves the configured partition field, rejecting a field
// that was never declared or that is not a time field — silently falling back
// to an unpartitioned table would hide a misconfiguration until the table grew.
func partitionColumnOf(indexes []driver.Index, field string) (string, error) {
	if field == "" {
		return "", nil
	}

	wanted, err := resolveColumn(field)
	if err != nil {
		return "", err
	}
	for _, idx := range indexes {
		column, err := resolveColumn(idx.Field)
		if err != nil {
			return "", err
		}
		if column != wanted {
			continue
		}
		if idx.Kind != driver.KindTime {
			return "", fmt.Errorf("%w: partition field %q is not a time field", driver.ErrInvalidField, field)
		}
		return column, nil
	}

	return "", fmt.Errorf("%w: partition field %q is not an indexed field", driver.ErrInvalidField, field)
}

// columnType maps a declared field kind onto a Doris column type. Time fields
// become real DATETIME columns so the table stays usable by tools that need a
// time column — partition pruning, BI tools, log search — instead of forcing
// every consumer to parse text.
func columnType(kind driver.Kind) string {
	if kind == driver.KindTime {
		return "DATETIME(6)"
	}
	return "VARCHAR(4096)"
}

// columnKinds resolves declared indexes into a column-name keyed lookup.
func columnKinds(indexes []driver.Index) map[string]driver.Kind {
	kinds := make(map[string]driver.Kind, len(indexes))
	for _, idx := range indexes {
		if column, err := resolveColumn(idx.Field); err == nil {
			kinds[column] = idx.Kind
		}
	}
	return kinds
}

func buildWhereSQL(filters []driver.Filter, loc *time.Location) (string, []any, error) {
	clauses := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))

	for _, filter := range filters {
		column, err := resolveColumn(filter.Field)
		if err != nil {
			return "", nil, err
		}

		expr := quote(column)
		if opSQL, ok := binaryOpSQL[filter.Op]; ok {
			clauses = append(clauses, expr+" "+opSQL+" ?")
			args = append(args, normalizeValue(filter.Value, loc))
			continue
		}

		if filter.Op != driver.OpIn {
			return "", nil, driver.ErrUnsupportedOp
		}

		values, err := driver.FlattenInValues(filter.Value)
		if err != nil {
			return "", nil, err
		}
		placeholders := make([]string, len(values))
		for i, value := range values {
			placeholders[i] = "?"
			args = append(args, normalizeValue(value, loc))
		}
		clauses = append(clauses, fmt.Sprintf("%s IN (%s)", expr, strings.Join(placeholders, ", ")))
	}

	return strings.Join(clauses, " AND "), args, nil
}

func buildOrderSQL(sorts []driver.Sort) (string, error) {
	parts := make([]string, 0, len(sorts))
	for _, sort := range sorts {
		column, err := resolveColumn(sort.Field)
		if err != nil {
			return "", err
		}
		direction := "ASC"
		if sort.Desc {
			direction = "DESC"
		}
		parts = append(parts, quote(column)+" "+direction)
	}
	return strings.Join(parts, ", "), nil
}

// appendPagination writes LIMIT/OFFSET. Doris rejects a bare OFFSET, so an
// offset without a limit needs an explicit upper bound.
func appendPagination(sb *strings.Builder, args []any, limit, offset int) []any {
	if limit > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, limit)
	} else if offset > 0 {
		sb.WriteString(" LIMIT 9223372036854775807")
	}
	if offset > 0 {
		sb.WriteString(" OFFSET ?")
		args = append(args, offset)
	}
	return args
}

func buildSelectSQL(database, table string, q driver.Query, loc *time.Location) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}

	whereSQL, args, err := buildWhereSQL(q.Filters, loc)
	if err != nil {
		return "", nil, err
	}
	orderSQL, err := buildOrderSQL(q.Sorts)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s, %s, %s FROM %s",
		quote(colID), quote(colData), quote(colFields), qualified(database, table))
	if whereSQL != "" {
		sb.WriteString(" WHERE " + whereSQL)
	}
	if orderSQL != "" {
		sb.WriteString(" ORDER BY " + orderSQL)
	}
	args = appendPagination(&sb, args, q.Limit, q.Offset)

	return sb.String(), args, nil
}

func buildCountSQL(database, table string, q driver.Query, loc *time.Location) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}

	whereSQL, args, err := buildWhereSQL(q.Filters, loc)
	if err != nil {
		return "", nil, err
	}

	sql := "SELECT COUNT(*) FROM " + qualified(database, table)
	if whereSQL != "" {
		sql += " WHERE " + whereSQL
	}
	return sql, args, nil
}

func buildValuesSQL(
	database, table, field string,
	q driver.Query,
	size int,
	kinds map[string]driver.Kind,
	loc *time.Location,
) (string, []any, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return "", nil, driver.ErrNegativePagination
	}
	if size < 0 {
		return "", nil, driver.ErrNegativeSize
	}

	column, err := resolveColumn(field)
	if err != nil {
		return "", nil, err
	}

	whereSQL, args, err := buildWhereSQL(q.Filters, loc)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT DISTINCT %s AS term FROM %s WHERE ", quote(column), qualified(database, table))
	if whereSQL != "" {
		sb.WriteString(whereSQL + " AND ")
	}
	// A DATETIME column cannot be compared against '', so the blank guard only
	// applies to text columns.
	fmt.Fprintf(&sb, "%s IS NOT NULL", quote(column))
	if kinds[column] != driver.KindTime {
		fmt.Fprintf(&sb, " AND %s != ''", quote(column))
	}
	sb.WriteString(" ORDER BY term ASC")
	if size > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, size)
	}

	return sb.String(), args, nil
}
