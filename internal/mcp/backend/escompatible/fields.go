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

package escompatible

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/pkg/types"
)

type fieldCapability struct {
	Type         string   `json:"type"`
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
	Indices      []string `json:"indices"`
}

type fieldCapsResponse struct {
	Indices []string                              `json:"indices"`
	Fields  map[string]map[string]fieldCapability `json:"fields"`
}

type exactField struct {
	Name    string
	Indices []string
}

type fieldSchema struct {
	exact                map[string][]exactField
	times                []string
	sourceFields         map[string]bool
	sourceTime           bool
	sourceProfile        bool
	sourceProfileIndices []string
}

func queryTimeFieldNames() []string {
	return []string{types.DocumentFieldUploadedTimestamp, constant.ESUploadedTimeField, constant.ESTimeField}
}

func schemaFieldNames() []string {
	fields := []string{types.DocumentFieldRegion, types.DocumentFieldHostname, types.DocumentFieldContainerHostname, types.DocumentFieldContainerHostNamespace, types.DocumentFieldTracerName}
	result := make([]string, 0, len(fields)*2+3)
	for _, field := range fields {
		result = append(result, field, field+".*")
	}
	result = append(result, queryTimeFieldNames()...)
	for _, group := range profileFieldGroups() {
		for _, field := range group {
			if !slices.Contains(result, field) {
				result = append(result, field)
			}
		}
	}
	return result
}

func (r *Reader) queryFields(ctx context.Context) (*fieldSchema, error) {
	r.fieldsMu.RLock()
	fields, fetched := r.fields, r.fieldsAt
	r.fieldsMu.RUnlock()
	if fields != nil && time.Since(fetched) < constant.ESFieldCapsCacheTTL {
		return fields, nil
	}
	select {
	case r.fieldsGate <- struct{}{}:
		defer func() { <-r.fieldsGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.fieldsMu.RLock()
	fields, fetched = r.fields, r.fieldsAt
	r.fieldsMu.RUnlock()
	if fields != nil && time.Since(fetched) < constant.ESFieldCapsCacheTTL {
		return fields, nil
	}
	var response fieldCapsResponse
	err := r.request(ctx, constant.ESFieldCapsOperation, r.index, &response)
	// Relay search prefixes can resolve dated indices without being ES aliases.
	// Only expand an unresolved selector, never a configured, mapped index.
	if (missingIndex(err) || err == nil && len(response.Fields) == 0 && len(response.Indices) == 0) && !strings.ContainsAny(r.index, "*?,") {
		err = r.request(ctx, constant.ESFieldCapsOperation, r.index+"*", &response)
	}
	if err != nil {
		return nil, err
	}
	fields, err = resolveFieldSchema(&response)
	if err != nil {
		return nil, err
	}
	r.fieldsMu.Lock()
	r.fields, r.fieldsAt = fields, time.Now()
	r.fieldsMu.Unlock()
	return fields, nil
}

func resolveFieldSchema(response *fieldCapsResponse) (*fieldSchema, error) {
	if len(response.Fields) == 0 && len(response.Indices) == 0 {
		return nil, fmt.Errorf("%w: no field capabilities found for configured event index", event.ErrDecodeFailed)
	}
	schema := &fieldSchema{exact: make(map[string][]exactField), sourceFields: make(map[string]bool)}
	for _, field := range []string{types.DocumentFieldRegion, types.DocumentFieldHostname, types.DocumentFieldContainerHostname, types.DocumentFieldContainerHostNamespace, types.DocumentFieldTracerName} {
		base := response.Fields[field]
		if keyword, ok := base["keyword"]; ok && keyword.Searchable && keyword.Aggregatable {
			if len(base) > 1 && len(keyword.Indices) == 0 {
				schema.sourceFields[field] = true
			}
			schema.exact[field] = append(schema.exact[field], exactField{Name: field, Indices: keyword.Indices})
		}
		if _, text := base["text"]; text {
			var candidates []string
			for name, capabilities := range response.Fields {
				if strings.HasPrefix(name, field+".") {
					if keyword, ok := capabilities["keyword"]; ok && keyword.Searchable && keyword.Aggregatable {
						candidates = append(candidates, name)
					}
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
				indices := response.Fields[name]["keyword"].Indices
				if len(schema.exact[field]) > 0 && len(indices) == 0 {
					indices = base["text"].Indices
				}
				if len(schema.exact[field]) > 0 && len(indices) == 0 {
					schema.sourceFields[field] = true
				}
				schema.exact[field] = append(schema.exact[field], exactField{Name: name, Indices: indices})
			}
		}
		// Missing/unindexed fields and text without a complete keyword projection
		// still exist in _source. Never silently exclude those indices.
		schema.sourceFields[field] = schema.sourceFields[field] || !exactCoverage(base, schema.exact[field])
	}
	coveredTimes := make(map[string]bool)
	allTimesCovered := false
	for _, field := range queryTimeFieldNames() {
		if capability, ok := response.Fields[field]["date"]; ok && capability.Searchable && capability.Aggregatable {
			schema.times = append(schema.times, field)
			if len(capability.Indices) == 0 {
				allTimesCovered = true
			}
			for _, index := range capability.Indices {
				coveredTimes[index] = true
			}
		}
		for kind := range response.Fields[field] {
			if kind != "date" && kind != "unmapped" {
				schema.sourceTime = true
			}
		}
		if date, ok := response.Fields[field]["date"]; ok && (!date.Searchable || !date.Aggregatable) {
			schema.sourceTime = true
		}
	}
	if len(schema.times) == 0 {
		schema.sourceTime = true
	}
	if !allTimesCovered {
		for _, index := range response.Indices {
			if !coveredTimes[index] {
				schema.sourceTime = true
			}
		}
	}
	schema.sourceProfileIndices = unindexedProfileIndices(response)
	schema.sourceProfile = len(schema.sourceProfileIndices) > 0
	return schema, nil
}

func unindexedProfileIndices(response *fieldCapsResponse) []string {
	indices := response.Indices
	if len(indices) == 0 {
		// Some compatible endpoints omit index names for uniform mappings.
		indices = []string{""}
	}
	var unindexed []string
	for _, index := range indices {
		covered := false
		for _, group := range profileFieldGroups() {
			complete := true
			for _, field := range group {
				present := false
				for kind, capability := range response.Fields[field] {
					// Exists queries can use doc values even when index:false.
					if kind != "unmapped" && (capability.Searchable || capability.Aggregatable) &&
						(len(capability.Indices) == 0 || slices.Contains(capability.Indices, index)) {
						present = true
					}
				}
				complete = complete && present
			}
			covered = covered || complete
		}
		if !covered {
			unindexed = append(unindexed, index)
		}
	}
	return unindexed
}

func exactCoverage(base map[string]fieldCapability, variants []exactField) bool {
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

func (f exactField) scope(query any) any {
	if len(f.Indices) == 0 {
		return query
	}
	return map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"terms": map[string]any{"_index": f.Indices}}, query}}}
}

