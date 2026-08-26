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
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

var (
	ErrNotReady              = errors.New("profiling result is not ready")
	ErrNotFound              = errors.New("profiling result is not found")
	ErrUnavailable           = errors.New("profiling result is unavailable")
	ErrResponseTooLarge      = errors.New("profiling result response is too large")
	ErrForbidden             = errors.New("profiling result access is forbidden")
	ErrWrongKind             = errors.New("job is not a profiling job")
	ErrRepositoryUnavailable = errors.New("profiling result repository is unavailable")
)

// Profile is one stored profiling result without storage implementation fields.
type Profile struct {
	Hostname          string
	Region            string
	UploadedAt        time.Time
	CapturedAt        time.Time
	ContainerID       string
	ContainerHostname string
	ContainerType     string
	ContainerQoS      string
	ProfileType       string
	Profile           *profilev1.Profile
}

// Page is one stable page of published Profiles.
type Page struct {
	Items   []Profile
	Limit   int
	Offset  int
	HasMore bool
}

// Repository reads published results by the public request ID.
type Repository interface {
	IsPublished(ctx context.Context, requestID string) (bool, error)
	List(
		ctx context.Context,
		requestID string,
		limit int,
		offset int,
	) ([]Profile, error)
}
