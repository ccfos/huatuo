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

// Package event defines MCP's storage-neutral event query contract.
package event

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ccfos/huatuo/internal/flamegraph"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

var (
	ErrInvalidQuery   = errors.New("invalid MCP event query")
	ErrDecodeFailed   = errors.New("MCP event response decoding failed")
	ErrUnsupportedOp  = errors.New("MCP event reader operation unsupported")
	ErrIncompleteData = errors.New("MCP event reader returned incomplete data")
)

// Reader describes diagnostic reads, not a database's CRUD or query language.
// Count must be exact; implementations unable to guarantee that return
// ErrUnsupportedOp. Tools then fall back to bounded event searches.
//
// All reads use the same logical predicate semantics: alternatives within a
// target list are OR; distinct target lists and time/region/tracer predicates
// are AND. Time boundaries include both endpoints and select the adapter's
// diagnostic clock, normalized into Document.Timestamp.
// Adapters implement storage-specific column mapping and deduplication once
// for search, count, and tracer aggregation.
//
// SearchEvents returns newest diagnostic time first, at most Query.Limit documents,
// and explicitly reports omitted results. Unknown totals remain nil. Offset
// applies before the limit. Each result/document and locator is owned by the
// caller; adapters must not reuse mutable buffers after return. Cross-request
// count/search snapshots are not implied.
//
// TracerNames(profileOnly=true) advertises only analyzable ProfileFrames. Profile
// reads normalize payloads in the adapter, not in Tools. Cancellation/deadlines
// must reach the underlying database operation and terminate it promptly.
// Adapters enforce bounded row/byte decoding and perform no implicit retries;
// retryable driver errors implement RetryableError. BackendType is a fixed
// registered adapter identity, never a query-derived metric label.
type Reader interface {
	BackendType() string
	SearchEvents(ctx context.Context, query *Query) (SearchResult, error)
	Count(ctx context.Context, query *Query) (int64, error)
	TracerNames(ctx context.Context, query *Query) ([]string, error)
}

type ManagedReader interface {
	Reader
	Close(ctx context.Context) error
}

// TracerCounter is optional; Tools can fall back to individual exact counts.
// Counts must be nonnegative/exact and use the same deduplication/predicates as
// Count/SearchEvents. Missing tracer buckets mean zero matches.
type TracerCounter interface {
	TracerCounts(ctx context.Context, query *Query) ([]TracerCount, error)
}

// Query carries logical predicates; RecordID selects a native source record
// alongside region/time/target/tracer constraints. Field paths stay in adapters.
type Query struct {
	Region                  string
	Hostnames               []string
	ContainerHostnames      []string
	ContainerHostNamespaces []string
	TracerNames             []string
	RecordID                string
	StartTime               time.Time
	EndTime                 time.Time
	ProfileOnly             bool
	Limit                   int
	Offset                  int
}

// SourceRef is a native locator, not an executable query or a UI URL.
type SourceRef struct {
	Backend  string            `json:"backend"`
	RecordID string            `json:"record_id,omitempty"`
	Region   string            `json:"region"`
	Locator  map[string]string `json:"locator,omitempty"`
}

// Document shares HuaTuo's event envelope. Adapters can provide normalized
// profile frames without exposing their physical payload representation.
type Document struct {
	types.Document
	Timestamp         time.Time              `json:"timestamp,omitzero"`
	TracerData        any                    `json:"tracer_data"`
	RecordID          string                 `json:"-"`
	Source            *SourceRef             `json:"source,omitempty"`
	ProfileFrames     []flamegraph.FrameData `json:"-"`
	ProfileSampleType string                 `json:"-"`
	ProfileSampleUnit string                 `json:"-"`
}

func (d *Document) QueryTimestamp() time.Time {
	if !d.Timestamp.IsZero() {
		return d.Timestamp
	}
	return d.UploadedTimestamp.Time
}

func (d *Document) MarshalJSON() ([]byte, error) {
	type plainDocument Document
	var uploaded *timeutil.Timestamp
	if !d.UploadedTimestamp.IsZero() {
		uploaded = &d.UploadedTimestamp
	}
	return json.Marshal(struct {
		*plainDocument
		Uploaded *timeutil.Timestamp `json:"uploaded_timestamp,omitempty"`
	}{plainDocument: (*plainDocument)(d), Uploaded: uploaded})
}

type TracerCount struct {
	Name  string
	Count int64
}

type SearchResult struct {
	Documents  []*Document
	Truncated  bool
	TotalCount *int64
	CountExact bool
	Warnings   []string
}

// QueryStatus preserves completeness information independently of the backend.
type QueryStatus struct {
	Region     string   `json:"region"`
	Returned   int      `json:"returned"`
	Truncated  bool     `json:"truncated"`
	TotalCount *int64   `json:"total_count,omitempty"`
	CountExact bool     `json:"count_exact"`
	Warnings   []string `json:"warnings,omitempty"`
}

func (r SearchResult) Status(region string) QueryStatus {
	return QueryStatus{
		Region: region, Returned: len(r.Documents), Truncated: r.Truncated,
		TotalCount: r.TotalCount, CountExact: r.CountExact, Warnings: append([]string(nil), r.Warnings...),
	}
}

// RoutedDocument leaves reader-owned results untouched; routing is authoritative
// for both the envelope and its locator.
func RoutedDocument(document *Document, backend, region string) *Document {
	if document == nil {
		return nil
	}
	copyDocument := *document
	copyDocument.Region = region
	source := SourceRef{Backend: backend, RecordID: document.RecordID, Region: region}
	if document.Source != nil {
		source = *document.Source
		source.Region = region
		if source.Backend == "" {
			source.Backend = backend
		}
		if source.RecordID == "" {
			source.RecordID = document.RecordID
		}
		source.Locator = make(map[string]string, len(document.Source.Locator))
		for key, value := range document.Source.Locator {
			source.Locator[key] = value
		}
	}
	copyDocument.Source = &source
	return &copyDocument
}
