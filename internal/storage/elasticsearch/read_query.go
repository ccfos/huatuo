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
	"errors"
	"fmt"
	"sort"
	"strings"
)

// FieldCapability describes one mapping type returned by _field_caps.
type FieldCapability struct {
	Type         string   `json:"type"`
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
	Indices      []string `json:"indices"`
}

// FieldCapabilities preserves per-index mappings; resolving physical indices
// must never replace the caller's alias or configured search selector.
type FieldCapabilities struct {
	Indices []string                              `json:"indices"`
	Fields  map[string]map[string]FieldCapability `json:"fields"`
}

// ExactField is a keyword projection and the indices where it can be used.
type ExactField struct {
	Name    string
	Indices []string
}

// ExactFieldMapping reports whether indexed exact operations cover every mapping.
// Incomplete coverage is not permission to silently drop documents or scan source.
type ExactFieldMapping struct {
	Variants []ExactField
	Complete bool
}

// ResolveExactField accepts native keyword and keyword projections of text.
// Other types require an explicit caller policy rather than analyzed matching.
func (c *FieldCapabilities) ResolveExactField(field string) ExactFieldMapping {
	var result ExactFieldMapping
	base := c.Fields[field]
	ambiguous := false
	if keyword, ok := base["keyword"]; ok && keyword.Searchable && keyword.Aggregatable {
		ambiguous = len(base) > 1 && len(keyword.Indices) == 0
		result.Variants = append(result.Variants, ExactField{Name: field, Indices: keyword.Indices})
	}
	if _, text := base["text"]; text {
		var candidates []string
		for name, capabilities := range c.Fields {
			if keyword, ok := capabilities["keyword"]; strings.HasPrefix(name, field+".") && ok && keyword.Searchable && keyword.Aggregatable {
				candidates = append(candidates, name)
			}
		}
		sort.Strings(candidates)
		for _, name := range candidates {
			if name == field+".keyword" {
				candidates = []string{name}
				break
			}
		}
		if len(candidates) > 0 {
			name := candidates[0]
			indices := c.Fields[name]["keyword"].Indices
			if len(result.Variants) > 0 && len(indices) == 0 {
				indices = base["text"].Indices
			}
			ambiguous = ambiguous || len(result.Variants) > 0 && len(indices) == 0
			result.Variants = append(result.Variants, ExactField{Name: name, Indices: indices})
		}
	}
	result.Complete = !ambiguous && exactCoverage(base, result.Variants)
	return result
}

func exactCoverage(base map[string]FieldCapability, variants []ExactField) bool {
	if len(variants) == 0 {
		return false
	}
	for kind := range base {
		if kind != "keyword" && kind != "text" {
			return false
		}
	}
	for kind, capability := range base {
		if kind == "keyword" && capability.Searchable && capability.Aggregatable {
			continue
		}
		if kind != "text" {
			return false
		}
		covered := make(map[string]bool)
		for _, variant := range variants {
			if !strings.Contains(variant.Name, ".") {
				continue
			}
			if len(variant.Indices) == 0 {
				return len(base) == 1
			}
			for _, index := range variant.Indices {
				covered[index] = true
			}
		}
		if len(capability.Indices) == 0 {
			return false
		}
		for _, index := range capability.Indices {
			if !covered[index] {
				return false
			}
		}
	}
	return true
}

// ExactTerms scopes each projection inside the original search selector.
// Callers must separately handle incomplete mapping coverage.
func ExactTerms(variants []ExactField, values []string) (any, error) {
	if len(variants) == 0 {
		return nil, errors.New("exact terms require a keyword mapping")
	}
	queries := make([]any, 0, len(variants))
	for _, variant := range variants {
		var query any = map[string]any{"terms": map[string]any{variant.Name: values}}
		if len(variant.Indices) > 0 {
			query = map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"terms": map[string]any{"_index": variant.Indices}}, query}}}
		}
		queries = append(queries, query)
	}
	if len(queries) == 1 {
		return queries[0], nil
	}
	return map[string]any{"bool": map[string]any{"should": queries, "minimum_should_match": 1}}, nil
}

// TermsAggregationOptions describes a top-N string-value aggregation, not an
// exhaustive grouping or a guarantee of exact distributed bucket counts.
type TermsAggregationOptions struct {
	Field    string
	Variants []ExactField
	Limit    int
	// AllowSourceScript explicitly permits the costly fallback for mixed mappings.
	// It requires readable _source and server-side scripts; it does not scan locally.
	AllowSourceScript bool
}

// TermsAggregation avoids doc-value fielddata conflicts on mixed mappings.
// The caller owns the query, selector, completeness checks, and request budget.
func TermsAggregation(options *TermsAggregationOptions) (map[string]any, error) {
	if options == nil || options.Field == "" || len(options.Variants) == 0 || options.Limit <= 0 {
		return nil, errors.New("terms aggregation requires a field, keyword mappings, and a positive limit")
	}
	terms := map[string]any{"field": options.Variants[0].Name, "size": options.Limit}
	if len(options.Variants) > 1 {
		if !options.AllowSourceScript {
			return nil, fmt.Errorf("terms aggregation for %s has mixed mappings; source script fallback is not enabled", options.Field)
		}
		// Even filtered-out shards may try to load text fielddata. Reading source
		// avoids that without replacing aliases with physical index requests.
		delete(terms, "field")
		terms["script"] = map[string]any{
			"lang":   "painless",
			"source": "def s = params['_source']; if (s == null || s.isEmpty()) { throw new IllegalArgumentException('terms aggregation requires readable _source'); } return s[params['field']];",
			"params": map[string]any{"field": options.Field},
		}
		terms["value_type"] = "string"
	}
	return map[string]any{"terms": terms}, nil
}

// ValueCount is a string terms bucket. Count inherits server aggregation accuracy.
type ValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// DecodeValueCounts validates string buckets without inventing missing counts.
// HTTP, timeout, and shard checks must happen before accepting the aggregation.
func DecodeValueCounts(raw json.RawMessage, limit int) ([]ValueCount, error) {
	if limit <= 0 {
		return nil, errors.New("terms bucket limit must be positive")
	}
	var aggregation struct {
		Buckets *[]struct {
			Key   *string `json:"key"`
			Count *int64  `json:"doc_count"`
		} `json:"buckets"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &aggregation) != nil || aggregation.Buckets == nil {
		return nil, errors.New("missing or invalid terms buckets")
	}
	counts := make([]ValueCount, 0, min(len(*aggregation.Buckets), limit))
	for _, bucket := range *aggregation.Buckets {
		if bucket.Key == nil || bucket.Count == nil || *bucket.Count < 0 {
			return nil, errors.New("terms bucket requires a string key and nonnegative doc_count")
		}
		if len(counts) >= limit {
			break
		}
		counts = append(counts, ValueCount{Value: *bucket.Key, Count: *bucket.Count})
	}
	return counts, nil
}
