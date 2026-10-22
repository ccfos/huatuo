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
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/pkg/types"
)

func sourceTargets(filter *event.Query) map[string][]string {
	if filter == nil {
		return nil
	}
	targets := map[string][]string{
		types.DocumentFieldHostname:               filter.Hostnames,
		types.DocumentFieldContainerHostname:      filter.ContainerHostnames,
		types.DocumentFieldContainerHostNamespace: filter.ContainerHostNamespaces,
		types.DocumentFieldTracerName:             filter.TracerNames,
	}
	if filter.Region != "" {
		targets[types.DocumentFieldRegion] = []string{filter.Region}
	}
	return targets
}

func (s *fieldSchema) needsSource(filter *event.Query, catalog bool) bool {
	if s.sourceTime || catalog && s.sourceFields[types.DocumentFieldTracerName] ||
		filter != nil && filter.ProfileOnly && s.sourceProfile {
		return true
	}
	for field, values := range sourceTargets(filter) {
		if len(values) > 0 && s.sourceFields[field] {
			return true
		}
	}
	return false
}

// scanSource keeps safe indexed predicates to reduce work, then applies exact
// predicates to original values. Text analysis must not turn host/tracer names
// into token matches. A single bounded search owns no scroll/PIT/task state.
func (r *Reader) scanSource(ctx context.Context, filter *event.Query, fields *fieldSchema) ([]*event.Document, error) {
	coarse := event.Query{}
	if filter != nil {
		coarse = *filter
	}
	if fields.sourceFields[types.DocumentFieldRegion] {
		coarse.Region = ""
	}
	if fields.sourceFields[types.DocumentFieldHostname] {
		coarse.Hostnames = nil
	}
	if fields.sourceFields[types.DocumentFieldContainerHostname] {
		coarse.ContainerHostnames = nil
	}
	if fields.sourceFields[types.DocumentFieldContainerHostNamespace] {
		coarse.ContainerHostNamespaces = nil
	}
	if fields.sourceFields[types.DocumentFieldTracerName] {
		coarse.TracerNames = nil
	}
	if fields.sourceTime {
		coarse.StartTime, coarse.EndTime = time.Time{}, time.Time{}
	}
	// Keep indexed payload qualification wherever it is safe. Otherwise ordinary
	// events in healthy indices could exhaust the compatibility scan budget.
	if fields.sourceProfile {
		coarse.ProfileOnly = false
	}
	query, err := buildQuery(&coarse, fields)
	if err != nil {
		return nil, err
	}
	if filter != nil && filter.ProfileOnly && fields.sourceProfile && len(fields.sourceProfileIndices) > 0 && !slices.Contains(fields.sourceProfileIndices, "") {
		eligibility := map[string]any{"bool": map[string]any{
			"should":               []any{indexedProfileQuery(), map[string]any{"terms": map[string]any{"_index": fields.sourceProfileIndices}}},
			"minimum_should_match": 1,
		}}
		// _index is only a filter within the original selector, not a replacement
		// for it: alias filters and search routing must remain authoritative.
		query = map[string]any{"bool": map[string]any{"filter": []any{query, eligibility}}}
	}
	var response struct {
		queryStatus
		Hits *struct {
			Hits []struct {
				ID     string          `json:"_id"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	body := map[string]any{"query": query, "size": constant.MaxSourceScanDocuments + 1, "sort": []string{"_doc"}, "track_total_hits": false}
	if err := r.request(ctx, constant.ESSearchOperation, body, &response); err != nil {
		return nil, err
	}
	if err := response.check(); err != nil {
		return nil, err
	}
	if response.Hits == nil {
		return nil, fmt.Errorf("%w: compatibility search response is missing hits", event.ErrDecodeFailed)
	}
	if len(response.Hits.Hits) > constant.MaxSourceScanDocuments {
		return nil, &incompleteDataError{summary: fmt.Sprintf("source compatibility query exceeds %d candidate documents; narrow time/target filters; no exact count or complete result is available", constant.MaxSourceScanDocuments)}
	}
	documents := make([]*event.Document, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var source map[string]json.RawMessage
		if err := json.Unmarshal(hit.Source, &source); err != nil || source == nil {
			return nil, fmt.Errorf("%w: compatibility search hit has no readable source", event.ErrDecodeFailed)
		}
		matches, err := matchesSource(source, filter)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		var document event.Document
		if err := decodeDocument(hit.Source, &document); err != nil {
			return nil, fmt.Errorf("%w: decode compatibility event: %w", event.ErrDecodeFailed, err)
		}
		document.RecordID = hit.ID
		document.Source = &event.SourceRef{Backend: r.BackendType(), RecordID: hit.ID, Region: document.Region}
		documents = append(documents, &document)
	}
	return documents, nil
}

func matchesSource(source map[string]json.RawMessage, filter *event.Query) (bool, error) {
	if filter == nil {
		return true, nil
	}
	for field, values := range sourceTargets(filter) {
		if len(values) == 0 {
			continue
		}
		raw := source[field]
		if len(raw) == 0 || string(raw) == "null" {
			return false, nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, fmt.Errorf("%w: source field %s must be a string", event.ErrDecodeFailed, field)
		}
		if !slices.Contains(values, value) {
			return false, nil
		}
	}
	if !filter.StartTime.IsZero() || !filter.EndTime.IsZero() {
		matched := false
		for _, field := range queryTimeFieldNames() {
			raw := source[field]
			if len(raw) == 0 || string(raw) == "null" {
				continue
			}
			timestamp, err := decodeTimestamp(raw)
			if err != nil {
				return false, fmt.Errorf("%w: invalid source time field %s: %w", event.ErrDecodeFailed, field, err)
			}
			matched = matched || ((filter.StartTime.IsZero() || !timestamp.Before(filter.StartTime)) && (filter.EndTime.IsZero() || !timestamp.After(filter.EndTime)))
		}
		if !matched {
			return false, nil
		}
	}
	if filter.ProfileOnly {
		var tracer any
		if err := json.Unmarshal(source["tracer_data"], &tracer); err != nil {
			return false, nil
		}
		for _, group := range profileFieldGroups() {
			present := true
			for _, field := range group {
				present = present && sourcePathExists(tracer, strings.Split(strings.TrimPrefix(field, "tracer_data."), "."))
			}
			if present {
				return true, nil
			}
		}
		return false, nil
	}
	return true, nil
}

func sourcePathExists(value any, path []string) bool {
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if sourcePathExists(item, path) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return value != nil
	}
	object, ok := value.(map[string]any)
	return ok && sourcePathExists(object[path[0]], path[1:])
}

func (r *Reader) searchSourceEvents(ctx context.Context, filter *event.Query, fields *fieldSchema, limit, offset int) (event.SearchResult, error) {
	documents, err := r.scanSource(ctx, filter, fields)
	if err != nil {
		return event.SearchResult{}, err
	}
	sort.SliceStable(documents, func(i, j int) bool { return documents[i].Timestamp.After(documents[j].Timestamp) })
	total := int64(len(documents))
	end := min(offset+limit, len(documents))
	result := event.SearchResult{Documents: documents[min(offset, len(documents)):end], TotalCount: &total, CountExact: true, Truncated: end < len(documents)}
	if filter != nil && filter.ProfileOnly {
		for _, document := range result.Documents {
			source, err := json.Marshal(document)
			if err != nil {
				return event.SearchResult{}, fmt.Errorf("%w: encode profile source: %w", event.ErrDecodeFailed, err)
			}
			frames, sampleType, sampleUnit, err := normalizeProfile(ctx, source)
			if err != nil {
				if ctx.Err() != nil {
					return event.SearchResult{}, ctx.Err()
				}
				result.Warnings = append(result.Warnings, "profile payload could not be analyzed: "+err.Error())
			} else {
				document.ProfileFrames, document.ProfileSampleType, document.ProfileSampleUnit = frames, sampleType, sampleUnit
			}
		}
	}
	return result, nil
}
