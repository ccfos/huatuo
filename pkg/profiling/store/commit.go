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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

const (
	commitCollection = "profiling_publications"
	markerIDPrefix   = "profiling-publication:"
)

// commitMarker proves that all profiling windows for a request are query-visible.
// A marker is also written when a successful profiling run has no samples.
type commitMarker struct {
	RequestID   string    `json:"request_id"`
	PublishedAt time.Time `json:"published_at"`
}

type markerMapper struct{}

// commitStore publishes and verifies profiling result commit markers.
type commitStore struct {
	store *storage.Store[*commitMarker]
	now   func() time.Time
}

// newCommitStore creates a commit store on the shared result backend.
func newCommitStore(ctx context.Context, config *driver.Config) (*commitStore, error) {
	markerStore, err := storage.NewFromConfig[*commitMarker](
		ctx,
		config,
		commitCollection,
		markerMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("create profiling commit store: %w", err)
	}
	return &commitStore{
		store: markerStore,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}, nil
}

// Commit writes the final commit marker synchronously.
func (s *commitStore) Commit(ctx context.Context, requestID string) error {
	marker := &commitMarker{RequestID: requestID, PublishedAt: s.now()}
	if err := s.store.Save(ctx, marker, driver.SaveOptions{WaitForVisibility: true}); err != nil {
		return fmt.Errorf("publish profiling result %q: %w", requestID, err)
	}
	return nil
}

// IsCommitted reports whether a durable commit marker exists.
func (s *commitStore) IsCommitted(ctx context.Context, requestID string) (bool, error) {
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
	if marker.PublishedAt.IsZero() {
		// Older versions used a zero timestamp for an unpublished staging marker.
		return false, nil
	}
	return true, nil
}

// Ready verifies that the commit backend can serve reads.
func (s *commitStore) Ready(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("profiling commit store is not initialized")
	}
	if _, err := s.store.Count(ctx, driver.Query{Limit: 1}); err != nil {
		return fmt.Errorf("profiling commit store readiness: %w", err)
	}
	return nil
}

// Close releases the commit backend.
func (s *commitStore) Close(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close(ctx)
}

func markerID(requestID string) string {
	return markerIDPrefix + requestID
}

func (markerMapper) ID(marker *commitMarker) string {
	if marker == nil {
		return ""
	}
	return markerID(marker.RequestID)
}

func (markerMapper) Encode(marker *commitMarker) ([]byte, error) {
	return json.Marshal(marker)
}

func (markerMapper) Decode(record driver.Record) (*commitMarker, error) {
	var marker commitMarker
	if err := json.Unmarshal(record.Data, &marker); err != nil {
		return nil, err
	}
	return &marker, nil
}

func (markerMapper) Fields(*commitMarker) (map[string]any, error) {
	return nil, nil
}

func (markerMapper) Indexes() []driver.Index {
	return nil
}
