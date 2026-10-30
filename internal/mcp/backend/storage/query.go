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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	mcputils "github.com/ccfos/huatuo/internal/mcp/utils"
	storagees "github.com/ccfos/huatuo/internal/storage/elasticsearch"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	DefaultSearchLimit       = 100
	MaxShardFailureSummaries = 3
	MaxShardReasonDepth      = 3
	MaxShardIdentifierBytes  = 128
	ESSearchOperation        = "search"
	ESCountOperation         = "count"
	ProfileFramesField       = "tracer_data.flamedata.label"
	ProfileSamplesField      = "tracer_data.flamedata.profile.sample.value"
	ProfileLocationsField    = "tracer_data.flamedata.profile.location.line.function_id"
	ProfileFunctionsField    = "tracer_data.flamedata.profile.function.name"
	ProfileSampleTypesField  = "tracer_data.flamedata.profile.sample_type.type"
)

type queryStatus struct {
	TimedOut bool `json:"timed_out"`
	Shards   struct {
		Total      *int           `json:"total"`
		Successful *int           `json:"successful"`
		Failed     int            `json:"failed"`
		Failures   []shardFailure `json:"failures"`
	} `json:"_shards"`
}

type shardFailure struct {
	Index  string      `json:"index"`
	Shard  *int        `json:"shard"`
	Reason shardReason `json:"reason"`
}

type shardReason struct {
	Type     string       `json:"type"`
	Reason   string       `json:"reason"`
	CausedBy *shardReason `json:"caused_by"`
}

type incompleteDataError struct {
	summary   string
	retryable bool
}

func (e *incompleteDataError) Error() string             { return e.summary }
func (e *incompleteDataError) Unwrap() error             { return event.ErrIncompleteData }
func (e *incompleteDataError) Retryable() bool           { return e.retryable }
func (e *incompleteDataError) DiagnosticSummary() string { return e.summary }

func (s *queryStatus) check() error {
	if !s.TimedOut && s.Shards.Failed == 0 {
		return nil
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "Elasticsearch returned incomplete data: timed_out=%t failed_shards=%d", s.TimedOut, s.Shards.Failed)
	// Missing counters must remain unknown, rather than imply all shards failed.
	if s.Shards.Total != nil {
		fmt.Fprintf(&summary, " total_shards=%d", *s.Shards.Total)
	}
	if s.Shards.Successful != nil {
		fmt.Fprintf(&summary, " successful_shards=%d", *s.Shards.Successful)
	}
	for _, failure := range s.Shards.Failures[:min(len(s.Shards.Failures), MaxShardFailureSummaries)] {
		fmt.Fprintf(&summary, "; index=%q", boundedDiagnosticValue(failure.Index, MaxShardIdentifierBytes))
		if failure.Shard != nil {
			fmt.Fprintf(&summary, " shard=%d", *failure.Shard)
		}
		reason := &failure.Reason
		for depth := 0; reason != nil && depth < MaxShardReasonDepth; depth++ {
			fmt.Fprintf(&summary, " type=%q reason=%q", boundedDiagnosticValue(reason.Type, event.MaxBackendErrorTypeBytes), boundedDiagnosticValue(reason.Reason, event.MaxBackendErrorReasonBytes))
			reason = reason.CausedBy
			if reason != nil {
				if depth+1 < MaxShardReasonDepth {
					summary.WriteString(" caused_by:")
				} else {
					summary.WriteString(" (further causes omitted)")
				}
			}
		}
	}
	if len(s.Shards.Failures) > MaxShardFailureSummaries {
		fmt.Fprintf(&summary, "; %d more shard failure details omitted", len(s.Shards.Failures)-MaxShardFailureSummaries)
	}
	return &incompleteDataError{
		summary:   boundedDiagnosticValue(summary.String(), event.MaxBackendErrorBodyBytes),
		retryable: s.retryable(),
	}
}

