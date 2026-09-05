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
	Collection     = "profiling_publications"
	markerIDPrefix = "profiling-publication:"
)

// Marker proves that all profiling windows for a request are query-visible.
// A marker is also written when a successful profiling run has no samples.
type Marker struct {
	RequestID   string    `json:"request_id"`
	PublishedAt time.Time `json:"published_at"`
}

type markerMapper struct{}

// Store publishes and verifies profiling result commit markers.
type Store struct {
	store *storage.Store[*Marker]
	now   func() time.Time
}

// NewFromConfig creates a publication Store on the shared result backend.
func NewFromConfig(ctx context.Context, config *driver.Config) (*Store, error) {
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

// Publish writes the final commit marker synchronously.
func (s *Store) Publish(ctx context.Context, requestID string) error {
	marker := &Marker{RequestID: requestID, PublishedAt: s.now()}
	if err := s.store.SaveSync(ctx, marker); err != nil {
		return fmt.Errorf("publish profiling result %q: %w", requestID, err)
	}
	return nil
}

// IsPublished reports whether a durable commit marker exists.
func (s *Store) IsPublished(ctx context.Context, requestID string) (bool, error) {
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

func (markerMapper) Fields(*Marker) (map[string]any, error) {
	return nil, nil
}

func (markerMapper) Indexes() []driver.Index {
	return nil
}