func (s *fieldSchema) terms(field string, values []string) (any, error) {
	variants := s.exact[field]
	if len(variants) == 0 {
		return nil, fmt.Errorf("%w: %s has no searchable keyword mapping", event.ErrUnsupportedOp, field)
	}
	queries := make([]any, 0, len(variants))
	for _, variant := range variants {
		queries = append(queries, variant.scope(map[string]any{"terms": map[string]any{variant.Name: values}}))
	}
	if len(queries) == 1 {
		return queries[0], nil
	}
	return map[string]any{"bool": map[string]any{"should": queries, "minimum_should_match": 1}}, nil
}

func (s *fieldSchema) timeRange(bounds map[string]string) any {
	// Override legacy mappings that accept space-separated dates but not RFC3339.
	parameters := make(map[string]string, len(bounds)+1)
	for key, value := range bounds {
		parameters[key] = value
	}
	parameters["format"] = constant.ESQueryTimeFormat
	queries := make([]any, 0, len(s.times))
	for _, field := range s.times {
		queries = append(queries, map[string]any{"range": map[string]any{field: parameters}})
	}
	if len(queries) == 1 {
		return queries[0]
	}
	return map[string]any{"bool": map[string]any{"should": queries, "minimum_should_match": 1}}
}

func (s *fieldSchema) timeSort() any {
	if len(s.times) == 1 {
		return map[string]any{s.times[0]: map[string]any{"order": "desc", "unmapped_type": "date"}}
	}
	return map[string]any{"_script": map[string]any{"type": "number", "order": "desc", "script": map[string]any{"lang": "painless", "source": constant.ESQueryTimeSortScript}}}
}