func (s *queryStatus) retryable() bool {
	// Repeating an unchanged date predicate cannot fix a format mismatch. Inspect
	// every failure, including details omitted from the bounded diagnostic text.
	for _, failure := range s.Shards.Failures {
		for reason := &failure.Reason; reason != nil; reason = reason.CausedBy {
			if reason.Type == "date_time_parse_exception" || strings.Contains(reason.Reason, "failed to parse date field") {
				return false
			}
		}
	}
	return true
}

func missingIndex(err error) bool {
	var response *ResponseError
	return errors.As(err, &response) && response.StatusCode == http.StatusNotFound
}

func (r *Reader) SearchEvents(ctx context.Context, filter *event.Query) (event.SearchResult, error) {
	if err := validateQuery(filter); err != nil {
		return event.SearchResult{}, err
	}
	limit, offset := DefaultSearchLimit, 0
	if filter != nil {
		if filter.Limit > 0 {
			limit = min(filter.Limit, event.MaxDocuments)
		}
		offset = filter.Offset
		if offset < 0 || offset > event.MaxDocuments {
			return event.SearchResult{}, fmt.Errorf("%w: offset must be between 0 and %d", event.ErrInvalidQuery, event.MaxDocuments)
		}
	}
	fields, err := r.queryFields(ctx)
	if err != nil {
		return event.SearchResult{}, err
	}
	if fields.needsSource(filter, false) {
		return r.searchSourceEvents(ctx, filter, fields, limit, offset)
	}
	query, err := buildQuery(filter, fields)
	if err != nil {
		return event.SearchResult{}, err
	}
	body := map[string]any{
		"query": query, "size": limit + 1, "from": offset,
		"sort":             []any{fields.timeSort()},
		"track_total_hits": false,
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
	if err := r.request(ctx, ESSearchOperation, body, &response); err != nil {
		return event.SearchResult{}, err
	}
	if err := response.check(); err != nil {
		return event.SearchResult{}, err
	}
	if response.Hits == nil {
		return event.SearchResult{}, fmt.Errorf("%w: search response is missing hits", event.ErrDecodeFailed)
	}
	documents := make([]*event.Document, 0, min(len(response.Hits.Hits), limit))
	var warnings []string
	for _, hit := range response.Hits.Hits {
		if err := ctx.Err(); err != nil {
			return event.SearchResult{}, err
		}
		if len(documents) >= limit {
			break
		}
		var document event.Document
		if len(hit.Source) == 0 || string(hit.Source) == "null" {
			return event.SearchResult{}, fmt.Errorf("%w: search hit is missing source", event.ErrDecodeFailed)
		}
		if err := decodeDocument(hit.Source, &document); err != nil {
			return event.SearchResult{}, fmt.Errorf("%w: decode event source: %w", event.ErrDecodeFailed, err)
		}
		document.RecordID = hit.ID
		document.Source = &event.SourceRef{Backend: r.BackendType(), RecordID: hit.ID, Region: document.Region}
		if filter != nil && filter.ProfileOnly {
			frames, sampleType, sampleUnit, err := normalizeProfile(ctx, hit.Source)
			if err != nil {
				if ctx.Err() != nil {
					return event.SearchResult{}, ctx.Err()
				}
				warnings = append(warnings, "profile payload could not be analyzed: "+err.Error())
			} else {
				document.ProfileFrames, document.ProfileSampleType, document.ProfileSampleUnit = frames, sampleType, sampleUnit
			}
		}
		documents = append(documents, &document)
	}
	return event.SearchResult{Documents: documents, Truncated: len(response.Hits.Hits) > limit, Warnings: warnings}, nil
}

func (r *Reader) Count(ctx context.Context, filter *event.Query) (int64, error) {
	if err := validateQuery(filter); err != nil {
		return 0, err
	}
	fields, err := r.queryFields(ctx)
	if err != nil {
		return 0, err
	}
	if fields.needsSource(filter, false) {
		documents, err := r.scanSource(ctx, filter, fields)
		return int64(len(documents)), err
	}
	query, err := buildQuery(filter, fields)
	if err != nil {
		return 0, err
	}
	var response struct {
		queryStatus
		Count *int64 `json:"count"`
	}
	if err := r.request(ctx, ESCountOperation, map[string]any{"query": query}, &response); err != nil {
		return 0, err
	}
	if err := response.check(); err != nil {
		return 0, err
	}
	if response.Count == nil || *response.Count < 0 {
		return 0, fmt.Errorf("%w: count response is missing a nonnegative count", event.ErrDecodeFailed)
	}
	return *response.Count, nil
}

func (r *Reader) TracerNames(ctx context.Context, query *event.Query) ([]string, error) {
	counts, err := r.TracerCounts(ctx, query)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(counts))
	for index := range counts {
		names[index] = counts[index].Name
	}
	return names, nil
}

