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

// Package publication owns the durable profiling result commit marker.
package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

const (
	Collection            = "profiling_publications"
	markerIDPrefix        = "profiling-publication:"
	markerRecordType      = "profiling_publication"
	markerRecordTypeField = "record_type"
	markerStateField      = "state"
	profileRequestIDField = "tracer_id"
	stagingRecoveryBatch  = 100
)

type markerState string

const (
	markerStateStaging   markerState = "staging"
	markerStatePublished markerState = "published"
)

// Marker proves that all profiling windows for a request are query-visible.
// A marker is also written when a successful profiling run has no samples.
type Marker struct {
	RecordType  string      `json:"record_type,omitempty"`
	RequestID   string      `json:"request_id"`
	State       markerState `json:"state,omitempty"`
	PreparedAt  time.Time   `json:"prepared_at,omitzero"`
	PublishedAt time.Time   `json:"published_at,omitzero"`
}

type markerMapper struct{}

// Store publishes and verifies profiling result commit markers.
type Store struct {
	store *storage.Store[*Marker]
	now   func() time.Time
}

// NewStore creates a publication Store on the shared result backend.
func NewStore(ctx context.Context, config *driver.Config) (*Store, error) {
	markerStore, err := storage.NewFromConfig[*Marker](
		ctx,
		config,
		Collection,
		markerMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("create profiling publication Store: %w", err)
	}
	return &Store{
		store: markerStore,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}, nil
}

// Prepare hides any previous result, removes stale artifacts, and records a
// recoverable staging boundary before a newly admitted execution starts.
func (s *Store) Prepare(ctx context.Context, requestID string) error {
	if err := validateRequestID(requestID); err != nil {
		return err
	}
	if err := s.saveStaging(ctx, requestID); err != nil {
		return fmt.Errorf("stage profiling result %q: %w", requestID, err)
	}
	if err := s.deleteArtifacts(ctx, requestID); err != nil {
		return fmt.Errorf("remove stale profiling result %q: %w", requestID, err)
	}
	return nil
}

// Publish writes the final commit marker synchronously.
func (s *Store) Publish(ctx context.Context, requestID string) error {
	if err := validateRequestID(requestID); err != nil {
		return err
	}
	marker, err := s.store.Get(ctx, markerID(requestID))
	if err != nil {
		return fmt.Errorf("read staged profiling result %q: %w", requestID, err)
	}
	if marker == nil || marker.RequestID != requestID || marker.State != markerStateStaging {
		return fmt.Errorf("profiling result %q is not staged", requestID)
	}
	marker.RecordType = markerRecordType
	marker.State = markerStatePublished
	marker.PublishedAt = s.now()
	if err := s.store.SaveSync(ctx, marker); err != nil {
		return fmt.Errorf("publish profiling result %q: %w", requestID, err)
	}
	return nil
}

// Discard hides an unsuccessful execution before deleting its partial data.
// The staging marker remains recoverable if cleanup fails.
func (s *Store) Discard(ctx context.Context, requestID string) error {
	if err := validateRequestID(requestID); err != nil {
		return err
	}
	if err := s.saveStaging(ctx, requestID); err != nil {
		return fmt.Errorf("hide profiling publication %q: %w", requestID, err)
	}
	if err := s.deleteArtifacts(ctx, requestID); err != nil {
		return fmt.Errorf("discard profiling artifacts %q: %w", requestID, err)
	}
	if err := s.store.Delete(ctx, markerID(requestID)); err != nil {
		return fmt.Errorf("discard profiling publication %q: %w", requestID, err)
	}
	return nil
}

// IsPublished reports whether a durable commit marker exists.
func (s *Store) IsPublished(ctx context.Context, requestID string) (bool, error) {
	if err := validateRequestID(requestID); err != nil {
		return false, err
	}
	marker, err := s.store.Get(ctx, markerID(requestID))
	if errors.Is(err, driver.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read profiling publication %q: %w", requestID, err)
	}
	if marker == nil || marker.RequestID != requestID {
		return false, fmt.Errorf("profiling publication %q is invalid", requestID)
	}
	if marker.State == markerStateStaging {
		return false, nil
	}
	if marker.State == markerStatePublished && !marker.PublishedAt.IsZero() {
		return true, nil
	}
	// Markers written before staging support only carried PublishedAt.
	if marker.State == "" && !marker.PublishedAt.IsZero() {
		return true, nil
	}
	return false, fmt.Errorf("profiling publication %q is invalid", requestID)
}

// RecoverStaging removes partial results left by a previous Node process.
// Recovery must complete before the HTTP server accepts new Operations.
func (s *Store) RecoverStaging(ctx context.Context) error {
	for {
		markers, err := s.store.Query(ctx, driver.Query{
			Filters: []driver.Filter{
				{Field: markerRecordTypeField, Op: driver.OpEq, Value: markerRecordType},
				{Field: markerStateField, Op: driver.OpEq, Value: markerStateStaging},
			},
			Limit: stagingRecoveryBatch,
		})
		if err != nil {
			return fmt.Errorf("query staged profiling results: %w", err)
		}
		if len(markers) == 0 {
			return nil
		}
		for _, marker := range markers {
			if marker == nil || marker.RequestID == "" {
				return errors.New("recover staged profiling results: invalid marker")
			}
			if err := s.Discard(ctx, marker.RequestID); err != nil {
				return fmt.Errorf(
					"recover staged profiling result %q: %w",
					marker.RequestID,
					err,
				)
			}
		}
	}
}

// Ready verifies that the publication backend can serve reads.
func (s *Store) Ready(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("profiling publication Store is not initialized")
	}
	if _, err := s.store.Count(ctx, driver.Query{Limit: 1}); err != nil {
		return fmt.Errorf("profiling publication Store readiness: %w", err)
	}
	return nil
}

// Close releases the publication backend.
func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close(ctx)
}

func (s *Store) saveStaging(ctx context.Context, requestID string) error {
	return s.store.SaveSync(ctx, &Marker{
		RecordType: markerRecordType,
		RequestID:  requestID,
		State:      markerStateStaging,
		PreparedAt: s.now(),
	})
}

func (s *Store) deleteArtifacts(ctx context.Context, requestID string) error {
	_, err := s.store.DeleteByQuery(ctx, driver.Query{Filters: []driver.Filter{
		{Field: profileRequestIDField, Op: driver.OpEq, Value: requestID},
	}})
	return err
}

func validateRequestID(requestID string) error {
	if requestID == "" {
		return errors.New("profiling publication request ID is required")
	}
	return nil
}

func markerID(requestID string) string {
	return markerIDPrefix + requestID
}

func (markerMapper) ID(marker *Marker) string {
	if marker == nil {
		return ""
	}
	return markerID(marker.RequestID)
}

func (markerMapper) Encode(marker *Marker) ([]byte, error) {
	return json.Marshal(marker)
}

func (markerMapper) Decode(data []byte) (*Marker, error) {
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, err
	}
	return &marker, nil
}

func (markerMapper) Fields(marker *Marker) (map[string]any, error) {
	if marker == nil {
		return nil, nil
	}
	return map[string]any{
		markerRecordTypeField: marker.RecordType,
		markerStateField:      marker.State,
	}, nil
}

func (markerMapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: markerRecordTypeField},
		{Field: markerStateField},
	}
}
