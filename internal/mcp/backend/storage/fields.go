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

package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	storagees "github.com/ccfos/huatuo/internal/storage/elasticsearch"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	ESFieldCapsOperation  = "field_caps"
	ESUploadedTimeField   = "uploaded_time"
	ESTimeField           = "time"
	ESFieldCapsCacheTTL   = 5 * time.Minute
	ESQueryTimeFormat     = "strict_date_optional_time_nanos"
	ESQueryTimeSortScript = "def v; if (doc.containsKey('uploaded_timestamp') && doc['uploaded_timestamp'].size() != 0) { v = doc['uploaded_timestamp'].value; } else if (doc.containsKey('uploaded_time') && doc['uploaded_time'].size() != 0) { v = doc['uploaded_time'].value; } else if (doc.containsKey('time') && doc['time'].size() != 0) { v = doc['time'].value; } else { return 0; } return v instanceof ZonedDateTime ? v.toInstant().toEpochMilli() : v.getMillis();"
)

type (
	fieldCapability   = storagees.FieldCapability
	fieldCapsResponse = storagees.FieldCapabilities
	exactField        = storagees.ExactField
)

type fieldSchema struct {
	exact                map[string][]exactField
	times                []string
	sourceFields         map[string]bool
	sourceTime           bool
	sourceProfile        bool
	sourceProfileIndices []string
}

func queryTimeFieldNames() []string {
	return []string{types.DocumentFieldUploadedTimestamp, ESUploadedTimeField, ESTimeField}
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
	if fields != nil && time.Since(fetched) < ESFieldCapsCacheTTL {
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
	if fields != nil && time.Since(fetched) < ESFieldCapsCacheTTL {
		return fields, nil
	}
	var response fieldCapsResponse
	err := r.request(ctx, ESFieldCapsOperation, r.index, &response)
	// Relay search prefixes can resolve dated indices without being ES aliases.
	// Only expand an unresolved selector, never a configured, mapped index.
	if (missingIndex(err) || err == nil && len(response.Fields) == 0 && len(response.Indices) == 0) && !strings.ContainsAny(r.index, "*?,") {
		err = r.request(ctx, ESFieldCapsOperation, r.index+"*", &response)
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
		mapping := response.ResolveExactField(field)
		schema.exact[field] = mapping.Variants
		// Missing/unindexed fields and text without a complete keyword projection
		// still exist in _source. Never silently exclude those indices.
		schema.sourceFields[field] = !mapping.Complete
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

func (s *fieldSchema) terms(field string, values []string) (any, error) {
	variants := s.exact[field]
	if len(variants) == 0 {
		return nil, fmt.Errorf("%w: %s has no searchable keyword mapping", event.ErrUnsupportedOp, field)
	}
	return storagees.ExactTerms(variants, values)
}

func (s *fieldSchema) timeRange(bounds map[string]string) any {
	// Override legacy mappings that accept space-separated dates but not RFC3339.
	parameters := make(map[string]string, len(bounds)+1)
	for key, value := range bounds {
		parameters[key] = value
	}
	parameters["format"] = ESQueryTimeFormat
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
	return map[string]any{"_script": map[string]any{"type": "number", "order": "desc", "script": map[string]any{"lang": "painless", "source": ESQueryTimeSortScript}}}
}
