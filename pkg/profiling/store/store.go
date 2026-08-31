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
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/types"
)

const (
	defaultIndex   = "huatuo-bamai"
	maxSearchLimit = 1001
)

// Config contains the Elasticsearch settings used by profiling storage.
type Config struct {
	Addresses []string
	Username  string
	Password  string
	Index     string
}

// Filter selects profiling aggregation windows.
type Filter struct {
	ID                string
	Region            string
	Hostname          string
	ContainerID       string
	ContainerHostname string
	TracerID          string
	StartTime         time.Time
	EndTime           time.Time
	ProfileType       string
	Limit             int
	Offset            int
}

// Store persists and queries profiling aggregation windows.
type Store struct {
	store *storage.Store[*Document]
}

// NewFromConfig creates profiling storage backed by Elasticsearch.
func NewFromConfig(ctx context.Context, config Config) (*Store, error) {
	index := config.Index
	if index == "" {
		index = defaultIndex
	}
	profileStore, err := storage.NewFromConfig[*Document](ctx, &driver.Config{
		Driver:      "elasticsearch",
		ESAddresses: config.Addresses,
		ESUsername:  config.Username,
		ESPassword:  config.Password,
		ESIndex:     index,
	}, Collection, mapper{})
	if err != nil {
		return nil, err
	}
	log.WithField("driver", "elasticsearch").WithField("index", index).Info(
		"initialized profile storage",
	)
	return &Store{store: profileStore}, nil
}

// Save persists a profiling window and waits until subsequent queries can see it.
func (s *Store) Save(ctx context.Context, document *Document) error {
	if s == nil || s.store == nil {
		return errors.New("profile storage is not initialized")
	}
	if document != nil {
		document.UploadedTimestamp = time.Now().UTC()
	}
	return s.store.SaveSync(ctx, document)
}

// Close releases the underlying storage client.
func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close(ctx)
}

// Ready checks whether the backing store can serve queries.
func (s *Store) Ready(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("profile storage is not initialized")
	}
	if _, err := s.store.Count(ctx, driver.Query{Limit: 1}); err != nil {
		return fmt.Errorf("profile storage readiness: %w", err)
	}
	return nil
}

// Search returns profiling windows matching filter.
func (s *Store) Search(ctx context.Context, filter *Filter) ([]*Document, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("profile storage is not initialized")
	}
	return s.store.Query(ctx, buildSearchQuery(filter))
}

// ListByTracerID returns one page of profiling windows for a task.
func (s *Store) ListByTracerID(
	ctx context.Context,
	tracerID string,
	limit int,
	offset int,
) ([]*Document, error) {
	return s.Search(ctx, &Filter{
		TracerID: tracerID,
		Limit:    limit,
		Offset:   offset,
	})
}

// Values returns distinct indexed values matching filter.
func (s *Store) Values(
	ctx context.Context,
	filter *Filter,
	field string,
) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("profile storage is not initialized")
	}
	normalizedField, err := normalizeAggregationField(field)
	if err != nil {
		return nil, err
	}
	return s.store.Values(
		ctx,
		normalizedField,
		buildAggregationQuery(filter),
		normalizeSearchLimit(filter),
	)
}

func buildSearchQuery(filter *Filter) driver.Query {
	query := buildAggregationQuery(filter)
	query.Limit = normalizeSearchLimit(filter)
	if filter != nil && filter.Offset > 0 {
		query.Offset = filter.Offset
	}
	query.Sorts = []driver.Sort{{Field: types.DocumentFieldUploadedTimestamp, Desc: true}}
	return query
}

func buildAggregationQuery(filter *Filter) driver.Query {
	query := driver.Query{Filters: make([]driver.Filter, 0, 8)}
	if filter == nil {
		return query
	}
	if !filter.StartTime.IsZero() {
		query.Filters = append(query.Filters, driver.Filter{
			Field: types.DocumentFieldUploadedTimestamp,
			Op:    driver.OpGte,
			Value: filter.StartTime.UTC().Format(time.RFC3339Nano),
		})
	}
	if !filter.EndTime.IsZero() {
		query.Filters = append(query.Filters, driver.Filter{
			Field: types.DocumentFieldUploadedTimestamp,
			Op:    driver.OpLte,
			Value: filter.EndTime.UTC().Format(time.RFC3339Nano),
		})
	}
	if filter.TracerID != "" || filter.ID != "" {
		id := filter.TracerID
		if id == "" {
			id = filter.ID
		}
		query.Filters = append(query.Filters, driver.Filter{
			Field: types.DocumentFieldTracerID + ".keyword",
			Op:    driver.OpEq,
			Value: id,
		})
	}
	appendTextFilter := func(field, value string) {
		if value == "" {
			return
		}
		query.Filters = append(query.Filters, driver.Filter{
			Field: field + ".keyword",
			Op:    driver.OpEq,
			Value: value,
		})
	}
	appendTextFilter(types.DocumentFieldRegion, filter.Region)
	appendTextFilter(types.DocumentFieldContainerID, filter.ContainerID)
	appendTextFilter(types.DocumentFieldContainerHostname, filter.ContainerHostname)
	appendTextFilter(fieldProfileType, filter.ProfileType)
	if filter.Hostname != "" {
		appendTextFilter(types.DocumentFieldHostname, filter.Hostname)
		query.Filters = append(query.Filters, driver.Filter{
			Field: types.DocumentFieldContainerHostname,
			Op:    driver.OpEq,
			Value: "",
		})
	}
	return query
}

func normalizeAggregationField(field string) (string, error) {
	switch field {
	case "id":
		return types.DocumentFieldTracerID, nil
	case "profile_type":
		return fieldProfileType, nil
	case types.DocumentFieldRegion,
		types.DocumentFieldHostname,
		types.DocumentFieldContainerHostname,
		types.DocumentFieldContainerHostNamespace,
		types.DocumentFieldContainerID,
		types.DocumentFieldContainerType,
		types.DocumentFieldContainerQoS,
		types.DocumentFieldTracerName,
		types.DocumentFieldTracerID,
		types.DocumentFieldTracerType:
		return field, nil
	default:
		return "", fmt.Errorf("invalid aggregation field: %q", field)
	}
}

func normalizeSearchLimit(filter *Filter) int {
	if filter == nil || filter.Limit <= 0 {
		return 100
	}
	return min(filter.Limit, maxSearchLimit)
}
