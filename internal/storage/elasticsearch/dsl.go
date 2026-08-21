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

package elasticsearch

import (
	"encoding/json"
	"fmt"
	"regexp"

	escount "github.com/elastic/go-elasticsearch/v8/typedapi/core/count"
	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types/enums/sortorder"

	"huatuo-bamai/internal/storage/driver"
)

var fieldNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

type (
	termsAgg struct {
		Field string `json:"field"`
		Size  int    `json:"size"`
	}
	termsAggBody struct {
		Terms termsAgg `json:"terms"`
	}
	valuesBody struct {
		Size  int                     `json:"size"`
		Query *types.Query            `json:"query,omitempty"`
		Aggs  map[string]termsAggBody `json:"aggs"`
	}
	valuesResponse struct {
		Aggregations struct {
			Terms struct {
				Buckets []struct {
					Key any `json:"key"`
				} `json:"buckets"`
			} `json:"terms"`
		} `json:"aggregations"`
	}
)

func validateFieldName(field string) error {
	if !fieldNamePattern.MatchString(field) {
		return driver.ErrInvalidField
	}
	return nil
}

func buildSearchRequest(q driver.Query) ([]byte, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return nil, driver.ErrNegativePagination
	}

	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}

	size := defaultQuerySize
	if q.Limit > 0 {
		size = q.Limit
	}
	req := essearch.Request{
		Query:          query,
		TrackTotalHits: true,
		Size:           &size,
	}
	if q.Offset > 0 {
		req.From = &q.Offset
	}
	if len(q.Sorts) > 0 {
		sorts, err := buildSort(q.Sorts)
		if err != nil {
			return nil, err
		}
		req.Sort = sorts
	}
	return json.Marshal(req)
}

func buildCountRequest(q driver.Query) ([]byte, error) {
	if q.Limit < 0 || q.Offset < 0 {
		return nil, driver.ErrNegativePagination
	}

	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}
	return json.Marshal(escount.Request{Query: query})
}

func buildValuesRequest(field string, q driver.Query, size int) ([]byte, error) {
	if err := validateFieldName(field); err != nil {
		return nil, err
	}
	if q.Limit < 0 || q.Offset < 0 {
		return nil, driver.ErrNegativePagination
	}
	if size < 0 {
		return nil, driver.ErrNegativeSize
	}

	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}
	body := valuesBody{
		Size:  0,
		Query: query,
		Aggs:  map[string]termsAggBody{"terms": {Terms: termsAgg{Field: field, Size: size}}},
	}
	return json.Marshal(body)
}

func buildQuery(filters []driver.Filter) (*types.Query, error) {
	if len(filters) == 0 {
		return &types.Query{MatchAll: &types.MatchAllQuery{}}, nil
	}

	var filterClauses []types.Query
	var mustNotClauses []types.Query

	for _, filter := range filters {
		clause, negate, err := buildClause(filter)
		if err != nil {
			return nil, err
		}
		if negate {
			mustNotClauses = append(mustNotClauses, clause)
		} else {
			filterClauses = append(filterClauses, clause)
		}
	}

	boolQuery := &types.BoolQuery{}

	if len(filterClauses) > 0 {
		boolQuery.Filter = filterClauses
	}
	if len(mustNotClauses) > 0 {
		boolQuery.MustNot = mustNotClauses
	}
	return &types.Query{Bool: boolQuery}, nil
}

func buildClause(filter driver.Filter) (types.Query, bool, error) {
	if err := validateFieldName(filter.Field); err != nil {
		return types.Query{}, false, err
	}

	switch filter.Op {
	case driver.OpEq:
		// empty string match -> must_not exists (field does not exist)
		if s, ok := filter.Value.(string); ok && s == "" {
			q := types.Query{
				Exists: &types.ExistsQuery{Field: filter.Field},
			}
			return q, true, nil
		}
		q := types.Query{Term: map[string]types.TermQuery{filter.Field: {Value: driver.NormalizeValue(filter.Value)}}}
		return q, false, nil
	case driver.OpNe:
		q := types.Query{Term: map[string]types.TermQuery{filter.Field: {Value: driver.NormalizeValue(filter.Value)}}}
		return q, true, nil
	case driver.OpGt, driver.OpGte, driver.OpLt, driver.OpLte:
		rangeQ, err := buildRangeClause(filter)
		if err != nil {
			return types.Query{}, false, err
		}
		return types.Query{Range: map[string]types.RangeQuery{filter.Field: rangeQ}}, false, nil
	case driver.OpIn:
		values, err := driver.FlattenInValues(filter.Value)
		if err != nil {
			return types.Query{}, false, err
		}
		termsQ := types.NewTermsQuery()
		termsQ.TermsQuery[filter.Field] = values
		return types.Query{Terms: termsQ}, false, nil
	default:
		return types.Query{}, false, fmt.Errorf("%w: %s", driver.ErrUnsupportedOp, filter.Op)
	}
}

func buildRangeClause(filter driver.Filter) (types.RangeQuery, error) {
	if s, ok := driver.NormalizeValue(filter.Value).(string); ok {
		return buildDateRangeClause(filter.Op, s)
	}
	f, ok := asFloat64(filter.Value)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported range value type", driver.ErrUnsupportedOp)
	}
	return buildNumberRangeClause(filter.Op, f)
}

func buildNumberRangeClause(op driver.Op, value float64) (types.RangeQuery, error) {
	f := types.Float64(value)
	q := types.NumberRangeQuery{}

	switch op {
	case driver.OpGt:
		q.Gt = &f
	case driver.OpGte:
		q.Gte = &f
	case driver.OpLt:
		q.Lt = &f
	case driver.OpLte:
		q.Lte = &f
	default:
		return nil, fmt.Errorf("%w: %s", driver.ErrUnsupportedOp, op)
	}
	return q, nil
}

func buildDateRangeClause(op driver.Op, value string) (types.RangeQuery, error) {
	q := types.DateRangeQuery{}

	switch op {
	case driver.OpGt:
		q.Gt = &value
	case driver.OpGte:
		q.Gte = &value
	case driver.OpLt:
		q.Lt = &value
	case driver.OpLte:
		q.Lte = &value
	default:
		return nil, fmt.Errorf("%w: %s", driver.ErrUnsupportedOp, op)
	}
	return q, nil
}

func buildSort(sorts []driver.Sort) ([]types.SortCombinations, error) {
	result := make([]types.SortCombinations, 0, len(sorts))
	for _, s := range sorts {
		if err := validateFieldName(s.Field); err != nil {
			return nil, err
		}
		order := sortorder.Asc
		if s.Desc {
			order = sortorder.Desc
		}
		opt := types.NewSortOptions()
		opt.SortOptions[s.Field] = types.FieldSort{Order: &order}
		result = append(result, opt)
	}
	return result, nil
}

func asFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}