func (r *Reader) TracerCounts(ctx context.Context, filter *event.Query) ([]event.TracerCount, error) {
	if err := validateQuery(filter); err != nil {
		return nil, err
	}
	fields, err := r.queryFields(ctx)
	if err != nil {
		return nil, err
	}
	if fields.needsSource(filter, true) {
		documents, err := r.scanSource(ctx, filter, fields)
		if err != nil {
			return nil, err
		}
		byName := make(map[string]int64)
		for _, document := range documents {
			if document.TracerName != "" {
				byName[document.TracerName]++
			}
		}
		return sortedTracerCounts(byName, tracerCountLimit(filter)), nil
	}
	query, err := buildQuery(filter, fields)
	if err != nil {
		return nil, err
	}
	limit := tracerCountLimit(filter)
	variants := fields.exact[types.DocumentFieldTracerName]
	if len(variants) == 0 {
		return nil, fmt.Errorf("%w: tracer_name has no aggregatable keyword mapping", event.ErrUnsupportedOp)
	}
	aggregation, err := storagees.TermsAggregation(&storagees.TermsAggregationOptions{
		Field: types.DocumentFieldTracerName, Variants: variants, Limit: limit,
		AllowSourceScript: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: tracer aggregation: %w", event.ErrUnsupportedOp, err)
	}
	body := map[string]any{"size": 0, "query": query, "aggregations": map[string]any{"terms": aggregation}}
	var response struct {
		queryStatus
		Aggregations map[string]json.RawMessage `json:"aggregations"`
	}
	if err := r.request(ctx, ESSearchOperation, body, &response); err != nil {
		return nil, err
	}
	if err := response.check(); err != nil {
		return nil, err
	}
	raw := response.Aggregations["terms"]
	if raw == nil {
		raw = response.Aggregations["sterms#terms"]
	}
	values, err := decodeTracerCounts(raw, limit)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func tracerCountLimit(filter *event.Query) int {
	if filter != nil && filter.Limit > 0 {
		return min(filter.Limit, event.MaxTracerCatalogSize+1)
	}
	return DefaultSearchLimit
}

func sortedTracerCounts(byName map[string]int64, limit int) []event.TracerCount {
	counts := make([]event.TracerCount, 0, len(byName))
	for name, count := range byName {
		counts = append(counts, event.TracerCount{Name: name, Count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count == counts[j].Count {
			return counts[i].Name < counts[j].Name
		}
		return counts[i].Count > counts[j].Count
	})
	return counts[:min(len(counts), limit)]
}

func decodeTracerCounts(raw json.RawMessage, limit int) ([]event.TracerCount, error) {
	values, err := storagees.DecodeValueCounts(raw, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: tracer aggregation: %w", event.ErrDecodeFailed, err)
	}
	counts := make([]event.TracerCount, 0, len(values))
	for _, value := range values {
		counts = append(counts, event.TracerCount{Name: value.Value, Count: value.Count})
	}
	return counts, nil
}

func buildQuery(filter *event.Query, fields *fieldSchema) (map[string]any, error) {
	clauses := make([]any, 0, 7)
	if filter == nil {
		return map[string]any{"bool": map[string]any{"filter": clauses}}, nil
	}
	if filter.Limit < 0 {
		return nil, fmt.Errorf("%w: document limit must not be negative", event.ErrInvalidQuery)
	}
	if !filter.StartTime.IsZero() && !filter.EndTime.IsZero() && filter.StartTime.After(filter.EndTime) {
		return nil, fmt.Errorf("%w: start must precede end", event.ErrInvalidQuery)
	}
	if len(filter.Region) > mcputils.MaxRegionNameLength {
		return nil, fmt.Errorf("%w: region is too long", event.ErrInvalidQuery)
	}
	appendTerms := func(field string, values []string, maximumItems, maximumLength int) error {
		if len(values) == 0 {
			return nil
		}
		if len(values) > maximumItems {
			return fmt.Errorf("%w: too many %s values", event.ErrInvalidQuery, field)
		}
		for _, value := range values {
			if len(value) > maximumLength {
				return fmt.Errorf("%w: %s value is too long", event.ErrInvalidQuery, field)
			}
		}
		clause, err := fields.terms(field, values)
		if err != nil {
			return err
		}
		clauses = append(clauses, clause)
		return nil
	}
	if filter.Region != "" {
		if err := appendTerms(types.DocumentFieldRegion, []string{filter.Region}, 1, mcputils.MaxRegionNameLength); err != nil {
			return nil, err
		}
	}
	if filter.RecordID != "" {
		clauses = append(clauses, map[string]any{"ids": map[string]any{"values": []string{filter.RecordID}}})
	}
	for _, target := range []struct {
		field                       string
		values                      []string
		maximumItems, maximumLength int
	}{
		{types.DocumentFieldHostname, filter.Hostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{types.DocumentFieldContainerHostname, filter.ContainerHostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{types.DocumentFieldContainerHostNamespace, filter.ContainerHostNamespaces, event.MaxTargetValues, event.MaxTargetValueLength},
		{types.DocumentFieldTracerName, filter.TracerNames, event.MaxTracerNames, event.MaxTracerNameLength},
	} {
		if err := appendTerms(target.field, target.values, target.maximumItems, target.maximumLength); err != nil {
			return nil, err
		}
	}
	rangeBounds := make(map[string]string)
	if !filter.StartTime.IsZero() {
		rangeBounds["gte"] = timeutil.FormatUTC(filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		rangeBounds["lte"] = timeutil.FormatUTC(filter.EndTime)
	}
	if len(rangeBounds) > 0 {
		clauses = append(clauses, fields.timeRange(rangeBounds))
	}
	if filter.ProfileOnly {
		clauses = append(clauses, indexedProfileQuery())
	}
	return map[string]any{"bool": map[string]any{"filter": clauses}}, nil
}

func indexedProfileQuery() any {
	var profiles []any
	// Both persisted Go JSON and protobuf JSON are accepted by the payload decoder.
	for _, fields := range profileFieldGroups() {
		predicates := make([]any, 0, len(fields))
		for _, field := range fields {
			predicates = append(predicates, map[string]any{"exists": map[string]any{"field": field}})
		}
		profiles = append(profiles, map[string]any{"bool": map[string]any{"filter": predicates}})
	}
	return map[string]any{"bool": map[string]any{"should": profiles, "minimum_should_match": 1}}
}

func profileFieldGroups() [][]string {
	return [][]string{
		{ProfileFramesField},
		{ProfileSamplesField, ProfileLocationsField, ProfileFunctionsField, ProfileSampleTypesField},
		{ProfileSamplesField, strings.ReplaceAll(ProfileLocationsField, "function_id", "functionId"), ProfileFunctionsField, strings.ReplaceAll(ProfileSampleTypesField, "sample_type", "sampleType")},
	}
}

func validateQuery(filter *event.Query) error {
	if filter == nil {
		return nil
	}
	if filter.Limit < 0 {
		return fmt.Errorf("%w: document limit must not be negative", event.ErrInvalidQuery)
	}
	if !filter.StartTime.IsZero() && !filter.EndTime.IsZero() && filter.StartTime.After(filter.EndTime) {
		return fmt.Errorf("%w: start must precede end", event.ErrInvalidQuery)
	}
	if len(filter.Region) > mcputils.MaxRegionNameLength {
		return fmt.Errorf("%w: region is too long", event.ErrInvalidQuery)
	}
	if len(filter.RecordID) > event.MaxRecordIDLength {
		return fmt.Errorf("%w: record ID exceeds %d bytes", event.ErrInvalidQuery, event.MaxRecordIDLength)
	}
	for _, target := range []struct {
		values        []string
		items, length int
	}{
		{filter.Hostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{filter.ContainerHostnames, event.MaxTargetValues, event.MaxTargetValueLength},
		{filter.ContainerHostNamespaces, event.MaxTargetValues, event.MaxTargetValueLength},
		{filter.TracerNames, event.MaxTracerNames, event.MaxTracerNameLength},
	} {
		if len(target.values) > target.items {
			return fmt.Errorf("%w: too many target values", event.ErrInvalidQuery)
		}
		for _, value := range target.values {
			if len(value) > target.length {
				return fmt.Errorf("%w: target value is too long", event.ErrInvalidQuery)
			}
		}
	}
	return nil
}

func decodeDocument(raw json.RawMessage, document *event.Document) error {
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return err
	}
	var selected json.RawMessage
	var selectedField string
	for _, field := range queryTimeFieldNames() {
		selected = source[field]
		if len(selected) > 0 && string(selected) != "null" {
			selectedField = field
			break
		}
	}
	if len(selected) == 0 || string(selected) == "null" {
		return fmt.Errorf("missing uploaded_timestamp, uploaded_time or time")
	}
	timestamp, err := decodeTimestamp(selected)
	if err != nil {
		return err
	}
	if selectedField == types.DocumentFieldUploadedTimestamp {
		// The shared event envelope only accepts RFC3339 for this typed field.
		normalized, err := json.Marshal(timestamp)
		if err != nil {
			return err
		}
		source[selectedField] = normalized
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	// Preserve payload integers through event responses and source-profile reads.
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(document); err != nil {
		return err
	}
	document.Timestamp = timestamp.Time.UTC()
	return nil
}

func decodeTimestamp(selected json.RawMessage) (timeutil.Timestamp, error) {
	var timestamp timeutil.Timestamp
	if err := json.Unmarshal(selected, &timestamp); err != nil {
		var text string
		if decodeErr := json.Unmarshal(selected, &text); decodeErr != nil {
			text = string(selected)
		}
		// Old ES mappings store space-separated dates in _source, not just in doc values.
		for _, layout := range []string{
			"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 -07:00",
			"2006-01-02 15:04:05", "2006/01/02 15:04:05 -0700",
			"2006/01/02 15:04:05", "2006-01-02",
		} {
			if parsed, parseErr := time.Parse(layout, text); parseErr == nil {
				timestamp.Time = parsed
				break
			}
		}
		if timestamp.IsZero() {
			// ES date sources may retain integer epoch_millis as numbers or
			// strings. Do not pass them through float64 or guess a seconds unit.
			millis, parseErr := strconv.ParseInt(text, 10, 64)
			if parseErr != nil {
				return timestamp, err
			}
			timestamp.Time = time.UnixMilli(millis).UTC()
			if year := timestamp.Year(); year < 0 || year > 9999 {
				return timeutil.Timestamp{}, fmt.Errorf("epoch_millis timestamp is outside the supported RFC3339 year range")
			}
		}
	}
	if timestamp.IsZero() {
		return timestamp, fmt.Errorf("query timestamp is empty")
	}
	return timestamp, nil
}
