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
	"errors"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

var testIndexes = []driver.Index{
	{Field: "uploaded_time", Kind: driver.KindTime},
	{Field: "hostname"},
	{Field: "tracer_data.flamedata.profile_type"},
}

func testOptions() TableOptions {
	return TableOptions{
		Buckets:        4,
		Replicas:       1,
		PartitionField: "uploaded_time",
		RetentionDays:  30,
	}
}

func TestResolveColumn(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		want    string
		wantErr bool
	}{
		{name: "plain field", field: "hostname", want: "f_hostname"},
		// The profile query service appends .keyword to every string filter;
		// it names no field outside Elasticsearch.
		{name: "keyword suffix stripped", field: "hostname.keyword", want: "f_hostname"},
		{name: "dotted path flattened", field: "tracer_data.flamedata.profile_type", want: "f_tracer_data_flamedata_profile_type"},
		{name: "keyword suffix on dotted path", field: "tracer_data.flamedata.profile_type.keyword", want: "f_tracer_data_flamedata_profile_type"},
		{name: "uppercase lowered", field: "HostName", want: "f_hostname"},
		{name: "empty field", field: "", wantErr: true},
		{name: "only keyword suffix", field: ".keyword", wantErr: true},
		{name: "only separators", field: "...", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveColumn(tt.field)
			if tt.wantErr {
				if !errors.Is(err, driver.ErrInvalidField) {
					t.Errorf("resolveColumn(%q) error = %v, want ErrInvalidField", tt.field, err)
				}
				return
			}
			if err != nil {
				t.Errorf("resolveColumn(%q) returned error: %v", tt.field, err)
				return
			}
			if got != tt.want {
				t.Errorf("resolveColumn(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

func TestNormalizeValue(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	instant := time.Date(2026, 8, 20, 18, 5, 6, 500000000, time.UTC)

	tests := []struct {
		name  string
		value any
		loc   *time.Location
		want  any
	}{
		{
			name:  "time rendered in session zone",
			value: instant,
			loc:   shanghai,
			want:  "2026-08-21 02:05:06.500000",
		},
		{
			name:  "nil location falls back to UTC",
			value: instant,
			want:  "2026-08-20 18:05:06.500000",
		},
		// The profile query service formats its range bounds as RFC3339.
		{
			name:  "rfc3339 string converted",
			value: "2026-08-20T18:05:06.5Z",
			loc:   shanghai,
			want:  "2026-08-21 02:05:06.500000",
		},
		// driver.NormalizeValue emits this layout.
		{
			name:  "driver layout converted",
			value: "2026-08-20 18:05:06.500 +0000",
			loc:   shanghai,
			want:  "2026-08-21 02:05:06.500000",
		},
		{name: "non-time string untouched", value: "hostname-1", loc: shanghai, want: "hostname-1"},
		{name: "non-string untouched", value: int64(42), loc: shanghai, want: int64(42)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeValue(tt.value, tt.loc); got != tt.want {
				t.Errorf("normalizeValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildCreateTableSQL(t *testing.T) {
	sql, err := buildCreateTableSQL("huatuo", "profiling_metadata", testIndexes, testOptions())
	if err != nil {
		t.Fatalf("buildCreateTableSQL() returned error: %v", err)
	}

	wants := []string{
		// The partition column must lead the key, and Doris requires key
		// columns to come first in the column list.
		"`f_uploaded_time` DATETIME(6) NOT NULL",
		"UNIQUE KEY(`f_uploaded_time`, `id`)",
		"PARTITION BY RANGE(`f_uploaded_time`) ()",
		"`f_hostname` VARCHAR(4096) NULL",
		"`fields` VARIANT NULL",
		"`data` STRING NULL",
		"INDEX `idx_f_hostname` (`f_hostname`) USING INVERTED PROPERTIES(\"parser\" = \"basic\")",
		"\"dynamic_partition.enable\" = \"true\"",
		"\"dynamic_partition.start\" = \"-30\"",
		"\"dynamic_partition.end\" = \"3\"",
	}
	for _, want := range wants {
		if !strings.Contains(sql, want) {
			t.Errorf("buildCreateTableSQL() missing %q\ngot:\n%s", want, sql)
		}
	}

	// Time columns carry no inverted index: partition pruning and the key
	// prefix already cover them.
	if strings.Contains(sql, "idx_f_uploaded_time") {
		t.Errorf("buildCreateTableSQL() indexed a time column\ngot:\n%s", sql)
	}
}

func TestBuildCreateTableSQLWithoutPartitionOrIndex(t *testing.T) {
	opts := testOptions()
	opts.PartitionField = ""

	sql, err := buildCreateTableSQL("huatuo", "tracing_documents", testIndexes, opts)
	if err != nil {
		t.Fatalf("buildCreateTableSQL() returned error: %v", err)
	}

	if !strings.Contains(sql, "UNIQUE KEY(`id`)") {
		t.Errorf("buildCreateTableSQL() key = unexpected\ngot:\n%s", sql)
	}
	for _, unwanted := range []string{"PARTITION BY", "dynamic_partition"} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("buildCreateTableSQL() contains %q with the feature disabled\ngot:\n%s", unwanted, sql)
		}
	}
	// Without a partition column the time field is an ordinary column.
	if !strings.Contains(sql, "`f_uploaded_time` DATETIME(6) NULL") {
		t.Errorf("buildCreateTableSQL() time column = unexpected\ngot:\n%s", sql)
	}
}

func TestBuildCreateTableSQLRejectsBadPartitionField(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		// Falling back to an unpartitioned table would hide the mistake until
		// the table had grown too large to fix quietly.
		{name: "field was never declared", field: "not_an_index"},
		{name: "field is not a time field", field: "hostname"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := testOptions()
			opts.PartitionField = tt.field

			if _, err := buildCreateTableSQL("huatuo", "t", testIndexes, opts); !errors.Is(err, driver.ErrInvalidField) {
				t.Errorf("buildCreateTableSQL() error = %v, want ErrInvalidField", err)
			}
		})
	}
}

func TestBuildSelectSQL(t *testing.T) {
	utc := time.UTC
	tests := []struct {
		name     string
		query    driver.Query
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "no filters",
			query:   driver.Query{},
			wantSQL: "SELECT `id`, `data`, `fields` FROM `huatuo`.`t`",
		},
		{
			name: "filters sorts and pagination",
			query: driver.Query{
				Filters: []driver.Filter{
					{Field: "hostname.keyword", Op: driver.OpEq, Value: "node-1"},
					{Field: "uploaded_time", Op: driver.OpGte, Value: "2026-08-20T18:05:06Z"},
				},
				Sorts:  []driver.Sort{{Field: "uploaded_time", Desc: true}},
				Limit:  50,
				Offset: 10,
			},
			wantSQL: "SELECT `id`, `data`, `fields` FROM `huatuo`.`t` WHERE `f_hostname` = ? AND " +
				"`f_uploaded_time` >= ? ORDER BY `f_uploaded_time` DESC LIMIT ? OFFSET ?",
			wantArgs: []any{"node-1", "2026-08-20 18:05:06.000000", 50, 10},
		},
		{
			// Doris rejects a bare OFFSET, so an explicit upper bound stands in.
			name:     "offset without limit",
			query:    driver.Query{Offset: 5},
			wantSQL:  "SELECT `id`, `data`, `fields` FROM `huatuo`.`t` LIMIT 9223372036854775807 OFFSET ?",
			wantArgs: []any{5},
		},
		{
			name:     "in operator",
			query:    driver.Query{Filters: []driver.Filter{{Field: "hostname", Op: driver.OpIn, Value: []string{"a", "b"}}}},
			wantSQL:  "SELECT `id`, `data`, `fields` FROM `huatuo`.`t` WHERE `f_hostname` IN (?, ?)",
			wantArgs: []any{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := buildSelectSQL("huatuo", "t", tt.query, utc)
			if err != nil {
				t.Fatalf("buildSelectSQL() returned error: %v", err)
			}
			if sql != tt.wantSQL {
				t.Errorf("buildSelectSQL() sql =\n%s\nwant:\n%s", sql, tt.wantSQL)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("buildSelectSQL() args = %v, want %v", args, tt.wantArgs)
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Errorf("buildSelectSQL() args[%d] = %v, want %v", i, args[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestBuildSelectSQLRejectsBadQuery(t *testing.T) {
	if _, _, err := buildSelectSQL("huatuo", "t", driver.Query{Limit: -1}, time.UTC); !errors.Is(err, driver.ErrInvalidQuery) {
		t.Errorf("buildSelectSQL() error = %v, want ErrInvalidQuery", err)
	}

	query := driver.Query{Filters: []driver.Filter{{Field: "hostname", Op: driver.Op("like"), Value: "x"}}}
	if _, _, err := buildSelectSQL("huatuo", "t", query, time.UTC); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("buildSelectSQL() error = %v, want ErrUnsupportedOp", err)
	}
}

func TestBuildCountSQL(t *testing.T) {
	sql, args, err := buildCountSQL("huatuo", "t",
		driver.Query{Filters: []driver.Filter{{Field: "region", Op: driver.OpEq, Value: "dev"}}}, time.UTC)
	if err != nil {
		t.Fatalf("buildCountSQL() returned error: %v", err)
	}

	want := "SELECT COUNT(*) FROM `huatuo`.`t` WHERE `f_region` = ?"
	if sql != want {
		t.Errorf("buildCountSQL() sql = %q, want %q", sql, want)
	}
	if len(args) != 1 || args[0] != "dev" {
		t.Errorf("buildCountSQL() args = %v, want [dev]", args)
	}
}

func TestBuildValuesSQL(t *testing.T) {
	kinds := columnKinds(testIndexes)

	t.Run("text column guards against blanks", func(t *testing.T) {
		sql, args, err := buildValuesSQL("huatuo", "t", "hostname", driver.Query{}, 20, kinds, time.UTC)
		if err != nil {
			t.Fatalf("buildValuesSQL() returned error: %v", err)
		}

		want := "SELECT DISTINCT `f_hostname` AS term FROM `huatuo`.`t` WHERE `f_hostname` IS NOT NULL " +
			"AND `f_hostname` != '' ORDER BY term ASC LIMIT ?"
		if sql != want {
			t.Errorf("buildValuesSQL() sql =\n%s\nwant:\n%s", sql, want)
		}
		if len(args) != 1 || args[0] != 20 {
			t.Errorf("buildValuesSQL() args = %v, want [20]", args)
		}
	})

	// Comparing a DATETIME column against '' is a type error in Doris.
	t.Run("time column omits the blank guard", func(t *testing.T) {
		sql, _, err := buildValuesSQL("huatuo", "t", "uploaded_time", driver.Query{}, 0, kinds, time.UTC)
		if err != nil {
			t.Fatalf("buildValuesSQL() returned error: %v", err)
		}
		if strings.Contains(sql, "!= ''") {
			t.Errorf("buildValuesSQL() compared a time column to a blank string:\n%s", sql)
		}
	})

	t.Run("negative size", func(t *testing.T) {
		if _, _, err := buildValuesSQL("huatuo", "t", "hostname", driver.Query{}, -1, kinds, time.UTC); !errors.Is(err, driver.ErrInvalidQuery) {
			t.Errorf("buildValuesSQL() error = %v, want ErrInvalidQuery", err)
		}
	})
}
