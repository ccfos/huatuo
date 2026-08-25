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

package result

import (
	"context"
	"errors"
	"fmt"

	profileservice "huatuo-bamai/internal/profiler/service"
)

type profileStore interface {
	GetProfilesByTracerIDPage(
		ctx context.Context,
		tracerID string,
		limit int,
		offset int,
	) ([]*profileservice.ProfileDocument, error)
}

type publicationReader interface {
	IsPublished(ctx context.Context, requestID string) (bool, error)
}

// StorageRepository maps request IDs to the existing tracer_id storage field.
type StorageRepository struct {
	store        profileStore
	publications publicationReader
}

// NewStorageRepository adapts the existing profile query storage.
func NewStorageRepository(
	store profileStore,
	publications publicationReader,
) (*StorageRepository, error) {
	if store == nil {
		return nil, errors.New("create profiling result repository: profile Store is required")
	}
	if publications == nil {
		return nil, errors.New("create profiling result repository: publication Store is required")
	}
	return &StorageRepository{store: store, publications: publications}, nil
}

// IsPublished checks the durable result commit marker.
func (r *StorageRepository) IsPublished(ctx context.Context, requestID string) (bool, error) {
	published, err := r.publications.IsPublished(ctx, requestID)
	if err != nil {
		return false, fmt.Errorf(
			"%w: query publication for request %q: %w",
			ErrRepositoryUnavailable,
			requestID,
			err,
		)
	}
	return published, nil
}

// List reads committed records visible through the shared profile Store.
func (r *StorageRepository) List(
	ctx context.Context,
	requestID string,
	limit int,
	offset int,
) ([]Profile, error) {
	documents, err := r.store.GetProfilesByTracerIDPage(ctx, requestID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: query profiles for request %q: %w",
			ErrRepositoryUnavailable,
			requestID,
			err,
		)
	}
	profiles := make([]Profile, 0, len(documents))
	for _, document := range documents {
		if document == nil {
			return nil, errors.New("query published profiles: Store returned a nil document")
		}
		profiles = append(profiles, Profile{
			Hostname:          document.Hostname,
			Region:            document.Region,
			UploadedAt:        document.UploadedTime,
			CapturedAt:        document.CapturedAt(),
			ContainerID:       document.ContainerID,
			ContainerHostname: document.ContainerHostname,
			ContainerType:     document.ContainerType,
			ContainerQOS:      document.ContainerQOS,
			ProfileType:       document.TracerData.Flamedata.ProfileType,
			Profile:           &document.TracerData.Flamedata.Profile,
		})
	}
	return profiles, nil
}
