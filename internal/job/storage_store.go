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

package job

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

const jobStorageCollection = "jobs"

var jobQueryFields = map[string]struct{}{
	"id":           {},
	"user_id":      {},
	"container_id": {},
	"hostname":     {},
	"status":       {},
	"kind":         {},
	"subtype":      {},
	"created_at":   {},
	"ended_at":     {},
}

type storageStore struct {
	store *storage.Store[*Job]
}

func newStore(ctx context.Context, dsn string) (Store, error) {
	jobStore, err := storage.NewFromConfig[*Job](
		ctx,
		&driver.Config{Driver: "sqlite", SQLiteDSN: dsn},
		jobStorageCollection,
		recordMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("open job storage: %w", err)
	}
	return &storageStore{store: jobStore}, nil
}

func (s *storageStore) Get(ctx context.Context, jobID string) (*Job, error) {
	storedJob, err := s.store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return cloneJob(storedJob), nil
}

func (s *storageStore) Create(ctx context.Context, job *Job) error {
	if job == nil || job.revision != 1 {
		return fmt.Errorf("%w: created job revision must be 1", ErrInvalidQuery)
	}
	return s.store.Save(ctx, job, driver.SaveOptions{Mode: driver.SaveModeCreateOnly})
}

func (s *storageStore) Save(ctx context.Context, job *Job) (*Job, error) {
	if job == nil || job.revision <= 0 || job.revision == math.MaxInt64 {
		return nil, fmt.Errorf("%w: saved job revision is invalid", ErrInvalidQuery)
	}
	persisted := cloneJob(job)
	persisted.revision++
	err := s.store.Save(ctx, persisted, driver.SaveOptions{
		Mode: driver.SaveModeConditional,
		Conditions: []driver.Filter{
			{Field: "revision", Op: driver.OpEq, Value: job.revision},
		},
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

func (s *storageStore) List(ctx context.Context, query *Query) ([]*Job, error) {
	storageQuery, err := buildStorageQuery(query)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.Query(ctx, storageQuery)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		jobs[i] = cloneJob(jobs[i])
	}
	return jobs, nil
}

func (s *storageStore) DeleteTerminalBefore(
	ctx context.Context,
	endedBefore time.Time,
	limit int,
) (int64, error) {
	if endedBefore.IsZero() {
		return 0, fmt.Errorf("%w: cleanup boundary is required", ErrInvalidQuery)
	}
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("%w: cleanup limit must be between 1 and 1000", ErrInvalidQuery)
	}
	return s.store.DeleteByQuery(ctx, driver.DeleteQuery{
		Filters: []driver.Filter{
			{Field: "status", Op: driver.OpEq, Value: string(StatusTerminal)},
			{Field: "ended_at", Op: driver.OpNe, Value: ""},
			{Field: "ended_at", Op: driver.OpLte, Value: driver.NormalizeValue(endedBefore)},
		},
		Limit: limit,
	})
}

func (s *storageStore) Ping(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *storageStore) Close() error {
	return s.store.Close(context.Background())
}

func buildStorageQuery(query *Query) (driver.Query, error) {
	if err := validateQuerySort(query); err != nil {
		return driver.Query{}, err
	}
	if query == nil {
		return driver.Query{
			Sorts: []driver.Sort{{Field: "created_at", Desc: true}, {Field: "id", Desc: true}},
		}, nil
	}

	filters := make([]driver.Filter, 0, 8)
	appendEqual := func(field, value string) {
		if value != "" {
			filters = append(filters, driver.Filter{Field: field, Op: driver.OpEq, Value: value})
		}
	}
	appendEqual("id", query.ID)
	if !query.IsAdmin {
		appendEqual("user_id", query.UserID)
	}
	appendEqual("container_id", query.ContainerID)
	appendEqual("hostname", query.Hostname)
	if len(query.Statuses) != 0 {
		statuses := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			statuses[i] = string(status)
		}
		filters = append(filters, driver.Filter{Field: "status", Op: driver.OpIn, Value: statuses})
	}
	if len(query.Kinds) != 0 {
		kinds := make([]string, len(query.Kinds))
		for i, kind := range query.Kinds {
			kinds[i] = string(kind)
		}
		filters = append(filters, driver.Filter{Field: "kind", Op: driver.OpIn, Value: kinds})
	}
	if len(query.Subtypes) != 0 {
		filters = append(filters, driver.Filter{Field: "subtype", Op: driver.OpIn, Value: query.Subtypes})
	}

	sortField, descending := querySort(query)
	sorts := []driver.Sort{{Field: sortField, Desc: descending}}
	if sortField != "id" {
		sorts = append(sorts, driver.Sort{Field: "id", Desc: descending})
	}
	return driver.Query{
		Filters: filters,
		Sorts:   sorts,
		Limit:   query.Limit,
		Offset:  query.Offset,
	}, nil
}

func validateQuerySort(query *Query) error {
	field, _ := querySort(query)
	if _, ok := jobQueryFields[field]; !ok {
		return fmt.Errorf("%w: unsupported sort field %q", ErrInvalidQuery, field)
	}
	return nil
}

func querySort(query *Query) (string, bool) {
	if query == nil || query.Sort == "" {
		return "created_at", true
	}
	field := query.Sort
	descending := strings.HasPrefix(field, "-")
	return strings.TrimPrefix(field, "-"), descending
}
