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

	"github.com/ccfos/huatuo/internal/storage/driver"
)

var fieldNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

type deleteByQueryBody struct {
	Query *types.Query `json:"query"`
}

func validateFieldName(field string) error {
	if !fieldNamePattern.MatchString(field) {
		return driver.ErrInvalidField
	}
	return nil
}

func buildSearchRequest(q driver.Query) ([]byte, error) {
	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}

	size := q.BatchSize
	if size == 0 {
		size = driver.DefaultBatchSize
	}
	// Include skipped hits so a small limit does not shrink every scroll page.
	// Bound the addition by size to avoid overflowing Offset+Limit.
	if q.Offset < size {
		size = q.Offset + min(q.Limit, size-q.Offset)
	}

	req := essearch.Request{
		Query: query,
		// Scroll rejects disabled total-hit tracking, including on ES 7.
		TrackTotalHits: true,
		Size:           &size,
	}
	// Scroll advances through skipped records without the from+size window limit.
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
	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}
	return json.Marshal(escount.Request{Query: query})
}

func buildDeleteByQueryRequest(q driver.DeleteQuery) ([]byte, error) {
	if len(q.Filters) == 0 {
		return nil, fmt.Errorf(
			"%w: query deletion requires at least one filter",
			driver.ErrInvalidQuery,
		)
	}
	if q.Limit < 0 {
		return nil, fmt.Errorf(
			"%w: delete limit must be non-negative",
			driver.ErrInvalidQuery,
		)
	}

	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}
	return json.Marshal(deleteByQueryBody{Query: query})
}

func buildValuesRequest(field string, q driver.Query, size int) ([]byte, error) {
	if err := validateFieldName(field); err != nil {
		return nil, err
	}
	query, err := buildQuery(q.Filters)
	if err != nil {
		return nil, err
	}
	hitsSize := 0
	body := essearch.Request{
		Size:  &hitsSize,
		Query: query,
		Aggregations: map[string]types.Aggregations{
			"terms": {
				Terms: &types.TermsAggregation{Field: &field, Size: &size},
			},
		},
	}
	return json.Marshal(body)
}

func buildQuery(filters []driver.Filter) (*types.Query, error) {
	if len(filters) == 0 {
		return &types.Query{MatchAll: &types.MatchAllQuery{}}, nil
	}

	clauses := make([]types.Query, 0, len(filters))
	for _, filter := range filters {
		clause, err := buildClause(filter)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}

	return &types.Query{Bool: &types.BoolQuery{Filter: clauses}}, nil
}

func buildClause(filter driver.Filter) (types.Query, error) {
	if err := validateFieldName(filter.Field); err != nil {
		return types.Query{}, err
	}
	value, err := driver.NormalizeFilterValue(filter)
	if err != nil {
		return types.Query{}, err
	}

	switch filter.Op {
	case driver.OpEq:
		return types.Query{Term: map[string]types.TermQuery{filter.Field: {Value: value}}}, nil
	case driver.OpNe:
		return types.Query{Bool: &types.BoolQuery{
			Filter:  []types.Query{{Exists: &types.ExistsQuery{Field: filter.Field}}},
			MustNot: []types.Query{{Term: map[string]types.TermQuery{filter.Field: {Value: value}}}},
		}}, nil
	case driver.OpExists:
		return types.Query{Exists: &types.ExistsQuery{Field: filter.Field}}, nil
	case driver.OpNotExists:
		return types.Query{Bool: &types.BoolQuery{
			MustNot: []types.Query{{Exists: &types.ExistsQuery{Field: filter.Field}}},
		}}, nil
	case driver.OpIn:
		terms := types.NewTermsQuery()
		terms.TermsQuery[filter.Field] = value.([]any)
		return types.Query{Terms: terms}, nil
	case driver.OpPrefix:
		return types.Query{Prefix: map[string]types.PrefixQuery{
			filter.Field: {Value: value.(string)},
		}}, nil
	default:
		rangeQuery, err := buildRangeClause(filter.Op, value)
		if err != nil {
			return types.Query{}, err
		}
		return types.Query{Range: map[string]types.RangeQuery{filter.Field: rangeQuery}}, nil
	}
}

func buildRangeClause(op driver.Op, value any) (*types.UntypedRangeQuery, error) {
	// NumberRangeQuery converts every bound to float64, losing large integers.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode range value: %w", driver.ErrInvalidQuery, err)
	}
	query := &types.UntypedRangeQuery{}
	switch op {
	case driver.OpGt:
		query.Gt = raw
	case driver.OpGte:
		query.Gte = raw
	case driver.OpLt:
		query.Lt = raw
	case driver.OpLte:
		query.Lte = raw
	default:
		return nil, fmt.Errorf("%w: %s", driver.ErrUnsupportedOp, op)
	}

	return query, nil
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
